package ingest

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件是 VL 投递的「双阈值攒批 + 并发投递」层（对齐 Vector / Filebeat 的批处理模型）。
//
// 改造前的形态（runtime.go 的 insertInBatchesCtxYield）：按**固定 500 条**切批、**逐批串行**
// 发请求——没有字节维度、没有时间维度、也没有任何重试；批与批之间的网络等待全部叠加在墙钟上，
// 而任何一次瞬时失败都会让整轮返回错误（上层重排队 ⇒ 整批重发）。
//
// 现在的形态（三个阈值**先到先发**）：
//   - 字节上限 insertBatchMaxBytes（4 MiB，**压缩前**的 NDJSON 请求体字节数）；
//   - 条数上限 insertBatchMaxEvents（500 条，事故兜底，见该常量的注释）；
//   - 时间上限 insertBatchFlushTimeout（1s，从**第一个事件进批**开始计时，到点即把残批交出去）。
//
// 发出侧：insertBatchWorkers 个 worker 并发投递，单批失败按指数退避重试到 insertBatchMaxAttempts
// 次；重试耗尽仍失败 ⇒ **可见地失败**（计数 + 错误日志 + 返回错误给上层），绝不静默丢弃。

const (
	// insertBatchMaxEvents 是单次 VL 插入的**硬条数上限**。
	//
	// 背景（2026-09-28 生产事故）：vlsup 客户端是 http.Client{Timeout: 5 * time.Second}，
	// 而恢复期会把积压一次性拼成一个大 payload 投递；当状态文件达 1.2 GB 时，
	// 单请求远超 5s → `vlsup: insert request: ... context deadline exceeded`
	// → writeProjectionDay 失败 → 「日志采集运行时创建失败」→ 采集静默停摆（无重试、无告警）。
	// 分批后每批体量可控，单请求远低于该超时。
	//
	// **保留 500 而不是放大**：字节阈值挡的是「行长正常、总量巨大」，本条挡的是
	// 「字节没超但条数爆炸」（超长行/空行密集）——两个维度互为兜底，任一触发就切批。
	insertBatchMaxEvents = 500

	// insertBatchMaxBytes 是单批**压缩前** NDJSON 请求体的字节上限（4 MiB）。
	//
	// 官方（Vector）默认 `batch.max_bytes = 10MB`，那是通用场景；本仓取 4 MiB 是事故校准值：
	// 2026-09-28 的停摆正是「单请求装不下大 payload ⇒ 撑爆 5s 写超时 ⇒ 采集停摆」。
	// gzip 之后（实测 ≈73× 压缩比）4 MiB 原文只有几十 KiB 上线，VL 侧解压/解析压力可控。
	insertBatchMaxBytes = 4 << 20

	// insertBatchFlushTimeout 是**攒批等待上限**（官方 Vector 默认 `timeout_secs = 1`）。
	//
	// 语义：从批内第一个事件进入缓冲区开始计时，到点即把残批交出去；于是任何已接纳的事件
	// 最多等这么久就一定被发出（批不满 500 条、也不到 4 MiB 也照发）。
	//
	// 形态说明（与 Vector 的差别，刻意为之）：Vector 那边是「异步收事件 + 独立 flush 线程」，
	// 本仓的投递入口 insertInBatchesCtxYield 是**同步喂入**（调用方一次性给整批 events），
	// 不存在「缓冲区里有事件但没人推进」的窗口，所以不需要另起一个定时器 goroutine ——
	// 在每次 push 时比较「首个事件进批的墙钟」与阈值即等价（见 insertBatchAccumulator.push）。
	// 少了那条 goroutine 也少了它在 Stop 时的 join 与泄漏面。
	insertBatchFlushTimeout = time.Second

	// insertBatchWorkers 是并发投递的 worker 数（官方 Filebeat 高吞吐档 `worker: 8` 是 60 源场景，
	// 本仓 19 源取 4）。安全边界：VL 侧 `-search.maxConcurrentRequests=16`、40 核、CPU 几乎空转，
	// 4 路并发写入不会成为 VL 的瓶颈。
	insertBatchWorkers = 4

	// insertBatchMaxAttempts 是单批的**总尝试次数**（首次 + 重试）。有界是硬要求：
	// 无界重试会把「VL 变慢」放大成「Worker 投递线程全被占住」。
	insertBatchMaxAttempts = 3

	// insertBatchRetryBaseDelay 是重试指数退避的基数（100ms → 200ms → 400ms…，封顶 insertBatchRetryMaxDelay）。
	insertBatchRetryBaseDelay = 100 * time.Millisecond

	// insertBatchRetryMaxDelay 是单次退避的封顶值（防止尝试次数被放大后退避失控）。
	insertBatchRetryMaxDelay = 2 * time.Second
)

