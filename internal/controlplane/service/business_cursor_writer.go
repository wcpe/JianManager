package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 业务事件位点账本写入器（ADR-107 §2：写入异步、批量、不阻塞扇出）。
//
// 设计要点（逐条对应 ADR-107）：
//   - **旁路**：扇出热路径只把一条位点塞进有界队列，落库完全异步。写失败只记 WARN + 计数，
//     绝不影响事件投递——位点是「恢复用的旁路」，不是主链路；
//   - **有界且丢最旧**：队列满即丢最旧并递增 dropped。位点丢失的后果是「Loom 保守多拉一次快照」，
//     可接受；反向（阻塞扇出）会让整条事件通道跟着数据库抖动，不可接受；
//   - **批量**：默认满 500 条或满 1 秒先到者触发，避免逐条 INSERT 把 SQLite/MySQL 打满；
//   - **优雅停机 flush**：Close 时排空队列再退出，尽量不丢已入队位点。
//
// 与 BusinessStreamBuffer 的关系：缓冲是**非持久**的短窗口（30s/20 万条），本账本是**持久**的
// 长窗口（默认 7 天）。二者只共享 streamSeq，互不依赖。
const (
	// BusinessCursorQueueSize 位点队列默认容量（ADR-107 §2：默认 10000）。
	BusinessCursorQueueSize = 10_000
	// BusinessCursorBatchSize 单批落库条数阈值（ADR-107 §2：默认 500 条）。
	BusinessCursorBatchSize = 500
	// BusinessCursorFlushInterval 定时落库间隔（ADR-107 §2：默认 1 秒）。
	BusinessCursorFlushInterval = time.Second
	// BusinessCursorWriteTimeout 单次批量写入的超时，避免数据库挂起时占住写入协程无限期。
	BusinessCursorWriteTimeout = 10 * time.Second
)

// BusinessCursorMetrics 是位点写入器的内存计数快照（ADR-107：反馈队列健康度）。
//
// 全部为旁路指标：任何一个数字变差都**不影响**事件投递，只影响「缺口可检测的精度」。
type BusinessCursorMetrics struct {
	// Queued 累计成功入队的位点数。
	Queued uint64
	// Written 累计成功落库的位点数。
	Written uint64
	// Dropped 因队列打满而丢弃的最旧位点数。
	Dropped uint64
	// Failed 落库失败的行数（按行计，不按批计，便于与 Written 对照）。
	Failed uint64
	// Depth 当前队列深度（瞬时值）。
	Depth int
	// Capacity 队列容量（诊断用）。
	Capacity int
}

// BusinessCursorWriter 是位点账本的异步入库器（有界队列 + 批量落库）。
//
// 并发模型：Enqueue 只做一次非阻塞的「入队或丢最旧」，不持任何跨调用锁；
// 唯一持有 mu 的路径是丢弃最旧元素与取快照，临界区只含一次切片头移动。
type BusinessCursorWriter struct {
	db *gorm.DB

	mu   sync.Mutex
	ring []model.BusinessEventCursor
	// head 指向最旧元素；size 为有效条数。满时先推进 head（丢最旧）再写入。
	head int
	size int

	notify chan struct{}
	stop   chan struct{}
	done   chan struct{}

	// closeOnce 保证 Close 幂等（既可能被 defer 调用，也可能被测试多次调用）；
	// startOnce 保证 Start 只起一个消费协程。
	closeOnce sync.Once
	startOnce sync.Once
	// closed 置位后 Enqueue 直接丢弃：停机期间不再往将被丢弃的环里写。
	closed atomic.Bool
	// started 记录消费协程是否已启动：未启动时 Enqueue 仍可入队（测试可直接手动 flush）。
	started atomic.Bool

	queued  atomic.Uint64
	written atomic.Uint64
	dropped atomic.Uint64
	failed  atomic.Uint64

	// flushEvery / batchSize 可在测试中收窄，生产走默认常量。
	flushEvery time.Duration
	batchSize  int
}

// BusinessCursorWriterOptions 是写入器的可调参数（零值走 ADR-107 默认）。
type BusinessCursorWriterOptions struct {
	// QueueSize 有界队列容量，<=0 取默认 10000。
	QueueSize int
	// BatchSize 单批落库条数，<=0 取默认 500。
	BatchSize int
	// FlushInterval 定时落库间隔，<=0 取默认 1 秒。
	FlushInterval time.Duration
}

