package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
)

// Options 构造端到端管道。
type Options struct {
	// Mode 默认 FILE_PRIMARY。
	Mode AcquireMode
	// LogSourceID / SourceGeneration 账本主键；ParserVersion 绑定 normalize。
	LogSourceID      string
	SourceGeneration string
	// Path FILE_PRIMARY 日志路径（latest.log）。
	Path string
	// RotateTo 轮转目标路径（可选）。
	RotateTo string
	// Reader STDIO_PRIMARY 输入；与 Path 互斥。
	Reader io.Reader
	// Stream stdout|stderr。
	Stream string
	// SourceCategory instance|worker|node。
	SourceCategory logtypes.Source
	// Delivery WAL durable 后的投递钩子。
	Delivery DeliveryHook
	// AppendRejected 是「WAL 追加被拒（暂停/上限）」的处理钩子（#56）：非 nil 时由它**调度**一次
	// 路径外排空（不再在 emit→ingest 链上同步执行整窗 replay）；nil 时沿用 acquire 的同步语义。
	AppendRejected func()
	// Location / BaseTime / IngestTimeUTC 传递给 normalize。
	Location      *time.Location
	BaseTime      time.Time
	IngestTimeUTC string
	// Charset 源正文的字符集（auto/utf-8/gbk/gb18030）；空串为 auto。
	// 未知取值在 New 处直接失败——按源声明错字符集会静默产出乱码，属于必须暴露的配置错误。
	Charset string
	// Limits 多行上限；零字段在生产组合根补为 1MiB/512 行，显式非零限制原样保留。
	Limits normalize.Limits
	// Capacity 可选容量预算；零值用 acquire.DefaultCapacityBudget。
	Capacity         *acquire.CapacityBudget
	CapacityProvider func() (acquire.CapacityBudget, error)
	// SuppressRecursiveVL source=worker 时禁止失败写回 VL。
	SuppressRecursiveVL bool
	// ReclaimProof 在请求结果已登记且投影完成后登记恢复责任证明。
	ReclaimProof func(events []logtypes.Event) error
	// ContextReclaimProof优先于旧hook，沿同一投递owner传递取消、绝对截止与查询预算。
	ContextReclaimProof func(context.Context, []logtypes.Event) error
	// DurablePersist 在 WAL 提交后、HTTP 投递前写入耐久快照。
	DurablePersist func() error
	// DeferredDelivery非nil时真实耐久后仅登记source-key；独立库默认仍同步完成投递。
	DeferredDelivery func()
	CommittedPersist func(context.Context, []acquire.CommitIdentity) error
	ContextDelivery  func(context.Context, []logtypes.Event, bool) (DeliveryResult, error)
	// Ledger/WAL 可由 Worker 生产运行时注入持久恢复实例；为空时创建内存实例。
	Ledger *ledger.Ledger
	WAL    *acquire.WAL
	// StartAtEOF 是 FILE_PRIMARY 的**默认起点**：tailer 从当前文件末尾起读。
	//
	// 语义分工（2026-10-30 裁定 D）：
	//   - ResumeCursor != nil ⇒ 采过而且找得到上次位置，从那里继续（不受本字段影响）；
	//   - ResumeCursor == nil ⇒ 这个源没采过（首次部署/换绑定），按本字段决定起点。
	//
	// 生产置 true：新绑定时不回头吃文件里的历史（那是别人的日志）。
	// 测试保持零值（false）以便验 tailer 从文件头读起的机制本身。
	StartAtEOF bool
	// ResumeCursor 是持久化的采集游标（每源一行，见 ingest/cursor_registry.go）。
	// 非 nil 且与当前文件匹配时，tailer 从该位置续读（不重复投递上次已投递的字节）。
	ResumeCursor *acquire.SourceCursor
	// Sampling 采集侧采样与降级策略。零值 = 未启用 = 恒等（零行为变化）。
	// 启用后所有事件入口统一经 emit 收口，保证「进 WAL 的事件」与「deliver 面看到的事件」
	// 是同一批——两者分叉是最危险的一类不一致。
	Sampling sampling.Policy
	// WALLimits 是单源 WAL 真实积压上限（日志容量配置接线）；nil 表示沿用 acquire 包默认。
	// 注意它约束的是**当前积压**，不是累计追加量。
	WALLimits *acquire.WALLimits
	// MaxReplayEventsPerDrain 是暂停/恢复期单轮外发事件数上限（回放限速）；0 表示沿用默认。
	MaxReplayEventsPerDrain int
	// ReadLimits 在读取原始正文前独立限制单行和单轮字节。
	ReadLimits *acquire.ReadLimits
	// WALReserve 为条目取得长期字节预留，直到实际剪枝才释放。
	WALReserve func(int64) (func(), error)
}

