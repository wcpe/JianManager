// 本文件是**恢复与配额**面：采集暂停后的自动复活（maybeResumePausedSources）、
// 恢复期的配额折算、调度回放（schedule*/drainScheduledReplay）、以及持久化
// 前置的提交/延迟投递（persistCommittedBeforeDelivery / deferDurableDelivery）。
//
// 恢复策略的共同前提：缺口必须能被**确定地**判定落库，才允许越过它推进水位；
// 判不动的保持可见，宁可慢也不谎报。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// isInitialHotAuthority 判定记录是否是「HOT 首写」形态的权威（NewStableRecord(OwnerHot,1,...)）：
// 这种记录可以由并发源的投影发布安全合并，不需要按权威切换拒绝。
func isInitialHotAuthority(rec *catalog.Record) bool {
	if rec == nil {
		return false
	}
	return rec.Owner == catalog.OwnerHot && rec.Generation == 1 && !rec.RecoveryRequired &&
		rec.WriteRoute.Owner == catalog.OwnerHot && rec.WriteRoute.Generation == 1 &&
		rec.WriteRoute.DirID == rec.OwnerDirID && !rec.WriteRoute.Frozen
}

func publishedProjectionGenerations(projection *catalog.PublishedProjection) []string {
	if projection == nil {
		return nil
	}
	if len(projection.ProjectionGenerations) > 0 {
		return append([]string(nil), projection.ProjectionGenerations...)
	}
	if projection.ProjectionGeneration != "" {
		return []string{projection.ProjectionGeneration}
	}
	return nil
}

func publishedSourceProjectionGenerations(projection *catalog.PublishedProjection, source SourceConfig) []string {
	if projection != nil && len(projection.SourceProjections) > 0 {
		for _, scope := range projection.SourceProjections {
			if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
				return scope.ProjectionGenerations
			}
		}
		return nil
	}
	return publishedProjectionGenerations(projection)
}

func appendUniqueGeneration(generations []string, generation string) []string {
	for _, existing := range generations {
		if existing == generation {
			return generations
		}
	}
	return append(generations, generation)
}

func (m *Manager) load() error {
	// FR-496：状态从嵌入式 SQLite 索引读入（旧 JSON 迁移已在 openIndex 完成）。
	if m.index == nil {
		// 兼容直接构造 Manager 的测试：没有索引可用，只能保持空状态。
		return nil
	}
	loaded, err := m.loadStateFromIndex()
	if err != nil {
		return err
	}
	m.state = *loaded
	return nil
}

// persist 把内存状态增量写入采集索引（FR-496 spec §2.3）：只写本批次变更的行（UPSERT），
// 事务提交即持久；未变更行不产生任何写语句（空闲轮询不重写历史）。
//
// 成本按「变更源」收敛（这是「60 源下单次持久化 ≤50ms」的关键）：
//   - 只有账本修订号变化、或段覆盖签名变化的源，才重建账本派生行
//     （gap/delivery_batch，占行数绝对多数；WAL 行已于 2026-10-29 随 source_wal 表删除）；
//   - 根表（source/position/projection/source_aux/instance_binding）行数为 O(源数)，
//     每轮由索引层全量比对（几百行量级），因此「投影代次 / 发布待定 / 实例绑定」这类由
//     Manager 直接改 state 的字段无须任何显式标记即天然正确——但前提是**每轮都必须跑
//     apply**：跳过整次写入等于连根表也不比对了，投影代次等元数据变更会静默丢失
//     （曾以 instances 重启用例转红抓到：发布后的 generation 未落库，重启后重复投影）。
//
// 生产 307MB 副本实测：修复前每次持久化对全部 746,884 行做规划+比对（≈3.0s），
// 且因指纹口径缺陷每次都整表重写（47s）；修复后无变更轮次只比对几百行根表（毫秒级）。
//
// 并发与排队（FR-498 并发采集轮的连带修复）：
//   - 单执行者：构建 + 落库同一时刻只有一次在跑，因此「构建顺序 = 落库顺序」，不可能出现
//     旧快照后写（那会让 persistedRev 已推进但索引回到旧值 → 变更永久丢失）；
//   - 落库在 m.mu 之外：SQLite 事务（批量源下几十毫秒级）不再阻塞登记的 m.mu 短临界区；
//   - 合并（coalescing）：等待者只等「一个周期」，且一个周期可被多个调用方共享——短变更的
//     调用者（实例登记，RPC 截止 10s）不会被采集轮里多个源的持久化挤到队尾
//     （race 实测：不合并时登记 ≥1s 截止，TestRegisterInstanceNotBlockedBySaturatedPollRound 转红）。
//
// 锁序：cycleMu → registerMu → pendingMu → persistGate → m.mu。
// persist 保持既有契约：**返回即「当前已声明的变更全部落库」**（FR-499 的「登记返回即落库」
// 保证不变），且走**优先队列**（用户面：采集轮/登记/解算/关停的延迟优先于恢复链吞吐）。
func (m *Manager) persist() error {
	return m.persistCtx(context.Background(), persistPriority)
}

// persistRecoveryStep 是**恢复/重放链**的落库入口：只做**一步**有界落库（受每周期总预算约束）
// 后立即交门，不在这里循环把整段差异吃完。
//
// 为什么（2026-10-03 现场：persistGate 门闩饿死采集轮，SIGQUIT 8 个 goroutine 等 2+ 分钟）：
// `releaseRecovery` 尾部此前调用 persist()——它"循环到做完"的契约在 4.5M 行差异下意味着 ~35 个
// 周期（默认 131k 行/周期）一口气吃完，全程反复夺门；等待者（含采集轮）被压在门外 ⇒ read_pos
// 冻结 + VL 零写入。现在恢复链一步一让，完整性由**驱动链的趟循环**保证（启动恢复的 pass 循环、
// 常驻对账的轮次），且 `persistCovered` 只在整批完成时推进 ⇒ 不会谎报已覆盖。
func (m *Manager) persistRecoveryStep(ctx context.Context) error {
	// 恢复链同样走**有界等待 → 交后台 → 立即返回**（2026-10-03 现场：恢复步整步占门，
	// 6 个投递/采集单元卡 Cond.Wait）。语义不变：水位只在真落完时推进，未完成由驱动循环续做 ✓。
	return m.persistCtx(ctx, persistMode{oneStep: true, maxWait: m.persistHotBudgetOf()})
}

// persistStepYield 是分步持久化「让路」的**下限**（真实窗口见 Manager.persistYieldOf）。
//
// 为什么需要它（2026-10-02 事故）：一次调用若把整批差异做完，就会独占持久化门；采集轮与登记
// 的持久化都被挡在门外，表现为「采集停摆、登记超时」。
//
// 为什么 5ms 不够（2026-10-03 现场）：持门者每 5ms 回来抢一次 `g.mu`，等待者在 Mutex 上被持续
// 压制（SIGQUIT 等 2+ 分钟）。让路窗口必须覆盖**一次完整调度量级**（默认 GOMAXPROCS×5ms、
// 下限 20ms，键 log_index.persist.cycle_yield 可配），且让路只约束「刚交门的那个调用者」。
const persistStepYield = 20 * time.Millisecond

// defaultPersistPriorityStreakMax 是老化阈值默认值（见 persistPriorityStreakMax）。
const defaultPersistPriorityStreakMax = 8

// defaultPersistHotBudget 是**热路径**（采集/投递侧）在持久化门上最多等多久。
//
// 为什么热路径必须"等不起"（2026-10-03 现场二度复现：SIGQUIT 20:48 采集轮仍在同一把 Cond 上等待）：
// 一次落库的**单元**可以在**一条 SQL 语句**里跑很久（如范围剪枝一次删数百万行）——单元预算只在
// 语句之间检查，语句本身不可打断 ✗。于是"分步/让路"再公平也没用：持门者不放门，采集侧就得等 ✗。
// 因此热路径改成：**有界等待 + 超时即交给后台持久化通道**，绝不为落库阻塞采集 ✓。
const defaultPersistHotBudget = 30 * time.Millisecond

// stopGrace 是**关停落库的有界宽限**（P0 复盘 ✓）：高于既有 5s 先例（那只是 WS Shutdown ✓）、
// 远低于现场"200s/30s 未退" ✗；有界即可，宁可降级退出也不无限等（守护面强杀更不干净 ✓）。
// 注：**声明为 var 以便测试临时缩短**（生产不写它 ✓；测试并行时须自持互斥 ✓）。
var stopGrace = 45 * time.Second

// ErrPersistDeferred 表示本次落库尚未完成，已交给后台通道继续处理。
// 采集投递链收到该错误时必须停止向下游发送，等待后续重试。
var ErrPersistDeferred = errors.New("ingest: durable persist deferred")