// NewBusinessCursorWriter 创建位点写入器（未启动消费协程，调用 Start 启动）。
// db 为 nil 时所有入队位点会被静默丢弃（等价「未装配账本」，不影响投递）。
func NewBusinessCursorWriter(db *gorm.DB, opts BusinessCursorWriterOptions) *BusinessCursorWriter {
	size := opts.QueueSize
	if size <= 0 {
		size = BusinessCursorQueueSize
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = BusinessCursorBatchSize
	}
	every := opts.FlushInterval
	if every <= 0 {
		every = BusinessCursorFlushInterval
	}
	return &BusinessCursorWriter{
		db:         db,
		ring:       make([]model.BusinessEventCursor, size),
		notify:     make(chan struct{}, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		flushEvery: every,
		batchSize:  batch,
	}
}

// Capacity 返回队列容量（诊断/测试）。
func (w *BusinessCursorWriter) Capacity() int {
	if w == nil {
		return 0
	}
	return cap(w.ring)
}

// Start 启动后台消费协程（幂等）。
func (w *BusinessCursorWriter) Start() {
	if w == nil {
		return
	}
	w.startOnce.Do(func() {
		w.started.Store(true)
		go w.run()
	})
}

// Enqueue 非阻塞地把一条位点投给写入器。
//
// **绝不阻塞、绝不返回错误**：这是扇出热路径上唯一的调用点，任何阻塞或分支都直接
// 放大成事件通道的抖动。queue 满时丢最旧（dropped++）——保守多拉一次快照的代价可接受。
func (w *BusinessCursorWriter) Enqueue(c model.BusinessEventCursor) {
	if w == nil || w.closed.Load() {
		return
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}

	w.mu.Lock()
	if w.size >= len(w.ring) {
		// 丢最旧：推进 head 并覆盖该槽位，size 保持不变。
		w.ring[w.head] = c
		w.head = (w.head + 1) % len(w.ring)
		w.mu.Unlock()
		w.dropped.Add(1)
		w.queued.Add(1)
		// 队列刚打过满：唤醒写入协程尽快排水，避免继续丢。
		w.signal()
		return
	}
	idx := (w.head + w.size) % len(w.ring)
	w.ring[idx] = c
	w.size++
	full := w.size >= w.batchSize
	w.mu.Unlock()

	w.queued.Add(1)
	if full {
		// 攒够一批：提前唤醒，让批量写不必等满 1 秒。
		w.signal()
	}
}

// signal 非阻塞地唤醒写入协程（容量 1 的信道天然合并重复通知）。
func (w *BusinessCursorWriter) signal() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// run 是后台消费循环：批量到量 / 定时 / 停机三选一触发落库。
func (w *BusinessCursorWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.flushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			// 停机：把剩余位点排空（可能多批），然后返回让 Close 的等待结束。
			for w.flush() > 0 {
			}
			return
		case <-w.notify:
			for w.flush() > 0 {
			}
		case <-ticker.C:
			for w.flush() > 0 {
			}
		}
	}
}

// flush 取一批（至多 batchSize 条）落库，返回本次取出的条数。
//
// 循环调用直到返回 0 即可排空：每次最多取一批，避免一次搬运上万条导致长事务与内存尖峰。
func (w *BusinessCursorWriter) flush() int {
	batch := w.dequeue(w.batchSize)
	if len(batch) == 0 {
		return 0
	}
	w.write(batch)
	return len(batch)
}

// dequeue 从环里取出至多 n 条（FIFO 顺序，保持位点基本有序写入）。
func (w *BusinessCursorWriter) dequeue(n int) []model.BusinessEventCursor {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size == 0 {
		return nil
	}
	if n > w.size {
		n = w.size
	}
	out := make([]model.BusinessEventCursor, 0, n)
	for i := 0; i < n; i++ {
		idx := (w.head + i) % len(w.ring)
		out = append(out, w.ring[idx])
		w.ring[idx] = model.BusinessEventCursor{} // 断开引用，避免残留
	}
	w.head = (w.head + n) % len(w.ring)
	w.size -= n
	return out
}

// write 批量插入一批位点。失败只记 WARN + 计数，**绝不 panic、绝不重试放大故障**。
//
// 用 ON CONFLICT DO NOTHING：stream_seq 唯一索引是天然的幂等兜底，
// 重启前后重复入队（或未来多写入方）不会产生重复行，也不会因唯一键冲突整批失败。
func (w *BusinessCursorWriter) write(batch []model.BusinessEventCursor) {
	if w.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), BusinessCursorWriteTimeout)
	defer cancel()
	rows := make([]model.BusinessEventCursor, len(batch))
	copy(rows, batch)
	err := w.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&rows, w.batchSize).Error
	if err != nil {
		w.failed.Add(uint64(len(batch)))
		slog.Warn("业务事件位点落库失败（不影响事件投递，仅缺口可检测性降级）",
			"count", len(batch), "error", err)
		return
	}
	w.written.Add(uint64(len(batch)))
}

// FlushNow 同步把当前队列排空（测试与停机兜底用）。返回本轮落库的条数。
func (w *BusinessCursorWriter) FlushNow() int {
	if w == nil {
		return 0
	}
	total := 0
	for {
		n := w.flush()
		if n == 0 {
			return total
		}
		total += n
	}
}

// Close 停止消费协程并刷出剩余位点（幂等）。未 Start 过时直接排空后返回。
//
// 停机顺序很重要：先关 stop 让消费协程走「排空后返回」分支，再等 done。
// 若未启动协程则这里同步排空，行为与「已启动后停机」一致（直接关闭队列里的数据）。
func (w *BusinessCursorWriter) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.stop)
		if w.started.Load() {
			<-w.done
			return
		}
		w.FlushNow()
	})
}

// Metrics 返回写入器指标快照。
func (w *BusinessCursorWriter) Metrics() BusinessCursorMetrics {
	if w == nil {
		return BusinessCursorMetrics{}
	}
	w.mu.Lock()
	depth := w.size
	w.mu.Unlock()
	return BusinessCursorMetrics{
		Queued:   w.queued.Load(),
		Written:  w.written.Load(),
		Dropped:  w.dropped.Load(),
		Failed:   w.failed.Load(),
		Depth:    depth,
		Capacity: len(w.ring),
	}
}