// Pipeline 端到端：tail/stdio → normalize → WAL → DeliveryHook → CanReclaim 门禁。
type Pipeline struct {
	mode                AcquireMode
	key                 ledger.SourceKey
	led                 *ledger.Ledger
	wal                 *acquire.WAL
	inner               *acquire.Pipeline
	tailer              *acquire.FileTailer
	bound               *NormalizeBoundary
	hook                DeliveryHook
	reclaimProof        func(events []logtypes.Event) error
	contextReclaimProof func(context.Context, []logtypes.Event) error
	src                 logtypes.SourceIdentity
	normalizeOpts       normalize.Options
	reader              io.Reader
	// stdioBuf STDIO 会话内未消费行缓冲。
	stdioRead uint64
	// deliveredCount 累计已进入投递钩子的事件数（测试断言用）。
	//
	// 原先保留 []logtypes.Event 全量切片，会随会话内总事件数线性增长却无人读取内容
	// （FR-484 阶段5：64 源实测 ≈36MB），故只保留计数。
	deliveredCount   atomic.Int64
	deferredDelivery func()
	contextDelivery  func(context.Context, []logtypes.Event, bool) (DeliveryResult, error)
	// 默认同步组合的拒绝回退只记原子待办，采集解锁后在原调用栈/ctx排空。
	fallbackDrain atomic.Bool
	// acquired仅是当前采集临界区的有限局部批次引用；解锁时移交调用栈并立刻清空，不作排队正文。
	acquired []logtypes.Event
	// lineMode false=normalize 多行；测试可切 LineHook 对照。
	useNormalize bool
	// sampler 采集侧采样器（每源一个，跨批保留窗口状态）。nil = 未启用。
	sampler *sampling.Sampler
	// capacityProvider 风暴降级用的读数来源。与容量门禁**同一真源**（V1 的
	// Options.CapacityProvider / Capacity），不自己再采一遍磁盘——重复采数只会制造分叉。
	capacityProvider func() (acquire.CapacityBudget, error)
	// lastDegradeSample 是上一次读取降级读数的时刻（节流用）。
	// 磁盘读数是一次 statfs，每批都读会与容量门禁叠加成双倍开销。
	lastDegradeSample time.Time
	// archiveImportDeferred 统计「因 WAL 余量耗尽而推迟归档导入」的次数（缺陷二观测面）。
	//
	// 为什么要可观测：推迟是**设计内的正常让路**（积压会由回收推进），但若某个源长期
	// 停在「余量耗尽」上，那就是回收停滞而非导入问题——两个原因必须能在现场被区分，
	// 否则下一次排障又会把它读成「导入坏了」。
	//
	// 原子量：本结构既被采集轮 worker 调用（pollSource 路径），也被归档扫描路径调用，
	// 两者可并发碰同一个源 ⇒ 普通 int64 会构成数据竞争。
	archiveImportDeferred atomic.Int64
	// mu只串行化采集状态：tailer/boundary/sampler与Append。单轮工作量仍受读取字节界限制。
	// 锁序为p.mu→ledger/WAL叶子锁；调用方先释放Manager.mu，不能反向等待采集锁。
	// Commit/SQLite、逻辑guard等待、网络/verify/退避和ReclaimProof全部在本锁外执行。
	// 投递单飞由inner逻辑许可负责，不能再把这个采集锁当投递owner。
	// TryReclaim只使用ledger/WAL自有锁，回调不重取p.mu，避免恢复责任链自锁。
	mu sync.Mutex
}

// ArchiveImportDeferred 返回「因 WAL 余量耗尽而推迟归档导入」的累计次数。
//
// 必须导出（本仓库的既有纪律）：新增计数器若不接到可读面，就是又一个「实现存在但不可达」——
// 现场排障时读不到，读数等于不存在。归档扫描的汇总日志会带上它。
func (p *Pipeline) ArchiveImportDeferred() int64 {
	if p == nil {
		return 0
	}
	return p.archiveImportDeferred.Load()
}

