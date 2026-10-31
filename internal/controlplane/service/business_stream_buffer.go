package service

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// 业务事件流环形缓冲（§4，见 ADR-106）。
//
// 设计要点（与规格逐条对应）：
//   - **双阈值**：时间窗（默认 30s）或容量（默认 20 万条），先到者生效；
//   - **固定切片 + 头尾指针**，不使用通用环形队列库：20 万个 *Frame 若走链表/扩容切片，
//     会持续产生后备数组扩容与老对象回收，CP 在 2000 事件/秒下 GC 压力不可控；
//   - **非持久**：不落盘、不落库，进程重启即失；
//   - 容量溢出丢最旧并递增 droppedCount；时间窗老化属于窗口的正常行为，不计入 dropped
//     （它由 replay_gap 的可观测计数体现）。
const (
	// BusinessStreamBufferCapacity 环形缓冲默认容量上限（§4.1 推导：约 100MB）。
	BusinessStreamBufferCapacity = 200_000
	// BusinessStreamBufferWindow 环形缓冲默认时间窗（覆盖浏览器/网络抖动的常见断线重连）。
	BusinessStreamBufferWindow = 30 * time.Second
)

// BusinessFrame 是一条待下发的业务事件帧（SSE `event: business.event` 的 data 段）。
//
// `payload` 是**不透明载荷**：JM 只做 JSON 透传，不解析、不校验业务内容（§5）。
// 未导出成员是缓冲内部状态，不参与序列化。
type BusinessFrame struct {
	Node         string          `json:"node,omitempty"`
	Instance     uint            `json:"instance,omitempty"`
	InstanceName string          `json:"instanceName,omitempty"`
	Domain       string          `json:"domain"`
	// Action 域内动作（deposit / mail_sent …）。JM 只透传，不校验、不枚举。
	// 下游（Loom）用它区分同一域内的不同事件类型，不必自己挖 payload。
	Action       string          `json:"action,omitempty"`
	Topic        string          `json:"topic,omitempty"`
	DedupKey     string          `json:"dedupKey,omitempty"`
	Seq          int64           `json:"seq"`
	OccurredAt   int64           `json:"occurredAt"`
	Payload      json.RawMessage `json:"payload"`

	// rendered 是预渲染的整帧 SSE 字节（`id:` + `event:` + `data:` + 空行）。
	// 一次序列化、全部订阅者共享只读切片：把「每订阅者各序列化一次」的 CPU 从扇出热路径上移走。
	rendered []byte
	// enqueuedAt 是写入缓冲的时刻，仅用于时间窗老化；与 OccurredAt（探针侧事件时间）无关。
	enqueuedAt time.Time
}

// Bytes 返回预渲染的 SSE 帧字节（含结尾空行）。字节切片为只读共享，调用方不得修改。
func (f *BusinessFrame) Bytes() []byte {
	if f == nil {
		return nil
	}
	return f.rendered
}

// renderLocked 渲染 SSE 帧。须在 seq 已确定后调用。
//
// 载荷无法构成合法 JSON 时（探针侧上游坏帧）降级为 JSON 字符串，既保证 SSE data 段永远合法，
// 也不丢内容——这里判断的是「能否放进 JSON」，不是「校验业务语义」。
func (f *BusinessFrame) renderLocked() {
	if len(f.Payload) == 0 {
		f.Payload = json.RawMessage("null")
	} else if !json.Valid(f.Payload) {
		f.Payload = json.RawMessage(quoteJSONString(string(f.Payload)))
	}
	data, err := json.Marshal(f)
	if err != nil {
		// 理论不可达（Payload 已归一为合法 JSON）；兜底为不含载荷的信封，绝不 panic。
		data = []byte(`{"domain":` + quoteJSONString(f.Domain) + `,"seq":` + strconv.FormatInt(f.Seq, 10) + `,"payload":null}`)
	}
	b := make([]byte, 0, len(data)+48)
	b = append(b, "id: "...)
	b = strconv.AppendInt(b, f.Seq, 10)
	b = append(b, "\nevent: business.event\ndata: "...)
	b = append(b, data...)
	b = append(b, "\n\n"...)
	f.rendered = b
}

