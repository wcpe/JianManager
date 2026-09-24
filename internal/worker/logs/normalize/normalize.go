// Package normalize 实现 FR-474 Worker 日志 Multiline 与归一化。
//
// 将行级来源转换为可追溯的 logtypes.Event：Java/MC 多行堆栈与 Caused by 归并为一条事件，
// Go slog level= 结构化行按显式级别解析；解析失败保留原文 + parse_status，绝不发明 level。
// 事件身份（event_id / parser_version / record_start|end）继承 FR-472 Shared Contracts。
//
// 本包不重新定义 FR-472；字段语义以 docs/specs/worker-log-platform-contract/spec.md 为准。
// 不触碰 ledger/acquire/vlsup/catalog。
package normalize

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ParserVersion 源分段绑定的固定解析版本。正常重放/接管不得悄悄更换；
// 重新解析属于新的 projection/dataset generation。
const ParserVersion = "1.0.0"

// ParseStatus 事件解析状态；写入 Event.Fields[FieldParseStatus]。
type ParseStatus string

const (
	// StatusOK 成功解析（含被下一时间戳边界正常闭合的多行事件）。
	StatusOK ParseStatus = "OK"
	// StatusRaw 解析失败：保留原文，level 不发明。
	StatusRaw ParseStatus = "RAW"
	// StatusPartial FlushPartial 吐出的未完成多行事件。
	StatusPartial ParseStatus = "PARTIAL"
	// StatusTruncated 超过 MaxLines/MaxBytes 的半条事件，不静默拼接下一事件。
	StatusTruncated ParseStatus = "TRUNCATED"
	// StatusTimeout 超过 Timeout 仍未闭合的半条事件。
	StatusTimeout ParseStatus = "TIMEOUT"
)

const (
	// FieldParseStatus Event.Fields 键：解析状态。
	FieldParseStatus = "parse_status"
	// FieldMessage Event.Fields 键：多行/原文消息（与 Event.Message 一致）。
	FieldMessage = "_msg"
	// FieldThread Event.Fields 键：Java 线程名（若解析到）。
	FieldThread = "thread"
	// FieldEncodingSanitized 标记正文含非法 UTF-8，已被替换为 U+FFFD。
	// 置位意味着**正文与源文件原始字节不一致**，调用方不得当作原文一致。
	FieldEncodingSanitized = "encoding_sanitized"
)

// Limits 多行缓冲上限。零值表示不限制。
type Limits struct {
	MaxLines int
	MaxBytes int
	Timeout  time.Duration
}

// Options 归一化选项。
type Options struct {
	// Source 日志源身份；ParserVersion 为空时填入本包 ParserVersion。
	Source logtypes.SourceIdentity
	// Stream stdout|stderr，写入 Event.Stream。
	Stream string
	// IngestTimeUTC Worker 接收时间（RFC3339）；空则在 New 时取 now UTC。
	IngestTimeUTC string
	// Location 解释 [HH:MM:SS] 的时区；默认 UTC。
	Location *time.Location
	// BaseTime 无日期时刻的锚点（跨午夜用）；零值在解析时取 now。
	BaseTime time.Time
	// Limits 多行上限。
	Limits Limits
	// RecordBase 源位置基址；事件 record_start/end = RecordBase + 会话内 0 起行号。
	RecordBase uint64
	// RetainEvents 是否在会话内保留全部已产出事件供 Result/Events 读取。
	//
	// 长驻流式归一化（NormalizeBoundary）**不得**开启：它从不读历史事件，而保留会让
	// 内存随会话内总行数线性增长（FR-483 阶段5 实测 192k 行 ≈141MB）。一次性批量入口
	// （ProcessLines）与需要检查全部产出的测试才开启。
	RetainEvents bool
}

// Stats 事件数与原文行数分列，禁止混用（规格 §3.1）。
type Stats struct {
	EventCount      int `json:"event_count"`
	LineCount       int `json:"line_count"`
	RawEvents       int `json:"raw_events"`
	PartialEvents   int `json:"partial_events"`
	TruncatedEvents int `json:"truncated_events"`
	TimeoutEvents   int `json:"timeout_events"`
	PendingLines    int `json:"pending_lines"`
}