// New 创建管道。Mode 默认 FILE_PRIMARY。
func New(opts Options) (*Pipeline, error) {
	mode := opts.Mode
	if mode == "" {
		mode = DefaultMode()
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("pipeline: unknown acquire mode %q", mode)
	}
	if opts.LogSourceID == "" || opts.SourceGeneration == "" {
		return nil, fmt.Errorf("pipeline: log_source_id and source_generation are required")
	}
	key := ledger.SourceKey{
		LogSourceID:      opts.LogSourceID,
		SourceGeneration: opts.SourceGeneration,
	}
	src := logtypes.SourceIdentity{
		LogSourceID:      key.LogSourceID,
		SourceGeneration: key.SourceGeneration,
		ParserVersion:    normalize.ParserVersion,
	}
	led := opts.Ledger
	if led == nil {
		led = ledger.New()
	}
	wal := opts.WAL
	if wal == nil {
		wal = acquire.NewWAL(led, key)
	}

	stream := opts.Stream
	if stream == "" {
		stream = "stdout"
	}
	cat := opts.SourceCategory
	if cat == "" {
		cat = logtypes.SourceInstance
	}

	if !normalize.IsValidCharset(opts.Charset) {
		return nil, fmt.Errorf("pipeline: 未知日志字符集 %q（支持 auto/utf-8/gbk/gb18030）", opts.Charset)
	}
	multilineLimits, err := productionMultilineLimits(opts.Limits)
	if err != nil {
		return nil, err
	}
	nopts := normalize.Options{
		Source:        src,
		Stream:        stream,
		IngestTimeUTC: opts.IngestTimeUTC,
		Location:      opts.Location,
		Charset:       opts.Charset,
		BaseTime:      opts.BaseTime,
		Limits:        multilineLimits,
	}
	bound := NewNormalizeBoundary(nopts)

	inner := acquire.NewPipeline(led, key, wal)
	inner.SetDurablePersist(opts.DurablePersist)
	inner.SetCommittedPersist(opts.CommittedPersist)
	if opts.DeferredDelivery != nil && opts.CommittedPersist == nil {
		return nil, fmt.Errorf("pipeline: deferred delivery requires exact committed persistence")
	}
	if opts.Capacity != nil {
		inner.SetCapacityBudget(*opts.Capacity)
	}
	inner.SetCapacityProvider(opts.CapacityProvider)
	// 单源积压上限与回放限速：配置接线（此前 SetLimits 无任何调用点，现场只剩硬编码
	// 16MiB/5000 条，而配置里的 max_wal_bytes 被接到了只增不减的累计量上）。
	if opts.WALLimits != nil {
		wal.ApplyLimits(*opts.WALLimits)
	}
	if opts.MaxReplayEventsPerDrain > 0 {
		inner.SetMaxReplayEventsPerDrain(opts.MaxReplayEventsPerDrain)
	}
	if opts.SuppressRecursiveVL || cat == logtypes.SourceWorker {
		inner.SetSourceWorker(true)
	}

	p := &Pipeline{
		mode:                mode,
		key:                 key,
		led:                 led,
		wal:                 wal,
		inner:               inner,
		bound:               bound,
		hook:                opts.Delivery,
		reclaimProof:        opts.ReclaimProof,
		contextReclaimProof: opts.ContextReclaimProof,
		src:                 src,
		normalizeOpts:       nopts,
		reader:              opts.Reader,
		useNormalize:        true,
		deferredDelivery:    opts.DeferredDelivery,
		contextDelivery:     opts.ContextDelivery,
	}
	if opts.Sampling.Enabled {
		p.sampler = sampling.New(opts.Sampling)
	}
	if opts.CapacityProvider != nil {
		p.capacityProvider = opts.CapacityProvider
	} else if opts.Capacity != nil {
		budget := *opts.Capacity
		p.capacityProvider = func() (acquire.CapacityBudget, error) { return budget, nil }
	}

	if opts.Delivery != nil || opts.ContextDelivery != nil {
		inner.SetDeliverContext(p.deliverContext)
	}
	// #56：追加被拒时的**调度**接线（见 Options.AppendRejected）。
	if opts.AppendRejected != nil {
		inner.SetAppendRejectedHook(opts.AppendRejected)
	} else {
		inner.SetAppendRejectedHook(func() { p.fallbackDrain.Store(true) })
	}

	switch mode {
	case ModeFilePrimary:
		if opts.Path == "" {
			return nil, fmt.Errorf("pipeline: FILE_PRIMARY requires Path")
		}
		tailer := acquire.NewFileTailer(led, key, opts.Path, wal, bound)
		tailer.SetSourceCategory(cat)
		if opts.RotateTo != "" {
			tailer.SetRotateTarget(opts.RotateTo)
		}
		if opts.StartAtEOF {
			if err := tailer.AnchorAtEOF(); err != nil {
				return nil, err
			}
		}
		if opts.ResumeCursor != nil {
			if err := tailer.ResumeFrom(*opts.ResumeCursor); err != nil {
				return nil, err
			}
		}
		p.tailer = tailer
	case ModeStdioPrimary:
		if opts.Path != "" {
			// A managed Raw spool is durable before acquisition. Reuse the file
			// cursor machinery while retaining STDIO_PRIMARY source semantics.
			p.tailer = acquire.NewFileTailer(led, key, opts.Path, wal, bound)
			p.tailer.SetSourceCategory(cat)
			break
		}
		if opts.Reader == nil {
			return nil, fmt.Errorf("pipeline: STDIO_PRIMARY requires Reader")
		}
		_ = led.RegisterSegment(key, ledger.Segment{
			Path:          "stdio://managed-raw",
			Kind:          ledger.SegmentStdio,
			ParserVersion: src.ParserVersion,
		})
	}
	if opts.ReadLimits != nil {
		if err := p.SetReadLimits(*opts.ReadLimits); err != nil {
			return nil, err
		}
	}
	if opts.WALReserve != nil {
		if err := wal.SetByteReservation(opts.WALReserve); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// SetReadLimits 将同一读取前预算应用于活文件和有限归档。
func (p *Pipeline) SetReadLimits(limits acquire.ReadLimits) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tailer != nil {
		return p.tailer.SetReadLimits(limits)
	}
	return nil
}

// CloseArchiveReaders 释放归档在途句柄；不改变账本或原物责任。
func (p *Pipeline) CloseArchiveReaders() error {
	// 归档导入已删除（2026-10-30）：不再有在途归档句柄。
	return nil
}

// Mode 返回当前采集模式。
func (p *Pipeline) Mode() AcquireMode { return p.mode }

// Key 返回账本源键。
func (p *Pipeline) Key() ledger.SourceKey { return p.key }

// Ledger 返回采集账本。
func (p *Pipeline) Ledger() *ledger.Ledger { return p.led }

// WAL 返回本地 WAL。
func (p *Pipeline) WAL() *acquire.WAL { return p.wal }

// DeliverPending 对已暂停的源执行一次「只投递、不读取」的自愈尝试（缺陷 A）：
// 投递已 durable 存量 → 登记投递结果 → 推进回收 → 按滞回条件评估恢复采集。
//
// 为什么需要它：暂停期间 FileTailer 不再读取，采集轮便不再产生新批次；而投递、回收与
// 恢复评估都挂在「有新批次」的路径上——不自愈就永远停摆（生产实证停 13+ 小时）。
func (p *Pipeline) DeliverPending() (bool, error) {
	return p.DeliverPendingContext(context.Background())
}

// AcquireDeliveryGuard转交唯一逻辑owner；采集入口不取此许可，因而不等待VL。
func (p *Pipeline) AcquireDeliveryGuard(ctx context.Context) (context.Context, func(), error) {
	return p.inner.AcquireDeliveryGuard(ctx)
}
func (p *Pipeline) OwnsDeliveryGuard(ctx context.Context) bool { return p.inner.OwnsDeliveryGuard(ctx) }
func (p *Pipeline) HasPendingDelivery(ctx context.Context) (bool, error) {
	return p.inner.HasPendingDelivery(ctx)
}

// DeliverPendingContext只取得逻辑投递许可；网络/验证/回收证明都不持采集p.mu。
func (p *Pipeline) DeliverPendingContext(ctx context.Context) (bool, error) {
	if p == nil || p.inner == nil {
		return false, nil
	}
	ctx, release, err := p.AcquireDeliveryGuard(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	events, err := p.inner.DeliverPendingContext(ctx)
	if err != nil || len(events) == 0 {
		return false, err
	}
	if p.DeliveryState() == logtypes.DeliveryUnknown && p.deferredDelivery != nil {
		return false, nil
	}
	if p.DeliveryState() != logtypes.DeliveryUnknown {
		if proofErr := p.reclaimContext(ctx, events); proofErr != nil {
			return true, proofErr
		}
	}
	return true, nil
}

func (p *Pipeline) deliverContext(ctx context.Context, events []logtypes.Event, replay bool) (int, bool, error) {
	p.mu.Lock()
	hook, contextual := p.hook, p.contextDelivery
	p.mu.Unlock()
	var res DeliveryResult
	var err error
	if contextual != nil {
		res, err = contextual(ctx, events, replay)
	} else if hook != nil {
		res, err = hook.Deliver(events, replay)
	}
	if err == nil {
		p.deliveredCount.Add(int64(len(events)))
	}
	return res.HTTPStatus, res.AckLost, err
}

// finishAcquisition是所有采集入口唯一的锁外提交点。dispatch永不执行旧投递成功尾段。
func (p *Pipeline) finishAcquisition(outErr *error) {
	p.finishAcquisitionContext(context.Background(), outErr)
}

func (p *Pipeline) finishAcquisitionContext(ctx context.Context, outErr *error) {
	events := p.acquired
	p.acquired = nil
	p.mu.Unlock()
	defer func() {
		if p.fallbackDrain.Swap(false) {
			if p.deferredDelivery != nil {
				for _, entry := range p.wal.Snapshot() {
					if entry.Durable {
						p.deferredDelivery()
						break
					}
				}
				return
			}
			if _, err := p.DeliverPendingContext(ctx); err != nil {
				*outErr = errors.Join(*outErr, err)
			}
		}
	}()
	if len(events) == 0 {
		return
	}
	if err := p.finishEvents(ctx, events); err != nil {
		*outErr = errors.Join(*outErr, err)
	}
}

func (p *Pipeline) finishEvents(ctx context.Context, events []logtypes.Event) error {
	qualified, err := p.inner.CommitAndQualify(ctx, events)
	if err != nil {
		return err
	}
	if p.deferredDelivery != nil {
		p.deferredDelivery()
		return nil
	}
	ctx, release, err := p.AcquireDeliveryGuard(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := p.inner.DeliverQualified(ctx, qualified); err != nil {
		return err
	}
	if p.DeliveryState() != logtypes.DeliveryUnknown {
		return p.reclaimContext(ctx, events)
	}
	return nil
}

// reclaimContext在采集锁外仅选一个hook；旧nil/default调用与旧hook语义保持不变。
func (p *Pipeline) reclaimContext(ctx context.Context, events []logtypes.Event) error {
	if p.contextReclaimProof != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return p.contextReclaimProof(ctx, events)
	}
	if p.reclaimProof != nil {
		return p.reclaimProof(events)
	}
	return nil
}

func (p *Pipeline) RecordDeliveryResult(ctx context.Context, events []logtypes.Event, result DeliveryResult) error {
	return p.inner.RecordDeliveryResult(ctx, events, result.HTTPStatus, result.AckLost)
}

// EvaluateResume 在不产生投递的前提下，按滞回条件评估一次采集恢复（返回是否已恢复）。
func (p *Pipeline) EvaluateResume() bool {
	if p == nil || p.wal == nil {
		return false
	}
	return p.wal.EvaluateResume()
}

// Boundary 返回 normalize 边界钩子。
func (p *Pipeline) Boundary() *NormalizeBoundary { return p.bound }

// FileTailer 返回 FILE_PRIMARY tailer（STDIO 模式为 nil）。
func (p *Pipeline) FileTailer() *acquire.FileTailer { return p.tailer }

// DeliveredCount 返回累计已进入 DeliveryHook 的事件数（含成功记账）。
//
// 只保留计数而不保留事件内容：后者会随会话内总事件数线性增长却无人读取内容
// （FR-484 阶段5 实测 64 源 ≈36MB）。若将来确有读取事件内容的需求，应先明确
// 保留窗口与释放时机，不要恢复无界切片。
func (p *Pipeline) DeliveredCount() int { return int(p.deliveredCount.Load()) }

// EventsEmitted 返回 normalize 边界累计事件数。
func (p *Pipeline) EventsEmitted() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bound.EventsEmitted()
}

// SetRotateTarget 更新轮转目标。
func (p *Pipeline) SetRotateTarget(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tailer != nil {
		p.tailer.SetRotateTarget(path)
	}
}

func (p *Pipeline) PrepareRotation() (bool, error) {
	if p == nil || p.tailer == nil {
		return false, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tailer.PrepareRotation()
}

func (p *Pipeline) CurrentSegmentUnread() bool {
	if p == nil || p.tailer == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tailer.CurrentSegmentUnread()
}

func (p *Pipeline) RebaseCurrentSegment(pos uint64) error {
	if p == nil || p.tailer == nil {
		return fmt.Errorf("pipeline: file tailer not configured")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tailer.RebaseCurrentSegment(pos)
}

// FlushClosedSegment closes the pending multiline event when a live file was
// physically rotated.
//
// 段有真实 EOF，但**事件未必完整**：一条堆栈完全可能被打断在两段文件之间。所以这里走
// FlushUnclosed（显式 `multiline_unclosed=rotation` 标记 + PARTIAL），不走 FlushComplete
// ——后者的 closeStatus 只要事件头带时间戳就返回 OK，会让切分点从数据里消失。
// 为什么仍必须在此闭合（而不是把缓冲区留到新文件）：轮转后的恢复责任靠 ConfirmRotationCoverage
// 把 covered 推到 Durable，gz 导入按 `startFrom - segmentStart` 跳过已读前缀；未闭合的 pending
// 行不在 Durable 里，留着它会让 gz 重导入**重喂同一批行**，把堆栈行重复拼进事件。
func (p *Pipeline) FlushClosedSegment() (batch []logtypes.Event, errOut error) {
	if p == nil || p.bound == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.finishAcquisition(&errOut)
	p.bound.FlushUnclosed(normalize.UnclosedReasonCrossRotation)
	events := p.bound.DrainEvents()
	if len(events) == 0 {
		return nil, nil
	}
	// 这里同样必须过采样收口：闭合的跨行事件是**成功路径**（活文件轮转时闭合半条事件），
	// 它的返回值会被采集轮直接投递。若绕过采样，这一批就成了「未采样却进投递」的例外，
	// 现场表现为「大部分时候压得住、偶尔冒出一大批原文」。
	return p.emit(events)
}

func (p *Pipeline) ConfirmRotationCoverage() error {
	if p == nil || p.tailer == nil {
		return fmt.Errorf("pipeline: file tailer not configured")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tailer.ConfirmRotationCoverage()
}

// FlushStaleMultiline 在未闭合多行缓冲已持续超过 timeout 时强制闭合它（显式未闭合标记）。
//
// 为什么必须有这条出路（缺陷 B，2026-10-02）：未闭合缓冲平时只由「下一行到达」推进。
// 源一旦长时间静默（MC 服务器空闲、实例挂起），缓冲既不产出事件也不推进 durable——
// 而轮转恢复要靠 durable 覆盖已读前缀（ConfirmRotationCoverage + gz 的 skipBytes），
// 于是这段悬挂区间既没被覆盖、也没有事件，链路卡在「轮转分段不可读」。
// 超时强制闭合把它落成一条显式 PARTIAL 事件并推进 durable，链路重新可推进。
//
// 返回本批产出的事件（已过采样收口与 WAL/账本），可直接投递；无可冲刷时返回 nil。
func (p *Pipeline) FlushStaleMultiline(now time.Time, timeout time.Duration) (batch []logtypes.Event, errOut error) {
	if p == nil || p.bound == nil || timeout <= 0 {
		return nil, nil
	}
	p.mu.Lock()
	defer p.finishAcquisition(&errOut)
	events, ok := p.bound.FlushStaleUnclosed(now, timeout, normalize.UnclosedReasonIdle)
	if !ok || len(events) == 0 {
		return nil, nil
	}
	return p.emit(events)
}

// Poll 读取增量：FILE_PRIMARY tail / STDIO 行流 → normalize 事件 → WAL → delivery。
// 返回本批已入 WAL 的完整事件（不含仍在缓冲的半条）。
func (p *Pipeline) Poll() (batch []logtypes.Event, errOut error) {
	p.mu.Lock()
	defer p.finishAcquisition(&errOut)
	return p.pollLocked()
}

// pollLocked 是 Poll 的**锁内实现**：调用方必须已持 `p.mu`（见 `Pipeline.mu` 的锁序说明）。
func (p *Pipeline) pollLocked() ([]logtypes.Event, error) {
	switch p.mode {
	case ModeFilePrimary:
		return p.pollFile()
	case ModeStdioPrimary:
		if p.tailer != nil {
			return p.pollFile()
		}
		return p.pollStdio()
	default:
		return nil, fmt.Errorf("pipeline: unsupported mode %q", p.mode)
	}
}

func (p *Pipeline) pollFile() ([]logtypes.Event, error) {
	if p.tailer == nil {
		return nil, fmt.Errorf("pipeline: file tailer not configured")
	}
	// tailer 用 NormalizeBoundary：complete 时 emit=false，事件由 boundary 持有。
	if _, err := p.tailer.Poll(); err != nil {
		// 仍尝试冲刷已完整事件。
		evs := p.bound.DrainEvents()
		if len(evs) > 0 {
			evs, _ = p.emit(evs)
		}
		return evs, err
	}
	events := p.bound.DrainEvents()
	if len(events) == 0 {
		return nil, nil
	}
	return p.emit(events)
}

func (p *Pipeline) pollStdio() ([]logtypes.Event, error) {
	if p.reader == nil {
		return nil, fmt.Errorf("pipeline: stdio reader not configured")
	}
	sc := bufio.NewScanner(p.reader)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		abs := p.stdioRead
		end, _, _ := p.bound.Feed(line, abs, abs+uint64(len(line))+1)
		p.stdioRead = end
		// complete 与否：事件都在 boundary；stdio 只推进 read 水位。
		_ = p.led.AdvanceRead(p.key, end)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		_ = p.led.RecordGap(p.key, p.stdioRead, p.stdioRead, ledger.GapReasonStdioReadError, err.Error())
		return p.bound.DrainEvents(), err
	}
	batch := p.bound.DrainEvents()
	if len(batch) == 0 {
		return nil, nil
	}
	return p.emit(batch)
}

// Flush 冲刷多行半条事件（崩溃/轮转前）。显式 PARTIAL/OK 状态，不静默丢。
func (p *Pipeline) Flush() (batch []logtypes.Event, errOut error) {
	return p.FlushContext(context.Background())
}

// FlushContext允许独立同步组合在关停的当前调用栈内完成既有冲刷；生产outbox仍只本地耐久。
func (p *Pipeline) FlushContext(ctx context.Context) (batch []logtypes.Event, errOut error) {
	p.mu.Lock()
	defer p.finishAcquisitionContext(ctx, &errOut)
	return p.flushLocked()
}

// flushLocked 是 Flush 的**锁内实现**：调用方必须已持 `p.mu`。
func (p *Pipeline) flushLocked() ([]logtypes.Event, error) {
	// tailer.FlushPartial → boundary.FlushPartial → normalize.FlushPartial。
	if p.tailer != nil {
		if _, err := p.tailer.FlushPhysicalEOF(); err != nil {
			return nil, err
		}
		_, _, _ = p.tailer.FlushPartial()
	} else {
		_, _, _ = p.bound.FlushPartial()
	}
	events := p.bound.DrainEvents()
	if len(events) == 0 {
		return nil, nil
	}
	return p.emit(events)
}

// Drain Poll + Flush，一次取完当前可见完整事件。
//
// 两段必须在**同一段临界区**内完成（SAMPLING-1）：否则「Poll 之后、Flush 之前」正是
// `Stop` / 预备切换的 `Flush` 能插进来的窗口，一次 Drain 会与它交错两次。
func (p *Pipeline) Drain() (batch []logtypes.Event, errOut error) {
	p.mu.Lock()
	defer p.finishAcquisition(&errOut)
	polled, err := p.pollLocked()
	flushed, ferr := p.flushLocked()
	out := append(append([]logtypes.Event{}, polled...), flushed...)
	if err != nil {
		return out, err
	}
	return out, ferr
}

// Sampler 返回采集侧采样器（未启用时为 nil）。观测面与降级信号注入用。
func (p *Pipeline) Sampler() *sampling.Sampler { return p.sampler }

// degradeSampleInterval 是降级读数的节流间隔。
const degradeSampleInterval = time.Second

// RefreshDegrade 按当前容量读数推进风暴降级状态机，返回状态是否发生变化。
//
// 触发源刻意复用容量门禁**同一份** CapacityBudget（V1 的 Options.Capacity/CapacityProvider），
// 不另起一套「背压读数」：两个真源迟早会给出两个结论，而现场只会看到「有时候降级有时候不降级」。
// 读数按 degradeSampleInterval 节流——磁盘使用率是一次 statfs，每批都读会与容量门禁叠加。
func (p *Pipeline) RefreshDegrade(now time.Time) bool {
	if p.sampler == nil || p.capacityProvider == nil {
		return false
	}
	// 与事件路径共用同一把 per-pipeline 锁（SAMPLING-1）：本方法写 `lastDegradeSample` 并改写
	// `Sampler` 的降级状态，而 `Sampler.Process`（在 `emit` 内）也在并发读它。
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastDegradeSample.IsZero() && now.Sub(p.lastDegradeSample) < degradeSampleInterval {
		return false
	}
	p.lastDegradeSample = now
	budget, err := p.capacityProvider()
	if err != nil {
		// 读数拿不到就不降级：降级是「已知资源吃紧」时的自保动作，
		// 不是「读数失败」时的猜测。拿不到读数时误降级会白白压掉日志。
		return false
	}
	sig := sampling.Signal{DiskPercent: budget.DiskUsagePercent}
	if budget.MaxWALBytes > 0 {
		sig.BacklogBytes = 0 // WAL 字节数未从 acquire 暴露；见 spec 的「未做」栏
	}
	changed, _ := p.sampler.UpdateSignal(sig, now)
	return changed
}

// emit 是全部事件入口的**唯一收口**：采样 → WAL/账本 → 返回实际落库的那批事件。
//
// 为什么必须同时替换「进账本的事件」与「返回给调用方的事件」：
// p.ingest 只负责 WAL/账本，而调用方（采集轮）拿返回值去 deliver + 投影校验。
// 若只改前者，账本按采样集推进、deliver 面按未采样集校验，两边都自认正确——
// 这是最危险的一类不一致（现场表现是校验永远失败或缺口永远挂着）。
//
// 采样未启用时 sampler 为 nil，本方法逐指令等价于直接调用 p.ingest。
//
// **并发契约（SAMPLING-1）**：调用方必须已持 `p.mu`（本方法只由 Poll/Flush/Drain/
// FlushClosedSegment/FlushStaleMultiline/ImportArchive 在各自临界区内调用）。
func (p *Pipeline) emit(events []logtypes.Event) ([]logtypes.Event, error) {
	events = p.sampleEvents(events)
	if len(events) == 0 {
		return nil, nil
	}
	if err := p.ingest(events); err != nil {
		return events, err
	}
	return events, nil
}

// sampleEvents 只做**采样收口**，不入 WAL、不投递。
//
// 为什么要把它从 emit 里拆出来（缺陷二修复，2026-10-10）：归档导入需要「采样**整个归档**一次，
// 然后按批入账」。若对每一批各采一次样，采样决策就会随切批边界漂移（采样窗口是有状态的），
// 同一份归档在「分批」与「不分批」下会得到不同的落库集合 —— 那是口径分叉，
// 而不是本次修复想要的「切批不改语义」。故采样必须保持整包一次，切批只发生在采样之后。
//
// 采样未启用（sampler == nil）时本方法逐指令等价于恒等返回（零行为变化）。
//
// **并发契约（SAMPLING-1）**：调用方必须已持 `p.mu` —— `Sampler` 内部无锁，两个 goroutine
// 同时 `Process` 会并发写 `s.sigs`（map）⇒ `fatal error: concurrent map writes`。
func (p *Pipeline) sampleEvents(events []logtypes.Event) []logtypes.Event {
	if p.sampler == nil {
		return events
	}
	return p.sampler.Process(events)
}

// ingest WAL append → Commit → DeliveryHook。容量/缺口由 acquire.Pipeline 处理。
func (p *Pipeline) ingest(events []logtypes.Event, reservations ...*acquire.AppendReservation) error {
	if err := p.inner.AppendEvents(events, reservations...); err != nil {
		return err
	}
	p.acquired = append(p.acquired, events...)
	return nil
}

// ResolveUnknownThroughProjection keeps the UNKNOWN audit fact while advancing
// the delivery prefix after a persisted canonical projection was verified.
func (p *Pipeline) ResolveUnknownThroughProjection(events []logtypes.Event) error {
	if p == nil || len(events) == 0 {
		return nil
	}
	return p.led.ResolveDeliveryThroughRecovery(p.key, events[0].Record.Start, events[len(events)-1].Record.End)
}

// SetArchiveOwnerRegistry 注入归档归属注册表（跨代次查询「该归档属于哪个代次」）。
//
// 由上层（ingest Manager）在登记每个源时注入；未注入时导入器只认轮转关联这一条归属凭据，
// 而**不会**回退成"假设当前代次"。
func (p *Pipeline) SetArchiveOwnerRegistry(fn func(logSourceID, cleanPath, objectID string) (string, bool)) {
	// 归档导入已删除：owner registry 不再需要转发。
}

// SetArchiveBackfill 注入「按原代次补账」通道（命中其它代次且其账本可寻址时使用）。
func (p *Pipeline) SetArchiveBackfill(fn func(ownerGeneration, archivePath, objectID string) error) {
	// 归档导入已删除：backfill 不再需要转发。
}

// ArchiveImportBatchMin / ArchiveImportBatchSlack 决定归档导入的**单批事件数上界**。
//
// 缺陷二（2026-10-10 生产实测）：`inst:220/file` 的 544,278 行 gz 解析出 **128,856** 个事件，
// 而原实现把它作为**一个**批次喂给 `p.emit`（WAL 追加 → Commit → 投递）。单源 WAL 条目上限
// 当时生效 20000 ⇒ 一次追加即越闸 6.4 倍 ⇒ `单源积压超过上限，已暂停该源采集`；重启后更坏
// （`entries=424205/20000`），`delivery_pos` 恒 None、VL 侧 0 行 —— 数据进不去、源永久卡死
// 且不自愈（5 分钟内暂停源 101→102，零自愈）。
//
// 修法：把「读取整个 gz」与「一次投递全部事件」解耦——读仍是一次顺序读（读盘不重复），
// 但**入账与投递按批推进**，每批都独立走完整的
// `WAL 追加 → fsync/Commit(durable) → 投递 → 结果落账` 链路。
//
// 为什么取「上限的一半」：闸门判据是 `backlog > maxEntries`，而单批追加量为 B ⇒
// 只要 `当前积压 + B ≤ 上限` 就不会在**正常推进**的源上越闸。取一半给 reclaim 留出
// 推进余量（每批投递成功都会推进回收，积压在批与批之间回落），同时不至于把批切得过碎
// （批越小，WAL 追加/投递的固定开销占比越高）。字节维度同理取一半。
//
// **不放宽上限**：本常量只决定「一次喂多少」，闸门阈值与语义一字不动（wal.go 的上限存在
// 的理由是防单源积压撑爆状态文件，曾有 1.2GB 状态文件事故）。
// **不绕过 WAL**：每批都先落 WAL 再投递，WAL 的「先持久化再投递」耐久性保证逐批成立。
// ── 归档（gz / 纯文本轮转）导入已整体删除（2026-10-30 用户裁定）────────────────
//
// 用户原话：「把读取gz归档日志通通给我删了，只需要读取日志然后往vl塞，
// 有就有没有就没有了」。
//
// 删除内容：archiveImportBudget / archiveImportBatchSize / AssessArchive /
// ImportArchive / finishArchiveAcquisition / archiveBaseDay 及配套 const。
// 归档导入原本为「回读轮转历史」服务，它与实时采集共用同一套位置账本与内容锚判定，
// 留着任何一半都会造成“发现说已导入、导入器却不看”的漂移。整块切掉。
// ─────────────────────────────────────────────────────────────────

// archiveBaseDay 给「文件名不带日期」的归档形态提供**跨重导稳定**的无日期时刻锚点日。
//
// 语义：优先返回该段上已固化的 BaseDay；没有则用文件 mtime 的 UTC 日，并**立即固化**
// 回账本段，使此后每次重导都取到同一日。
//
// 为什么不能直接用 mtime 而不固化：mtime 会随文件被追加而前进，用它当锚点同样会让
// 同一段在不同时刻补出不同日期（就是本方法要根除的病），故首次取到后必须钉死。
//
// 失败返回 false：调用方保持既有行为（回落 normalize 的 now），不引入新的硬失败。
func (p *Pipeline) archiveBaseDay(path string) (time.Time, bool) {
	if p == nil || p.led == nil {
		return time.Time{}, false
	}
	// 段上已固化：直接复用（幂等，也是绝大多数重导走的路径）。
	entry := p.led.Get(p.key)
	if entry != nil {
		for i := range entry.Segments {
			if filepath.Clean(entry.Segments[i].Path) != filepath.Clean(path) {
				continue
			}
			if entry.Segments[i].BaseDay == "" {
				break
			}
			day, err := time.ParseInLocation("2006-01-02", entry.Segments[i].BaseDay, time.UTC)
			if err != nil {
				break
			}
			return day, true
		}
	}
	// 首次导入：用 mtime 的 UTC 日作初始锚点，并固化。
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	fixed := info.ModTime().UTC()
	day := time.Date(fixed.Year(), fixed.Month(), fixed.Day(), 0, 0, 0, 0, time.UTC)
	if err := p.led.SetSegmentBaseDay(p.key, path, day.Format("2006-01-02")); err != nil {
		// 固化失败不阻断本次导入：本次仍用该日，下次会再尝试固化。
		return day, true
	}
	return day, true
}

// SetDelivery 运行时替换投递钩子。
func (p *Pipeline) SetDelivery(h DeliveryHook) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hook = h
	p.contextDelivery = nil
	p.inner.SetDeliverContext(p.deliverContext)
}

// SuppressedReplayDrains 转发 acquire 层的「冗余整窗重投抑制」读数（#60-Z1）。
func (p *Pipeline) SuppressedReplayDrains() int64 { return p.inner.SuppressedReplayDrains() }

// TryReclaim 经 logtypes.CanReclaim 门禁推进 reclaim_position。
// HTTP 2xx / REQUEST_DONE 单独不得回收；需恢复分段责任已转移且无 hold。
func (p *Pipeline) TryReclaim() (uint64, error) {
	return p.wal.TryReclaim()
}

// TryReclaimThrough把当前发布证明的上界传给WAL原子正文闸，不能由旧宽分段越界。
func (p *Pipeline) TryReclaimThrough(through uint64) (uint64, error) {
	return p.wal.TryReclaimThrough(through)
}

// SelfFailureReasons 转发**最近自愈失败原因**（新→旧、有界 ✓；见 acquire.noteSelfFailure ✓）。
//
// 为什么要有这层（2026-10-05 实测 ✓）：`ingest` 持有的是本包装类型 ⇒ 少了这层转发，读数依旧
// **不可达** ✗（又一次"实现存在但不可达"✓）⇒ 每次加导出都要**顺着包装链一路接出去** ✓。
func (p *Pipeline) SelfFailureReasons() []string {
	if p == nil || p.inner == nil {
		return nil
	}
	return p.inner.SelfFailureReasons()
}

// CanReclaimNow 报告当前是否还有「已确认投递但未回收」的前缀。
//
// 2026-10-29 改写：不再看恢复分段——回收只需 `min(delivery, max(read,durable)) > reclaim`。
func (p *Pipeline) CanReclaimNow() bool {
	ent := p.led.Get(p.key)
	if ent == nil {
		return false
	}
	frontier := max(ent.Positions.Read, ent.Positions.Durable)
	if ent.Positions.Delivery < frontier {
		frontier = ent.Positions.Delivery
	}
	return frontier > ent.Positions.Reclaim
}

// Gaps 返回账本缺口（容量暂停/坏记录/投递失败可见）。
func (p *Pipeline) Gaps() []ledger.Gap {
	ent := p.led.Get(p.key)
	if ent == nil {
		return nil
	}
	return ent.Gaps
}

// Positions 返回四水位。
func (p *Pipeline) Positions() (logtypes.Positions, bool) {
	ent := p.led.Get(p.key)
	if ent == nil {
		return logtypes.Positions{}, false
	}
	return ent.Positions, true
}

// DeliveryState 返回最近批次投递状态。
func (p *Pipeline) DeliveryState() logtypes.DeliveryState {
	ent := p.led.Get(p.key)
	if ent == nil {
		return logtypes.DeliveryNotSent
	}
	return ent.DeliveryState
}

// Stats 返回 normalize 统计（事件数与行数分列）。
func (p *Pipeline) Stats() normalize.Stats { p.mu.Lock(); defer p.mu.Unlock(); return p.bound.Stats() }

// EnsurePathFile 便捷：校验 FILE_PRIMARY 路径存在。
func EnsurePathFile(path string) error {
	if path == "" {
		return fmt.Errorf("pipeline: empty path")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("pipeline: stat %s: %w", path, err)
	}
	return nil
}