// persistMode 决定一次持久化调用的排队档位与推进步数。
type persistMode struct {
	// priority=true 走**优先队列**（用户面路径：采集轮的落库、实例登记、解算、归档导入、关停）：
	// 普通队列里的恢复/重放链必须为它让位（用户面延迟 > 恢复链吞吐）。
	priority bool
	// maxWait>0 时是**热路径**：在门上最多等这么久，超时即交给后台通道并返回（不阻塞采集 ✗），
	// 且最多执行一步（不做"循环到做完"✗）。
	maxWait time.Duration
	// failOnHandoff 用于 WAL→投递前的耐久门：交办尚未完成时必须向上游报错，禁止提前投递。
	failOnHandoff bool
	// oneStep=true 只做一步就交门（恢复/重放链）；false = 循环到「此后再无差异」才返回
	// （同步语义：登记返回即已落库、Stop 关停前落完）。
	oneStep bool
	// handoffObs 非 nil 时，在**发生交办**（等门超预算 / 一步后仍有差异）那一刻 +1（#59）。
	// 用途：让调用方能**明确表述**"已交办后台（受理即返）"与"本次已真落库"——persistCtx 的
	// 返回值（nil/error）本身区分不了这两者。其它调用点传 nil ⇒ 零开销。
	handoffObs *atomic.Int64
	// planReuse=true 允许复用门外的既有计划（**只给采集/投递热路径**，2026-10-03 回退修复 ①）：
	// 热路径本就"超时交后台、最终一致"⇒ 复用窗口内的计划不改变可观测语义，却把"每个调用者
	// 各自规划（持 m.mu 重建全源镜像）"降为"每窗口一次"。**强一致路径（登记/关停/恢复步）必须
	// 保持 false**：它们要求"返回即落库"，复用会跳过本轮状态变更的落库（实测会破坏恢复/崩溃
	// 回归用例）。
	planReuse     bool
	qualification *committedRequirement
}

type committedRequirement struct {
	key       string
	pipe      *pipeline.Pipeline
	required  []acquire.CommitIdentity
	confirmed bool
}

func (plan *persistPlan) provesCommit(required *committedRequirement) bool {
	if plan == nil || required == nil {
		return false
	}
	receipt := plan.committed[required.key]
	if receipt == nil || receipt.pipe != required.pipe || receipt.err != nil {
		return false
	}
	for _, id := range required.required {
		if _, ok := receipt.identities[id]; !ok {
			return false
		}
	}
	return true
}

var (
	persistPriority      = persistMode{priority: true}
	persistNormalOneStep = persistMode{oneStep: true}
)

// reconcileYieldFromOpts / reconcileSliceFromOpts 解析对账让路配置：直连字段优先，
// 其次走 ReplayTuning（= `log_reconcile.yield/slice_events` 的配置面），都没有则用默认 ✓。
func reconcileYieldFromOpts(opts Options) time.Duration {
	if opts.ReconcileYield > 0 {
		return opts.ReconcileYield
	}
	if opts.ReplayTuning != nil && opts.ReplayTuning.ReconcileYield > 0 {
		return opts.ReplayTuning.ReconcileYield
	}
	return 0
}

func reconcileSliceFromOpts(opts Options) int {
	if opts.ReconcileSliceEvents > 0 {
		return opts.ReconcileSliceEvents
	}
	if opts.ReplayTuning != nil && opts.ReplayTuning.ReconcileSliceEvents > 0 {
		return opts.ReplayTuning.ReconcileSliceEvents
	}
	return 0
}

// ScanTuning 是**段读聚合**让路与轮内预算的配置面（键 log_index.scan.*；零值各自回退默认 ✓）。
type ScanTuning struct {
	SliceRows int
	Yield     time.Duration
	Budget    time.Duration
}

// defaultScanYield / defaultScanSliceRows 是段读聚合路径让路的默认值（见 scanYield 字段注释）。
const (
	defaultScanYield     = time.Millisecond
	defaultScanSliceRows = 8192
)

func (m *Manager) scanYieldOf() time.Duration {
	if m == nil || m.scanYield <= 0 {
		return defaultScanYield
	}
	return m.scanYield
}

// defaultScanBudget 是单源单轮段读聚合的默认时间预算（见 scanBudget 字段注释）。
const defaultScanBudget = 2 * time.Second

func (m *Manager) scanBudgetOf() time.Duration {
	if m == nil || m.scanBudget <= 0 {
		return defaultScanBudget
	}
	return m.scanBudget
}

// scanValueOf / scanSliceRowsOf 解析段读旋钮：直连字段优先，其次 ScanTuning（配置面 ✓），
// 都没有则交给 accessor 的默认值 ✓（这两个 helper 只负责"取到显式配置"，0 即未配置 ✓）。
func scanValueOf(direct time.Duration, tuning *ScanTuning, pick func(*ScanTuning) time.Duration) time.Duration {
	if direct > 0 {
		return direct
	}
	if tuning != nil {
		if v := pick(tuning); v > 0 {
			return v
		}
	}
	return 0
}

func scanSliceRowsOf(direct int, tuning *ScanTuning) int {
	if direct > 0 {
		return direct
	}
	if tuning != nil && tuning.SliceRows > 0 {
		return tuning.SliceRows
	}
	return 0
}

func (m *Manager) scanSliceOf() int {
	if m == nil || m.scanSliceRows <= 0 {
		return defaultScanSliceRows
	}
	return m.scanSliceRows
}

// defaultRecoveryQuotaFactor / defaultRecoveryDrainGrace 是**恢复期独立配额**的默认值。
//
// 依据（2026-10-04 现场 + 用户定调 ✓）：恢复期的回放/补账是设计行为（gz 归档补账写回原代次
// backlog ✓），必然把源推过常规闸 ⇒ 常规闸在恢复期注定自锁 ✗；而恢复完成后若仍按拓宽判，就是
// 削弱常规期语义 ✗（红线）。宽限取 30m：现场排空动辄小时级 ⇒ 更短的窗有"刚回归即再锁"风险 ✓。
const (
	defaultRecoveryQuotaFactor = 4.0
	defaultRecoveryDrainGrace  = 30 * time.Minute
)

func (m *Manager) recoveryQuotaFactorOf() float64 {
	if m == nil || m.recoveryQuotaFactor < 1 {
		return defaultRecoveryQuotaFactor
	}
	return m.recoveryQuotaFactor
}

func (m *Manager) recoveryDrainGraceOf() time.Duration {
	if m == nil || m.recoveryDrainGrace <= 0 {
		return defaultRecoveryDrainGrace
	}
	return m.recoveryDrainGrace
}

// recoveryInProgress 返回"恢复是否进行中"（既有可检状态 ✓；测试可注入 ✓）。
func (m *Manager) recoveryInProgress() bool {
	if m != nil && m.recoveryProbe != nil {
		return m.recoveryProbe()
	}
	return m != nil && m.StartupRecoveryStatus().InProgress
}

// SourceResumeDiagnostic 是**逐源**暂停/复活判据的可观测快照（现场排障用 ✓）。
//
// 为什么需要它（2026-10-04 现场：`gap_open` 在降而 `paused` 平 ✗）：`gap_open` 是**全局计数**，
// 而单源解除暂停要求**它自己**的缺口全部清零 ✓ ⇒ 必须按**源**看才知道"清零却仍暂停"是 bug ✗
// 还是"仍有残余缺口/暂停原因不在本类"的正确行为 ✓。本快照把这条判据的每个输入都摊开 ✓。
type SourceResumeDiagnostic struct {
	LogSourceID   string
	AcquirePaused bool
	PauseReason   string
	// BacklogPause 表示暂停原因是否属本包积压/容量类（只有 true 才走滞回自愈 ✓）。
	BacklogPause bool
	// Unresolved 是该源未消解缺口数；UnresolvedByReason 按原因细分。
	Unresolved         int
	UnresolvedByReason map[string]int
	// Blocking 是"仍会挡住 ResumeAcquire"的条数（= 未消解且不在分类白名单里的 ✓）。
	Blocking int
	// BacklogEntries / Limits 是当前积压与生效上限（含恢复期拓宽 ✓）。
	BacklogEntries int64
	MaxEntries     int64
	// BacklogTotalEntries / ReplayEntries 是**总量视图**与其中"回放归因"的条数 ✓。
	//
	// 判据（用户 2026-10-05 指定 ✓）：`BacklogTotalEntries == BacklogEntries` ⇒ **无回放归因**
	// （WAL 增长 = 复活后的**常规新数据** ⇒ 方案① 不适用 ✓，靠背压/滞回压制 ✓）；
	// `BacklogTotalEntries > BacklogEntries` ⇒ **存在回放归因** ⇒ 再去点名该走 `IngestReplay` 的调用点 ✓。
	BacklogTotalEntries int64
	ReplayEntries       int64
	// DroppedEntries / DroppedBytes 是该源**累计被丢弃**的读数（只增不减）。
	//
	// 对齐 Fluent Bit `memrb_dropped_chunks` / `memrb_dropped_bytes`（2026-10-29）：
	// 积压闸已改为“丢最旧、不暂停”，丢弃因此成了**设计内行为**。它必须可观测，
	// 否则运维看到“这个源的日志少了”时分不清是该源本就没写、还是丢在了内存队列里。
	//
	// 注：与 `UnresolvedByReason["WAL_BACKLOG_DROPPED"]` **口径不同**：
	// 那个是缺口条数（每丢一段登记一条，会被合并/裁剪），这里是**实际丢弃量**。
	DroppedEntries int64
	DroppedBytes   int64
	// ReadPos / ReclaimPos：三水位补齐（现场离线核 `reclaim ≤ read` 不变量 ✓）。
	ReadPos    uint64
	ReclaimPos uint64
	// DeliveryPos / DurablePos 是账本水位（靶②判据：缺口是否在**已投递水位之上** ⇒ 需恢复链重投
	// 而非水位覆盖 ✓）。
	DeliveryPos uint64
	DurablePos  uint64
	// UnresolvedGaps 摊开每条未消解缺口的区间与原因（现场用它与上面的水位对齐 ✓）。
	UnresolvedGaps []UnresolvedGapView
}

