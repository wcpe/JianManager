package catalog

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// 本文件给 journal 的**写路径**加一层有界攒批。它存在的理由不是性能优化，
// 而是「让分区记录能重新落盘而不退回当年的机械盘开销」。改动前请读完整段背景。
//
// ## 为什么必须重新落盘
//
// 2026-10-30 的裁定把生产 Catalog journal 改成纯内存（`apps/worker/main.go` 里
// `catalog.NewMemJournal()`），理由是 `JSONLFileStore.AppendEntry` 每次追加都
// `f.Sync()` —— 在机械盘上那是每批投递都要付费的寻道开销。
//
// 那个裁定的前提是「分区记录是可随时重建的派生状态」。在**旧架构**下这是对的：
// 记录由 `publishSnapshot` 在投递时创建，重启后重新发布即可重建。
//
// 但 2026-11-02「简化投递路径」之后前提被推翻了：记录改由 ingest 的
// `ensureCatalogPartition` **只在投递成功时**补建（非测试调用点 = 1）。
// 于是它从「派生状态」变成了「承载历史的唯一事实」——而进程重启后它归零，
// 该源**之前所有天**在查询侧变成「不存在」（VL 里行还在，没人知道去查它）。
// 生产实证：`inst:318` 的 2026-11-02 有 1746 行，查询侧报「这天没日志」。
//
// ## 取舍（父级 2026-11-05 裁定）
//
//   - **读路径不受影响**：`MemJournal` 仍是权威内存视图，查询性能零变化；
//   - **写路径攒批**：不再每次 append 都 Sync，而是按「时间 or 条数」批量落盘；
//   - 代价是**丢数据窗口**，上界见 `BatchedStore` 的注释。

const (
	// DefaultJournalSyncInterval 是自动落盘的默认间隔。
	//
	// 取 2s 的依据：机械盘单次 fsync 实测量级是毫秒～十几毫秒，2s 一次意味着
	// 每源每秒的 fsync 成本被摊薄两个数量级；而 2s 也足够短，站点级故障最多
	// 丢最近 2s 的分区记录。
	DefaultJournalSyncInterval = 2 * time.Second

	// DefaultJournalMaxBatch 是触发**立即落盘**的待落盘条数。
	//
	// 它的作用是给队列一个上界：突发投递（恢复期一次补很多天）不能让内存里的
	// 待落盘条目无限增长。超过这个数就当场同步落盘一次。
	DefaultJournalMaxBatch = 256
)

// ErrBatchedStoreClosed 表示写入发生在 Store 关闭之后（调用方装配错误）。
var ErrBatchedStoreClosed = errors.New("catalog: batched journal store already closed")

// BatchedStore 把任意 Store 包成「有界攒批」的写路径。
//
// ## 丢数据窗口（必须明确写出来，这是本设计的核心取舍）
//
// `AppendEntry` 只把条目放进内存队列并立即**成功返回**，真正的落盘发生在：
//
//   - 后台定时器每 `interval` 一次；
//   - 待落盘条数达到 `maxBatch` 时由调用方当场触发一次；
//   - 显式调用 `Flush()` 或 `Close()`。
//
// 因此**崩溃/断电最多丢失最近 `interval` 时间窗内的分区记录**（正常关停走 `Close`，不丢）。
//
// 为什么这个窗口可接受：同一窗口内的投递本身也未必完成（投递确认与 journal 落盘
// 是两条独立的写），所以「记了一条没有数据的记录」与「有数据但记不下来」的概率同阶；
// 而反过来，把窗口压到 0（每批 fsync）正是 2026-10-30 被否掉的做法。若要更小的窗口，
// 把 `interval` 调到 1s 或更小即可，成本线性增长。
//
// ## 错误语义的弱化（必须知情）
//
// `JournalWithStore.Append` 原本的契约是「先持久化，再进入内存视图」——落盘失败则
// 内存视图也不前进，于是「持久日志里不存在的权威绝不会被查询侧看到」。
// 攒批之后这条**必然被弱化**：`AppendEntry` 返回成功时数据还没落盘，落盘失败只能在
// 之后的 flush 里发现，那时内存视图已经前进了。
//
// 处置方式（不静默）：flush 失败会打 WARN 并计入 `Stats().LastError` / `FlushFailed`，
// 供 /metrics 与排障读取。方向仍然是「可能多露一条记录」而不是「丢数据」——
// 因为记录在、数据在，只是重启后那条记录会消失（回到本次修复前的形态）。
//
// ## 并发
//
// `AppendEntry` 只在持锁期间碰 `pending`，绝不在锁内做 I/O（fsync 可能几十毫秒，
// 持锁做 I/O 会把所有投递路径一起拖住）。`flush` 先换出整批再在锁外写。
// `Close` 幂等。
//
// ## 为什么批量方法优先
//
// 底层若是 `*JSONLFileStore`，一次 flush 走 `AppendEntries`（**一次 open + 一次 Sync**），
// 而不是逐条 `AppendEntry`（每条一次 open + 一次 Sync）。这才是把当年那个
// 「每批投递都要付的寻道开销」真正摊薄的地方——攒批只是手段，单次 Sync 才是收益。
// 其它 Store 实现退化为逐条写，语义不变。
//
// `Append` 的 seq 校验仍在 `JournalWithStore` 里（本层不分配、不改序号），
// 因此重放语义与改动前逐字相同。
type BatchedStore struct {
	inner    Store
	interval time.Duration
	maxBatch int

	mu          sync.Mutex
	pending     []JournalEntry
	closed      bool
	flushed     uint64
	flushFailed uint64
	lastErr     error

	quit chan struct{}
	done chan struct{}
}