// quoteJSONString 把任意字符串安全编码为 JSON 字符串字面量。
func quoteJSONString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// BusinessStreamBuffer 是业务事件的内存环形缓冲（§4.2）。
//
// 并发模型：Append 持写锁，读取方法持读锁。锁内只做切片搬运与指针赋值，
// 唯一的分配是「每条事件一次的帧渲染」——这是必要的，且比每订阅者各渲染一次便宜一个数量级。
type BusinessStreamBuffer struct {
	mu sync.RWMutex
	// buf 是固定容量的槽位数组；head 指向最旧，有效区间为 [head, head+size) 对 capacity 取模。
	buf      []*BusinessFrame
	head     int
	size     int
	capacity int
	window   time.Duration
	// oldestSeq/newestSeq 是已写入的位点边界；空缓冲时 oldestSeq 为 0。
	// 进程重启后两者从 0 重新开始（非持久§4.4）。
	oldestSeq int64
	newestSeq int64
	// dropped 统计因容量打满而丢弃的最旧条数（时间窗老化不计）。
	dropped uint64

	now func() time.Time
}

// NewBusinessStreamBuffer 创建环形缓冲。capacity <= 0 取默认 20 万条，window <= 0 取默认 30 秒。
func NewBusinessStreamBuffer(capacity int, window time.Duration) *BusinessStreamBuffer {
	if capacity <= 0 {
		capacity = BusinessStreamBufferCapacity
	}
	if window <= 0 {
		window = BusinessStreamBufferWindow
	}
	return &BusinessStreamBuffer{
		buf:      make([]*BusinessFrame, capacity),
		capacity: capacity,
		window:   window,
		now:      time.Now,
	}
}

