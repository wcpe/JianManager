package ingest

import (
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
)

// AttachSpool 把磁盘缓冲挂到**当前所有**管道的 WAL 上（FR-510）。
//
// # 必须在管道建好之后调
//
// WAL 是每个管道自己持有的，所以「挂缓冲」只能逐个挂。本方法遍历当前已登记的管道。
// 后续新建的管道**不会**自动挂上——所以调用方应该在采集运行时启动完成后、
// 且新源登记路径也会重复调用本方法（它是幂等的：SetSpoolSink 已挂过就跳过）。
//
// # 幂等
//
// `WAL.SetSpoolSink` 内部有「已挂则跳过」的判断，所以重复调用安全。
//
// # 为什么默认拒绝挂载（2026-11-05）
//
// FR-510 的 spool 在本快照里**只有写端**：`spool.Spool` 的 `Peek` / `PeekEvents` /
// `Ack` 在 spool 包外零调用（全仓只有它自己的测试在用）。也就是说写进去的事件
// **永远不会被重投**。
//
// 那不叫「缓冲」，叫「换个地方丢」，而且比直接丢更坏：
//   - 缺口 detail 会因为「交给了磁盘缓冲」而显得数据得救了（易误导排障方向）；
//   - 磁盘上堆着一堆永远不会被读的数据，还会撞容量上限后再丢一轮。
//
// 所以装配层必须先声明消费者已到位，否则本方法直接拒绝并大声告警。
// 将来真的把读端（`PeekEvents → 投递 → Ack`）接上了，就把
// `Options.SpoolConsumerReady` 置为 true，这里会自动放行。
func (m *Manager) AttachSpool(sink acquire.SpoolSink) {
	if m == nil || sink == nil {
		return
	}
	if !SpoolConsumerAvailable() {
		slog.Error("磁盘缓冲未挂接：本构建的 spool 只有写端、没有消费者，写进去的事件不会被重投；"+
			"挂上它只会把「丢弃」打扮成「已缓冲」",
			"hint", "要么把 log_spool.enabled 保持关闭，要么先接上读端")
		return
	}
	m.attachSpoolUnchecked(sink)
}

// SpoolConsumerAvailable 报告本构建是否真的具备 spool 读端（`PeekEvents` + `Ack`）。
//
// 它是 `AttachSpool` 的门禁，也是测试可以直接断言的事实。改这个值之前请先读
// `AttachSpool` 的注释：开启它就必须同步把消费者接上，否则审计面会说谎。
func SpoolConsumerAvailable() bool {
	return spoolConsumerReady
}

// spoolConsumerReady 是编入本二进制的常量，不是可配开关。
//
// 为什么不做成配置项：它不是运维选项而是**构建能力**。「写端在、读端不在」是代码事实，
// 不该让运维以为改一行 yml 就能把数据救回来。
const spoolConsumerReady = false

// attachSpoolUnchecked 是实际挂载（已有消费者时的路径，以及将来接上读端后复用）。
func (m *Manager) attachSpoolUnchecked(sink acquire.SpoolSink) {
	m.mu.Lock()
	pipes := make([]interface{ WAL() *acquire.WAL }, 0, len(m.pipes))
	for _, p := range m.pipes {
		pipes = append(pipes, walProvider{p})
	}
	m.mu.Unlock()

	attached := 0
	for _, wp := range pipes {
		if w := wp.WAL(); w != nil {
			w.SetSpoolSink(sink)
			attached++
		}
	}
	slog.Info("磁盘缓冲已挂接到采集管道", "pipes", attached)
}

// walProvider 是把 pipeline 适配成「能提供 WAL」的极小包装。
//
// 为什么不直接用一个 map 存 WAL：管道是 WAL 的拥有者，从管道取才是单一路径；
// 另存一份 WAL 指针会让「换管道」时出现两份不一致的引用。
type walProvider struct {
	p interface{ WAL() *acquire.WAL }
}

func (w walProvider) WAL() *acquire.WAL { return w.p.WAL() }
