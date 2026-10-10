package pipeline

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// Limits 多行上限。
	Limits normalize.Limits
	// Capacity 可选容量预算；零值用 acquire.DefaultCapacityBudget。
	Capacity         *acquire.CapacityBudget
	CapacityProvider func() (acquire.CapacityBudget, error)
	// SuppressRecursiveVL source=worker 时禁止失败写回 VL。
	SuppressRecursiveVL bool
	// ReclaimProof 在请求结果已登记且投影完成后登记恢复责任证明。
	ReclaimProof func(events []logtypes.Event) error
	// DurablePersist 在 WAL 提交后、HTTP 投递前写入耐久快照。
	DurablePersist func() error
	// Ledger/WAL 可由 Worker 生产运行时注入持久恢复实例；为空时创建内存实例。
	Ledger *ledger.Ledger
	WAL    *acquire.WAL
	// Sampling 采集侧采样与降级策略。零值 = 未启用 = 恒等（零行为变化）。
	// 启用后所有事件入口统一经 emit 收口，保证「进 WAL 的事件」与「deliver 面看到的事件」
	// 是同一批——两者分叉是最危险的一类不一致。
	Sampling sampling.Policy
	// WALLimits 是单源 WAL 真实积压上限（日志容量配置接线）；nil 表示沿用 acquire 包默认。
	// 注意它约束的是**当前积压**，不是累计追加量。
	WALLimits *acquire.WALLimits
	// MaxReplayEventsPerDrain 是暂停/恢复期单轮外发事件数上限（回放限速）；0 表示沿用默认。
	MaxReplayEventsPerDrain int
}

