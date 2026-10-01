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

// resolveGapsLandedByDelivery 在投递成功路径上自动消解缺口：本批次事件已被确认落库
// （写 VL + 逐字段可见性校验 + catalog 发布，见 Manager.deliver），故凡是**完全落在**
// 本批区间内的未解决缺口，都已由这次成功写入证明「不需要再补」。
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
	entry := pipe.Ledger().Get(pipe.Key())
	if entry == nil || ledger.UnresolvedGapCountOf(entry) == 0 {
		return
	}
	from, to := events[0].Record.Start, events[0].Record.End
	for _, event := range events {
		if event.Record.Start < from {
			from = event.Record.Start
		}
		if event.Record.End > to {
			to = event.Record.End
		}
	}
	resolved, err := pipe.Ledger().ResolveGapsCoveredByRange(pipe.Key(), from, to,
		gapResolutionDeliveryRetry, ledger.GapReasonStdioRawWriteFailed)
	if err != nil {
		slog.Warn("投递成功后自动消解缺口失败（下轮重试）",
			"logSourceID", source.LogSourceID, "error", err)
		return
	}
	if resolved > 0 {
		slog.Info("投递成功已确认覆盖，自动消解缺口（无需人工介入）",
			"logSourceID", source.LogSourceID, "resolved", resolved,
			"rangeFrom", from, "rangeTo", to,
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

// autoResolveGapsFromPublished 用「已发布投影」证据自动消解缺口，并报告账本是否被改变。
//
// 为什么需要它（与投递路径互补）：WAL 条目一旦被回收（reclaim 放行＝恢复责任已转移到
// 已发布投影），就不可能再通过重投生成覆盖证据；此时唯一成立且可自证的依据是
// publishedClosedForSource（该源全部事件的区间都落在已发布投影的 ClosedVisibleSeq 内）。
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
	m.mu.Lock()
	saved := m.state.Sources[source.LogSourceID+"/"+source.SourceGeneration]
	m.mu.Unlock()
	closed, complete := m.publishedClosedForSource(source, saved)
	if !complete || closed == 0 {
		// 没有成立的全量发布证据：不得凭空消解（缺口语义红线）。
		return false
	}
	resolved, err := p.Ledger().ResolveGapsThroughExcept(p.Key(), closed,
		gapResolutionPublished, ledger.GapReasonStdioRawWriteFailed)
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