// NormalizeResult 一批归一化输出。
type NormalizeResult struct {
	Events []logtypes.Event `json:"events"`
	Stats  Stats            `json:"stats"`
}

type pending struct {
	startLine int
	endLine   int
	lines     []string
	nbytes    int
	level     string
	thread    string
	eventTime time.Time
	hasTime   bool
	// startClock 超时时钟；优先事件语义时间，否则 FeedAt 传入时间。
	startClock time.Time
	hasClock   bool
	sawHeader  bool // 是否解析到事件头（时间/level）
}

type Normalizer struct {
	opts   Options
	src    logtypes.SourceIdentity
	loc    *time.Location
	ingest string
	base   time.Time

	cur      *pending
	events   []logtypes.Event
	stats    Stats
	lineIdx  int
	feedNow  time.Time // 最近一次 FeedAt 的显式时间
	hasFeedT bool
}

// New 创建归一化器。
func New(opts Options) *Normalizer {
	src := opts.Source
	if src.ParserVersion == "" {
		src.ParserVersion = ParserVersion
	}
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	ingest := opts.IngestTimeUTC
	if ingest == "" {
		ingest = time.Now().UTC().Format(time.RFC3339Nano)
	}
	base := opts.BaseTime
	return &Normalizer{
		opts:   opts,
		src:    src,
		loc:    loc,
		ingest: ingest,
		base:   base,
	}
}

// ProcessLines 批量归一化：喂完所有行后将缓冲中的半条事件按 FlushPartial 状态吐出。
// 该入口一次性返回全部产出事件，故必须保留事件历史。
func ProcessLines(lines []string, opts Options) NormalizeResult {
	opts.RetainEvents = true
	n := New(opts)
	for _, line := range lines {
		n.Feed(line)
	}
	n.FlushPartial()
	return n.Result()
}

// Feed 处理一行（无显式墙钟；超时仅在事件自带语义时间时生效）。
// 返回因本行而闭合的事件。
func (n *Normalizer) Feed(line string) []logtypes.Event {
	return n.FeedAt(line, time.Time{})
}

// FeedAt 处理一行，并用 at 作为超时时钟（零值表示不提供墙钟）。
func (n *Normalizer) FeedAt(line string, at time.Time) []logtypes.Event {
	n.stats.LineCount++
	lineNo := n.lineIdx
	n.lineIdx++
	if !at.IsZero() {
		n.feedNow = at
		n.hasFeedT = true
	}

	pl := parseLine(line, n.loc, n.effectiveBase())

	// 超时：在接纳本行前，若已有未闭合事件且时钟超限，先以 TIMEOUT 闭合。
	var out []logtypes.Event
	if n.cur != nil && n.opts.Limits.Timeout > 0 {
		if clock, ok := n.curClock(pl, at); ok {
			if clock.Sub(n.cur.startClock) > n.opts.Limits.Timeout {
				out = append(out, n.emit(n.cur, StatusTimeout))
				n.cur = nil
			}
		}
	}

	// 容量：本行将写入未闭合事件时检查 MaxLines/MaxBytes。
	if n.cur != nil && n.exceedsLimit(line) {
		out = append(out, n.emit(n.cur, StatusTruncated))
		n.cur = nil
	}

	switch {
	case pl.kind == kindEventStart:
		if n.cur != nil {
			out = append(out, n.emit(n.cur, n.closeStatus(n.cur)))
			n.cur = nil
		}
		n.cur = n.openFrom(line, lineNo, pl, at)
	case pl.kind == kindContinuation:
		// 堆栈 / Caused by / 异常 FQCN / 缩进行并入当前事件。
		if n.cur == nil {
			n.cur = n.openFrom(line, lineNo, pl, at)
			n.cur.sawHeader = false
		} else {
			n.appendToCur(line, lineNo, pl)
		}
	default:
		// 无法归入事件头或续行的文本：闭合当前事件（若有），本行以 RAW 保留原文，绝不发明 level。
		if n.cur != nil {
			out = append(out, n.emit(n.cur, n.closeStatus(n.cur)))
			n.cur = nil
		}
		raw := n.buildEvent(
			[]string{line},
			lineNo,
			lineNo,
			"",
			"",
			time.Time{},
			false,
			StatusRaw,
		)
		if n.opts.RetainEvents {
			n.events = append(n.events, raw)
		}
		out = append(out, raw)
		n.stats.EventCount++
		n.stats.RawEvents++
	}
	return out
}

