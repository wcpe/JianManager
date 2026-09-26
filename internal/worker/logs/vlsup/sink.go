package vlsup

import (
	"errors"
	"log/slog"
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
func (s IndependentLoggerSink) ReportFailure(instance string, err error) {
	if s.Logger == nil || err == nil {
		return
	}
	s.Logger.Error("vlsup supervisor failure", "instance", instance, "error", err)
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