// Pipeline 端到端：tail/stdio → normalize → WAL → DeliveryHook → CanReclaim 门禁。
type Pipeline struct {
	mode          AcquireMode
	key           ledger.SourceKey
	led           *ledger.Ledger
	wal           *acquire.WAL
	inner         *acquire.Pipeline
	tailer        *acquire.FileTailer
	imp           *acquire.ArchiveImporter
	bound         *NormalizeBoundary
	hook          DeliveryHook
	reclaimProof  func(events []logtypes.Event) error
	src           logtypes.SourceIdentity
	normalizeOpts normalize.Options
	reader        io.Reader
	// stdioBuf STDIO 会话内未消费行缓冲。
	stdioRead uint64
	// deliveredCount 累计已进入投递钩子的事件数（测试断言用）。
	//
	// 原先保留 []logtypes.Event 全量切片，会随会话内总事件数线性增长却无人读取内容
	// （FR-484 阶段5：64 源实测 ≈36MB），故只保留计数。
	deliveredCount int
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
	nopts := normalize.Options{
		Source:        src,
		Stream:        stream,
		IngestTimeUTC: opts.IngestTimeUTC,
		Location:      opts.Location,
		Charset:       opts.Charset,
		BaseTime:      opts.BaseTime,
		Limits:        opts.Limits,
	}
	bound := NewNormalizeBoundary(nopts)

	inner := acquire.NewPipeline(led, key, wal)
	inner.SetDurablePersist(opts.DurablePersist)
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
		mode:          mode,
		key:           key,
		led:           led,
		wal:           wal,
		inner:         inner,
		bound:         bound,
		hook:          opts.Delivery,
		reclaimProof:  opts.ReclaimProof,
		src:           src,
		normalizeOpts: nopts,
		reader:        opts.Reader,
		useNormalize:  true,
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
	p.imp = acquire.NewArchiveImporter(led, key)

	if opts.Delivery != nil {
		inner.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
			res, err := p.hook.Deliver(events, replay)
			if err != nil {
				return 0, res.AckLost, err
			}
			p.deliveredCount += len(events)
			return res.HTTPStatus, res.AckLost, nil
		})
	}
	// #56：追加被拒时的**调度**接线（见 Options.AppendRejected）。
	if opts.AppendRejected != nil {
		inner.SetAppendRejectedHook(opts.AppendRejected)
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
	return p, nil
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
	if p == nil || p.inner == nil {
		return false, nil
	}
	events, err := p.inner.DeliverPending()
	if err != nil || len(events) == 0 {
		return false, err
	}
	// 与正常投递路径同构（见 ingest）：投递成功后登记恢复责任证明，否则回收门禁不会放行，
	// 「积压回落 → 低水位 → 恢复采集」这一链在暂停源上永远打不开。
	if p.DeliveryState() != logtypes.DeliveryUnknown && p.reclaimProof != nil {
		if proofErr := p.reclaimProof(events); proofErr != nil {
			return true, proofErr
		}
	}
	return true, nil
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
func (p *Pipeline) DeliveredCount() int { return p.deliveredCount }

// EventsEmitted 返回 normalize 边界累计事件数。
func (p *Pipeline) EventsEmitted() int { return p.bound.EventsEmitted() }

// SetRotateTarget 更新轮转目标。
func (p *Pipeline) SetRotateTarget(path string) {
	if p.tailer != nil {
		p.tailer.SetRotateTarget(path)
	}
}

func (p *Pipeline) PrepareRotation() (bool, error) {
	if p == nil || p.tailer == nil {
		return false, nil
	}
	return p.tailer.PrepareRotation()
}

func (p *Pipeline) CurrentSegmentUnread() bool {
	return p != nil && p.tailer != nil && p.tailer.CurrentSegmentUnread()
}

func (p *Pipeline) RebaseCurrentSegment(pos uint64) error {
	if p == nil || p.tailer == nil {
		return fmt.Errorf("pipeline: file tailer not configured")
	}
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
func (p *Pipeline) FlushClosedSegment() ([]logtypes.Event, error) {
	if p == nil || p.bound == nil {
		return nil, nil
	}
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
func (p *Pipeline) FlushStaleMultiline(now time.Time, timeout time.Duration) ([]logtypes.Event, error) {
	if p == nil || p.bound == nil || timeout <= 0 {
		return nil, nil
	}
	events, ok := p.bound.FlushStaleUnclosed(now, timeout, normalize.UnclosedReasonIdle)
	if !ok || len(events) == 0 {
		return nil, nil
	}
	return p.emit(events)
}

// Poll 读取增量：FILE_PRIMARY tail / STDIO 行流 → normalize 事件 → WAL → delivery。
// 返回本批已入 WAL 的完整事件（不含仍在缓冲的半条）。
func (p *Pipeline) Poll() ([]logtypes.Event, error) {
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
func (p *Pipeline) Flush() ([]logtypes.Event, error) {
	// tailer.FlushPartial → boundary.FlushPartial → normalize.FlushPartial。
	if p.tailer != nil {
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
func (p *Pipeline) Drain() ([]logtypes.Event, error) {
	polled, err := p.Poll()
	flushed, ferr := p.Flush()
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
func (p *Pipeline) sampleEvents(events []logtypes.Event) []logtypes.Event {
	if p.sampler == nil {
		return events
	}
	return p.sampler.Process(events)
}

// ingest WAL append → Commit → DeliveryHook。容量/缺口由 acquire.Pipeline 处理。
func (p *Pipeline) ingest(events []logtypes.Event) error {
	if err := p.inner.Ingest(events); err != nil {
		return err
	}
	if p.DeliveryState() == logtypes.DeliveryUnknown {
		// An UNKNOWN request cannot be reclaimed from HTTP status alone. The
		// recovery path must first reconcile it from a durable projection.
		return nil
	}
	if p.reclaimProof != nil {
		return p.reclaimProof(events)
	}
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
	if p == nil || p.imp == nil {
		return
	}
	p.imp.SetOwnerRegistry(fn)
}

// SetArchiveBackfill 注入「按原代次补账」通道（命中其它代次且其账本可寻址时使用）。
func (p *Pipeline) SetArchiveBackfill(fn func(ownerGeneration, archivePath, objectID string) error) {
	if p == nil || p.imp == nil {
		return
	}
	p.imp.SetBackfill(fn)
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
const (
	// archiveImportBatchMax 是单批上限：即使配置把 WAL 上限抬得很高，也不允许单批回到
	// 「一次几十万条」的形态——那正是 5s 客户端超时与 VL 单请求体量失控的来源
	// （见 insertBatchMaxEvents 的同型教训）。
	archiveImportBatchMax = 5000
)

// archiveImportBudget 返回**本轮**归档导入可放行的事件数上界，以及「本轮是否可以推进」。
//
// 取两重界（先到者生效）：
//  1. **上限的一半**（`maxEntries/2`）：给 reclaim 留推进余量。闸门判据是
//     `backlog > maxEntries`，而单批追加量 ≤ 上限的一半 ⇒ 空积压的源追加完也不会越闸。
//  2. **当前余量**（`maxEntries - 当前积压`）：源已经带了积压时，按剩下的空间切批。
//     没有这一条，「稳态带积压的源」仍会被一次 1/2 上限的追加顶过闸（例如积压 15000、
//     上限 20000、追加 10000 ⇒ 25000 > 20000 ⇒ 暂停）。生产现场的源普遍带积压，
//     故这一条不是理论顾虑，而是让修复对**真实形态**成立的必要条件。
//
// 返回 ok=false 表示**余量已耗尽**（当前积压已顶到上限）。此时调用方必须**放弃本轮推进**，
// 而不是强行取一个最小批次：
//   - 强推必然越闸 ⇒ 触发 `单源积压超过上限，已暂停该源采集` ⇒ 那次暂停又会拒绝后续
//     追加 ⇒ 正是本缺陷要消灭的「源停死」形态；
//   - 放弃本轮**不丢数据**：归档没被读完就仍是「未导入」，采集轮 250ms 后必再发现它，
//     而积压会在这段时间里被回收推进（`Ingest` 每次追加前都会 `TryReclaim`）。
//
// 读的是 WAL 自己的**生效**上限（含恢复期拓宽与默认值归一，见 acquire.WAL.EffectiveLimits），
// 而不是再写一个常数：上限被配置抬高/降低时批次自动同步
// ——「代码默认 2000 条、生产生效 20000 条」这种脱节正是本缺陷的现场形态之一。
func (p *Pipeline) archiveImportBudget() (int, bool) {
	if p.wal == nil {
		// 没有 WAL 可读（测试替身/异常构造）：退回「一次读完整包」的既有行为。
		return 0, true
	}
	maxEntries, _ := p.wal.EffectiveLimits()
	if maxEntries <= 0 {
		return 0, true
	}
	size := int(maxEntries / 2)
	if size > archiveImportBatchMax {
		size = archiveImportBatchMax
	}
	// 顺序固定：先取上限，再**被余量夹住**。余量夹取必须最后做——若先套下限再夹，
	// 下限本身就可能大于余量（`min > headroom`）⇒ 放行量越闸，正是本缺陷的形态。
	backlog, _ := p.wal.Backlog()
	headroom := int(maxEntries - backlog)
	if headroom <= 0 {
		return 0, false
	}
	if headroom < size {
		size = headroom
	}
	return size, true
}

// archiveImportBatchSize 返回单批事件数上界（不含「余量已耗尽」这一信息）。
// 仅供诊断与回归断言使用；生产路径必须用 archiveImportBudget —— 否则会把
// 「余量耗尽」误读成「可以推一个最小批次」，那正是越闸暂停的入口。
func (p *Pipeline) archiveImportBatchSize() int {
	size, ok := p.archiveImportBudget()
	if !ok {
		return 0
	}
	return size
}

// ImportArchive 导入 gzip 归档；已关联轮转时跳过整包，禁止双计。
//
// **分批入账 + 跨轮续传**（缺陷二修复，2026-10-10）。
//
// 现场：`inst:220/file` 的 544,278 行 gz 解析出 **128,856** 个事件（单源上限 20000 的 6.4 倍），
// 原实现把它作为**一个**批次交给 WAL ⇒ 一次追加即越闸 ⇒ `单源积压超过上限，已暂停该源采集`；
// 越闸后 `WAL.Append` 的首判据（已暂停）拒绝**后续**追加 ⇒ 源在暂停上停死、
// `delivery_pos` 恒 None、VL 侧 0 行，且 5 分钟内零自愈（暂停源 101→102）。
//
// 为什么必须「跨轮」而不只是「循环切批」（实测教训，本修复的第二版）：
// 第一版在一个调用内循环切批，**仍然转红** —— 因为回收（`TryReclaim`）只在
// 「责任已转移」时放行，而一批刚投完的条目往往还不可回收，于是积压**逐批累加**：
// 300 条上限下第 2 批就把 entries 顶到 512 并触发暂停。切批只降低**单次追加量**，
// 并不降低**累积量**。真正有界的是「每轮只推到本轮余量允许的位置，然后让路」。
//
// 于是本次的语义是：
//   - 单次调用最多推进到「当前余量允许」的边界（余量 = 生效上限 − 当前积压）；
//   - 位置随后**落回账本分段**（EndPos），归档仍标记为**未导入** ⇒ 采集轮下一轮继续；
//   - 只有真正读到归档 EOF 才 `MarkImported`（见 acquire.ArchiveImporter 的说明）；
//   - 采集轮 250ms 一跳，故「跨轮」不是延迟问题，而是让回收有机会在两轮之间推进。
//
// 不丢数据的论证（逐条）：
//   - 已推进的每一批都走完整链路（WAL 追加 → Commit(durable) → 投递 → 结果落账）；
//   - 未推进的部分既没被读进 WAL、也没被标成已导入 ⇒ 必然会被下一轮重新发现；
//   - 续读位置来自账本分段（可持久化），故进程重启后仍从断点续读，不重复读已消费前缀；
//   - 因此「丢」不可能发生（每段要么已落库、要么仍在待导入集合里），
//     而「重」由既有 event_id + 位置账本幂等收敛（本修复不新增判据）。
func (p *Pipeline) ImportArchive(path string) (*acquire.ImportResult, error) {
	// 先给回收一次机会，再评估本轮余量。
	//
	// 为什么这一步不能省（现场「不自愈」形态的机制本体）：`acquire.Pipeline.Ingest` 在每次
	// 追加**之前**才 TryReclaim；而「余量耗尽」时我们**不会**调用 Ingest ⇒ 若不在这里补一次
	// 回收，源一旦顶到上限就再也没有任何东西推动回收 ⇒ 余量永远回不来 ⇒ 归档导入永久推迟。
	// 这与现场观察到的「暂停源 101→102、5 分钟零自愈」是同一种死结，必须在这里断开。
	//
	// 语义与既有采集循环一致（那里每轮也先 TryReclaim 再追加）；失败不致命：
	// 无恢复分段/责任未转移时 TryReclaim 本就不放行（会原样返回错误），此时按当前余量判定。
	if p.wal != nil {
		if _, err := p.wal.TryReclaim(); err != nil {
			// 回收被门禁拒绝是常态而非异常（见 WAL.TryReclaim 的说明）：它仍会按账本当前水位
			// 剪枝，故不能因这个错误而中止导入。
			_ = err
		}
	}
	// 本轮可放行的事件上限（0 = 不限，即退回既有「一次读完整包」）。
	budget, ok := p.archiveImportBudget()
	if !ok {
		// 余量已耗尽（回收也确实推不动）：**本轮不推进**。这不是失败、更不是丢数据
		// ——归档仍未被标记为已导入，采集轮 250ms 后必再发现它，并在那时再试一次回收。
		// 强行取最小批次反而会越闸并触发「暂停」⇒ 源停死（正是本次要消灭的形态）。
		p.archiveImportDeferred.Add(1)
		return &acquire.ImportResult{Skipped: true,
			Reason: "wal headroom exhausted; archive import deferred to next round"}, nil
	}
	var current acquire.ImportResult
	{
		// 单轮只读一批：读满即停并回 Partial，位置落回账本 ⇒ 下一轮从断点继续。
		res, err := p.imp.ImportGzipBounded(path, budget)
		if err != nil || res == nil {
			return res, err
		}
		current = *res
	}
	if current.Skipped || len(current.Events) == 0 {
		out := current
		return &out, nil
	}

	normalizeOpts := p.normalizeOpts
	if day, dayErr := archiveUTCDay(path); dayErr == nil {
		normalizeOpts.BaseTime = day.Add(12 * time.Hour)
	}
	boundary := NewNormalizeBoundary(normalizeOpts)
	for _, raw := range current.Events {
		boundary.Feed([]byte(raw.Message), raw.Record.Start, raw.Record.End)
	}
	boundary.FlushComplete()
	events := boundary.DrainEvents()
	for i := range events {
		if events[i].Fields == nil {
			events[i].Fields = make(map[string]string)
		}
		events[i].Fields["archive_object_id"] = current.ArchiveObjectID
	}

	result := current
	result.Events = events
	// ImportedCount 保持**采样前**的条数：它回答的是「这次从归档里导入了多少」，
	// 而 Events 才是「实际落库的那批」。两者口径不同不能混用，否则现场无法判断归档是否完整。
	result.ImportedCount = len(events)

	// 采样收口仍作用于放行的那批（整批一次，口径不随切批漂移）。
	sampled := p.sampleEvents(events)
	result.Events = sampled

	var ingestErr error
	if len(sampled) > 0 {
		// 逐批走完整链路：WAL 追加 → Commit(durable) → 投递 → 结果落账。
		ingestErr = p.ingest(sampled)
	}
	if ingestErr != nil {
		// 本批未落库：如实上报（不伪装成成功），且**不置 Imported** ⇒ 下一轮重来。
		return &result, ingestErr
	}

	// 「已导入」只在**归档真的读完**（Partial=false）且本批已 durable 覆盖时才置位。
	// 这是与「读了一半就算导入完成」最要紧的区别：后者会把未读到的尾部静默丢掉。
	if !result.Partial {
		pos, ok := p.Positions()
		if ok && len(sampled) > 0 && pos.Durable >= sampled[len(sampled)-1].Record.End {
			if markErr := p.imp.MarkImported(path, result.ArchiveObjectID,
				sampled[0].Record.Start, sampled[len(sampled)-1].Record.End); markErr != nil {
				return &result, markErr
			}
		}
	}
	return &result, nil
}

func archiveUTCDay(path string) (time.Time, error) {
	name := filepath.Base(path)
	if len(name) < len("2006-01-02") {
		return time.Time{}, fmt.Errorf("pipeline: archive filename has no UTC day")
	}
	day := name[:len("2006-01-02")]
	if strings.Count(day, "-") != 2 {
		return time.Time{}, fmt.Errorf("pipeline: archive filename has no UTC day")
	}
	return time.ParseInLocation("2006-01-02", day, time.UTC)
}

// SetDelivery 运行时替换投递钩子。
func (p *Pipeline) SetDelivery(h DeliveryHook) {
	p.hook = h
	p.inner.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		if h == nil {
			return 0, false, nil
		}
		res, err := h.Deliver(events, replay)
		if err != nil {
			return 0, res.AckLost, err
		}
		p.deliveredCount += len(events)
		return res.HTTPStatus, res.AckLost, nil
	})
}

// SuppressedReplayDrains 转发 acquire 层的「冗余整窗重投抑制」读数（#60-Z1）。
func (p *Pipeline) SuppressedReplayDrains() int64 { return p.inner.SuppressedReplayDrains() }

// TryReclaim 经 logtypes.CanReclaim 门禁推进 reclaim_position。
// HTTP 2xx / REQUEST_DONE 单独不得回收；需恢复分段责任已转移且无 hold。
func (p *Pipeline) TryReclaim() (uint64, error) {
	return p.wal.TryReclaim()
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

// CanReclaimNow 按当前账本恢复分段状态评估是否允许回收。
func (p *Pipeline) CanReclaimNow() bool {
	ent := p.led.Get(p.key)
	if ent == nil {
		return false
	}
	for _, ref := range ent.RecoveryRefs {
		if ref.CoversFrom <= ent.Positions.Reclaim && ref.CoversTo > ent.Positions.Reclaim {
			return logtypes.CanReclaim(ent.Positions, ref.State, ref.ReleaseReason, ref.HasHold)
		}
	}
	// 无覆盖 reclaim 前缀的恢复分段：不得回收。
	return false
}

// BindRecoverySegment 登记受管恢复分段（STAGED）。
func (p *Pipeline) BindRecoverySegment(segID, path string, coversFrom, coversTo uint64) error {
	return p.wal.BindRecoverySegment(segID, path, coversFrom, coversTo)
}

// TransitionRecovery 推进恢复分段责任状态。
func (p *Pipeline) TransitionRecovery(segID string, to logtypes.RecoverySegmentState, reason logtypes.ReleaseReason, receiver string) error {
	return p.led.TransitionRecovery(p.key, segID, to, reason, receiver)
}

func (p *Pipeline) RecoveryRef(segID string) (ledger.RecoveryRef, bool) {
	entry := p.led.Get(p.key)
	if entry == nil {
		return ledger.RecoveryRef{}, false
	}
	for _, ref := range entry.RecoveryRefs {
		if ref.SegmentID == segID {
			return ref, true
		}
	}
	return ledger.RecoveryRef{}, false
}

func (p *Pipeline) RecoveryCovering(from, to uint64) (ledger.RecoveryRef, bool) {
	entry := p.led.Get(p.key)
	if entry == nil {
		return ledger.RecoveryRef{}, false
	}
	for _, ref := range entry.RecoveryRefs {
		if ref.State != logtypes.RecoveryCleaned && ref.CoversFrom <= from && ref.CoversTo >= to {
			return ref, true
		}
	}
	return ledger.RecoveryRef{}, false
}

func (p *Pipeline) SetRecoveryHold(segID string, hold bool) error {
	return p.led.SetRecoveryHold(p.key, segID, hold)
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
func (p *Pipeline) Stats() normalize.Stats { return p.bound.Stats() }

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