// UnresolvedGapView 是单条未消解缺口的最小可读视图（区间 + 原因 + 是否可分类消解 ✓）。
type UnresolvedGapView struct {
	Start      uint64
	End        uint64
	Reason     string
	Classiable bool
	// AboveDelivery 表示该缺口**整体位于已投递水位之上**（⇒ 水位覆盖判据不可能消解它 ✓，
	// 只能靠恢复链重投后由投递成功证据消解 ✓）——靶②的核心判据 ✓。
	AboveDelivery bool
}

// ResumeDiagnostics 返回所有源的暂停/复活判据快照（按 LogSourceID 升序，稳定可 diff ✓）。
// sweepBacklogTotal 返回**闸视图**总积压（全部源的条目数之和 ✓）：背压判据的输入 ✓。
func (m *Manager) sweepBacklogTotal() int64 {
	if m != nil && m.sweepBacklogOf != nil {
		return m.sweepBacklogOf()
	}
	if m == nil {
		return 0
	}
	muT0 := time.Now()
	m.mu.Lock()
	m.noteMuWait("backlog", muT0)
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		if p != nil {
			pipes = append(pipes, p)
		}
	}
	m.mu.Unlock()
	var total int64
	for _, p := range pipes {
		if wal := p.WAL(); wal != nil {
			entries, _ := wal.BacklogAndLimits()
			total += entries
		}
	}
	return total
}

// BackpressureSkips 返回因净涨背压而跳过的轮数（可观测 ✓）。
func (m *Manager) BackpressureSkips() int64 {
	if m == nil {
		return 0
	}
	return m.backpressureSkips.Load()
}

// RecoveryQuotaStats 暴露恢复期配额的当前口径（可观测 ✓）：生效系数、宽限截止、是否恢复中。
func (m *Manager) RecoveryQuotaStats() (factor float64, graceUntil time.Time, recovering bool) {
	if m == nil {
		return 1, time.Time{}, false
	}
	m.mu.Lock()
	until := m.recoveryGraceUntil
	m.mu.Unlock()
	recovering = m.recoveryInProgress()
	if recovering || (!until.IsZero() && time.Now().Before(until)) {
		return m.recoveryQuotaFactorOf(), until, recovering
	}
	return 1, until, false
}

// syncRecoveryQuota 把"恢复期独立配额"同步到全部管道（每轮 tick + Start 各调一次 ✓）。
//
// 规则（用户定调 ✓）：恢复中 ⇒ 设 factor ✓；恢复刚结束 ⇒ 记宽限截止，期间继续拓宽 ✓；
// 宽限过期 ⇒ 回归常规闸 ✓（届时超常规闸的源按**常规语义**暂停 = 正确行为 ✓，红线不削弱 ✗）。
// noteMuWait 记录一次 m.mu 获取的等待时长（按调用相 ∪ 取最大 ✓）。
//
// 用途（四相下探判据 ✓）：**单点极大** ⇒ 该相被长持有者挡住（支 A ⇒ 点名该相 ✓）；
// **稳定小** ⇒ 该相耗在遍历本身（支 B ⇒ 三遍并一遍 ✓）。
func (m *Manager) noteMuWait(label string, t0 time.Time) {
	if m == nil {
		return
	}
	v := time.Since(t0).Nanoseconds()
	var a *atomic.Int64
	switch label {
	case "backlog":
		a = &m.muWaitBacklog
	case "quota":
		a = &m.muWaitQuota
	case "sources":
		a = &m.muWaitSources
	case "resume":
		a = &m.muWaitResume
	default:
		return
	}
	for {
		o := a.Load()
		if v <= o || a.CompareAndSwap(o, v) {
			return
		}
	}
}

func (m *Manager) syncRecoveryQuota() {
	if m == nil {
		return
	}
	recovering := m.recoveryInProgress()
	muT0 := time.Now()
	m.mu.Lock()
	m.noteMuWait("quota", muT0)
	if recovering {
		m.recoveryGraceUntil = time.Time{}
	} else if m.recoveryGraceUntil.IsZero() {
		m.recoveryGraceUntil = time.Now().Add(m.recoveryDrainGraceOf())
		slog.Info("启动恢复已结束：进入排空宽限窗（期间仍按恢复期独立配额判）",
			"graceUntil", m.recoveryGraceUntil.Format(time.RFC3339),
			"grace", m.recoveryDrainGraceOf().String(), "factor", m.recoveryQuotaFactorOf())
	}
	until := m.recoveryGraceUntil
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		if p != nil {
			pipes = append(pipes, p)
		}
	}
	m.mu.Unlock()

	factor := 1.0
	if recovering || time.Now().Before(until) {
		factor = m.recoveryQuotaFactorOf()
	}
	for _, p := range pipes {
		wal := p.WAL()
		if wal == nil {
			continue
		}
		wal.SetRecoveryWiden(factor)
	}
}

// defaultResumeInterval 是容量自愈兜底的默认周期。
//
// 依据（2026-10-03 现场）：暂停的唯一恢复评估点挂在采集轮上（Append 前/投递后 TryReclaim），
// 而暂停期间采集轮只"只投递不读取"⇒ 一旦投递也停（或上游全堵），恢复评估再无人触发 ✗，
// 表现为暂停不回落、成批源停死。30s 足够低频（开销 ≈ 每源一次 O(1) 账本读）又能把
// "停死数小时"压缩到"最多 30s 后发现可恢复" ✓。
const defaultResumeInterval = 30 * time.Second

func (m *Manager) resumeIntervalOf() time.Duration {
	if m == nil || m.resumeInterval <= 0 {
		return defaultResumeInterval
	}
	return m.resumeInterval
}

// maybeResumePausedSources 是容量自愈兜底的一次执行：**只碰积压/容量类暂停** ✓。
//
// 红线（用户指令）：绝不越权清其它类暂停（容量门禁等）——原因前缀不属于本包积压类的源一律跳过 ✓；
// 对符合的源做「尽力排空存量 → 推进回收 → 滞回评估」，积压回落到滞回线即自动解除 ✓。
// defaultResumeBatchPerRound 是兜底环**每轮最多处理多少个可解源**。
//
// 为什么需要显式容量（2026-10-04 现场：15 分钟只解 1 个 ✗）：兜底环虽然遍历全部暂停源 ✓，
// 但每源的"补证据→消解缺口→评估恢复"是一整串动作，慢源会把一轮拖长 ⇒ 观察上就是"每轮只放行
// 一个" ✗。这里给出**有界但多源**的每轮容量（N 可配 ✓，默认 8），并记录本轮实际放行数 ✓，
// 使"每轮容量"成为可观测、可调的显式量 ✓，而不是由最慢源隐式决定 ✗。
const defaultResumeBatchPerRound = 8

func (m *Manager) resumeBatchOf() int {
	if m == nil || m.resumeBatchPerRound <= 0 {
		return defaultResumeBatchPerRound
	}
	return m.resumeBatchPerRound
}

