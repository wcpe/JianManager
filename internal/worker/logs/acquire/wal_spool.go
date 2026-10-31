package acquire

import (
	"log/slog"
	"sync/atomic"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// SpoolSink 是磁盘缓冲的写入接口（FR-510 的 P1.3）。
//
// 为什么在这里定义接口而不是直接 import spool 包：
//   - acquire 不应该为了一个可选的落盘能力而绑定具体实现；
//   - 测试需要注入“写入总是失败”的假实现，验证降级路径不卡死。
//
// `internal/worker/logs/spool.Spool` 天然满足本接口。
type SpoolSink interface {
	AppendEvent(key string, ev logtypes.Event) (uint64, error)
}

// spoolJob 一个待落盘的事件。
//
// 只装事件本身与源标识；**不装 WALEntry**，因为 WALEntry 含账本引用等与磁盘无关的东西。
type spoolJob struct {
	key string
	ev  logtypes.Event
}

// spoolQueueDepth 后台落盘队列深度。
//
// 取值依据：这个队列只在**积压越界即将丢弃**时才被填充，属于异常路径；
// 256 条足以吸收一阵突发，又不至于在磁盘卡顿时堆出巨量待写事件。
// 队列满 ⇒ 后台写不及 ⇒ 这些条目按原语义直接丢（不阻塞采集，这是硬约束）。
const spoolQueueDepth = 256

// SetSpoolSink 挂载磁盘缓冲。必须包在已经完成 New 的 WAL 上，且只调用一次。
//
// 传 nil 或 WAL 为 nil 时是安全的空操作（调用方不必判空）。
func (w *WAL) SetSpoolSink(sink SpoolSink) {
	if w == nil || sink == nil {
		return
	}
	w.mu.Lock()
	if w.spoolSink != nil {
		w.mu.Unlock()
		return
	}
	w.spoolSink = sink
	ch := make(chan spoolJob, spoolQueueDepth)
	w.spoolCh = ch
	w.mu.Unlock()

	// 后台落盘协程：唯一的磁盘 IO 发生处。
	//
	// 它不持 w.mu（那是硬约束：持锁做磁盘 IO 会让整个源的采集停摆）。
	go w.runSpoolWriter(ch, sink)
}

// runSpoolWriter 消费落盘队列。
//
// 退出条件：channel 被关闭。目前 WAL 没有 Close 生命周期，所以它随进程结束。
func (w *WAL) runSpoolWriter(ch <-chan spoolJob, sink SpoolSink) {
	for job := range ch {
		if _, err := sink.AppendEvent(job.key, job.ev); err != nil {
			atomic.AddInt64(&w.spoolFailed, 1)
			// 单条失败不阻断后续：队列里还有别的源的事件要继续落。
			slog.Warn("日志缓冲落盘失败（该条仍按原语义丢弃）",
				"logSourceID", job.key, "eventID", job.ev.EventID, "error", err)
			continue
		}
		atomic.AddInt64(&w.spoolSaved, 1)
	}
}

// offerSpoolLocked 把一批即将被丢弃的条目塞进落盘队列。
//
// 调用方**必须持 w.mu**。本函数只做内存拷贝与非阻塞 channel 发送，**不做磁盘 IO**。
//
// 返回实际入队的条数（其余因队列满而被跳过，那些条目仍按原语义丢弃）。
func (w *WAL) offerSpoolLocked(cut int) int {
	if w.spoolCh == nil || cut <= 0 {
		return 0
	}
	offered := 0
	for i := 0; i < cut; i++ {
		ev := w.entries[i].Event
		if ev.EventID == "" {
			// 空条目（理论上不会出现在这个前缀里）：跳过。
			continue
		}
		select {
		case w.spoolCh <- spoolJob{key: w.key.LogSourceID, ev: ev}:
			offered++
		default:
			// 队列满：后台写不及或磁盘卡。非阻塞放弃是该设计的核心——
			// 绝不为了落盘而阻塞采集（那正是 2026-10-29 裁定要避免的）。
			atomic.AddInt64(&w.spoolSkipped, 1)
		}
	}
	return offered
}

// SpoolStats 落盘侧读数（供观测与测试）。
type SpoolStats struct {
	// Saved 成功写入磁盘缓冲的条数。
	Saved int64
	// Failed 写缓冲失败的条数。
	Failed int64
	// Skipped 因队列满而未尝试写缓冲的条数（这些条目被真丢）。
	Skipped int64
	// Pending 尚在队列中未处理的条数。
	Pending int
}

// SpoolStats 返回落盘侧读量。
func (w *WAL) SpoolStats() SpoolStats {
	if w == nil {
		return SpoolStats{}
	}
	w.mu.Lock()
	ch := w.spoolCh
	w.mu.Unlock()
	s := SpoolStats{
		Saved:   atomic.LoadInt64(&w.spoolSaved),
		Failed:  atomic.LoadInt64(&w.spoolFailed),
		Skipped: atomic.LoadInt64(&w.spoolSkipped),
	}
	if ch != nil {
		s.Pending = len(ch)
	}
	return s
}

// spooledNumber 供测试断言用：当前有多少条已交给磁盘缓冲。
func (w *WAL) spooledCount() int64 {
	return atomic.LoadInt64(&w.spoolSaved)
}