// insertBatchLimits 是攒批与投递的全部阈值。
//
// 为什么做成结构体而不是直接读常量：生产走常量默认值，但用例必须用**真实路径**上的小额度
// 验出行为（例如「字节阈值 = 1 字节 ⇒ 每批恒为 1 条」），断言常量本身证明不了实现真的接线了。
type insertBatchLimits struct {
	MaxEvents   int
	MaxBytes    int
	FlushAfter  time.Duration
	Workers     int
	MaxAttempts int
	RetryBase   time.Duration
}

// InsertBatchTuning 是攒批/并发参数的上游配置面（Options.InsertBatch；nil ⇒ 全部用默认）。
type InsertBatchTuning struct {
	// MaxEvents 单批条数上限（0 ⇒ insertBatchMaxEvents=500）。
	MaxEvents int
	// MaxBytes 单批压缩前字节上限（0 ⇒ insertBatchMaxBytes=4 MiB）。
	MaxBytes int
	// FlushAfter 攒批等待上限（0 ⇒ insertBatchFlushTimeout=1s）。
	FlushAfter time.Duration
	// Workers 并发投递 worker 数（0 ⇒ insertBatchWorkers=4）。
	Workers int
	// MaxAttempts 单批总尝试次数（0 ⇒ insertBatchMaxAttempts=3）。
	MaxAttempts int
	// RetryBase 重试退避基数（0 ⇒ insertBatchRetryBaseDelay=100ms）。
	RetryBase time.Duration
}

// defaultInsertBatchLimits 返回生产参数。
func defaultInsertBatchLimits() insertBatchLimits {
	return insertBatchLimits{
		MaxEvents:   insertBatchMaxEvents,
		MaxBytes:    insertBatchMaxBytes,
		FlushAfter:  insertBatchFlushTimeout,
		Workers:     insertBatchWorkers,
		MaxAttempts: insertBatchMaxAttempts,
		RetryBase:   insertBatchRetryBaseDelay,
	}
}

// normalized 把每个字段的零值回退到默认。
//
// 为什么需要（2026-10-31 实测出来的）：insertBatchLimitsOf 读的是 Manager 上那个结构体字段，
// 而**不是所有 Manager 都由 New 构造**（包内用例就有直构造的形态）——零值结构体会让
// MaxEvents=0 ⇒ 按批容量预分配时除零 panic。有了这一次归一化，任何构造路径拿到的
// 都是可用参数，而不是要么 defaults、要么崩溃。
func (l insertBatchLimits) normalized() insertBatchLimits {
	def := defaultInsertBatchLimits()
	if l.MaxEvents <= 0 {
		l.MaxEvents = def.MaxEvents
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = def.MaxBytes
	}
	if l.FlushAfter <= 0 {
		l.FlushAfter = def.FlushAfter
	}
	if l.Workers <= 0 {
		l.Workers = def.Workers
	}
	if l.MaxAttempts <= 0 {
		l.MaxAttempts = def.MaxAttempts
	}
	if l.RetryBase <= 0 {
		l.RetryBase = def.RetryBase
	}
	return l
}

// insertBatchLimitsFrom 把配置面转成生效值（字段各自回退默认）。
func insertBatchLimitsFrom(t *InsertBatchTuning) insertBatchLimits {
	l := defaultInsertBatchLimits()
	if t == nil {
		return l
	}
	if t.MaxEvents > 0 {
		l.MaxEvents = t.MaxEvents
	}
	if t.MaxBytes > 0 {
		l.MaxBytes = t.MaxBytes
	}
	if t.FlushAfter > 0 {
		l.FlushAfter = t.FlushAfter
	}
	if t.Workers > 0 {
		l.Workers = t.Workers
	}
	if t.MaxAttempts > 0 {
		l.MaxAttempts = t.MaxAttempts
	}
	if t.RetryBase > 0 {
		l.RetryBase = t.RetryBase
	}
	return l
}