// tryResumeDiskPausedSource 尝试解除**磁盘容量**导致的采集暂停，成功返回 true。
//
// # 为什么需要它（2026-11-05 F4）
//
// 磁盘达到 `pauseAtPercent`（默认 90%）时 `ApplyCapacity` 会 `PauseAcquire`。
// 而解除暂停的三条路径全都到不了它：
//
//	① 追加前重估（pipeline 的 EvaluateCapacity）——源已暂停 ⇒ 不产生新批次 ⇒ 走不到；
//	② 兜底环候选门——只认 `IsBacklogPauseReason` ⇒ 磁盘原因被 `continue` 跳过；
//	③ WAL 的 maybeResumeBacklogLocked —— 同样只认积压前缀。
//
// ⇒ 源**停到进程重启为止**。而生产上磁盘占用是常态波动（日志轮转、其它服务释放空间），
// 不是单调恶化——“暂停”这个动作对磁盘占用**毫无帮助**（文件还在、别的进程还在写），
// 它只会白白停摆采集。
//
// # 为什么必须带滞回（两个阈值，不是一个）
//
// 若“磁盘降到 90% 以下”就恢复，会在 89.9% ⇄ 90.1% 之间反复“暂停→恢复→暂停”，
// 每轮都重建一次 tailer 与采集通道。所以：
//
//	暂停线：pauseAtPercent（默认 90）
//	恢复线：degradedAtPercent（默认 80）——**回升到 80% 以下才恢复**
//
// 这与 WAL 积压闸的“上限一半”滞回同一精神：避免在阈值附近抖动。
//
// # 为什么必须重新采样而不是读缓存
//
// `m.capacityProvider()` 每次调用都**当场采样**（`SampleDiskUsagePercent`），不是缓存值。
// 这正是本修复成立的前提：磁盘真的降下去了，这里就读得到。
//
// # 不做什么
//
// 不自行解除其它原因的暂停（只处理 `IsDiskPauseReason`）；不改水位；
// 不越过缺口闸——`ResumeAcquire` 自己会因为未消解缺口而拒绝，那是**正确**的（本例只负责
// “磁盘已回落到可接受区间”这一条判据，其余判据仍由既有机制把守）。
func (m *Manager) tryResumeDiskPausedSource(p *pipeline.Pipeline, entry *ledger.Entry) bool {
	if m == nil || p == nil || entry == nil || m.capacityProvider == nil {
		return false
	}
	// **先判原因**：本通道只处理磁盘容量暂停。
	//
	// 为何把判别放在函数内部而不只依靠调用方：调用方将来可能多一个（例如诊断入口、
	// 手动恢复），而“磁盘通道接管积压源”会绕过积压自己的滞回与缺口闸——
	// 那是比 F4 更难查的错误。放在这里，任何调用者都受同一道约束。
	if !entry.AcquirePaused || !acquire.IsDiskPauseReason(entry.PauseReason) {
		return false
	}
	budget, err := m.capacityProvider()
	if err != nil {
		// 采样失败不是“磁盘已降”。保持暂停，下轮重试（与既有保守方向一致）。
		return false
	}
	// 恢复线取 degraded 阈值（默认 80），而不是暂停线——滞回，避免阈值附近抖动。
	resumeBelow := budget.DegradedAtPercent
	if resumeBelow <= 0 {
		resumeBelow = 80
	}
	if budget.DiskUsagePercent >= resumeBelow {
		// 仍然偏高：保持暂停。不刷日志（兜底环频率高，每轮刷会淹没现场）。
		return false
	}
	if err := p.Ledger().ResumeAcquire(p.Key()); err != nil {
		// 磁盘已回落但**其余闸**仍拒绝（典型：未消解缺口）。
		// 这是正确行为，不是失败——把原因记下来即可，下轮磁盘仍低时会再试一次。
		slog.Debug("磁盘已回落但采集仍不能恢复（其余判据拒绝）",
			"logSourceID", p.Key().LogSourceID,
			"diskUsagePercent", budget.DiskUsagePercent,
			"resumeBelow", resumeBelow,
			"error", err.Error())
		return false
	}
	slog.Info("磁盘占用已回落，已解除因磁盘容量导致的采集暂停",
		"logSourceID", p.Key().LogSourceID,
		"diskUsagePercent", budget.DiskUsagePercent,
		"resumeBelow", resumeBelow,
		"pausedReasonBefore", entry.PauseReason)
	return true
}