func (n *Normalizer) effectiveBase() time.Time {
	if !n.base.IsZero() {
		return n.base
	}
	if n.hasFeedT && !n.feedNow.IsZero() {
		return n.feedNow
	}
	if n.opts.IngestTimeUTC != "" {
		if t, err := time.Parse(time.RFC3339Nano, n.opts.IngestTimeUTC); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, n.opts.IngestTimeUTC); err == nil {
			return t
		}
	}
	return time.Now()
}

func (n *Normalizer) curClock(next parsedLine, at time.Time) (time.Time, bool) {
	if n.cur == nil {
		return time.Time{}, false
	}
	// 优先用下一行的语义时间（同源时间轴）。
	if next.hasTime && n.cur.hasClock {
		return next.eventTime, true
	}
	if !at.IsZero() && n.cur.hasClock {
		return at, true
	}
	if next.hasTime && n.cur.hasTime {
		return next.eventTime, true
	}
	if !at.IsZero() && n.cur.hasTime {
		return at, true
	}
	return time.Time{}, false
}

func (n *Normalizer) exceedsLimit(line string) bool {
	lim := n.opts.Limits
	if lim.MaxLines > 0 && n.cur != nil && len(n.cur.lines)+1 > lim.MaxLines {
		return true
	}
	if lim.MaxBytes > 0 && n.cur != nil && n.cur.nbytes+len(line)+1 > lim.MaxBytes {
		return true
	}
	return false
}

func (n *Normalizer) openFrom(line string, lineNo int, pl parsedLine, at time.Time) *pending {
	p := &pending{
		startLine: lineNo,
		endLine:   lineNo,
		lines:     []string{line},
		nbytes:    len(line) + 1,
		level:     pl.level,
		thread:    pl.thread,
		eventTime: pl.eventTime,
		hasTime:   pl.hasTime,
		sawHeader: pl.kind == kindEventStart || pl.hasTime || pl.level != "",
	}
	switch {
	case pl.hasTime:
		p.startClock = pl.eventTime
		p.hasClock = true
	case !at.IsZero():
		p.startClock = at
		p.hasClock = true
	}
	return p
}

func (n *Normalizer) appendToCur(line string, lineNo int, pl parsedLine) {
	n.cur.lines = append(n.cur.lines, line)
	n.cur.endLine = lineNo
	n.cur.nbytes += len(line) + 1
	if pl.level != "" && n.cur.level == "" {
		n.cur.level = pl.level
	}
	if pl.thread != "" && n.cur.thread == "" {
		n.cur.thread = pl.thread
	}
	if pl.hasTime && !n.cur.hasTime {
		n.cur.eventTime = pl.eventTime
		n.cur.hasTime = true
		if !n.cur.hasClock {
			n.cur.startClock = pl.eventTime
			n.cur.hasClock = true
		}
	}
}

// closeStatus 下一事件边界闭合时的状态：成功解析→OK，否则 RAW；截断/超时优先。
func (n *Normalizer) closeStatus(p *pending) ParseStatus {
	switch {
	case p == nil:
		return StatusRaw
	case p.level != "" || p.sawHeader || p.hasTime:
		return StatusOK
	default:
		return StatusRaw
	}
}

// flushStatus FlushPartial 吐出时的状态：未完成多行→PARTIAL；其余同 closeStatus。
func (n *Normalizer) flushStatus(p *pending) ParseStatus {
	if p == nil {
		return StatusRaw
	}
	st := n.closeStatus(p)
	if st == StatusOK && len(p.lines) > 1 {
		// 多行仍未被下一时间戳闭合：显式半条状态，不静默伪装完整。
		return StatusPartial
	}
	return st
}

// FlushPartial 以显式状态吐出缓冲中的半条/未闭合事件并清空缓冲。
// 返回 (event, true)；无缓冲时 (zero, false)。
func (n *Normalizer) FlushPartial() (logtypes.Event, bool) {
	if n.cur == nil {
		return logtypes.Event{}, false
	}
	ev := n.emit(n.cur, n.flushStatus(n.cur))
	n.cur = nil
	return ev, true
}

