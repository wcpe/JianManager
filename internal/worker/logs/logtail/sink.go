// Package logtail 把 tailer 采集器接到 Worker 生产链路上。
//
// # 为什么单独一个包而不是塞进 main.go
//
// main.go 已经 2500+ 行，且采集器装配涉及「行 → 事件 → VL 行」的转换规则，
// 这些规则需要单测（尤其是时间解析与标签推导，错了就是脏数据）。
//
// # 与旧链路的边界
//
// 本包**不 import** `ingest` / `ledger` / `catalog`。它只依赖：
//   - `tailer`（采集）
//   - `vlsup`（写入 VL）
//   - `utcday`（时间归一）
//
// 这使得它可以与旧采集器**并行存在**，通过开关切换，互不影响。
package logtail

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// SourceResolver 把一个文件路径解析成它的源标签。
//
// # 为什么要单独抽接口
//
// 「文件路径 → log_source_id」的推导规则由实例绑定与核心规则决定（
// `policy_instances.go` 的 `instanceLivePath` 与 `policy_rules.go` 的规则表）。
// 本包不应该复制那套规则——那正是「同一概念多处实现」的坑。
//
// 装配层把旧实现的推导函数包进来即可；将来旧代码删除时，
// 把规则表搬到一个中立位置，本接口不变。
type SourceResolver interface {
	// Resolve 返回该路径对应的 log_source_id 与 stream（如 stdout）。
	// 无法识别时返回空字符串（调用方应跳过而非猜测）。
	Resolve(path string) (logSourceID, stream string)
}

// SourceResolverFunc 让普通函数可作为 SourceResolver。
type SourceResolverFunc func(path string) (logSourceID, stream string)

func (f SourceResolverFunc) Resolve(path string) (string, string) { return f(path) }

// Sink 把 tailer 的行转成 VL 的 JSONL 并写入。
//
// # 与旧链路的差异（刻意为之）
//
// 旧链路：行 → 规范化事件（多行合并/字符集/时区/级别解析）→ 账本登记 → 投递 → 校验。
// 本实现：行 → 最小事件（时间/级别/内容）→ 写 VL。
//
// **不做多行合并**：那是旧 `normalize` 包的职责（1226 行），
// 它不在本次重建范围内。如果将来需要，可以把它接在 Line → Event 之间。
type Sink struct {
	client   *vlsup.Client
	resolver SourceResolver
	// loc 解释行内 `[HH:MM:SS]` 的时区；nil 视为 UTC。
	//
	// 它不是“展示用时区”——它直接决定落库的 `_time` 值，配错就是整源时间轴偏移。
	loc *time.Location

	mu    sync.Mutex
	stats Stats
	// lastEvent 是同一源最近一条事件的时间（用于给只有时刻的行补日期 + 跨午夜回拨）。
	//
	// 采集是单调前进的，所以「上一条的时间」是判定日期的可靠参照；而回补历史文件时
	// 它会由文件名里的日期校正（见 resolveEventTime）。
	lastMu    sync.Mutex
	lastEvent map[string]time.Time // key: logSourceID
}

// Stats 是采集统计（供 /metrics 与排障）。
type Stats struct {
	// Lines 是已处理行数。
	Lines int64
	// Written 是成功写入 VL 的行数。
	Written int64
	// Failed 是写入失败的行数（重试后仍失败）。
	Failed int64
	// UnresolvedSource 是无法识别源的路径数（配置问题）。
	UnresolvedSource int64
	// Gaps 是「文件未读完就消失」的次数。
	Gaps int64
}

// NewSink 创建写入端。loc 为 nil 时按 UTC 解释行内时刻。
func NewSink(client *vlsup.Client, resolver SourceResolver, loc *time.Location) *Sink {
	if loc == nil {
		loc = time.UTC
	}
	return &Sink{client: client, resolver: resolver, loc: loc, lastEvent: map[string]time.Time{}}
}

