package sampling

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 汇总事件正文的固定标记与上限。
const (
	// AggregateMarker 是汇总事件正文的固定前缀，供查询面一眼识别
	// （也是「丢了多少、丢了什么」的可见出口）。
	AggregateMarker = "[采样抑制]"
	// aggregateSampleChars 是正文里单条样例的字符上限。样例要能定位问题，
	// 但一条汇总不该被样例撑得比原文还重（那就违背了成本治理的初衷）。
	aggregateSampleChars = 4096
	// aggregatePatternChars 是正文里模式预览的字符上限。
	aggregatePatternChars = 120
)

// 汇总事件写入 VL 的字段名（供查询面按规则/条数过滤）。统一 sampling_ 前缀，
// 避免与投影载荷的内置字段（level/stream/record_start…）撞名互相覆盖。
const (
	FieldRule       = "sampling_rule"
	FieldCount      = "sampling_suppressed_count"
	FieldLevel      = "sampling_level"
	FieldPattern    = "sampling_pattern"
	FieldRecordFrom = "sampling_record_start"
	FieldRecordTo   = "sampling_record_end"
	FieldTruncated  = "sampling_sample_truncated"
	FieldDegraded   = "sampling_degraded"
)

// SuppressedRun 是一段**位置相邻、同级别、同流、同模式、同规则**的被抑制事件。
//
// 「同级别」与「同流」是硬要求：跨级别的行不得混入同一汇总——否则汇总的 level 字段
// 必然撒谎，按级别的保留期也就对不上它声称的级别。
// 「位置相邻」是区间守恒的承重点（见 doc.go 的不变量 R）。
type SuppressedRun struct {
	// Rule 规则名（RuleLevelFilter / RuleBurstSuppress / …）。
	Rule string
	// Events 段内事件，按位置升序且两两相邻（rangesTouch 口径）。
	Events []logtypes.Event
}

// Span 返回该段的精确并集跨度。
//
// 之所以不是 min..max 凸包：段内事件本来就相邻，首尾即并集。
// 这里仍显式取 min(Start)/max(End) 而不是「首条 Start / 末条 End」，是为了让结论
// 不依赖调用方对输入排序的假设：相邻判定允许「后一条起点早于前一条终点」的乱序形态
// （Start <= last.End+1 即算相邻），此时「末条 End」会给出 End < Start 的非法区间，
// 而 max(End) 不会。段内事件互不重叠仍由调用方保证（相邻即衔接）。
func (r SuppressedRun) Span() logtypes.RecordRange {
	from := r.Events[0].Record.Start
	to := r.Events[0].Record.End
	for _, e := range r.Events[1:] {
		if e.Record.Start < from {
			from = e.Record.Start
		}
		if e.Record.End > to {
			to = e.Record.End
		}
	}
	return logtypes.RecordRange{Start: from, End: to}
}

// Count 返回被抑制条数。
func (r SuppressedRun) Count() int { return len(r.Events) }

// BuildAggregate 由一段被抑制事件构造汇总事件。
//
// 它走的是与普通事件**完全相同**的构造路径（logtypes.BuildEvent），因此：
//   - EventID 由 (源, Record 区间, parser 版本) 推导 ⇒ 同一段必然得到同一个 EventID；
//   - CanonicalHash 由 (事件时间, 级别, 流, 正文) 推导 ⇒ 重投时逐字节一致，
//     账本的 (EventID, CanonicalHash) 去重语义不受影响；
//   - 投影校验比对的是 canonical_content_hash/_time/_msg/level/stream，
//     汇总事件在这五项上与写出去的值一致，故照样能通过逐字段校验。
//
// 正文里不含挂钟时间：任何随时间变化的内容都会让重投产生不同的 CanonicalHash，
// 使「同一个 WAL 条目」在两次投递里长得不一样（多写一次），破坏幂等。
func BuildAggregate(run SuppressedRun, degraded bool) logtypes.Event {
	first := run.Events[0]
	// 只算一次并集跨度：Record、正文里的区间、区间字段三处必须同源，
	// 分别计算迟早会漂移（而漂移的表现是校验/缺口判据对不上，很难查）。
	span := run.Span()
	message, truncated := aggregateMessage(run)
	event := logtypes.BuildEvent(
		first.Source,
		span,
		first.EventTimeUTC,
		first.IngestTimeUTC,
		first.Level,
		first.Stream,
		message,
	)
	if event.Fields == nil {
		event.Fields = make(map[string]string, 8)
	}
	event.Fields[FieldRule] = run.Rule
	event.Fields[FieldCount] = strconv.Itoa(run.Count())
	event.Fields[FieldLevel] = NormalizeLevel(first.Level)
	event.Fields[FieldPattern] = MaskMessage(first.Message)
	event.Fields[FieldRecordFrom] = strconv.FormatUint(span.Start, 10)
	event.Fields[FieldRecordTo] = strconv.FormatUint(span.End, 10)
	event.Fields[FieldTruncated] = strconv.FormatBool(truncated)
	if degraded {
		event.Fields[FieldDegraded] = "true"
	}
	return event
}

// aggregateMessage 拼出汇总事件正文；第二个返回值报告样例是否被截断。
//
// 正文是「人看得懂 + 机器能解析」的折中：`键=值` 便于用 LogsQL 做检索与统计，
// 换行分段让它在 UI 里仍然可读。
func aggregateMessage(run SuppressedRun) (string, bool) {
	first := run.Events[0]
	last := run.Events[len(run.Events)-1]

	firstSample := TruncateRunes(first.Message, aggregateSampleChars)
	truncated := Truncated(first.Message, aggregateSampleChars)

	var b strings.Builder
	b.WriteString(AggregateMarker)
	fmt.Fprintf(&b, " 规则=%s", run.Rule)
	fmt.Fprintf(&b, " 级别=%s", levelForDisplay(first.Level))
	fmt.Fprintf(&b, " 条数=%d", run.Count())
	fmt.Fprintf(&b, " 区间=[%d,%d)", first.Record.Start, last.Record.End)
	fmt.Fprintf(&b, " 模板=%s", TruncateRunes(MaskMessage(first.Message), aggregatePatternChars))
	b.WriteString("\n首条=")
	b.WriteString(firstSample)
	// 末条只在确实有第二条且与首条不同时才写：刷屏场景里末条常与首条同模板，
	// 重复一遍只会白白放大汇总自身的体积。
	if run.Count() > 1 && last.Message != first.Message {
		lastSample := TruncateRunes(last.Message, aggregateSampleChars)
		if Truncated(last.Message, aggregateSampleChars) {
			truncated = true
		}
		b.WriteString("\n末条=")
		b.WriteString(lastSample)
	}
	return b.String(), truncated
}

func levelForDisplay(level string) string {
	if v := NormalizeLevel(level); v != "" {
		return v
	}
	// 空级别是真实形态（堆栈延续行、无级别前缀的行），如实标注而不是编造一个级别。
	return "UNKNOWN"
}

// IsAggregate 报告一条事件是否为本包产出的汇总事件（供查询面与回归使用）。
func IsAggregate(event logtypes.Event) bool {
	return strings.HasPrefix(event.Message, AggregateMarker)
}
