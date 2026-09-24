package pipeline

import (
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

type lineSpan struct {
	start uint64
	end   uint64
}

// NormalizeBoundary 实现 acquire.EventBoundaryHook，并将物理行喂给 normalize.Normalizer。
//
// 接线约定：
//   - complete=true 且 emit=false：normalize 已产出逻辑事件，由 Pipeline 从 DrainEvents 取走；
//     不让 acquire.FileTailer 再自建事件，避免双计。
//   - 未闭合多行：只暴露 PendingStart，FileTailer 只推进 read_position。
//   - normalize 以行号为 record；本 hook 按绝对源位置重映射 event_id。
//
// 内存约定（FR-484 阶段5）：linePos 是**滑动窗口**而非全会话历史。
// normalize 的行号是会话内单调递增的，若把每行位置全部留住，内存会随会话总行数
// 线性增长（192k 行 ≈130MB）。但 remap 只会引用「当前未闭合多行事件所覆盖的行」，
// 因此 DrainEvents 之后可以把窗口收缩到该事件的首行；lineBase 记录窗口首行对应的
// 会话行号，remap/PendingStart 都以它为基准换算下标。
type NormalizeBoundary struct {
	n *normalize.Normalizer
	// linePos 窗口内每行的绝对源位置；下标 +lineBase = 会话内行号。
	linePos []lineSpan
	// lineBase 窗口首行对应的会话内行号（恒有 lineBase+len(linePos) == 已喂入行数）。
	lineBase int
	// completed 已归一化、待 Pipeline 取走的事件。
	completed []logtypes.Event
	// lastStats 最近一次统计（事件数 vs 行数分列）。
	lastStats normalize.Stats
	// seq 会话内事件序（诊断）。
	seq int
}

// NewNormalizeBoundary 创建边界钩子。opts.Source.ParserVersion 为空时使用 normalize.ParserVersion。
func NewNormalizeBoundary(opts normalize.Options) *NormalizeBoundary {
	if opts.Source.ParserVersion == "" {
		opts.Source.ParserVersion = normalize.ParserVersion
	}
	return &NormalizeBoundary{n: normalize.New(opts)}
}

// Feed 实现 acquire.EventBoundaryHook。
func (b *NormalizeBoundary) Feed(line []byte, absPos, recordEnd uint64) (eventEnd uint64, complete bool, emit bool) {
	span := lineSpan{start: absPos, end: recordEnd}
	out := b.n.Feed(string(line))
	b.linePos = append(b.linePos, span)

	if len(out) == 0 {
		// 未形成完整事件：只推进 read。
		return span.end, false, false
	}
	var lastEnd uint64
	for _, ev := range out {
		remapped := b.remap(ev)
		b.completed = append(b.completed, remapped)
		lastEnd = remapped.Record.End
		b.seq++
	}
	if lastEnd == 0 {
		lastEnd = span.end
	}
	// complete 且由本 hook 持有事件负载；emit=false 阻止 FileTailer 重复 BuildEvent。
	return lastEnd, true, false
}

// FlushPartial 实现 acquire.EventBoundaryHook：冲刷 normalize 未闭合事件。
func (b *NormalizeBoundary) FlushPartial() ([]byte, uint64, bool) {
	ev, ok := b.n.FlushPartial()
	if !ok {
		return nil, 0, false
	}
	remapped := b.remap(ev)
	b.completed = append(b.completed, remapped)
	b.seq++
	_ = b.n.Result()
	return []byte(remapped.Message), remapped.Record.End, true
}

// FlushComplete closes a finite archive at EOF without labelling a valid final
// multiline event as a live-stream partial.
func (b *NormalizeBoundary) FlushComplete() ([]byte, uint64, bool) {
	ev, ok := b.n.FlushComplete()
	if !ok {
		return nil, 0, false
	}
	remapped := b.remap(ev)
	b.completed = append(b.completed, remapped)
	b.seq++
	_ = b.n.Result()
	return []byte(remapped.Message), remapped.Record.End, true
}

// PendingStart 实现 acquire.EventBoundaryHook。
func (b *NormalizeBoundary) PendingStart() uint64 {
	res := b.n.Result()
	if res.Stats.PendingLines == 0 || len(b.linePos) == 0 {
		return 0
	}
	// pending 从会话末尾未闭合行的首行开始；normalize 不直接暴露 start，用 stats 推算。
	// 推算在窗口坐标系内进行：末行下标是 len(linePos)-1。
	pending := res.Stats.PendingLines
	idx := len(b.linePos) - pending
	if idx < 0 {
		// pending 超过窗口（窗口已收缩到该事件首行时不应发生）：退化为窗口首行。
		idx = 0
	}
	if idx >= len(b.linePos) {
		return 0
	}
	return b.linePos[idx].start
}

// DrainEvents 取走并清空已归一化事件，并把 linePos 窗口收缩到当前未闭合多行事件的首行。
//
// 收缩依据：remap 只会引用「该事件覆盖的行」。会话中未闭合事件的首行由
// 喂入行数减去 PendingLines 得到；它之前的行位置此后不可能再被引用。
func (b *NormalizeBoundary) DrainEvents() []logtypes.Event {
	b.trimLinePos()
	if len(b.completed) == 0 {
		return nil
	}
	out := make([]logtypes.Event, len(b.completed))
	copy(out, b.completed)
	// 释放底层数组：只截断长度会让旧事件对象继续被数组持有。
	b.completed = nil
	b.lastStats = b.n.Result().Stats
	return out
}

// trimLinePos 把 linePos 窗口收缩到当前未闭合多行事件的首行（无 pending 时清空）。
//
// want = 首个 pending 行的下标：其之前的行已被 remap 消费过，不会再次被引用。
// want <= 0 表示「没有可裁的行」（pending 事件从窗口首行开始），必须原样保留——
// 这些行正是 FlushPartial/FlushComplete 关闭该事件时 remap 要用的。
func (b *NormalizeBoundary) trimLinePos() {
	pending := b.n.Result().Stats.PendingLines
	want := len(b.linePos) - pending
	if want <= 0 {
		return
	}
	if want >= len(b.linePos) {
		// 无未闭合行：窗口内所有位置都已不可能再被引用。
		b.lineBase += len(b.linePos)
		b.linePos = nil
		return
	}
	// 保留末尾 pending 行；用 copy 使旧数组可被回收。
	kept := make([]lineSpan, pending)
	copy(kept, b.linePos[want:])
	b.lineBase += want
	b.linePos = kept
}

// Stats 返回最近一次统计快照。
func (b *NormalizeBoundary) Stats() normalize.Stats {
	st := b.n.Result().Stats
	b.lastStats = st
	return st
}

// EventsEmitted 返回本边界累计产出事件数。
func (b *NormalizeBoundary) EventsEmitted() int { return b.seq }

// remap 将 normalize 的行号 record 映射为绝对源位置，并重算 event_id。
//
// normalize 的行号是**会话内**行号，需减去 lineBase 换算成窗口下标。
func (b *NormalizeBoundary) remap(ev logtypes.Event) logtypes.Event {
	startLine := int(ev.Record.Start) - b.lineBase
	endLine := int(ev.Record.End) - b.lineBase
	if len(b.linePos) == 0 {
		return ev
	}
	if startLine < 0 || startLine >= len(b.linePos) {
		// 事件引用了窗口外的行：说明窗口收缩超过了安全边界（缺陷），原样返回以便暴露。
		return ev
	}
	if endLine < startLine {
		endLine = startLine
	}
	if endLine >= len(b.linePos) {
		endLine = len(b.linePos) - 1
	}
	rec := logtypes.RecordRange{
		Start: b.linePos[startLine].start,
		End:   b.linePos[endLine].end,
	}
	src := ev.Source
	if src.ParserVersion == "" {
		src.ParserVersion = normalize.ParserVersion
	}
	out := logtypes.BuildEvent(src, rec, ev.EventTimeUTC, ev.IngestTimeUTC, ev.Level, ev.Stream, ev.Message)
	out.Fields = ev.Fields
	return out
}