// Consume 实现 tailer.Sink。
//
// # 写入失败怎么办
//
// 返回 error 让 collector 知道失败。collector 目前不会因此回退 offset
// （那是后续优化），但错误会被记入 stats 并告警——**不静默**。
//
// 这是一个明确的取舍：为了不让 offset 回退逻辑变复杂（需要多文件串行提交），
// 当前选择「如实报告失败」而不是「保证重投」。对齐用户裁定：
// 「日志不重要，投递失败就失败了，做好提示消息就行」。
func (s *Sink) Consume(ctx context.Context, lines []tailer.Line) error {
	if len(lines) == 0 {
		return nil
	}
	var payload strings.Builder
	s.mu.Lock()
	s.stats.Lines += int64(len(lines))
	s.mu.Unlock()

	written := 0
	for _, l := range lines {
		logSourceID, stream := s.resolver.Resolve(l.Path)
		if logSourceID == "" {
			s.mu.Lock()
			s.stats.UnresolvedSource++
			s.mu.Unlock()
			continue
		}
		row, err := buildRow(l, logSourceID, stream, s.eventTime(l, logSourceID))
		if err != nil {
			continue
		}
		payload.WriteString(row)
		payload.WriteByte('\n')
		written++
	}
	if written == 0 {
		return nil
	}
	n, err := s.client.InsertJSONLines(ctx, []byte(payload.String()))
	s.mu.Lock()
	if err != nil {
		s.stats.Failed += int64(written)
	} else {
		s.stats.Written += int64(n)
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("logtail: 写 VL 失败(%d 行): %w", written, err)
	}
	return nil
}

// Snapshot 返回当前统计。
func (s *Sink) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// RecordGap 记录一次「文件未读完就消失」。
func (s *Sink) RecordGap(path string, unreadBytes int64) {
	s.mu.Lock()
	s.stats.Gaps++
	s.mu.Unlock()
	slog.Warn("日志采集：文件在读完前消失（可能被压缩为归档），这段日志永久丢失",
		"path", path, "unreadBytes", unreadBytes)
}

// buildRow 把一行日志变成 VL 的 JSONL 行。
//
// # 字段选择（对齐旧链路的行形态，保证查询侧不用改）
//
//   - `_time`：完整 RFC3339（行内 `[HH:MM:SS]` + 日期 + 时区），解析不出就用当前时间
//   - `_msg`：原始行（不再做多行合并与级别剥离）
//   - `log_source_id` / `stream`：流的标签（VL 的 stream 索引靠它们）
//   - `level`：从 `[Server thread/INFO]` 这类段里提取，提不出留空
//   - `event_id`：内容指纹（源+时间+内容），供将来去重
func buildRow(l tailer.Line, logSourceID, stream, eventTime string) (string, error) {
	msg := string(l.Content)
	_, level := parseMinecraftLine(msg)
	row := map[string]string{
		"_time":         eventTime,
		"_msg":          msg,
		"log_source_id": logSourceID,
		"stream":        stream,
		"level":         level,
		// event_id 用内容指纹：同一条日志无论采几次都得相同的 id，
		// 供当前/将来「回补前去重」使用。
		"event_id": contentID(logSourceID, stream, eventTime, msg),
	}
	b, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseMinecraftLine 从 MC 日志行里提取时间与级别。
//
// 典型形态：
//
//	[18:28:02] [Server thread/INFO]: Environment: authHost=...
//	[19:07:39] [Server thread/ERROR]: Could not pass event
//
// # 为什么要解析而不是直接用采集时间
//
// 采集时间是「我们什么时候读到的」，不是「日志什么时候产生的」。
// 回补历史文件时两者可能相差几个月 —— 用采集时间会把历史日志全标成今天。
//
// # 为什么缺日期
//
// MC 日志行里只有 `HH:MM:SS`，日期要从未名推（`2026-11-06-14.log`）或不推。
// 本函数只管提取时刻，日期由调用方根据文件名补全——不在这里猜。
func parseMinecraftLine(line string) (timestamp, level string) {
	// 时间：[HH:MM:SS]
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end > 1 {
			candidate := line[1:end]
			if _, err := time.Parse("15:04:05", candidate); err == nil {
				timestamp = candidate
			}
		}
	}
	// 级别：[xxx/LEVEL]
	for _, lv := range []string{"ERROR", "WARN", "INFO", "DEBUG", "TRACE", "FATAL"} {
		if strings.Contains(line, "/"+lv+"]") {
			level = lv
			break
		}
	}
	return timestamp, level
}