// InsertBatchStats 是攒批/投递的内存读数快照（供 FR-485 使用；接 /metrics 是下一个任务）。
type InsertBatchStats struct {
	// Batches / Events / Bytes 是**被组织出来的批**的批数、事件数、压缩前字节数。
	Batches int64
	Events  int64
	Bytes   int64
	// Requests 是实际发出的 HTTP 请求总数（含重试）；Retries 是其中的重试次数。
	Requests int64
	Retries  int64
	// Failures 是**重试耗尽后仍失败**的批数（可见失败，上层重排）；Aborted 是因为更早的批
	// 失败而**没有发出去**的批数——不是丢弃：它们的 WAL 条目未被回收，下一轮重投。
	Failures int64
	Aborted  int64
	// MaxEventsPerBatch / MaxBytesPerBatch 是观测到的单批最大值（批量大小分布上界读数）。
	MaxEventsPerBatch int64
	MaxBytesPerBatch  int64
	// WaitTotal / WaitMax 是**攒批等待时长**（首个事件进批 → 交批）；样本数 = Batches。
	WaitTotal time.Duration
	WaitMax   time.Duration
	// AvgEventsPerBatch / AvgBytesPerBatch 由上面推出，方便读而不必自己做除法（Batches=0 时为 0）。
	AvgEventsPerBatch float64
	AvgBytesPerBatch  float64
}

// insertBatchCounters 是 InsertBatchStats 的原子累加器（并发 worker 各自原子累加，读侧零锁）。
type insertBatchCounters struct {
	batches   atomic.Int64
	events    atomic.Int64
	bytes     atomic.Int64
	requests  atomic.Int64
	retries   atomic.Int64
	failures  atomic.Int64
	aborted   atomic.Int64
	maxEvents atomic.Int64
	maxBytes  atomic.Int64
	waitTotal atomic.Int64
	waitMax   atomic.Int64
}

// observe 记一次「被组织出来的批」（成功或失败都算：它确实被组出来并尝试投递了）。
func (c *insertBatchCounters) observe(b *insertBatch) {
	c.batches.Add(1)
	c.events.Add(int64(b.events))
	c.bytes.Add(int64(len(b.payload)))
	recordMax(&c.maxEvents, int64(b.events))
	recordMax(&c.maxBytes, int64(len(b.payload)))
	c.waitTotal.Add(int64(b.wait))
	recordMax(&c.waitMax, int64(b.wait))
}

// recordMax 原子地把 v 抬到 max(*dst, v)（并发安全，失败重试）。
func recordMax(dst *atomic.Int64, v int64) {
	for {
		cur := dst.Load()
		if v <= cur || dst.CompareAndSwap(cur, v) {
			return
		}
	}
}

// snapshot 读出当前读数。
func (c *insertBatchCounters) snapshot() InsertBatchStats {
	b := c.batches.Load()
	s := InsertBatchStats{
		Batches:           b,
		Events:            c.events.Load(),
		Bytes:             c.bytes.Load(),
		Requests:          c.requests.Load(),
		Retries:           c.retries.Load(),
		Failures:          c.failures.Load(),
		Aborted:           c.aborted.Load(),
		MaxEventsPerBatch: c.maxEvents.Load(),
		MaxBytesPerBatch:  c.maxBytes.Load(),
		WaitTotal:         time.Duration(c.waitTotal.Load()),
		WaitMax:           time.Duration(c.waitMax.Load()),
	}
	if b > 0 {
		s.AvgEventsPerBatch = float64(s.Events) / float64(b)
		s.AvgBytesPerBatch = float64(s.Bytes) / float64(b)
	}
	return s
}

// insertBatch 是一个已组好、待发出的批。
type insertBatch struct {
	// seq 是批在**本次调用**内的序号（0 起）。派发按 seq 升序，失败时据此定位「谁先坏」。
	seq int
	// payload 是本批的 NDJSON 请求体（与 projectionPayload 逐字节一致）。
	payload []byte
	// events 是本批的事件条数；wait 是攒批等待（首个事件进批 → 交批）。
	events int
	wait   time.Duration
}

// insertBatchAccumulator 按「字节 / 条数 / 时间」三个阈值攒批，先到先切。
//
// 单个事件超过字节阈值时**单独成批发出**（不会无限等待，也不会被丢弃）——超长行是真实存在的
// （多行栈、base64），必须能发出去，只是它自己占一个请求。
type insertBatchAccumulator struct {
	limits     insertBatchLimits
	generation string

	buf       bytes.Buffer
	events    int
	startedAt time.Time
	seq       int
}