// Append 写入一帧并返回其位点 seq。
//
// seq 的取值规则：
//   - frame.Seq <= 0（生产路径：调用方不预置位点）→ 分配 newestSeq+1；
//   - frame.Seq > newestSeq（调用方自带位点）→ 采用它，跳过的位点即自然形成补读缺口；
//   - frame.Seq <= newestSeq（重复/回退）→ 仍分配 newestSeq+1，保证缓冲内 seq 严格递增。
//
// **为什么位点由缓冲分配**：SSE 的补读依赖 `id` 全局单调可比，而探针侧业务序号仅在
// 同一 domain 内单调（§9 风险表），无法作为跨实例的补读位点。故传输层位点在此统一给出，
// 同时它就是 SSE 的 `id` 与帧内 `seq`——满足「id 必须等于 seq」。
//
// 写入前先按时间窗老化，再在容量打满时丢最旧（dropped++）。整段持写锁，保证读者看到一致的窗口。
func (r *BusinessStreamBuffer) Append(f *BusinessFrame) int64 {
	if r == nil || f == nil {
		return 0
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	// ① 时间窗老化：窗口外的旧事件已不可补读。
	r.pruneLocked(now)
	// ② 容量打满：丢最旧并计数（§4.3：不逐个通知订阅者，等其补读时收 replay_gap）。
	for r.size >= r.capacity {
		r.popLocked()
		r.dropped++
	}

	if f.Seq > r.newestSeq {
		r.newestSeq = f.Seq
	} else {
		r.newestSeq++
		f.Seq = r.newestSeq
	}
	f.enqueuedAt = now
	f.renderLocked()

	idx := (r.head + r.size) % r.capacity
	r.buf[idx] = f
	r.size++
	r.oldestSeq = r.buf[r.head].Seq
	return f.Seq
}

// pruneLocked 物理移除时间窗外的旧帧。调用方须持写锁。
func (r *BusinessStreamBuffer) pruneLocked(now time.Time) {
	for r.size > 0 {
		if now.Sub(r.buf[r.head].enqueuedAt) <= r.window {
			return
		}
		r.popLocked()
	}
}

// popLocked 移除最旧一帧。调用方须持写锁。
func (r *BusinessStreamBuffer) popLocked() {
	if r.size == 0 {
		return
	}
	r.buf[r.head] = nil // 断开引用，避免被缓冲持有的帧长期无法回收
	r.head = (r.head + 1) % r.capacity
	r.size--
	if r.size == 0 {
		r.head = 0
		r.oldestSeq = 0
	} else {
		r.oldestSeq = r.buf[r.head].Seq
	}
}

// windowStartLocked 在**不修改缓冲**的前提下，跳过已超出时间窗的旧帧，返回有效区间起点与条数。
// 读取路径必须用它：否则长时间无新事件时，窗口外的旧帧会被当作「仍可补读」返回。
// 调用方须持读锁（或写锁）。
func (r *BusinessStreamBuffer) windowStartLocked(now time.Time) (start int, count int) {
	if r.size == 0 {
		return 0, 0
	}
	for start < r.size {
		if now.Sub(r.buf[(r.head+start)%r.capacity].enqueuedAt) <= r.window {
			break
		}
		start++
	}
	return start, r.size - start
}

// Since 返回位点 seq 之后的所有事件帧，并给出是否发生补读缺口。
//
// 第二个返回值为 true 表示 `seq+1` 早于窗口内最旧位点，即 seq 与窗口之间存在**必然补不到**的
// 区间；调用方必须先发 `event: replay_gap` 再从窗口起点补发（§3.3）。
// seq <= 0（未带 Last-Event-ID）由调用方自行短路，本方法按「从窗口起点开始」处理。
func (r *BusinessStreamBuffer) Since(seq int64) ([]*BusinessFrame, bool) {
	frames, gap, _ := r.SinceWithBoundary(seq)
	return frames, gap
}

// SinceWithBoundary 与 Since 相同，额外返回本次快照观察到的 newestSeq。
//
// SSE 处理器必须用这个边界做「补读快照 + 实时队列」的去重：边界与快照在同一把锁内取，
// 因此 `frame.Seq <= boundary` 的实时帧必然是补读已覆盖的重叠项，> boundary 的一定是新事件。
// 若分两次读取（先 Since 再 NewestSeq），两次之间到达的事件会被补读与实时流各下发一次。
func (r *BusinessStreamBuffer) SinceWithBoundary(seq int64) ([]*BusinessFrame, bool, int64) {
	if r == nil {
		return nil, false, 0
	}
	now := r.now()
	r.mu.RLock()
	defer r.mu.RUnlock()

	start, count := r.windowStartLocked(now)
	if count == 0 {
		return nil, false, r.newestSeq
	}
	oldest := r.buf[(r.head+start)%r.capacity].Seq
	newest := r.buf[(r.head+r.size-1)%r.capacity].Seq

	if seq+1 < oldest {
		// 位点早于窗口起点：缺口成立，从窗口起点整段补发。
		return r.copyRangeLocked(start, r.size), true, newest
	}
	if seq < 0 {
		seq = 0
	}
	if seq >= newest {
		// 位点已到（或超过）当前最新：无需补发。位点超前属异常，由调用方记 WARN。
		return nil, false, newest
	}
	// 位点在窗口内：从头顺序扫到第一个 seq 更大的位置。
	from := start
	for from < r.size && r.buf[(r.head+from)%r.capacity].Seq <= seq {
		from++
	}
	return r.copyRangeLocked(from, r.size), false, newest
}

// copyRangeLocked 复制 [from, end) 区间的帧指针（只复制指针，不复制帧体）。调用方须持读锁。
func (r *BusinessStreamBuffer) copyRangeLocked(from, end int) []*BusinessFrame {
	if end <= from {
		return nil
	}
	out := make([]*BusinessFrame, 0, end-from)
	for i := from; i < end; i++ {
		out = append(out, r.buf[(r.head+i)%r.capacity])
	}
	return out
}

// OldestSeq 返回窗口内最旧位点（窗口为空或已全部过期时为 0）。
func (r *BusinessStreamBuffer) OldestSeq() int64 {
	if r == nil {
		return 0
	}
	now := r.now()
	r.mu.RLock()
	defer r.mu.RUnlock()
	start, count := r.windowStartLocked(now)
	if count == 0 {
		return 0
	}
	return r.buf[(r.head+start)%r.capacity].Seq
}

// NewestSeq 返回已写入的最新位点（从未写入时为 0）。
func (r *BusinessStreamBuffer) NewestSeq() int64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.newestSeq
}

// Size 返回窗口内当前有效条数。
func (r *BusinessStreamBuffer) Size() int {
	if r == nil {
		return 0
	}
	now := r.now()
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, count := r.windowStartLocked(now)
	return count
}

// Dropped 返回因容量打满而丢弃的最旧条数。
func (r *BusinessStreamBuffer) Dropped() uint64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dropped
}

// Capacity 返回容量上限（诊断用）。
func (r *BusinessStreamBuffer) Capacity() int {
	if r == nil {
		return 0
	}
	return r.capacity
}

// Window 返回时间窗（诊断用）。
func (r *BusinessStreamBuffer) Window() time.Duration {
	if r == nil {
		return 0
	}
	return r.window
}