func (m *Manager) maybeResumePausedSources() (candidates, revived int) {
	muT0 := time.Now()
	m.mu.Lock()
	m.noteMuWait("resume", muT0)
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		if p != nil {
			pipes = append(pipes, p)
		}
	}
	m.mu.Unlock()
	// **稳定序**：轮转窗口只有在稳定序下才有意义 ✓（map 迭代序随机会让"轮转"退化成随机 ✓）。
	tScan := time.Now()
	defer func() { m.nsScanElig.Add(int64(time.Since(tScan))) }()
	sort.Slice(pipes, func(i, j int) bool {
		return pipes[i].Key().LogSourceID+pipes[i].Key().SourceGeneration <
			pipes[j].Key().LogSourceID+pipes[j].Key().SourceGeneration
	})
	// **全量资格扫描（gauge）**：合格暂停源总数 = 下一轮窗口该覆盖的总量 ✓。
	// 为什么单列：现场"`candidates` 恒定 2 而应复活 38" ✗ ⇒ 必须能一眼看出"**合格者有多少 vs 本轮处理了多少**" ✓。
	eligible := 0
	for _, p := range pipes {
		led := p.Ledger()
		if led == nil {
			continue
		}
		// **eligible 的口径**（2026-10-05 小件 ✓，与"★应复活"剪刀差对齐）：**计入**所有积压/容量类暂停源
		// （**含滞回未满足者** ✓）；**不计入**非积压类暂停与已解除者 ✓；已裁定放弃者的缺口被消解、但源可能
		// 仍在暂停 ⇒ **照样计入**（它仍是"可解源" ✓）。
		// ⇒ 与"★应复活"之差 = **滞回条件（backlog < 半闸）** ✓ ⇒ 现场"eligible 稳 46 而剪刀差在降"
		// **不是矛盾**，是两把尺口径不同 ✓✓。
		if st, ok := led.AcquireState(p.Key()); ok && st.Paused && acquire.IsBacklogPauseReason(st.PauseReason) {
			eligible++
		}
	}
	m.sweepEligibleNow.Store(int64(eligible))

	// **轮转窗口**（用户 2026-10-05 ②：只取前 N 且不轮转 ⇒ 长暂停源**永不被覆盖** ✗✗）：
	// 从上一轮停下的位置继续、绕回开头 ✓ ⇒ 任意合格源在 ⌈E/batch⌉ 轮内必被处理 ✓。
	handled := 0
	batch := m.resumeBatchOf()
	start := 0
	if len(pipes) > 0 {
		start = int(m.sweepCursor.Load() % uint64(len(pipes)))
	}
	scanned := 0
	for scanned < len(pipes) && handled < batch {
		p := pipes[(start+scanned)%len(pipes)]
		scanned++
		led := p.Ledger()
		if led == nil {
			continue
		}
		// #66-⑤：**早退之前**先给该源一次「只滚动/补段」的机会。否则"既非积压/容量类暂停、又无未解决
		// 缺口"的落后源（现场 `inst:160/202/203/179`：新窗零 `滚动回收` 行、全库零「回收被拒」行 ✓）
		// 三条面都到不了它：投递确认面要等新批次被确认（安静源没有 ✗）、自愈面准入要求"暂停或缺口"
		//（它两者皆非 ✗）、本环候选资格又要求"积压/容量类暂停"（早退 ✗）⇒ 回收链永久卡住。
		// 轮转窗口**会扫遍所有管道**（游标 ⌈E/batch⌉ 轮内覆盖全部 ✓）⇒ 挂在这里 ⇒ 任意落后源
		// 在有限轮内必被推进一次 ✓。
		//
		// **成本定界**：先做 O(1) 的"有欠账"前置（reclaim ≥ delivery ⇒ 无可推进 ⇒ 不 probe ✓，
		// 健康源每轮只多一次 Positions 读 ✓），再走**既有有界执行器**（单步预算 + 在飞上限；
		// 超预算只表示"本轮不等它"、后台照跑 ✓）⇒ **每轮有界** ✓。
		// **不新增旁路语义**：调用的就是两面共用的同一助手，hold 优先/幂等/绑定前后水位复读
		//（repairStalledReclaim → backfillDeliveryConfirmedCoverage ✓）全部继承 ✓。
		if pos, ok := led.Positions(p.Key()); ok {
			if src, srcOK := m.sourceConfigFor(p); srcOK {
				m.noteReclaimStall(p, src, pos)
			}
			if pos.Reclaim < pos.Delivery {
				if src, srcOK := m.sourceConfigFor(p); srcOK {
					m.sweepRollingProbes.Add(1)
					tRoll := time.Now()
					if !m.runSweepStepBounded(m.sweepStepBudgetOf(), func() { m.reclaimStep0RollingOnly(p, src) }) {
						m.skipBlocked.Add(1)
					}
					m.nsRolling.Add(int64(time.Since(tRoll)))
				}
			}
			// #66-⑥：**投递欠账**（read > delivery）同样在这里被**有界驱动一次**。
			// 为什么必须在"这里"：现场 `inst:160` 是 `read=441M / delivery=reclaim=331M` ⇒ ⑤ 的回收欠账
			// 前置恒假（reclaim 已追平 ✗），而三条现存投递面全都到不了这类源 —— 自愈面步 1 在
			// `AcquirePaused` **之内** ✗、自愈面状态闸要求"暂停或缺口" ✗、兜底环候选只服务积压/
			// 容量类暂停 ✗、#56 排空宿主只服务**登记过 `AppendRejected`** 的源 ✗ ⇒ 投递水位永久静止 ✗✗。
			//
			// **有界**：复用既有 `DeliverPending`（单次 ≤ `log_capacity.max_replay_events_per_drain`
			// 条，默认 500 ✓；只发 Durable 条目 ✓；位置账本幂等 ✓；未推进退避/失败不退避 ✓）
			// + 既有有界执行器（单步预算 + 在飞上限 ✓）。
			// **单源可重入幂等**：整窗 replay 的既有前提是"绝不并发对同源发起两次"
			//（见 schedulePendingReplay 注释 ✓）⇒ 必须与 #56 宿主**共用同一张在飞表**认领；
			// 认领不到（他面在飞）⇒ 本轮让路、**下一轮重试**（窗口轮转必然再来 ✓）。
			// **不复活**：不调 EvaluateResume、不改暂停态、不入候选口径 ⇒ 候选红线不变 ✓。
			// #179-②：**破环直投** —— 缝属"待重读"类且已 resolved（非终局）时不等实例 resume，
			// 直接把缝区间的 durable 存量经标准投递面投出 ⇒ 成功路径自动按既有证据口径关缝 ✓。
			//
			// **INGEST-12**：本步与相邻两步同样经 `runSweepStepBounded` + 同一张在飞表收口。
			// 旧形态是这一步唯一**裸调用**（相邻 9073/9103 都包在大执行器里）⇒ 其内部 O(n) 重活
			// （Ledger 深克隆 + WAL 全量快照 + 同步 VL 写）既不占预算也不进在飞计数，全都落在
			// 兜底环 ticker 单线程上，与 30 行前「论界**每轮有界** ✓」的自述直接矛盾。
			// 认领在**函数外**两档 O(1) 门闸之后做（`sourceConfigFor` 是内存查表），使"深克隆/快照
			// 这类开销"也只在认领成功后才发生；`directDeliverResolvedReReadGap` 自身不再认领
			// （避免同一 key 自锁）。
			key := p.Key().LogSourceID + "/" + p.Key().SourceGeneration
			if _, srcOK := m.sourceConfigFor(p); srcOK && m.tryClaimReplayInflight(key) {
				m.sweepDirectDeliverProbes.Add(1)
				tDirect := time.Now()
				started := make(chan struct{})
				finished := m.runSweepStepBounded(m.sweepStepBudgetOf(), func() {
					close(started)
					defer m.finishScheduledReplay(key) // 清在飞（待办仍在则下轮再来 ✓）
					if src, okSrc := m.sourceConfigFor(p); okSrc {
						m.directDeliverResolvedReReadGap(p, src)
					}
				})
				if !finished {
					select {
					case <-started:
						// 已发起、只是等待超预算：调用在后台继续，在飞标记由它结束时清 ✓
					default:
						// 未发起（在飞上限拒绝）：撤回认领 ⇒ 本轮不做该源，且不热自旋 ✓。
						m.finishScheduledReplay(key)
					}
				}
				m.nsRollingDirectDeliver.Add(int64(time.Since(tDirect)))
			}
			if pos.Delivery < pos.Read {
				key := p.Key().LogSourceID + "/" + p.Key().SourceGeneration
				if _, srcOK := m.sourceConfigFor(p); srcOK && m.tryClaimReplayInflight(key) {
					m.sweepDeliveryProbes.Add(1)
					tDeliver := time.Now()
					started := make(chan struct{})
					finished := m.runSweepStepBounded(m.sweepStepBudgetOf(), func() {
						close(started)
						defer m.finishScheduledReplay(key) // 清在飞（待办仍在则再唤醒 ✓）
						_, _ = p.DeliverPending()
					})
					if !finished {
						select {
						case <-started:
							// 已发起、只是等待超预算：调用在后台继续，在飞标记由它结束时清 ✓
						default:
							// 未发起（在飞上限拒绝）：撤回认领 ⇒ 本轮不排空该源，且**不热自旋** ✓
							//（本路径不登记 replayPending ⇒ 释放即可，下一轮重试 ✓）。
							m.finishScheduledReplay(key)
						}
					}
					m.nsRollingDelivery.Add(int64(time.Since(tDeliver)))
				}
			}
		}
		entry := led.Get(p.Key())
		if entry == nil || !entry.AcquirePaused {
			continue
		}
		if !acquire.IsBacklogPauseReason(entry.PauseReason) {
			// 2026-11-05 F4：磁盘容量暂停走**自己的**恢复通道。
			//
			// 它不能落进下面的积压候选分支——两者的恢复前提不同：
			// 积压靠「积压回落」自愈，而磁盘暂停**与积压无关**，只能靠重新采样磁盘。
			// 旧实现在这里直接 continue，于是磁盘暂停永不进入任何恢复路径，
			// 源会一直停到进程重启（生产常态：磁盘占用是波动量，不是单调恶化）。
			if acquire.IsDiskPauseReason(entry.PauseReason) {
				if m.tryResumeDiskPausedSource(p, entry) {
					m.resumedDiskSources.Add(1)
				}
				continue
			}
			m.skipEnumeration.Add(1) // 枚举类跳过（原因非积压类 ✓）——红线不变 ✓，但要可读 ✓
			continue
		}
		// **枚举类跳过**（不在管道/原因非积压类）：分类计数 ✓，让"41 家为何不动"可直接读出来 ✓。
		handled++ // 计入本轮容量：**处理**（无论最终是否放行）都占额度 ✓，避免慢源无限拖延一轮 ✗
		candidates++
		m.sweepCandidates.Add(1) // 计数归属在**观测发生处** ✓（直接调用者也能看到读数 ✓）
		if m.resumeProbe != nil {
			m.resumeProbe(p.Key().LogSourceID)
		}
		// **缺口闸兜底**（2026-10-03 现场终章的决定性根因）：积压类暂停的恢复前置里有一条
		// "账本无未解决 gap"——`ledger.ResumeAcquire` 在有缺口时**直接拒绝**
		// （见 internal/worker/logs/ledger/ledger.go:876-885 `unresolved gaps still block acquisition`）。
		// 现场形态正是"积压已清零（entries=0）仍无一复活" ✗✗——它们**不是**被积压挡住，而是被缺口挡住 ✓。
		// 因此这里必须先把「已发布投影证据能覆盖的缺口」消解掉（缺口语义红线：无证据不得凭空消解 ✗，
		// 由 autoResolveGapsFromPublished 自己把关 ✓），再评估恢复 ✓。
		if src, ok := m.sourceConfigFor(p); ok {
			// **有界**（同执行器 ✓）：本步要读该源的已发布投影并可能做整源扫描 ⇒ 慢源/被 m.mu 挡住时
			// 会让整个维护环停摆 ✗（现场 ticks=1 冻结的同类）。跳过后**下一轮重试**、语义不变 ✓。
			tResolve := time.Now()
			if !m.runSweepStepBounded(m.sweepStepBudgetOf(), func() { m.autoResolveGapsFromPublished(src, p) }) {
				m.skipBlocked.Add(1)
			}
			m.nsResolve.Add(int64(time.Since(tResolve)))
		}
		// 排空侧：先把已 durable 未确认的存量发出去（投递成功才会推进回收）✓
		// **有界等待**（2026-10-05 现场 ✓）：`DeliverPending`→`deliver` 会拿 `m.mu`，而"长持 m.mu 的
		// 规划者"（整源镜像重建那类 ✓）可以让它阻塞很久 ✗ ⇒ 维护环不许挂在这上面 ✗✗。
		// 照 `persistHot` 同型：超预算就**跳过本轮该步**（计入 skipBlocked ✓，继续下一源 ✓），
		// 被挡的那次调用会在后台自行完成 ✓（`m.mu` 释放后即返回 ✓，不丢工作 ✓）。
		tDeliver := time.Now()
		if !m.runSweepStepBounded(m.sweepStepBudgetOf(), func() { _, _ = p.DeliverPending() }) {
			m.skipBlocked.Add(1)
		}
		m.nsDelivery.Add(int64(time.Since(tDeliver)))
		// 回收侧再评估：TryReclaim（按当前水位剪枝）+ 滞回恢复 ✓
		before := led.Get(p.Key())
		wasPaused := before != nil && before.AcquirePaused
		// **有界**（同执行器 ✓）：`EvaluateResume` = `TryReclaim`（账本/分段门禁 ✓）+ 滞回评估 ⇒
		// 在 m.mu 被长持有或分段扫描慢时会阻塞 ✗。跳过只表示"本轮不评"，**下一轮照评** ✓
		// （滞回与分类消解都是幂等的 ✓，语义不变 ✓）。
		tEvaluate := time.Now()
		if !m.runSweepStepBounded(m.sweepStepBudgetOf(), func() { _ = p.EvaluateResume() }) {
			m.skipBlocked.Add(1)
		}
		m.nsEvaluate.Add(int64(time.Since(tEvaluate)))
		after := led.Get(p.Key())
		if wasPaused && after != nil && after.AcquirePaused {
			// **滞回/其它跳过**：评估过但未解除 ⇒ 分类计数 ✓（现场 41 家若全落这里 ⇒ 判据问题 ✓）。
			m.skipHysteresis.Add(1)
		}
		if wasPaused && after != nil && !after.AcquirePaused {
			revived++
			m.sweepRevived.Add(1)
		}
	}
	// 下一轮从**新的位置**继续 ✓（轮转的核心一行 ✓；scanned 含被跳过的项 ⇒ 游标必然前进 ✓）。
	if len(pipes) > 0 {
		m.sweepCursor.Store(uint64((start + scanned) % len(pipes)))
	}
	// 耗时读数的分母：**记在自己函数内** ✓（不碰调用方 ⇒ 少一个跨作用域锚点 ✓）。
	m.lastRoundSources.Store(int64(candidates))
	return candidates, revived
}

// sourceConfigFor 取某条管道对应的源配置（键形态与 autoResolveGapsFromPublished 内部一致 ✓）。
func (m *Manager) sourceConfigFor(p *pipeline.Pipeline) (SourceConfig, bool) {
	if m == nil || p == nil {
		return SourceConfig{}, false
	}
	key := p.Key()
	cfgKey := key.LogSourceID + "/" + key.SourceGeneration
	muT0 := time.Now()
	m.mu.Lock()
	m.noteMuWait("sources", muT0)
	src, ok := m.sources[cfgKey]
	if !ok {
		// 兼容：部分路径用存储命名空间作为键（见 runtime.go 里 m.pipes[storageNamespace] 的用法）。
		src, ok = m.sources[key.LogSourceID]
	}
	m.mu.Unlock()
	return src, ok
}