func newInsertBatchAccumulator(limits insertBatchLimits, generation string) *insertBatchAccumulator {
	return &insertBatchAccumulator{limits: limits, generation: generation}
}

// push 追加一个事件；返回非 nil 表示「前面的批已满/超时，先把它交出去」。
//
// 判定顺序刻意是「先切后加」：切批时缓冲区里是**上一个事件为止**的内容，新事件进新批，
// 因此既不会超上限，也不会把同一事件写进两个批。
func (a *insertBatchAccumulator) push(event logtypes.Event) (*insertBatch, error) {
	line, err := projectionPayload([]logtypes.Event{event}, a.generation)
	if err != nil {
		return nil, err
	}
	var out *insertBatch
	if a.events > 0 && a.shouldFlush(len(line)) {
		out = a.take()
	}
	if a.events == 0 {
		a.startedAt = time.Now()
	}
	a.buf.Write(line)
	a.events++
	return out, nil
}

// shouldFlush 判定「再加这一条是否已越过任一阈值」（三选一，先到先切）。
func (a *insertBatchAccumulator) shouldFlush(next int) bool {
	if a.events >= a.limits.MaxEvents {
		return true
	}
	if a.buf.Len()+next > a.limits.MaxBytes {
		return true
	}
	return time.Since(a.startedAt) >= a.limits.FlushAfter
}

// take 取出当前缓冲作为一批（空缓冲 ⇒ nil，**空批不发送**）。
func (a *insertBatchAccumulator) take() *insertBatch {
	if a.events == 0 {
		return nil
	}
	payload := make([]byte, a.buf.Len())
	copy(payload, a.buf.Bytes())
	b := &insertBatch{seq: a.seq, payload: payload, events: a.events, wait: time.Since(a.startedAt)}
	a.seq++
	a.buf.Reset()
	a.events = 0
	a.startedAt = time.Time{}
	return b
}

// accumulateInsertBatches 把整批事件按三阈值切成若干待发批（不含任何网络动作）。
func (m *Manager) accumulateInsertBatches(events []logtypes.Event, generation string, limits insertBatchLimits) ([]*insertBatch, error) {
	if len(events) == 0 {
		return nil, nil
	}
	acc := newInsertBatchAccumulator(limits, generation)
	batches := make([]*insertBatch, 0, 1+len(events)/limits.MaxEvents)
	for _, event := range events {
		full, err := acc.push(event)
		if err != nil {
			return nil, err
		}
		if full != nil {
			batches = append(batches, full)
		}
	}
	if tail := acc.take(); tail != nil {
		batches = append(batches, tail)
	}
	for _, b := range batches {
		m.insertBatchCounters.observe(b)
	}
	return batches, nil
}

