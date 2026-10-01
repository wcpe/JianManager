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
	changed := false
	if entry.AcquirePaused {
		delivered, err := p.DeliverPending()
		if delivered {
			changed = true
		} else if err != nil {
			// 已限频（本函数每源最小间隔 gapAutoResolveInterval），不会刷屏。
			slog.Warn("暂停源存量投递失败（下轮重试）",
				"logSourceID", source.LogSourceID, "error", err)
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
	return changed
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
		return false
	}
	m.gapAutoResolveAt[key] = now
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
	if m == nil || len(runs) == 0 {
		return
	}
	m.mu.Lock()
	saved, ok := m.state.Sources[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	merged := ledger.MergePositionRanges(append(append([]ledger.PositionRange{}, saved.VerifiedRuns...), runs...))
	if len(merged) > maxVerifiedRunsPerSource {
		merged = merged[len(merged)-maxVerifiedRunsPerSource:]
	}
	saved.VerifiedRuns = merged
	m.state.Sources[key] = saved
	m.mu.Unlock()
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