// ── #56：追加被拒 ⇒ 「排空存量」改为调度（宿主 = 既有容量自愈兜底环） ─────────────────
//
// 现场（opt2 三栈定名）：WAL.Append 被拒（暂停/上限）时原先在 emit→ingest 链上**同步**执行
// deliverPendingBestEffort（replay=true，整窗、百万级批）⇒ 采集轮停摆、goroutine 堆积 183→242，
// 而全程 m.mu 无慢持锁 ⇒ 在途阻塞。修法：摄取路径只登记待办 + 非阻塞唤醒（O(1)），
// 排空由**既有**兜底环在路径外执行（不新造环 ⇒ 生命周期/关停沿用已审的那条路径）。

// schedulePendingReplay 登记「该源需排空已 durable 存量」并唤醒兜底环（#56，摄取路径内只做这件事）。
//
// 合并语义：同源已有待办 ⇒ 不重复入队、不重复唤醒（避免一次暂停风暴刷出 N 次整窗 replay）。
// 单飞语义：该源正在排空时新到的被拒**仍会登记**（待办保留），由排空完成后的再唤醒接手；
// 绝不并发发起同源两次整窗 replay（那会打乱同源投递的串行前提）。
// 真正完成G/只读确认才解除旧失败退避；新输入不能借此延长或重置在飞verify预算。
func (m *Manager) scheduleConfirmedReplay(key string) {
	m.replayMu.Lock()
	delete(m.replayDue, key)
	m.replayMu.Unlock()
	m.schedulePendingReplay(key)
	m.replayMu.Lock()
	wake := m.replayWake
	m.replayMu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (m *Manager) schedulePendingReplay(key string) {
	if m == nil || key == "" {
		return
	}
	m.replayMu.Lock()
	if m.workClosed {
		m.replayMu.Unlock()
		return
	}
	if m.replayPending == nil {
		m.replayPending = make(map[string]struct{})
	}
	if m.replayWake == nil {
		m.replayWake = make(chan struct{}, 1)
	}
	_, dup := m.replayPending[key]
	m.replayPending[key] = struct{}{}
	if !dup {
		m.replayOrder = append(m.replayOrder, key)
	}
	wake := m.replayWake
	m.replayMu.Unlock()
	m.replayScheduledTotal.Add(1)
	if dup {
		return // 合并：同源已有待办 ⇒ 不重复唤醒
	}
	select {
	case wake <- struct{}{}:
	default: // 已有待唤醒信号：合并
	}
}

// replayWakeCh 返回唤醒通道（懒创建；nil 通道在 select 里等价于"永不就绪"，安全）。
func (m *Manager) replayWakeCh() <-chan struct{} {
	m.replayMu.Lock()
	defer m.replayMu.Unlock()
	if m.replayWake == nil {
		m.replayWake = make(chan struct{}, 1)
	}
	return m.replayWake
}

// takeScheduledReplay 认领一条「待排空且不在飞」的源（标记在飞、从待办移除）。
// 全部待办都在飞或没有待办 ⇒ 返回 false（调用方据此结束本轮）。
func (m *Manager) takeScheduledReplay() (string, bool) {
	m.replayMu.Lock()
	defer m.replayMu.Unlock()
	if m.workClosed {
		return "", false
	}
	if len(m.replayOrder) == 0 && len(m.replayPending) > 0 {
		for key := range m.replayPending {
			m.replayOrder = append(m.replayOrder, key)
		}
		sort.Strings(m.replayOrder)
	}
	for left := len(m.replayOrder); left > 0; left-- {
		key := m.replayOrder[0]
		m.replayOrder = m.replayOrder[1:]
		if _, pending := m.replayPending[key]; !pending {
			continue
		}
		_, busy := m.replayInflight[key]
		if busy || time.Now().Before(m.replayDue[key]) {
			m.replayOrder = append(m.replayOrder, key)
			continue
		}
		if m.replayInflight == nil {
			m.replayInflight = make(map[string]struct{})
		}
		m.replayInflight[key] = struct{}{}
		delete(m.replayPending, key)
		delete(m.replayDue, key)
		return key, true
	}
	return "", false
}

// tryClaimReplayInflight 以 #56 的**同一张在飞表**为"整窗 replay 排空"做**单源互斥**（#66-⑥）。
//
// 为什么必须共用这张表：`drainScheduledReplay`（#56 宿主）与兜底环轮转窗口的投递步（#66-⑥）
// 都会发起 `DeliverPending`（replay=true ⇒ ingest 侧**整窗 replace**），而该路径的既有前提是
// "绝不并发对同源发起两次整窗 replay（那会打乱同源投递的串行前提）"（见 schedulePendingReplay 注释 ✓）。
// 直调而不认领 = 把这个前提破掉 ✗✗ ⇒ 两条面必须认同一张在飞表 ✓。
//
// 与 takeScheduledReplay 的差别：本方法**不动** replayPending（钩子路径不是"追加被拒"登记来的 ✓）；
// 释放一律走 finishScheduledReplay（清在飞 + 待办仍在则再唤醒 ✓）。
func (m *Manager) tryClaimReplayInflight(key string) bool {
	if m == nil || key == "" {
		return false
	}
	m.replayMu.Lock()
	defer m.replayMu.Unlock()
	if m.workClosed {
		return false
	}
	if m.replayInflight == nil {
		m.replayInflight = make(map[string]struct{})
	}
	if _, busy := m.replayInflight[key]; busy {
		return false
	}
	m.replayInflight[key] = struct{}{}
	return true
}

// finishScheduledReplay 结束一次排空：清在飞标记；若该源在排空期间又被登记（新到的追加被拒）
// ⇒ 再唤醒一次，让待办在不远的将来被真正处理（不依赖下一轮兜底 tick）。
func (m *Manager) finishScheduledReplay(key string) {
	m.replayMu.Lock()
	delete(m.replayInflight, key)
	_, stillPending := m.replayPending[key]
	wake := m.replayWake
	m.replayMu.Unlock()
	if !stillPending || wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// abandonScheduledReplay 撤回一次认领（有界执行器因**在飞上限**拒绝发起时）：
// 待办放回集合、清在飞标记 ⇒ 留待下一轮，绝不把源永久标成"在飞"（否则该源再也不会被排空 ✗）。
func (m *Manager) abandonScheduledReplay(key string) {
	m.replayMu.Lock()
	delete(m.replayInflight, key)
	if !m.workClosed {
		if m.replayPending == nil {
			m.replayPending = make(map[string]struct{})
		}
		if _, pending := m.replayPending[key]; !pending {
			m.replayOrder = append(m.replayOrder, key)
		}
		m.replayPending[key] = struct{}{}
	}
	m.replayMu.Unlock()
}

// drainScheduledReplay 排空「因追加被拒而登记」的存量投递（#56）。由兜底环在**路径外**调用。
//
// 每源一步都走既有的**有界执行器**（慢源不拖死兜底环：超预算放弃等待、调用在后台跑完并自清在飞）。
// 返回本轮真正发起排空的源数（观测/回归用）。
func (m *Manager) drainScheduledReplay() int {
	handled := 0
	for {
		key, ok := m.takeScheduledReplay()
		if !ok {
			return handled
		}
		p, ok := m.pipeFor(key)
		if !ok || p == nil {
			m.finishScheduledReplay(key) // 源已注销：丢弃待办（无管道可排空）
			continue
		}
		handled++
		started := make(chan struct{})
		finished := m.runSweepStepBounded(m.sweepStepBudgetOf(), func() {
			close(started)
			defer m.finishScheduledReplay(key)
			if !m.deferredDelivery {
				if _, err := p.DeliverPending(); err != nil {
					m.deferDurableDelivery(key)
				}
				return
			}
			m.runDurableDelivery(key, p)
		})
		if !finished {
			select {
			case <-started:
				// 已发起、只是等待超预算：调用在后台继续，在飞标记由它结束时清除 ✓
			default:
				// 未发起（在飞上限拒绝）：撤回认领并**结束本轮** —— 不能就地重试，否则
				// takeScheduledReplay 会再次认领同一个键 ⇒ 热自旋（在飞上限在被别人占用时
				// 会持续拒绝，循环永不退出 ✗）。待办已放回集合 ⇒ 下一轮/下一次被拒再试 ✓。
				m.abandonScheduledReplay(key)
				return handled
			}
		}
	}
}

func (m *Manager) scheduleReplayRetry(key string) {
	interval := m.selfHealInterval
	if interval <= 0 {
		interval = gapAutoResolveInterval
	}
	m.replayMu.Lock()
	defer m.replayMu.Unlock()
	if m.workClosed {
		return
	}
	if m.replayPending == nil {
		m.replayPending = make(map[string]struct{})
	}
	if _, pending := m.replayPending[key]; !pending {
		m.replayOrder = append(m.replayOrder, key)
	}
	m.replayPending[key] = struct{}{}
	if m.replayDue == nil {
		m.replayDue = make(map[string]time.Time)
	}
	m.replayDue[key] = time.Now().Add(interval)
}

func (m *Manager) runDurableDelivery(key string, p *pipeline.Pipeline) {
	m.mu.Lock()
	source, exists := m.sources[key]
	m.mu.Unlock()
	if !exists {
		return
	}
	ctx, release, err := m.acquireSourceDelivery(m.lifecycleContext(), source)
	if err != nil {
		return
	}
	defer release()
	defer m.pruneCommittedReceipt(key, p)
	delivered, err := p.DeliverPendingContext(ctx)
	pending, pendingErr := p.HasPendingDelivery(ctx)
	if !pending && pendingErr == nil {
		m.mu.Lock()
		current := m.pipes[key] == p
		m.mu.Unlock()
		if current {
			// 全部精确ACK只结束正文外发，不代表后置责任证明与回收已完成。
			// 仍持同一逻辑owner，复用原有界助手逐字段读取当前PP/正文并复核ACK、hold和实际目标。
			if pos, ok := p.Ledger().Positions(p.Key()); ok && pos.Reclaim < pos.Delivery {
				m.rollingReclaimRepair(p, source, ctx)
				if after, ok := p.Ledger().Positions(p.Key()); ok && after.Reclaim < after.Delivery {
					// 当前证明仍不可得时沿原按源退避续做；不把已ACK正文重列为待外发。
					m.scheduleReplayRetry(key)
				}
			}
			if err != nil {
				// 后置落库失败也不能因delivered=true丢掉真实状态的持久化责任。
				m.requestPersist()
			}
		}
		if err != nil && !delivered {
			m.scheduleReplayRetry(key)
		}
		return
	}
	if pendingErr != nil || !delivered {
		m.scheduleReplayRetry(key)
		return
	}
	// true只代表本批真实ACK；后置hold/proof错误不能挡住后续精确未ACK条目。
	// UNKNOWN返回false走有界退避，不以同水位反复续排制造热循环。
	m.schedulePendingReplay(key)
}

// ScheduledReplayCount 返回「追加被拒 ⇒ 登记待办」的累计次数（#56 观测读数）。
func (m *Manager) ScheduledReplayCount() int64 {
	if m == nil {
		return 0
	}
	return m.replayScheduledTotal.Load()
}

// RunCapacityResumeLoop 是容量自愈兜底循环（低频、只读账本、ctx 结束即退）。
func (m *Manager) RunCapacityResumeLoop(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(m.resumeIntervalOf())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.lifecycleContext().Done():
			return
		case <-m.replayWakeCh():
			// #56：追加被拒的排空请求 —— 在**摄取路径之外**执行（这正是本修复的目的）。
			// 关停故事：环随 ctx 退出；已发起的排空调用是有界的 WAL 快照投递，其在飞时长与
			// sweepOnce 的既有有界步骤同源（超预算放弃**等待**、调用后台跑完，不丢数据：条目仍在 WAL）。
			m.drainScheduledReplay()
		case <-ticker.C:
			m.drainScheduledReplay()
			m.sweepTicks.Add(1) // 心跳：环活着就有读数 ✓（停止 ⇒ 心跳不动 ⇒ 一眼可辨 ✓）
			// **兜底 recover（连栈）**：一轮内的 panic 不许杀死整个兜底环 ✗（现场"计数恒定"的形态 ✓）；
			// 栈一起打（用户 2026-10-05：只打 panic 原文往往不足以一次定位 ✓）。
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.sweepPanics.Add(1)
						slog.Error("兜底环本轮 panic（已恢复，环继续）",
							"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
					}
				}()
				m.sweepOnce()
			}()
		}
	}
}

