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
func (m *Manager) AttachSpool(sink acquire.SpoolSink) {
	if m == nil || sink == nil {
		return
	}
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
