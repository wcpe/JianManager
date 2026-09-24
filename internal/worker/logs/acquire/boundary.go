package acquire

// EventBoundaryHook 与 FR-474 normalize 协调的导出钩子。
// FileTailer 按物理行喂入；complete=true 时才允许推进 durable checkpoint。
// incomplete 多行缓冲只推进 read_position。
type EventBoundaryHook interface {
	// Feed 处理一条物理行（不含换行符）。
	// complete：逻辑事件在此行结束。
	// eventEnd：完整事件覆盖的 record_end（源位置）。
	// emit：当 complete 时是否应产生可入 WAL 的逻辑事件负载。
	Feed(line []byte, absPos, recordEnd uint64) (eventEnd uint64, complete bool, emit bool)
	// FlushPartial 崩溃/轮转前冲刷未完成缓冲。
	// keep=true 则返回可 emit 的半事件（调用方决定丢弃或入隔离）。
	FlushPartial() (payload []byte, recordEnd uint64, keep bool)
	// PendingStart 未完成多行事件的首行源位置；崩溃后从此恢复拼接。
	PendingStart() uint64
}

// LineHook 将每一物理行视为独立完整事件（默认钩子；非 FR-474 多行语义）。
type LineHook struct {
	lastStart uint64
}

// NewLineHook 创建逐行完整事件钩子。
func NewLineHook() *LineHook { return &LineHook{} }

// Feed 实现 EventBoundaryHook。
func (h *LineHook) Feed(line []byte, absPos, recordEnd uint64) (uint64, bool, bool) {
	end := recordEnd
	_ = line
	h.lastStart = absPos
	return end, true, true
}

// FlushPartial 逐行模式无未完成缓冲。
func (h *LineHook) FlushPartial() ([]byte, uint64, bool) { return nil, 0, false }

// PendingStart 返回 0。
func (h *LineHook) PendingStart() uint64 { return 0 }

// MultilineBufferHook 供 FR-474 实现的参考骨架：
// 未调用完整边界前只暴露 PendingStart，不标记 complete。
// 本包不实现 Java/Go 解析，仅保留接口形状与 incomplete 行为契约。
type MultilineBufferHook struct {
	buf       []byte
	pendingAt uint64
	// IsEventStart 由 normalize 注入：判断某行是否开启新逻辑事件。
	IsEventStart func(line []byte) bool
	// MaxPendingLines/Bytes 超时与上限（契约：超时后可 FlushPartial）。
	MaxPendingLines int
	MaxPendingBytes int
	pendingLines    int
}

// NewMultilineBufferHook 创建多行缓冲钩子。isStart 必须由调用方提供。
func NewMultilineBufferHook(isStart func(line []byte) bool) *MultilineBufferHook {
	return &MultilineBufferHook{
		IsEventStart:    isStart,
		MaxPendingLines: 500,
		MaxPendingBytes: 256 * 1024,
	}
}

// Feed 实现 EventBoundaryHook：新事件起始行会结束上一事件。
func (h *MultilineBufferHook) Feed(line []byte, absPos, recordEnd uint64) (uint64, bool, bool) {
	if len(h.buf) == 0 {
		h.pendingAt = absPos
	}
	// 若已有缓冲且本行开启新事件，则上一事件在“本行之前”结束（exclusive absPos）。
	if len(h.buf) > 0 && h.IsEventStart != nil && h.IsEventStart(line) {
		end := absPos
		h.buf = append(h.buf[:0], line...)
		h.pendingAt = absPos
		h.pendingLines = 1
		return end, true, true
	}
	h.buf = append(h.buf, line...)
	if len(h.buf) > 0 {
		h.buf = append(h.buf, '\n')
	}
	h.pendingLines++
	// 未形成完整事件：只推进 read，不 durable。
	return recordEnd, false, false
}

// FlushPartial 返回未完成缓冲。
func (h *MultilineBufferHook) FlushPartial() ([]byte, uint64, bool) {
	if len(h.buf) == 0 {
		return nil, 0, false
	}
	end := h.pendingAt + uint64(len(h.buf))
	out := make([]byte, len(h.buf))
	copy(out, h.buf)
	h.buf = h.buf[:0]
	h.pendingLines = 0
	return out, end, true
}

// PendingStart 未完成事件首行位置。
func (h *MultilineBufferHook) PendingStart() uint64 { return h.pendingAt }