// sweepOnce 是兜底环的**一轮全部动作**（自 RunCapacityResumeLoop 原样抽出：只搬家，
// `continue→return` 与缩进由**精确文本替换**完成 ✓，逻辑逐字不变 ✓）。
func (m *Manager) sweepOnce() {
	roundStart := time.Now()
	defer func() { m.nsRoundTotal.Add(int64(time.Since(roundStart))) }()
	// **窗口化净涨背压**（2026-10-05 现场修正 ✗✓）：单轮比较太敏感 —— 现场 41 家
	// "paused + backlog_pause + blocking=0 + backlog < 半闸"三项前置全满足却无一复活 ✗，
	// 头号嫌疑就是**任何一轮抖动都被判净涨 ⇒ 永久跳过** ✗。
	// 改为：只有**连续 N 轮净涨**（默认 3，可配 ✓）才跳过；读的仍是**闸视图**积压 ✓。
	tBacklog := time.Now()
	now := m.sweepBacklogTotal()
	m.nsBacklogTotal.Add(int64(time.Since(tBacklog)))
	if m.lastSweepKnown && now > m.lastSweepBacklog {
		m.risingStreak.Add(1)
	} else {
		m.risingStreak.Store(0)
	}
	streak := m.risingStreak.Load()
	m.lastSweepBacklog, m.lastSweepKnown = now, true
	if streak >= int64(m.sweepBackpressureStreakOf()) {
		m.skipBackpressure.Add(1)
		slog.Warn("兜底环背压：连续多轮积压净涨，本轮不放宽也不复活",
			"streak", streak, "threshold", m.sweepBackpressureStreakOf(), "backlog", now)
		return // 原 `continue` ⇒ 抽出后同一语义：结束本轮 ✓
	}
	tQuota := time.Now()
	m.syncRecoveryQuota()
	m.nsSyncQuota.Add(int64(time.Since(tQuota)))   // 先同步配额口径（恢复中/宽限/回归 ✓），再扫暂停源 ✓
	cands, revived := m.maybeResumePausedSources() // 计数由函数内累加 ✓（避免双计 ✗）
	// **每轮为何无复活**（把猜测变读数 ✓）：有候选却零复活时打一条汇总 ✓，否则静默 ✓。
	if cands > 0 && revived == 0 {
		slog.Warn("兜底环本轮无复活（可观测：候选/复活/各类跳过计数）",
			"candidates", cands, "revived", revived,
			"skip_backpressure", m.skipBackpressure.Load(),
			"skip_hysteresis", m.skipHysteresis.Load(),
			"skip_enum", m.skipEnumeration.Load())
	}
}

// groupReconcileEvents 是常驻对账的**唯一分组入口**：把让路旋钮（slice/yield）与观测口
// 一次性接上，避免调用点各写各的（调用点漏接 = 让路静默失效 ✗）。
func (m *Manager) groupReconcileEvents(source SourceConfig, events []logtypes.Event) (map[string][]logtypes.Event, []string, error) {
	return groupEventsByUTCDayYielding(source, events, m.reconcileSliceOf(), m.reconcileYieldOf(), m.reconcileYieldNotify)
}

// defaultReconcileYield / defaultReconcileSliceEvents 是**对账 CPU 让路**的默认值。
//
// 依据（2026-10-03 现场）：60 台/113 万条规模下对账分组与采集抢核 ⇒ 采集轮"无锁无等待却推不动" ✗。
// 默认 4096 条 / 5ms：10 万条源额外延迟 ≈ 24×5ms ≈ 120ms（可忽略 ✓），但每个切片都交还 P ✓。
const (
	defaultReconcileYield       = 5 * time.Millisecond
	defaultReconcileSliceEvents = 4096
)

// reconcileYieldOf 归一化对账让路时长（<=0 ⇒ 默认；负值配置在启动期已被拒 ✗）。
func (m *Manager) reconcileYieldOf() time.Duration {
	if m == nil {
		return defaultReconcileYield
	}
	if m.reconcileYield > 0 {
		return m.reconcileYield
	}
	return defaultReconcileYield
}

// reconcileSliceOf 归一化让路切片大小（<=0 ⇒ 默认）。
func (m *Manager) reconcileSliceOf() int {
	if m == nil || m.reconcileSliceEvents <= 0 {
		return defaultReconcileSliceEvents
	}
	return m.reconcileSliceEvents
}

// persistHot 是采集/投递侧的统一入口：绝不为落库阻塞（超时交给后台通道）。
func (m *Manager) persistHot() error {
	return m.persistCtx(context.Background(), persistMode{
		priority:  true,
		maxWait:   m.persistHotBudgetOf(),
		planReuse: true,
	})
}

// persistDurableBeforeDelivery 是 WAL 提交后的耐久门。
// 交办后台不等于本批已落库，故超时必须返回 ErrPersistDeferred，让采集管道跳过本次投递。
func (m *Manager) persistDurableBeforeDelivery() error {
	return m.persistCtx(context.Background(), persistMode{
		priority:      true,
		maxWait:       m.persistHotBudgetOf(),
		planReuse:     true,
		failOnHandoff: true,
	})
}

// pruneCommittedReceipt在锁外计算现WAL交集；短m.mu只替换同一旧回执，不能覆盖新完成计划。
func (m *Manager) pruneCommittedReceipt(key string, pipe *pipeline.Pipeline) {
	m.pruneCommittedReceiptObserved(key, pipe, nil)
}

