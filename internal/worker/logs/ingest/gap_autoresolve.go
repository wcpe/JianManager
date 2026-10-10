package ingest

import (
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件实现缺口的**自动消解**出口（缺陷 A 修复）。
//
// 背景（2026-10-01/02 生产实测）：表 A 的两条既有出口都不足以自愈——
//   - 全节点自动路径 ResolveCoveredGaps 要求**每个**源都有完整已发布投影，任一源不满足
//     就整体返回错误（且对 STDIO_RAW_WRITE_FAILED 一律拒绝）；
//   - 单源人工路径 ResolveCoveredGapsForSource 只有管理员显式调用才发生。
//
// 于是「投递失败 → 记缺口 → 缺口挡住 ResumeAcquire → 采集暂停 → 不再产生新批次 →
// 缺口永无自动出口」形成闭环：现场持续 13+ 小时零新数据，直到人工按源解算 13 个源。
//
// 本文件补上两条按证据工作的自动出口，互不替代：
//  1. resolveGapsLandedByDelivery：一次投递**成功确认落库**后，消解被该批次区间
//     完全覆盖的缺口（重投成功＝那段数据确实已落库）。廉价、发生在每次成功投递上。
//  2. autoResolveGapsFromPublished：用「已发布投影覆盖」这一全局证据消解缺口，
//     覆盖「WAL 条目已被回收、无法再重投」的尾部。较贵（需按天聚合事件区间），
//     故只对确实被缺口/暂停卡住的源、且按源限频执行。
//
// 两者都刻意**不**触碰 GapReasonStdioRawWriteFailed：原始字节写入失败无法由投影证明。
const (
	// gapAutoResolveInterval 是「已发布投影」证据路径的单源最小尝试间隔。
	// 该路径每次都要遍历该源的事件段做按天聚合（O(事件数)），而它能产生新结论的前提是
	// 「catalog 发布了新投影」或「出现了新缺口」——二者都不需要 250ms 级的响应，
	// 10 秒足够让一次人工解算在几轮内完成闭环，同时把最坏 CPU 摊薄两个数量级。
	gapAutoResolveInterval = 10 * time.Second
)

// gapResolutionDeliveryRetry 是「重投成功」路径写入缺口的消解依据文本。
const gapResolutionDeliveryRetry = "delivery confirmed for this range"

// gapResolutionPublished 是「已发布投影」路径写入缺口的消解依据文本。
// 与既有全节点自动路径（ResolveCoveredGaps）使用同一措辞：两者依据同为已发布投影。
const gapResolutionPublished = "verified published projection"

// gapAutoResolvableReasons 是两条自动消解出口共用的**允许名单**（默认拒绝）。
//
// 名单语义（2026-10-02 复审 P1-1 修复）：只有「事件确实进过 WAL/段，因此本次会被重写并
// 逐字段校验」的原因才在名单内。此前的写法是**排除名单**（只排除 STDIO_RAW_WRITE_FAILED），
// 于是 APPEND_REJECTED（append 被拒 → 该批从未进 WAL）、容量门禁暂停（PAUSED）等原因只要
// 其**字节区间落在证据的凸包内部**就会被标成「delivery confirmed for this range」并放行
// ResumeAcquire——空洞被永久掩盖（replay=true 时证据是全量 canonical 集，凸包覆盖整段历史，
// 风险最大）。
//
// 两重判据各自独立，缺一不可：
//  1. 本名单：原因必须显式登记（未知/新增原因一律不自动消解）；
//  2. 区间连续覆盖（见 ledger.CoveredByPositionRanges）：缺口区间必须落在**某一段**
//     逐字段校验通过的写入区间内，而不是落在所有区间的凸包里。
var gapAutoResolvableReasons = []string{
	ledger.GapReasonDeliverError,
	ledger.GapReasonDeliverErrorWorkerSource,
	ledger.GapReasonWALCommitFailed,
}

// gapRangesOfEvents 把事件集合折成源位置上的**连续覆盖区间**（升序、互不相接）。
func gapRangesOfEvents(events []logtypes.Event) []ledger.PositionRange {
	ranges := make([]ledger.PositionRange, 0, len(events))
	for _, event := range events {
		ranges = append(ranges, ledger.PositionRange{From: event.Record.Start, To: event.Record.End})
	}
	return ledger.MergePositionRanges(ranges)
}

// resolveGapsLandedByDelivery 在投递成功路径上自动消解缺口：本批次事件已被确认落库
// （写 VL + 逐字段可见性校验 + catalog 发布，见 Manager.deliver），故凡是**完全落在**
// 本次写入面某一段连续区间内的、且原因在允许名单里的未解决缺口，都已由这次成功写入
// 证明「不需要再补」。
//
// 同时把本次**逐字段校验覆盖**的连续区间记入该源的持久凭据（见 recordVerifiedRuns）：
// WAL 条目一旦被回收，就不可能再通过重投生成覆盖证据，届时它是唯一成立的区间级凭据
// （见 autoResolveGapsFromPublished 的前置条件）。
//
// 只做 best-effort：消解失败不影响投递结果本身（投递成功已成立，缺口下轮再试）。
func (m *Manager) resolveGapsLandedByDelivery(source SourceConfig, events []logtypes.Event) {
	if m == nil || len(events) == 0 {
		return
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	if pipe == nil {
		return
	}
	// 连续覆盖区间，而不是 [min(Start), max(End)] 凸包：写入面里两个事件之间的任何空洞
	// 都意味着那段字节**没有**被本次写入覆盖（典型：append 被拒的批次从未进 WAL）。
	runs := gapRangesOfEvents(events)
	if len(runs) == 0 {
		return
	}
	// 先登记区间级凭据（与是否存在缺口无关）：WAL 条目被回收后，这是「已发布投影证据」出口
	// 唯一成立的区间级凭据，缺了它该出口就只能在末端水位上空转（见 autoResolveGapsFromPublished）。
	m.recordVerifiedRuns(key, runs)
	entry := pipe.Ledger().Get(pipe.Key())
	if entry == nil || ledger.UnresolvedGapCountOf(entry) == 0 {
		return
	}
	resolved, err := pipe.Ledger().ResolveGapsCoveredByRanges(pipe.Key(), runs,
		gapResolutionDeliveryRetry, gapAutoResolvableReasons...)
	if err != nil {
		slog.Warn("投递成功后自动消解缺口失败（下轮重试）",
			"logSourceID", source.LogSourceID, "error", err)
		return
	}
	if resolved > 0 {
		slog.Info("投递成功已确认覆盖，自动消解缺口（无需人工介入）",
			"logSourceID", source.LogSourceID, "resolved", resolved,
			"ranges", len(runs),
			"unresolvedRemaining", pipe.Ledger().UnresolvedGapCount(pipe.Key()))
	}
}

// selfHealPausedSource 对「已暂停或仍持有未解决缺口」的源执行一次限频自愈，报告账本是否被改变。
//
// 三步（顺序固定）：
//  1. 只投递、不读取地推动已 durable 存量外发（暂停期间 FileTailer 不读新数据，
//     若不自愈，「投递 → 回收 → 恢复评估」整条链没有任何触发点）；
//  2. 用已发布投影证据消解缺口（覆盖 WAL 条目已被回收、无法再重投的尾部）；
//  3. 缺口刚被消解而源仍在暂停时，补一次恢复评估（恢复检查点只在回收路径上）。
//
// **失败退避（2026-10-10，改动 C）**：投递失败一律按「本轮有没有推进校验覆盖」分流——
// 有推进 ⇒ **不退避**（否则会扼杀改动 A 的逐轮渐进收敛 ✗）；无推进的连续失败 ⇒ 指数退避
// （避免 10s 周期对同一批做无效重试地空转 ✗）。退避状态有界且被"有进展"立即重置。
func (m *Manager) selfHealPausedSource(source SourceConfig, p *pipeline.Pipeline) bool {
	if m == nil || p == nil {
		return false
	}
	entry := p.Ledger().Get(p.Key())
	if entry == nil {
		return false
	}
	if !entry.AcquirePaused && ledger.UnresolvedGapCountOf(entry) == 0 {
		return false
	}
	if !m.selfHealDue(p.Key().String()) {
		return false
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	coverageBefore := m.verifiedCoverageEpochOf(key)
	// 回收位置也要当作进展信号：补段/滚动回收本身就在减积压（虽不必然改变 `changed`）。
	// 少了它会误判成"无进展"⇒ 给一个正在收敛的源套上退避 ✗。
	reclaimBefore, _ := p.Ledger().Positions(p.Key())
	changed := false
	// 步 0（#62 ＋ #63）：先"补段"（已停投的源走不到投递确认面 ⇒ 此处是它们唯一的自动出口 ✓），
	// 再**链式推进回收** —— #63：单次探测只推到当前覆盖段末端（陈旧段 ⇒ 每轮只进一个排空批 ✗，
	// 现场 344 位/s/源 = 3.7 天 ✗✗），故改为"探测驱动的小循环"（≤8 步 / ≤200ms ✓）：
	// 典型 ~3 步即"推到陈旧段末端 → 补一整段 [reclaim, delivery] → 推到 delivery" ✓；
	// 生产仍灌料时每轮覆盖新增的已确认区间 ⇒ **追平生成速率** ✓（及格语义）。
	m.reclaimStep0RollingOnly(p, source)
	deliveryFailed := false
	if entry.AcquirePaused {
		// 无进展退避只闸**投递**这一步（而非整个自愈面）：回收滚动与缺口消解是廉价步骤，
		// 且正是"有进展"的来源；把它们一并闸住会连"发现进展"的机会一起掐掉 ✗。
		if m.selfHealStallDue(key) {
			delivered, err := p.DeliverPending()
			if delivered {
				changed = true
			} else if err != nil {
				deliveryFailed = true
				// 已限频（本函数每源最小间隔 gapAutoResolveInterval），不会刷屏。
				slog.Warn("暂停源存量投递失败（下轮重试）",
					"logSourceID", source.LogSourceID, "error", err)
			}
		} else {
			m.selfHealStallSkip.Add(1) // 观测：本轮投递被无进展退避挡下
		}
	}
	if m.autoResolveGapsFromPublished(source, p) {
		changed = true
	}
	// 未解决缺口已清零时补一次恢复评估：恢复检查点只挂在回收路径上，暂停源恰好没有新批次。
	if p.Ledger().UnresolvedGapCount(p.Key()) == 0 && p.EvaluateResume() {
		slog.Info("缺口已消解且积压已回落，采集已自动恢复（无需人工介入）",
			"logSourceID", source.LogSourceID)
		changed = true
	}
	// 退避簿记（必须放在三步之后：判据是本轮的**净结果**，不是某一步的局部结论）。
	//
	// 判据只有一条：**本轮是否有进展**。
	//   - 新增逐字段校验覆盖（coverageEpoch 前进）⇒ 渐进收敛正在生效，必须**立刻解除退避**
	//     （否则改动 A 会在退避期内停摆 ✗）；
	//   - 回收位置前移 / 账本被改变（缺口消解、恢复评估成功）⇒ 也是进展，同样不退避；
	//   - 投递失败**且**以上都没有 ⇒ 记一次无进展失败，连续无进展才推进退避。
	reclaimAfter, _ := p.Ledger().Positions(p.Key())
	if m.verifiedCoverageEpochOf(key) > coverageBefore || changed || reclaimAfter.Reclaim > reclaimBefore.Reclaim {
		m.noteSelfHealProgress(key)
	} else if deliveryFailed {
		m.noteSelfHealStall(key, source)
	}
	return changed
}

// selfHealBackoffState 是单源「无进展失败」的退避状态（改动 C）。
type selfHealBackoffState struct {
	// stalls 是连续无进展次数（有进展即清零）。
	stalls int
	// nextAllowedAt 是下一次允许自愈的时刻（退避解禁点）。
	nextAllowedAt time.Time
}

const (
	// selfHealStallBackoffAfter 是**进入退避前**允许的连续无进展次数。
	//
	// 为什么必须有这个滞回（而**不是**第一次无进展就退避）：投递失败的原因几乎总是**外部条件**
	// （VL 抖动 / 重启 / 短暂过载），而外部条件何时恢复，本进程只有在**真的试一次**之后才知道。
	// 一旦第一次失败就把投递闸住，一次几秒钟的 VL 抖动就会让源在退避期内**无法发现条件已恢复**
	// ⇒ 把"短暂失败"放大成长时间停摆 ✗（既有回归 TestPausedSourceSelfHealsEndToEnd 正是这条：
	// VL 恢复后的**下一次**采集轮必须自愈）。取 3 ⇒ 抖动仍能在一两轮内自愈，而真正卡死的源
	// 在约 30 秒后开始被限频。
	selfHealStallBackoffAfter = 3
	// selfHealStallBackoffBase / selfHealStallBackoffMax 是「无进展失败」的指数退避区间。
	//
	// 起点 30s（= 3 × gapAutoResolveInterval）：进入退避后重试频率立降到 1/3；
	// 封顶 **2 分钟**而不是更长的理由：退避期同时也是"外部条件恢复后仍被压制"的时延——
	// 封顶 2 分钟把这段时延控制在一个可接受的运维量级，同时仍把长期空转压到每 2 分钟一次
	// （**有界且可观测** ✓，绝不无界重试 ✗）。
	selfHealStallBackoffBase = 30 * time.Second
	selfHealStallBackoffMax  = 2 * time.Minute
	// selfHealBackoffMapCap 是退避状态表的段数上限（超过即按"已解禁且久未触碰"清理）。
	// 与 reReadGapProbeAt 的 4096 同量级：正常规模 = 源数（数十），本上限只在异常时兜底。
	selfHealBackoffMapCap = 4096
)

// noteSelfHealProgress 在有进展时重置该源的退避（"有进展 ⇒ 立即可再试"）。
func (m *Manager) noteSelfHealProgress(key string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.selfHealBackoff == nil {
		return
	}
	delete(m.selfHealBackoff, key)
}

// noteSelfHealStall 记一次「无进展」并按连续次数推进退避。
//
// 滞回（见 selfHealStallBackoffAfter）：前 N-1 次无进展**不设**解禁点（投递照常进行），
// 从第 N 次起设 `base × 2^(n-N)`（封顶 max）。
//
// 为什么退避必须可被"有进展"重置：连续失败次数只应在**同一个障碍**上累加；一旦出现新覆盖
// （或回收前移），说明那一段确实在往前走（改动 A 的渐进收敛），此时继续压制重试等于把
// 收敛掐死 ✗（重置就地发生在 recordVerifiedRunsReporting 与 noteSelfHealProgress 两处）。
func (m *Manager) noteSelfHealStall(key string, source SourceConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.selfHealBackoff == nil {
		m.selfHealBackoff = map[string]*selfHealBackoffState{}
	}
	st := m.selfHealBackoff[key]
	if st == nil {
		st = &selfHealBackoffState{}
		m.selfHealBackoff[key] = st
	}
	st.stalls++
	m.selfHealStallCount.Add(1)
	// 有界：本表按**源键**记录，正常规模与源数同级；只在异常膨胀时按"已解禁且久未触碰"清理，
	// 与 reReadGapProbeAt 的既有做法同形（绝不无界增长 ✓）。
	if len(m.selfHealBackoff) > selfHealBackoffMapCap {
		now := time.Now()
		for k, v := range m.selfHealBackoff {
			if k == key {
				continue
			}
			if !v.nextAllowedAt.IsZero() && now.After(v.nextAllowedAt) {
				delete(m.selfHealBackoff, k)
			}
		}
	}
	if st.stalls < selfHealStallBackoffAfter {
		// 滞回期内：不设解禁点（下一轮照常尝试 ⇒ 短暂的 VL 抖动仍能立即自愈 ✓）。
		return
	}
	delay := selfHealStallBackoffBase
	for i := selfHealStallBackoffAfter; i < st.stalls; i++ {
		delay *= 2
		if delay >= selfHealStallBackoffMax {
			delay = selfHealStallBackoffMax
			break
		}
	}
	st.nextAllowedAt = time.Now().Add(delay)
	// 有界 + 可观测：只在进入退避的那一次与之后每 8 次打一条，避免刷屏且仍能判定
	// "是不是一直在退避"。
	if st.stalls == selfHealStallBackoffAfter || st.stalls%8 == 0 {
		slog.Warn("暂停源自愈连续无进展，已进入退避（有进展或条件恢复即自动恢复）",
			"logSourceID", source.LogSourceID, "stalls", st.stalls,
			"nextRetryIn", delay.String(), "backoffMax", selfHealStallBackoffMax.String())
	}
}

// selfHealStallDue 判定该源是否已越过「无进展退避」的解禁点（越过即放行，未设退避 ⇒ 放行）。
func (m *Manager) selfHealStallDue(key string) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.selfHealBackoff[key]
	if st == nil || st.nextAllowedAt.IsZero() {
		return true
	}
	if time.Now().Before(st.nextAllowedAt) {
		return false
	}
	return true
}

// SelfHealStallCount 返回"无进展失败"累计次数（观测面）。
func (m *Manager) SelfHealStallCount() int64 {
	if m == nil {
		return 0
	}
	return m.selfHealStallCount.Load()
}

// autoResolveGapsFromPublished 用「已发布投影 + 逐字段校验覆盖区间」证据自动消解缺口，
// 并报告账本是否被改变。
//
// 为什么需要它（与投递路径互补）：WAL 条目一旦被回收（reclaim 放行＝恢复责任已转移到
// 已发布投影），就不可能再通过重投生成覆盖证据；此时唯一成立且可自证的依据是
// publishedClosedForSource（该源全部事件的区间都落在已发布投影的 ClosedVisibleSeq 内）
// 加上该源**已持久化的逐字段校验覆盖区间**（verified runs）。
//
// 与旧实现的差别（2026-10-02 复审 P1-2 修复）：旧实现用 ResolveGapsThroughExcept(closed)
// 做**末端水位**消解，而 closed 是「已发布投影的封闭水位」，与被拒批次是否真的落库毫无关系——
// 只要缺口末端 ≤ closed 就被宣称已确认落库。现在改为同一判据收口：
//  1. 缺口区间必须落在 verified runs 的**某一段连续区间**内（该区间来自一次写 VL 后逐字段
//     可见性校验通过、且 ≤ 已发布封闭水位），而不是落在末端水位之下；
//  2. 原因必须在允许名单（gapAutoResolvableReasons）内。
//
// 调用者负责限频（见 selfHealPausedSource）：本方法每次都要遍历该源事件段做按天聚合。
func (m *Manager) autoResolveGapsFromPublished(source SourceConfig, p *pipeline.Pipeline) bool {
	if m == nil || p == nil {
		return false
	}
	entry := p.Ledger().Get(p.Key())
	if entry == nil || ledger.UnresolvedGapCountOf(entry) == 0 {
		return false
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	closed, complete := m.publishedClosedForSource(source, saved)
	if !complete || closed == 0 {
		// 没有成立的全量发布证据：不得凭空消解（缺口语义红线）。
		return false
	}
	// 区间级凭据：只认「写 VL 后逐字段校验通过」的连续区间，并以已发布封闭水位为上界。
	runs := clipPositionRanges(m.verifiedRuns(key), closed)
	if len(runs) == 0 {
		// 没有任何逐字段校验凭据：不得按末端水位消解（那会把从未校验过的区间一并宣称已落库）。
		return false
	}
	resolved, err := p.Ledger().ResolveGapsCoveredByRanges(p.Key(), runs,
		gapResolutionPublished, gapAutoResolvableReasons...)
	if err != nil {
		slog.Warn("已发布投影证据自动消解缺口失败（下轮重试）",
			"logSourceID", source.LogSourceID, "error", err)
		return false
	}
	if resolved == 0 {
		return false
	}
	slog.Info("已发布投影已覆盖缺口区间，自动消解缺口（无需人工介入）",
		"logSourceID", source.LogSourceID, "resolved", resolved, "closed", closed,
		"ranges", len(runs),
		"unresolvedRemaining", p.Ledger().UnresolvedGapCount(p.Key()))
	return true
}

// selfHealDue 判断该源是否已越过最小自愈间隔（越过即立即占用本次机会）。
func (m *Manager) selfHealDue(key string) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	interval := m.selfHealInterval
	if interval <= 0 {
		interval = gapAutoResolveInterval
	}
	if m.gapAutoResolveAt == nil {
		m.gapAutoResolveAt = map[string]time.Time{}
	}
	if last, ok := m.gapAutoResolveAt[key]; ok && now.Sub(last) < interval {
		m.selfHealDueSkip.Add(1) // #66-⑤ 诊断：被限频闸挡下（≠ 被状态闸挡下 ✓）
		return false
	}
	m.gapAutoResolveAt[key] = now
	m.selfHealDuePass.Add(1) // #66-⑤ 诊断：越过限频闸 ⇒ 自愈面确已轮到该源 ✓
	return true
}

// maxVerifiedRunsPerSource 是单源「逐字段校验覆盖区间」凭据的保留段数上限。
//
// 取值权衡：连续写入会就地合并（一次正常投递把新批次接到已有区间尾部），稳态下每源通常只有
// 1 段；上限只在区间彼此不相接（历史上出现过空洞）时才起作用，把按源状态钉在常数级。
const maxVerifiedRunsPerSource = 64

// recordVerifiedRuns 把本次**逐字段校验通过**的连续区间并入该源的持久凭据（见
// persistedSource.VerifiedRuns），超出段数上限时按位置从旧到新裁剪。
func (m *Manager) recordVerifiedRuns(key string, runs []ledger.PositionRange) {
	m.recordVerifiedRunsReporting(key, runs)
}

// recordVerifiedRunsReporting 是 recordVerifiedRuns 的报告形态：返回本次是否**真的新增了覆盖**
// （即校验进展），供自愈路径的退避判定使用（见 selfHealPausedSource 的"有进展不退避"约束）。
//
// 「新增」的判据是**合并后与合并前不同**：纯重复登记（区间已被既有凭据覆盖）不算进展——
// 否则同一条凭据被反复登记就会把退避永久重置，退避形同虚设。段数上限裁剪带来的差异**不算**
// 进展（那是丢弃最旧段，不是新覆盖）。
func (m *Manager) recordVerifiedRunsReporting(key string, runs []ledger.PositionRange) bool {
	if m == nil || len(runs) == 0 {
		return false
	}
	w := m.watchMutexWait("recordVerifiedRuns.merge")
	m.mu.Lock()
	w.acquired()
	saved, ok := m.state.Sources[key]
	if !ok {
		m.mu.Unlock()
		w.done()
		return false
	}
	merged := ledger.MergePositionRanges(append(append([]ledger.PositionRange{}, saved.VerifiedRuns...), runs...))
	// 进展判据在**裁剪之前**取（裁剪是丢弃最旧段，不是新覆盖 ⇒ 不得算作进展）。
	grew := !samePositionRanges(merged, saved.VerifiedRuns)
	if len(merged) > maxVerifiedRunsPerSource {
		merged = merged[len(merged)-maxVerifiedRunsPerSource:]
	}
	saved.VerifiedRuns = merged
	m.state.Sources[key] = saved
	if grew {
		// 进展计数（per-source 单调递增）：自愈路径用它判定"本轮到底有没有推进"——
		// 有 ⇒ 不退避（否则会扼杀渐进收敛），无 ⇒ 退避（避免 10s 周期空转）。
		if m.verifiedCoverageEpoch == nil {
			m.verifiedCoverageEpoch = map[string]uint64{}
		}
		m.verifiedCoverageEpoch[key]++
		// **进展必须立刻解除退避**（2026-10-10 改动 C 的关键接口约定）。
		//
		// 为什么不能只在自愈面的收尾处判：退避闸在自愈面的**开头**（派发投递之前）判定，
		// 而本轮的新覆盖是由**投递路径**（deliver → 本函数）登记的——若等到下一轮收尾才解除，
		// 之前那次调用早已被退避挡下（投递零次执行），渐进收敛就会在退避期内整体停摆 ✗。
		// 在这里就地清空 ⇒ "一有进展立即恢复满速重试"，退避只压制**确实毫无进展**的空转 ✓。
		delete(m.selfHealBackoff, key)
	}
	m.mu.Unlock()
	w.done()
	return grew
}

// samePositionRanges 判定两个已合并的区间集合是否**逐段相同**（都是 MergePositionRanges 的产物：
// 升序、互不相接 ⇒ 逐段比较即等价于集合相等）。
func samePositionRanges(a, b []ledger.PositionRange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// verifiedCoverageEpochOf 返回该源「逐字段校验覆盖」的进展代次（单调递增；无记录为 0）。
//
// 为什么是代次而不是"区间集合"：调用方只需要回答"本轮有没有推进"，比较两个整数即 O(1)，
// 而比较区间集合要克隆整份凭据——自愈路径每源每轮都走一次，代价必须常数级。
func (m *Manager) verifiedCoverageEpochOf(key string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.verifiedCoverageEpoch[key]
}

// verifiedRuns 返回该源已持久化的逐字段校验覆盖区间（副本）。
func (m *Manager) verifiedRuns(key string) []ledger.PositionRange {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	saved := m.state.Sources[key]
	return append([]ledger.PositionRange(nil), saved.VerifiedRuns...)
}

// clipPositionRanges 把区间集合裁到 [0, ceiling] 内（超出部分截断，完全在 ceiling 之上的丢弃）。
//
// 用途：把「逐字段校验覆盖区间」这一凭据限制在已发布封闭水位之下——水位之上的写入虽已校验通过，
// 但尚未被标记为「已发布覆盖」，不应作为「已发布投影证据」用于消解。
func clipPositionRanges(runs []ledger.PositionRange, ceiling uint64) []ledger.PositionRange {
	out := make([]ledger.PositionRange, 0, len(runs))
	for _, r := range runs {
		if r.From > ceiling {
			continue
		}
		if r.To > ceiling {
			r.To = ceiling
		}
		out = append(out, r)
	}
	return ledger.MergePositionRanges(out)
}