// NewBatchedStore 用默认参数包装 inner。
//
// `interval <= 0` 表示**同步写**（每次 Append 立即落盘，等价于修复前的行为）；
// `maxBatch <= 0` 取默认值。这条「0 = 同步」的口径与 `manifest_sync_*` 系列一致，
// 便于运维用配置切回旧行为。
func NewBatchedStore(inner Store, interval time.Duration, maxBatch int) *BatchedStore {
	if maxBatch <= 0 {
		maxBatch = DefaultJournalMaxBatch
	}
	s := &BatchedStore{
		inner:    inner,
		interval: interval,
		maxBatch: maxBatch,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	if interval <= 0 {
		// 同步形态：不起后台 goroutine，AppendEntry 直接写底层。
		close(s.done)
		return s
	}
	go s.loop()
	return s
}

// BatchedStoreStats 是本层的可观测读数。
//
// 三个数字对应三种现场：`Flushed` 持续增长而 `FlushFailed` 不动 = 正常；
// `FlushFailed` 增长 = 盘满/权限问题（此时重启会丢记录）；`Pending` 恒不降 = 后台 loop 死了。
type BatchedStoreStats struct {
	Pending     int
	Flushed     uint64
	FlushFailed uint64
	LastError   string
}

func (s *BatchedStore) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-ticker.C:
			_ = s.flush()
		}
	}
}

// AppendEntry 入队并（必要时）触发落盘。实现 Store。
func (s *BatchedStore) AppendEntry(entry JournalEntry) error {
	if s == nil || s.inner == nil {
		return fmt.Errorf("catalog: batched store has no inner store")
	}
	if s.interval <= 0 {
		// 同步形态：不经队列，直接写底层（与修复前逐字相同）。
		return s.inner.AppendEntry(entry)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrBatchedStoreClosed
	}
	s.pending = append(s.pending, entry)
	n := len(s.pending)
	s.mu.Unlock()
	if n >= s.maxBatch {
		// 有界：不让待落盘条目无限增长。这里会在调用方 goroutine 上做一次 I/O，
		// 只在突发时发生（正常稳态走定时器）。
		return s.flush()
	}
	return nil
}

// LoadEntries 直接代理底层（读路径不进队列）。实现 Store。
func (s *BatchedStore) LoadEntries() ([]JournalEntry, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("catalog: batched store has no inner store")
	}
	// 读之前先落盘：否则「刚 append 尚未 flush」的条目在本进程内可见（journal 内存里有），
	// 但只要有人拿 LoadEntries 与内存视图对照就会看到不一致。先 flush 代价很小、语义最简。
	if s.interval > 0 {
		if err := s.flush(); err != nil {
			return nil, err
		}
	}
	return s.inner.LoadEntries()
}

// Flush 立即把待落盘条目写入底层存储。
func (s *BatchedStore) Flush() error { return s.flush() }

func (s *BatchedStore) flush() error {
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	// **锁外做 I/O**：fsync 可能几十毫秒，持锁会把所有投递路径一起拖住。
	var err error
	if bulk, ok := s.inner.(interface{ AppendEntries([]JournalEntry) error }); ok {
		err = bulk.AppendEntries(batch)
	} else {
		for _, entry := range batch {
			if writeErr := s.inner.AppendEntry(entry); writeErr != nil {
				err = errors.Join(err, writeErr)
			}
		}
	}
	s.mu.Lock()
	if err != nil {
		s.flushFailed += uint64(len(batch))
		s.lastErr = err
	} else {
		s.flushed += uint64(len(batch))
	}
	s.mu.Unlock()
	if err != nil {
		// **绝不静默**：这些分区记录没落盘 ⇒ 重启后它们会回到「查不到」。
		slog.Warn("journal 攒批落盘失败（这些分区记录重启后将不可查，数据本身不受影响）",
			"entries", len(batch), "error", err.Error())
	}
	return err
}

// Close 停止后台落盘并做最后一次写入。幂等。
func (s *BatchedStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.interval > 0 {
		close(s.quit)
		<-s.done
	}
	if s.inner == nil {
		return nil
	}
	// 关停必须落盘：正常退出不丢任何一条（丢窗口只在崩溃时成立）。
	return s.flush()
}

// Stats 返回本层读数。
func (s *BatchedStore) Stats() BatchedStoreStats {
	if s == nil {
		return BatchedStoreStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := BatchedStoreStats{Pending: len(s.pending), Flushed: s.flushed, FlushFailed: s.flushFailed}
	if s.lastErr != nil {
		stats.LastError = s.lastErr.Error()
	}
	return stats
}