// copied 仅用于确定性回归：观察实际完整身份编码次数并钉住锁外边界，生产调用恒为 nil。
func (m *Manager) pruneCommittedReceiptObserved(key string, pipe *pipeline.Pipeline, copied func(int)) {
	// 锁序仅m.mu；只捕获旧回执指针，完整身份交集与编码不持此锁。
	m.mu.Lock()
	previous := m.committedReceipts[key]
	m.mu.Unlock()
	live, err := pipe.WAL().CommittedIdentities()
	if copied != nil {
		copied(0)
	}
	if err != nil || previous == nil || previous.pipe != pipe {
		return
	}
	kept := &committedSourceReceipt{pipe: pipe, identities: make(map[acquire.CommitIdentity]struct{})}
	for id := range previous.identities {
		if _, ok := live[id]; ok {
			kept.identities[id] = struct{}{}
		}
	}
	m.mu.Lock()
	if m.committedReceipts[key] == previous && m.pipes[key] == pipe {
		m.committedReceipts[key] = kept
	}
	m.mu.Unlock()
}

// committedContains只接受实际完成计划的精确摘要；源级水位、generic nil均不能代替。
func (m *Manager) committedContains(key string, pipe *pipeline.Pipeline, required []acquire.CommitIdentity) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	receipt := m.committedReceipts[key]
	if receipt == nil || receipt.pipe != pipe || m.pipes[key] != pipe {
		return false
	}
	for _, id := range required {
		if _, ok := receipt.identities[id]; !ok {
			return false
		}
	}
	return true
}

func (m *Manager) persistCommittedBeforeDelivery(ctx context.Context, key string, pipe *pipeline.Pipeline, required []acquire.CommitIdentity) error {
	if len(required) == 0 {
		return nil
	}
	if m.committedContains(key, pipe, required) {
		return nil
	}
	m.mu.Lock()
	if m.pipes[key] != pipe {
		m.mu.Unlock()
		return fmt.Errorf("ingest: source pipeline changed before commit qualification")
	}
	// 首次启动/先前交办完成也必须取得本进程的实际计划回执，不能借persistedRev基线授证。
	delete(m.persistedRev, key)
	m.mu.Unlock()
	request := &committedRequirement{key: key, pipe: pipe, required: required}
	if err := m.persistCtx(ctx, persistMode{priority: true, maxWait: m.persistHotBudgetOf(), planReuse: true, failOnHandoff: true, qualification: request}); err != nil {
		return err
	}
	// 当前调用可消费它实际完成的旧变体回执；缓存仍只保留现WAL交集，不因并发新Append全局作废。
	m.mu.Lock()
	ownerCurrent := m.pipes[key] == pipe
	m.mu.Unlock()
	if !ownerCurrent || !request.confirmed && !m.committedContains(key, pipe, required) {
		m.requestPersist()
		return ErrPersistDeferred
	}
	return nil
}

// deferDurableDelivery 只登记来源，不把后台交办、内存Durable或上次失败当作外发许可。
func (m *Manager) deferDurableDelivery(key string) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	interval := m.selfHealInterval
	if interval <= 0 {
		interval = gapAutoResolveInterval
	}
	if m.durableDeliveryRetry == nil {
		m.durableDeliveryRetry = make(map[string]time.Time)
	}
	m.durableDeliveryRetry[key] = time.Now().Add(interval)
	m.mu.Unlock()
	if m.deferredDelivery {
		m.scheduleReplayRetry(key)
	}
}

// retryDeferredDurableDelivery 复用采集轮与既有排空器，无新goroutine、无锁内IO。
// m.mu仅保护单个来源的时点，位置读取与replayMu登记均在锁外；失败最多按自愈间隔重试一次。
func (m *Manager) retryDeferredDurableDelivery(key string, p *pipeline.Pipeline) {
	m.mu.Lock()
	next, pending := m.durableDeliveryRetry[key]
	stopped := m.stopped
	m.mu.Unlock()
	if !pending || stopped || p == nil || time.Now().Before(next) {
		return
	}
	positions, ok := p.Positions()
	if !ok {
		return
	}
	backlog, _ := p.WAL().Backlog()
	m.mu.Lock()
	// 位置读取期间可能又失败了一批；旧观察不得清掉新登记的欠账。
	if m.durableDeliveryRetry[key] != next {
		m.mu.Unlock()
		return
	}
	if positions.Delivery >= positions.Durable && backlog == 0 {
		delete(m.durableDeliveryRetry, key)
		m.mu.Unlock()
		return
	}
	interval := m.selfHealInterval
	if interval <= 0 {
		interval = gapAutoResolveInterval
	}
	m.durableDeliveryRetry[key] = time.Now().Add(interval)
	m.mu.Unlock()
	// 每轮仍由Pipeline重新取得真实持久提交资格，失败的后台尝试不会造成同步热自旋。
	m.schedulePendingReplay(key)
}

func (m *Manager) persistHotBudgetOf() time.Duration {
	if m != nil && m.persistHotBudget > 0 {
		return m.persistHotBudget
	}
	return defaultPersistHotBudget
}

// requestPersist 把「请落库」交给后台持久化通道（非阻塞；已有待办即跳过）。
// defaultResolvePersistBudget 是解算收尾落库的默认有界等待预算（#59）。
//
// 取值 2s 的取舍：调用方（CS/CP RPC）总预算通常在 5–10s 量级，而该路径前面还有一次
// 5s 的 cycleMu 有界获取（resolveGapsCycleMuWait）⇒ 两段相加仍有富余；同时远大于门上一次
// 正常周期（几十–几百 ms）⇒ 正常负载下**绝大多数调用仍走同步落库**，只有真被长周期挡住时才交办。
const defaultResolvePersistBudget = 2 * time.Second

// resolvePersistBudgetOf 归一化解算收尾落库预算。
func (m *Manager) resolvePersistBudgetOf() time.Duration {
	if m != nil && m.resolvePersistBudget > 0 {
		return m.resolvePersistBudget
	}
	return defaultResolvePersistBudget
}

// notePersistHandoff 记一次"交办后台"（#59；mode.handoffObs 为 nil ⇒ 零开销）。
func (m *Manager) notePersistHandoff(mode persistMode) {
	if mode.handoffObs != nil {
		mode.handoffObs.Add(1)
	}
}

// persistResolveTail 是**解算收尾的有界落库**（#59）：与强一致路径同档（priority=true、**不放宽**
// 计划复用），唯一差别是门上最多等 resolvePersistBudget；超预算即交办后台并返回。
//
// 语义不变量：
//
//	① 写不丢：交办走既有 persistRequests 后台通道（保证最终完成；既有回归
//	   TestAsyncHandoffDoesNotClaimCoverage 守住"交办不得谎报覆盖"）；
//	② 表述明确：返回值 durable 区分"本次已真落库"与"已交办后台"，并累计到
//	   ResolvePersistHandoffCount() 供探针同刻取；
//	③ 覆盖水位只在**完整落库**后推进（既有实现不变 —— persistCovered.Store 在 !incomplete 分支）。
func (m *Manager) persistResolveTail() (durable bool, err error) {
	var handoffs atomic.Int64
	mode := persistMode{priority: true, maxWait: m.resolvePersistBudgetOf(), handoffObs: &handoffs}
	if err := m.persistCtx(context.Background(), mode); err != nil {
		return false, err
	}
	if n := handoffs.Load(); n > 0 {
		m.persistResolveHandoffs.Add(n)
		slog.Info("解算收尾落库已交办后台（受理即返：账本变更已落账，索引落库由后台通道最终完成）",
			"section", "resolveGapsForSource", "budget", m.resolvePersistBudgetOf().String())
		return false, nil
	}
	return true, nil
}

// ResolvePersistHandoffCount 返回解算收尾"交办后台"的累计次数（#59 观测读数）。
func (m *Manager) ResolvePersistHandoffCount() int64 {
	if m == nil {
		return 0
	}
	return m.persistResolveHandoffs.Load()
}

// persistClearConflictsTail 是「清空身份冲突」收尾的**有界落库**。
//
// 复用 persistResolveTail 的取舍（同档 priority、不放宽计划复用、只在门上加了预算，超预算即
// 交办后台并返回）而不是另写一套，理由：#59 已经为"人工入口的收尾落库"定过口径，这里若换一套
// 预算/交办语义，两条人工路径的可观测行为就会漂移，运维面对同一类现象要读两种读数。
//
// 与 persistResolveTail 的唯一差别是交办日志的 section 与计数器的归属：
// 「解算收尾」的计数器若被本路径累加，探针就无法区分是解算卡还是清空卡。
func (m *Manager) persistClearConflictsTail() (durable bool, err error) {
	var handoffs atomic.Int64
	mode := persistMode{priority: true, maxWait: m.resolvePersistBudgetOf(), handoffObs: &handoffs}
	if err := m.persistCtx(context.Background(), mode); err != nil {
		return false, err
	}
	if n := handoffs.Load(); n > 0 {
		m.persistClearConflictsHandoffs.Add(n)
		slog.Info("清空身份冲突的落库已交办后台（受理即返：内存状态已清空，索引落库由后台通道最终完成）",
			"section", "clearIdentityConflicts", "budget", m.resolvePersistBudgetOf().String())
		return false, nil
	}
	return true, nil
}
