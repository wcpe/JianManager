package vlsup

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
)

// ErrRecursiveVLSink 表示失败日志被拒绝写入 VL 日志管道。
// FR-476 §3.1：VL 写失败日志必须走独立、限流且不回写 VL 的 sink，防止错误递归。
var ErrRecursiveVLSink = errors.New("vlsup: recursive write into VL log pipeline is forbidden")

// Sink 是 supervisor 进程错误的独立落点。
// 实现必须写入 VL 数据面之外的目标（Worker 进程日志、独立文件、宿主 stderr 等），
// 禁止经 VictoriaLogs HTTP insert / 查询管道回写。
type Sink interface {
	// ReportFailure 上报一个 supervisor 侧失败。
	ReportFailure(instance string, err error)
}

// VLWriter 由“可能写入 VL 管道”的 sink 实现，供守卫标志检查。
type VLWriter interface {
	// WritesIntoVL 报告该 sink 是否会把失败日志写回 VL 数据面。
	WritesIntoVL() bool
}

// IndependentLoggerSink 将 supervisor 失败写入独立 slog.Logger（默认 Worker 进程日志），
// 不与 VL 查询/采集管道共享写入路径。
type IndependentLoggerSink struct {
	Logger *slog.Logger
	// ForbidVL 明确声明本 sink 不写入 VL；守卫读取时恒为 false。
}

// WritesIntoVL 实现 VLWriter；独立 logger sink 永不写 VL。
func (s IndependentLoggerSink) WritesIntoVL() bool { return false }

// ReportFailure 实现 Sink。
//
// ## 为什么不能无条件用 Error（2026-10-31 生产缺陷）
//
// VictoriaLogs 把**所有级别**的日志都写到 stderr（info / warn / error 同一流），
// 而 supervisor 把这个流的**每一行**都当失败上报。旧实现无条件调 `logger.Error`，
// 于是 VL 每次启动打的几十行 info（内存上限、版本、监听地址）全部变成 ERROR：
//
//	生产实测：重启后 20 条 ERROR，逐条查全部是 VL 的 `info` 级日志
//
// 危害不是“多几行日志”，而是**真错会被噪音淹没**——运维 `grep ERROR` 看不到真正的问题。
// 本项目已经吃过一次“观测面失真”的亏（`log_vl_disk_used` 读到 903%、实际 4.5%）。
//
// 因此按 VL 行自带的级别字段分流。**解析不出级别时必须保持 Error**：
// Go panic 栈、segfault 信息这些非结构化输出恰恰最需要 ERROR，
// 不能因为“解析失败”就静默降级成 info。
func (s IndependentLoggerSink) ReportFailure(instance string, err error) {
	if err == nil {
		return
	}
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), vlDiagnosticLevel(err.Error()),
		"vlsup 独立进程诊断", "instance", instance, "error", err)
}

// vlDiagnosticLevel 从 VL 日志行里读出它自带的级别。
//
// VL 的 stderr 行格式（实测 v1.52.0，TAB 分隔）：
//
//	2026-10-31T07:53:00.041Z\tinfo\tVictoriaMetrics@…/memory.go:45\tlimiting caches to …
//	                         ↑ 级别字段
//
// **default 必须是 Error**（见 ReportFailure 的注释）。
func vlDiagnosticLevel(line string) slog.Level {
	// 只看前 4 段：级别在开头附近，不必扫描整行（行可能很长，脱敏后仍可很长）。
	for _, field := range strings.SplitN(line, "\t", 4) {
		switch strings.TrimSpace(strings.ToLower(field)) {
		case "info":
			return slog.LevelInfo
		case "warn", "warning":
			return slog.LevelWarn
		case "error", "fatal", "panic", "dpanic":
			return slog.LevelError
		}
	}
	// 非结构化输出（panic 栈 / 段错误 / 被截断行）⇒ 当错误处理（保守方向）。
	return slog.LevelError
}

// RecordingSink 用于测试：记录上报，并可模拟“写入 VL 管道”的非法 sink。
type RecordingSink struct {
	mu sync.Mutex
	// IntoVL 为 true 时 WritesIntoVL 返回 true，用于触发守卫。
	IntoVL  bool
	Reports []SinkReport
}

// SinkReport 是一次失败上报记录。
type SinkReport struct {
	Instance string
	Err      error
}

// WritesIntoVL 实现 VLWriter。
func (s *RecordingSink) WritesIntoVL() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.IntoVL
}

// ReportFailure 实现 Sink。
func (s *RecordingSink) ReportFailure(instance string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Reports = append(s.Reports, SinkReport{Instance: instance, Err: err})
}

// Snapshot 返回已记录的上报副本。
func (s *RecordingSink) Snapshot() []SinkReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SinkReport, len(s.Reports))
	copy(out, s.Reports)
	return out
}

// Count 返回上报次数。
func (s *RecordingSink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Reports)
}

// reportViaSink 经守卫将 supervisor 错误送往 sink。
// 守卫标志 allowVLRecursive=false（生产默认）时，任何 WritesIntoVL()==true 的 sink
// 都会被拒绝，且不产生递归写入；拒绝次数计入 guardBlocks。
func reportViaSink(sink Sink, allowVLRecursive bool, instance string, err error) (blocked bool) {
	if sink == nil || err == nil {
		return false
	}
	if !allowVLRecursive {
		if vw, ok := sink.(VLWriter); ok && vw.WritesIntoVL() {
			return true
		}
	}
	sink.ReportFailure(instance, err)
	return false
}