// dispatchInsertBatches 用 insertBatchWorkers 个 worker 并发投递已切好的批。
//
// 顺序契约（与改造前逐字一致的部分）：**派发顺序 = 事件顺序**——worker 从 FIFO 里按 seq 升序取批，
// 所以同源批的发出次序与事件次序一致，record_start/end 的连续性判定不受影响。
// 只有「完成次序」可能交错，而那是既有语义允许的：投递结果按**区间并集**记账
// （acquire 的 recordHTTPResults 会先排序区间），与完成先后无关。
//
// 失败契约：某批重试耗尽后仍失败 ⇒ 取消其余在飞批与未派发的批，返回该批的状态与错误；
// 上层据此判定本轮未完成（WAL 条目未回收 ⇒ 下一轮重投，幂等由 event_id 承担）。
func (m *Manager) dispatchInsertBatches(ctx context.Context, client *vlsup.Client, batches []*insertBatch, limits insertBatchLimits, yield func()) (int, error) {
	if len(batches) == 0 {
		// 空批不发送：VL 收到空请求只会白占一个连接与一次往返。
		return 0, nil
	}
	workers := limits.Workers
	if workers > len(batches) {
		workers = len(batches)
	}
	if workers < 1 {
		workers = 1
	}
	type sendOutcome struct {
		seq    int
		status int
		err    error
	}
	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan *insertBatch)
	outcomes := make(chan sendOutcome, len(batches))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				status, err := m.sendInsertBatch(sendCtx, client, b, limits)
				outcomes <- sendOutcome{seq: b.seq, status: status, err: err}
				if err != nil {
					// 失败即收手：不再取新批，让取消尽快收敛在飞批。
					return
				}
			}
		}()
	}
	// 派发者：按 seq 升序送批；批间让出（重建路径的限速语义，yield 为 nil 时零行为差异）。
	go func() {
		defer close(jobs)
		for i, b := range batches {
			if i > 0 && yield != nil {
				yield()
			}
			if sendCtx.Err() != nil {
				return
			}
			select {
			case jobs <- b:
			case <-sendCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	status := 0
	lastOK := -1
	var firstErr error
	firstFailStatus := 0
	failedSeq := -1
	sent := 0
	for out := range outcomes {
		sent++
		if out.err != nil {
			if firstErr == nil {
				firstErr, firstFailStatus, failedSeq = out.err, out.status, out.seq
				cancel() // 收手：其余在飞批随 ctx 取消，未派发的批不再发出
			}
			continue
		}
		if out.seq > lastOK {
			lastOK, status = out.seq, out.status
		}
	}
	if firstErr != nil {
		// 可见失败：计数 + 日志（不静默）。未发出的批单独计数，它们的 WAL 条目仍在。
		m.insertBatchCounters.failures.Add(1)
		if dropped := int64(len(batches) - sent); dropped > 0 {
			m.insertBatchCounters.aborted.Add(dropped)
		}
		slog.Error("VL 批量投递失败（重试耗尽，保留 WAL 待下轮重投）",
			"batches", len(batches), "failedSeq", failedSeq, "status", firstFailStatus,
			"abortedBatches", len(batches)-sent, "error", firstErr)
		return firstFailStatus, firstErr
	}
	for _, b := range batches {
		m.replaceRowsWritten.Add(int64(b.events))
	}
	return status, nil
}

// sendInsertBatch 发出一批，失败按指数退避有界重试。
func (m *Manager) sendInsertBatch(ctx context.Context, client *vlsup.Client, b *insertBatch, limits insertBatchLimits) (int, error) {
	attempts := limits.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := limits.RetryBase
	status := 0
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			m.insertBatchCounters.retries.Add(1)
			if err := sleepInsertBatchRetry(ctx, delay); err != nil {
				return status, err
			}
			if delay *= 2; delay > insertBatchRetryMaxDelay {
				delay = insertBatchRetryMaxDelay
			}
		}
		m.insertBatchCounters.requests.Add(1)
		st, err := client.InsertJSONLines(ctx, b.payload)
		status, lastErr = st, err
		if err == nil {
			return st, nil
		}
		if insertBatchFailureIsPermanent(st, err) || ctx.Err() != nil {
			return status, err
		}
	}
	return status, fmt.Errorf("ingest: insert batch seq=%d events=%d attempts=%d: %w",
		b.seq, b.events, attempts, lastErr)
}

// insertBatchFailureIsPermanent 判定「重试无意义」。
//
// 为什么不能只用 vlsup.IsPermanent（2026-10-31 实测出来的）：那个函数只认 ErrUnauthorized
// 与 *StatusError，而 vlsup 的 insertOnce 对非 2xx 返回的是**裸 fmt.Errorf**（没有类型可 errors.As）
// ⇒ 写入面的 4xx 从来不会被它识别成永久失败，本次改造首版就因此在 400 上白重试了 3 次。
//
// 因此这里额外按**状态码**判定：4xx 意味着「这批数据/这个请求本身不合法」，重发 100 次结果相同；
// 但 408（超时）与 429（限流）是典型的「稍后就好」类，必须留住重试。
func insertBatchFailureIsPermanent(status int, err error) bool {
	if err == nil {
		return false
	}
	if vlsup.IsPermanent(err) {
		return true
	}
	if status >= 400 && status < 500 {
		return status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
	}
	return false
}

// sleepInsertBatchRetry 是可取消的退避等待（关停不许被退避拖住）。
func sleepInsertBatchRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// insertBatchLimitsOf 返回生效的攒批参数（只在 New 时赋值、之后不改写 ⇒ 读不加锁）。
func (m *Manager) insertBatchLimitsOf() insertBatchLimits {
	if m == nil {
		return defaultInsertBatchLimits()
	}
	// 零值回退：未走 New 的 Manager（包内直构造的形态）拿到的是生产默认值，而不是除零。
	return m.insertBatch.normalized()
}

// InsertBatchStats 返回攒批/投递读数快照（FR-485 的读数面；接 /metrics 是下一个任务）。
func (m *Manager) InsertBatchStats() InsertBatchStats {
	if m == nil {
		return InsertBatchStats{}
	}
	return m.insertBatchCounters.snapshot()
}