// FlushComplete closes a finite, successfully read source such as a sealed
// gzip archive. EOF is a real event boundary there, so a valid multiline event
// is OK rather than PARTIAL. Live tails must continue to use FlushPartial.
func (n *Normalizer) FlushComplete() (logtypes.Event, bool) {
	if n.cur == nil {
		return logtypes.Event{}, false
	}
	ev := n.emit(n.cur, n.closeStatus(n.cur))
	n.cur = nil
	return ev, true
}

// DiscardPartial 丢弃缓冲中的半条事件（不产生事件）；返回丢弃的原始行数。
// 行数仍计入 Stats.LineCount，事件数不增加。
func (n *Normalizer) DiscardPartial() (int, bool) {
	if n.cur == nil {
		return 0, false
	}
	lines := len(n.cur.lines)
	n.cur = nil
	return lines, true
}

// Result 返回已产出事件与统计（含 PendingLines）。
func (n *Normalizer) Result() NormalizeResult {
	st := n.stats
	if n.cur != nil {
		st.PendingLines = len(n.cur.lines)
	}
	evs := make([]logtypes.Event, len(n.events))
	copy(evs, n.events)
	return NormalizeResult{Events: evs, Stats: st}
}

// Events 返回已产出事件切片副本。
func (n *Normalizer) Events() []logtypes.Event {
	evs := make([]logtypes.Event, len(n.events))
	copy(evs, n.events)
	return evs
}

func (n *Normalizer) emit(p *pending, st ParseStatus) logtypes.Event {
	ev := n.buildEvent(p.lines, p.startLine, p.endLine, p.level, p.thread, p.eventTime, p.hasTime, st)
	if n.opts.RetainEvents {
		n.events = append(n.events, ev)
	}
	n.stats.EventCount++
	switch st {
	case StatusRaw:
		n.stats.RawEvents++
	case StatusPartial:
		n.stats.PartialEvents++
	case StatusTruncated:
		n.stats.TruncatedEvents++
	case StatusTimeout:
		n.stats.TimeoutEvents++
	}
	return ev
}

func (n *Normalizer) buildEvent(
	lines []string,
	startLine, endLine int,
	level, thread string,
	eventTime time.Time,
	hasTime bool,
	st ParseStatus,
) logtypes.Event {
	// 损坏编码（非法 UTF-8）必须在此处净化，而不是留给投递序列化。
	//
	// 原因：事件正文进入 canonical_content_hash，但投递走 JSON 编码；JSON 会把非法字节
	// 替换为 U+FFFD，导致「hash 承诺的正文」与「VL 实际存储的正文」不是同一份
	// （FR-474 规格 §5 要求损坏编码可恢复、解析失败保留原文）。
	// 在此按已知替换规则净化后，hash 与落库正文一致，且净化事实可审计。
	message := sanitizeUTF8(joinLines(lines))
	eventTimeUTC := ""
	if hasTime {
		eventTimeUTC = eventTime.UTC().Format(time.RFC3339Nano)
	}
	src := n.src
	if src.ParserVersion == "" {
		src.ParserVersion = ParserVersion
	}
	rec := logtypes.RecordRange{
		Start: n.opts.RecordBase + uint64(startLine),
		End:   n.opts.RecordBase + uint64(endLine),
	}
	// level 可能为空（解析失败）：绝不发明；身份字段照常组装。
	ev := logtypes.BuildEvent(src, rec, eventTimeUTC, n.ingest, level, n.opts.Stream, message)
	ev.Fields = map[string]string{
		FieldParseStatus: string(st),
		FieldMessage:     message,
	}
	// 编码被净化时留下可审计标记（正文已改变，不得当作原文一致）。
	if message != joinLines(lines) {
		ev.Fields[FieldEncodingSanitized] = "true"
	}
	if thread != "" {
		ev.Fields[FieldThread] = thread
	}
	return ev
}

// sanitizeUTF8 把非法 UTF-8 字节替换为 U+FFFD，使正文可安全经 JSON 往返。
//
// 合法输入原样返回（零拷贝语义：不重建 string）。只对损坏输入做一次转换。
func sanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

func joinLines(lines []string) string {
	if len(lines) == 1 {
		return lines[0]
	}
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// ParserVersionOf 返回本包冻结的 parser_version。
func ParserVersionOf() string { return ParserVersion }