// contentID 用 (源, 流, 时间, 内容) 算内容指纹。
//
// # 为什么不用字节偏移
//
// 旧实现用 (源, 代次, record_start, record_end, parser_version)。
// 那套在文件被重写/截断后偏移就变了，同一条日志得到不同 id。
// 内容指纹不依赖位置，重读、重投、回补都得同一个值——这正是去重的前提。
//
// # 哈希选择
//
// 用 FNV-1a 64 位而不是 SHA-256：
//   - 每条日志都要算，SHA-256 在这里的 CPU 开销不必要
//   - event_id 只用于「同一批内去重 / 回补前查重」，不需要密码学强度
//   - 64 位碰撞概率在百万量级下约 1e-8，可接受
//
// 输出 16 位十六进制，便于人读与日志对比。
func contentID(logSourceID, stream, ts, msg string) string {
	h := fnv.New64a()
	h.Write([]byte(logSourceID))
	h.Write([]byte{0})
	h.Write([]byte(stream))
	h.Write([]byte{0})
	h.Write([]byte(ts))
	h.Write([]byte{0})
	h.Write([]byte(msg))
	return strconv.FormatUint(h.Sum64(), 16)
}

// InstancePathResolver 按实例工作目录推导源标签。
//
// # 推导规则（对齐旧实现 instanceLivePath + 规则表）
//
// 实例日志路径形如：
//
//	/home/xxx/servers/<实例名>/logs/latest.log     （bukkit/paper）
//	/home/xxx/servers/<实例名>/proxy.log.0          （bungee）
//
// 从中取 `<实例名>` 并映射到 `inst:<实例ID>`。但**实例名 ≠ 实例 ID**，
// 映射必须由装配层提供（那需要实例注册表）。所以本函数只做「提取实例名」，
// 真正的 ID 映射通过 nameToID 回调。
//
// 为什么这样拆：路径形态是稳定的（不会因 CP 版本变化），
// 而名字→ID 的映射来自运行时注册表，两者生命周期不同。
func InstancePathResolver(nameToID func(instanceName string) (id string, ok bool)) SourceResolverFunc {
	return func(path string) (string, string) {
		name, stream := instanceNameFromPath(path)
		if name == "" {
			return "", ""
		}
		id, ok := nameToID(name)
		if !ok {
			return "", ""
		}
		return "inst:" + id + "/" + stream, stream
	}
}

// instanceNameFromPath 从日志路径提取实例名与流名。
//
// 返回 (实例名, 流名)；无法识别时返回空。
func instanceNameFromPath(path string) (name, stream string) {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	dir := filepath.Dir(clean)

	// 形态 1：.../servers/<name>/logs/latest.log 或 <name>/logs/*.log
	if filepath.Base(dir) == "logs" {
		name = filepath.Base(filepath.Dir(dir))
		stream = streamFromFileName(base)
		return name, stream
	}

	// 形态 2：.../servers/<name>/proxy.log.N（bungee）
	if strings.HasPrefix(base, "proxy.log") {
		name = filepath.Base(dir)
		stream = "stdout"
		return name, stream
	}

	// 形态 3：.../servers/<name>/<name>.log（少数核心）
	if strings.HasSuffix(base, ".log") {
		name = filepath.Base(dir)
		stream = streamFromFileName(base)
		return name, stream
	}

	return "", ""
}

// streamFromFileName 报告该文件对应哪个流。
//
// MC 的 `latest.log` 不分 stdout/stderr（都合在一个文件里），统一记 stdout。
// 如果将来采集分离的 stderr 文件，在这里区分。
func streamFromFileName(base string) string {
	return "stdout"
}
