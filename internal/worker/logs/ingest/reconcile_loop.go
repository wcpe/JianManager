package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// 本文件实现**常驻增量对账**（后续项③）：把启动期一次性的「源 × UTC 天」条数级对账
// （见 reconcile.go）做成周期性、有界、可配的常驻协程。
//
// 为什么需要它：启动对账只覆盖「Worker 重启」这一时刻。运行期同样会出现「VL 侧数据缺失」
// 的形态（写入响应丢失、VL 部分写入、代次发布漂移、外部清理），而账本侧没有任何读数能
// 直接暴露它——只有把账本的「应有条数」与 VL 的「实有条数」再比一次才能发现。
//
// 设计约束（逐条对应验收）：
//   - **不打扰投递主路径**：只读对账阶段不持投递相关锁；**重发阶段同样不持 cycleMu**
//     （2026-10-03 收紧，见 replayMissingDays 的并发纪律）——它只在自己的短临界区（m.mu /
//     publishMu / 账本锁）里推进代次与水位，VL 写入一律离锁，且全程受切片预算约束。
//     此处曾写「重发阶段持 cycleMu」，但实现从未取过它（声明与实现分叉）；真按字面实现会把
//     整节点采集串行在重发之后（现场 54 个未暂停源停摆 18+ 分钟）。
//   - **复用既有重发机制**：判定逻辑与启动版同源（reconcileWithExpectation）；重发走
//     eventsForDays + writeProjectionPlan（与 applyStartupRecovery 的缺失天分支同形），
//     完成后走 resolveGapsLandedByDelivery（与投递成功路径同一出口，协同缺口自愈与
//     VerifiedRuns 凭据）。
//   - **幂等安全**：重发以 event_id 身份 + 逐字段校验为准（写 VL 后必须逐字段可见才发布），
//     重复重发只会生成新代次并取代旧代次，不产生重复数据。
//   - **观测量并入 G10 面**：每轮结果 checked/resent/skipped/errors 记入 ReconcileLoopStats
//     并以 Info 级日志打印（与 retention.DriverResult、sampling.Stats 同一落点风格）。
//
// 与启动版的**刻意差异**（处置策略，不是判定逻辑）：
//   - 启动版对「查询不可信」回退**整窗重发**——因为启动恢复必须给出确定结论；
//   - 常驻版对「查询不可信」**不重发**，只记错误等下一轮。理由：运行期数据主路径仍在工作，
//     对账失败（典型是 VL 不可达）时重发必然同样失败，立即整窗重发只会制造风暴；
//     而下一个周期会重新判定，判定成功且确有缺失时才重发。这不构成「少发」——
//     常驻对账是额外的安全网，不是数据路径的替代。

const (
	// reconcileLoopDefaultInterval 是常驻对账的周期间隔默认值。
	//
	// 取值依据：对账的价值是「发现运行期数据缺失」，而不是实时性——真正需要实时性的场景
	// （投递失败、缺口产生）已有事件驱动的自愈路径。15 分钟足够让一次外部事故在运维的
	// 一次巡检窗口内被发现，同时把每轮 VL 只读查询压到可忽略的量级。
	reconcileLoopDefaultInterval = 15 * time.Minute
	// reconcileLoopDefaultRoundBudget 是**单轮**对账的总预算（上限）。
	//
	// 为什么会超预算：单源对账要重建该源权威集合（O(段行数)，生产单源段可达数十万行）。
	// 预算到期即停止本轮剩余源（其判定留给下一轮轮转），绝不因为「没来得及对账」而误判缺失。
	reconcileLoopDefaultRoundBudget = 45 * time.Second
	// reconcileCandidateLockBudget 是"取候选等 m.mu"的**有界上限**（2026-10-04 收敛加速器）：
	// 超过即本轮放弃并留痕 `candidate_lock_timeout`（下一轮再来）——避免"等锁吃掉整轮预算"。
	reconcileCandidateLockBudget = 5 * time.Second
	// reconcileLoopDefaultMaxSources 是单轮对账的源数上限（轮转覆盖，逐步推进）。
	//
	// 为什么轮转而不是每轮全源：全源 × 全量的本地重建成本随源数与段长度线性增长，
	// 会变成常态 CPU 负担。轮转让单轮成本有界，代价只是「全量覆盖一遍」需要多个周期。
	reconcileLoopDefaultMaxSources = 8
	// reconcileLoopMaxSourcesLimit 是单轮源数上限的硬上界（配置误写不得让单轮失控）。
	reconcileLoopMaxSourcesLimit = 256
	// reconcileLoopMinInterval 是周期下限（防误配：过短会让 VL 只读查询变成常态负担）。
	reconcileLoopMinInterval = 30 * time.Second
)

// ReconcileLoopConfig 是常驻增量对账的配置面。
//
// 配置键（worker.yml）登记为 `log_reconcile_loop.*`；与 Reconcile 同样，当前生效路径是
// Options.ReconcileLoop（包内注入 + 默认值）：YAML → Options 的接线需改
// internal/worker/config.go 与 apps/worker/main.go，超出本项改动面。
type ReconcileLoopConfig struct {
	// Enabled 为 false 时**完全不跑**（连 VL 只读查询都不发）。默认 true。
	Enabled bool
	// Interval 是周期间隔（<reconcileLoopMinInterval 收敛到下限，0/负值取默认）。
	Interval time.Duration
	// RoundBudget 是单轮总预算（0/负值取默认）。
	RoundBudget time.Duration
	// MaxSourcesPerRound 是单轮对账的源数上限（0/负值取默认，上限 reconcileLoopMaxSourcesLimit）。
	MaxSourcesPerRound int
}

// DefaultReconcileLoopConfig 返回默认常驻对账配置（默认启用，温和参数）。
func DefaultReconcileLoopConfig() ReconcileLoopConfig {
	return ReconcileLoopConfig{
		Enabled:            true,
		Interval:           reconcileLoopDefaultInterval,
		RoundBudget:        reconcileLoopDefaultRoundBudget,
		MaxSourcesPerRound: reconcileLoopDefaultMaxSources,
	}
}

// normalized 把越界/零值收敛到默认（配置误写不得让常驻对账退化为无周期或无上限）。
func (c ReconcileLoopConfig) normalized() ReconcileLoopConfig {
	out := c
	if out.Interval <= 0 {
		out.Interval = reconcileLoopDefaultInterval
	}
	if out.Interval < reconcileLoopMinInterval {
		out.Interval = reconcileLoopMinInterval
	}
	if out.RoundBudget <= 0 {
		out.RoundBudget = reconcileLoopDefaultRoundBudget
	}
	if out.MaxSourcesPerRound <= 0 {
		out.MaxSourcesPerRound = reconcileLoopDefaultMaxSources
	}
	if out.MaxSourcesPerRound > reconcileLoopMaxSourcesLimit {
		out.MaxSourcesPerRound = reconcileLoopMaxSourcesLimit
	}
	return out
}

// reconcileLoopConfigOf 归一化常驻对账配置：nil 表示用默认（启用）。
func reconcileLoopConfigOf(configured *ReconcileLoopConfig) ReconcileLoopConfig {
	if configured == nil {
		return DefaultReconcileLoopConfig()
	}
	return configured.normalized()
}

// ReconcileLoopStats 是常驻对账的累计读数（G10 观测面：读数 + 每轮打印 + 只读访问器）。
type ReconcileLoopStats struct {
	// Rounds 是已执行的轮数（关闭时不推进）。
	Rounds uint64
	// CheckedSources / CheckedDays 是累计对账过的源数与「源 × 天」数。
	CheckedSources uint64
	CheckedDays    uint64
	// ResentSources / ResentDays 是累计触发重发的源数与 UTC 天数。
	ResentSources uint64
	ResentDays    uint64
	// SkippedSources 是对账一致（无需重发）的源数。
	SkippedSources uint64
	// Errors 是对账不可信 / 重发失败的次数（错误分类见 LastErrors）。
	Errors uint64
	// LastRoundAt / LastDuration 是上一轮的起止读数。
	LastRoundAt    time.Time
	LastDuration   time.Duration
	LastCandidates int
	// LastMissing 是上一轮发现缺失的「源:天」列表（升序，便于日志与取证比对）。
	LastMissing []string
	// LastErrors 是上一轮的错误分类列表（有界）。
	LastErrors []string
}

// ReconcileRoundResult 是**单轮**常驻对账的结论（供测试断言与运维手动触发时读取）。
type ReconcileRoundResult struct {
	RoundAt        time.Time
	Duration       time.Duration
	Candidates     int
	CheckedSources int
	CheckedDays    int
	// Missing 是发现「VL 条数不足」的「源:天」列表。
	Missing []string
	// ResentSources / ResentDays 是本轮触发重发的源数与天数。
	ResentSources int
	ResentDays    int
	// Skipped 是本轮对账一致（或源无可对账天）的源数。
	Skipped int
	// Errors 是本轮错误分类（有界；含查询不可信与重发失败）。
	Errors []string
}

// maxReconcileRoundErrors 限制单轮错误列表长度（观测日志不得被错误风暴淹没）。
const maxReconcileRoundErrors = 16

// reconcileView 是"对账只读视图"（候选 1，2026-10-04 父级裁定）：keys（有效源键，已排序）与
// sources（同一时刻副本）的**一致快照**，由持锁写侧重建、atomic 发布，读侧零取 m.mu。
//
// 为什么必须"同代"（本候选最关键约束，防误重发）：对账用 candidate.source 查询并对缺失重发；
// 若 sources 陈旧（源已换代）而 keys 新鲜，会"用旧代次查 VL ⇒ 查不到 ⇒ 误判缺失 ⇒ 误重发"。
// keys 与 sources 在同一持锁临界区内复制 ⇒ 二者必同代 ✓。
type reconcileView struct {
	keys    []string
	sources map[string]SourceConfig
}

const (
	// reconcileViewMinInterval 是发布节流：高频持锁路径内距上次发布过近则跳过重建，控成本。
	//
	// **v3（2026-10-04）已取消视图 TTL**：原 TTL=30min 本意是"防陈旧"，但生产实证
	// （#50 窗第二轮 07:46 `candidate_lock_timeout@cursor=inst:143`）：pollOnce 的刷新在
	// "重发阶段持 cycleMu + m.mu 长持"时频率远低于设计（40min 仅 17 次校验完成）⇒ 视图
	// **静默过期** ⇒ 对账回退 TryLock ⇒ 每 5s 超时。而快照本身是"keys+sources 同代"副本，
	// 且**源集合变化走 force 发布即时更新**（RegisterInstance 处）——陈旧代价仅"晚发现新源"，
	// 远小于"过期即等锁"。故视图发布后**永久可用**。
	reconcileViewMinInterval = 5 * time.Second
)

// publishReconcileViewLocked 在持锁状态下重建并发布视图（调用方必须已持 m.mu）。
// force=true 用于"源集合变更"（必须立即反映）；否则按 reconcileViewMinInterval 节流。
func (m *Manager) publishReconcileViewLocked(force bool) {
	if m == nil {
		return
	}
	if !force {
		last := m.reconcileViewPublishedAt.Load()
		if last != 0 && time.Since(time.Unix(0, last)) < reconcileViewMinInterval {
			return
		}
	}
	keys := make([]string, 0, len(m.pipes))
	for key := range m.pipes {
		if _, ok := m.sources[key]; !ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sources := make(map[string]SourceConfig, len(keys))
	for _, key := range keys {
		sources[key] = m.sources[key]
	}
	m.reconcileViewSnapshot.Store(&reconcileView{keys: keys, sources: sources})
	m.reconcileViewPublishedAt.Store(time.Now().UnixNano())
}

// currentReconcileView 返回当前视图（零锁 Load；v3 起**无 TTL**——发布后永久可用：
// 源集合变化由 force 发布即时更新，陈旧代价仅"晚发现新源"，见 reconcileViewMinInterval 注释）。
func (m *Manager) currentReconcileView() *reconcileView {
	if m == nil {
		return nil
	}
	v, _ := m.reconcileViewSnapshot.Load().(*reconcileView)
	return v
}

// nextReconcileCandidatesFromView 用只读视图构造候选（零取 m.mu；候选 1 主路径）。
// 返回 ok=false 表示视图不可用（未发布/过期），调用方应回退到持锁路径。
func (m *Manager) nextReconcileCandidatesFromView(limit int) ([]reconcileCandidate, bool) {
	view := m.currentReconcileView()
	if view == nil {
		return nil, false
	}
	if len(view.keys) == 0 {
		return nil, true
	}
	cur := int(m.reconcileLoopCursor.Load())
	if cur >= len(view.keys) || cur < 0 {
		cur = 0
		m.reconcileLoopCursor.Store(0)
	}
	count := limit
	if count <= 0 || count > len(view.keys) {
		count = len(view.keys)
	}
	out := make([]reconcileCandidate, 0, count)
	for i := 0; i < count; i++ {
		key := view.keys[(cur+i)%len(view.keys)]
		out = append(out, reconcileCandidate{key: key, source: view.sources[key]})
	}
	m.reconcileCursorKeySnapshot.Store(view.keys[cur])
	return out, true
}

// RunReconcileLoop 常驻对账循环：按 interval 周期执行一轮对账，直到 ctx 结束。
//
// 与采集轮共用同一 ctx 生命周期（见 Manager.Start 的启动点）；关闭配置时立即返回
// （不打日志风暴、不查 VL、不推进任何状态）。
// publishStartupReconcileSnapshots 在持锁状态下发布"配置 + 视图"快照（v2 修复，2026-10-04）。
//
// 为什么需要：配置快照的常规写点（SetReconcileLoopConfig）在生产从不被调用 ⇒ 快照恒空 ⇒
// 对账首道"配置读"永久走 TryLock ⇒ 锁紧张时每轮 5s 超时。启动时补一次发布即根治。
func (m *Manager) publishStartupReconcileSnapshots() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.reconcileLoopCfgSnapshot.Store(m.reconcileLoop)
	m.publishReconcileViewLocked(true)
	m.mu.Unlock()
}

func (m *Manager) RunReconcileLoop(ctx context.Context) {
	if m == nil {
		return
	}
	cfg := m.reconcileLoopConfig()
	if !cfg.Enabled {
		slog.Info("常驻增量对账未启用（关闭即不跑）")
		return
	}
	// 启动即"一次性取锁"发布配置与视图快照（2026-10-04 v2 修复）：
	// 配置快照的唯一常规写点 SetReconcileLoopConfig 在生产**从不被调用**（配置走默认值）
	// ⇒ 快照恒空 ⇒ 对账轮首道"配置读"仍走 TryLock ⇒ 锁紧张即 5s 超时（#49 首轮实证）。
	// 这里在循环启动时补一次发布：此后对账的配置读与取候选都零取 m.mu（pollOnce 持续刷新视图）。
	m.publishStartupReconcileSnapshots()
	slog.Info("常驻增量对账已启动",
		"interval", cfg.Interval.String(), "roundBudget", cfg.RoundBudget.String(),
		"maxSourcesPerRound", cfg.MaxSourcesPerRound)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("常驻增量对账已停止", "stats", m.ReconcileLoopStats())
			return
		case <-ticker.C:
			m.ReconcileRoundNow(ctx)
		}
	}
}

// ReconcileRoundNow 立即执行一轮常驻对账并返回本轮结论（幂等：重复调用只是重复判定）。
//
// 供周期驱动、运维手动触发与回归直接调用——把「周期调度」与「单轮判定」分开，
// 使「到点必跑」可被断言而不必依赖 sleep 时长。
func (m *Manager) ReconcileRoundNow(ctx context.Context) ReconcileRoundResult {
	started := time.Now()
	result := ReconcileRoundResult{RoundAt: started}
	if m == nil {
		return result
	}
	cfg, okCfg := m.reconcileLoopConfigBounded(m.reconcileLockBudgetOf())
	if !okCfg {
		// 锁紧张：本轮快速放弃（与候选同口径留痕）——预算与「轮」都不为等锁买单。
		result.Errors = appendReconcileError(result.Errors, "config_lock_timeout@cursor="+m.reconcileCursorKeyOfSnapshot())
		result.Duration = time.Since(started)
		m.recordReconcileRound(result)
		m.logReconcileRound(result)
		return result
	}
	if !cfg.Enabled {
		// 关闭即不跑：连统计都不推进，保证「关闭」与「跑过但无结论」可区分。
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// **先取候选（有界等待 m.mu；等锁不吃轮预算），再建立轮预算** —— 2026-10-04 收敛加速器：
	// 原序在等锁**之前**建立 roundCtx ⇒ 深积压下等锁数分钟把整轮预算吃光、拿锁时已过期
	// ⇒ 循环头立即 break（"轮从未开始"，checked=0）——总根源与 duration 波动之源均在此。
	candidates, ok := m.nextReconcileCandidatesBounded(cfg.MaxSourcesPerRound, m.reconcileLockBudgetOf())
	if !ok {
		result.Errors = appendReconcileError(result.Errors, "candidate_lock_timeout@cursor="+m.reconcileCursorKeyOfSnapshot())
		result.Duration = time.Since(started)
		m.recordReconcileRound(result)
		m.logReconcileRound(result)
		return result
	}
	result.Candidates = len(candidates)
	roundCtx, cancel := context.WithTimeout(ctx, cfg.RoundBudget)
	defer cancel()
	// 单源超时/查询超时与启动版共用同一套读数；但**启用开关独立**——常驻对账的开关是
	// ReconcileLoopConfig.Enabled（见 RunReconcileLoop 入口），不能让启动对账的关闭
	// （应急逃生口 log_reconcile.enabled=false）连带把常驻对账的每轮都判成"未启用"。
	queryCfg := m.reconcile
	queryCfg.Enabled = true
	for _, candidate := range candidates {
		if roundCtx.Err() != nil {
			result.Errors = appendReconcileError(result.Errors, "round_budget_exhausted")
			break
		}
		// 逐源处理 + **逐源检查点**（2026-10-03 "取消即全失"修复）：每个源处理结束（无论成败）
		// 即推进游标一格 ⇒ 轮预算取消时游标停在"第一个未处理源"——已完成的不重做、未完成的
		// 下一轮从断点继续，跨轮累积覆盖（判读 stats.CheckedSources 累计值；单轮值受 45s 预算限制）。
		m.reconcileOneCandidate(roundCtx, candidate, queryCfg, &result)
		m.advanceReconcileCursor()
	}
	result.Duration = time.Since(started)
	m.recordReconcileRound(result)
	m.logReconcileRound(result)
	return result
}

// reconcileOneCandidate 处理单个候选源（原 ReconcileRoundNow 循环体抽成方法，continue → return）：
// 调用方在**每次调用后**推进游标（逐源检查点）⇒ 已完成源不重做、未完成源下轮从断点续做。
func (m *Manager) reconcileOneCandidate(roundCtx context.Context, candidate reconcileCandidate, queryCfg ReconcileConfig, result *ReconcileRoundResult) {
	// VL 未就绪：本轮不判、更不重发——「不可信查询不重发」的既有取舍同样适用于「端点不可用」，
	// 而且把必然失败的重发挡在门外（2026-10-03 现场：worker 与 VL 同步重启时对账抢跑）。
	if err := m.ensureVLReady(roundCtx, candidate.source); err != nil {
		result.Errors = appendReconcileError(result.Errors, "vl_not_ready:"+candidate.key+":"+err.Error())
		return
	}
	expectation, err := m.canonicalDayCounts(candidate.key, candidate.source)
	if err != nil {
		result.Errors = appendReconcileError(result.Errors, "canonical_counts:"+err.Error())
		return
	}
	result.CheckedSources++
	if m.reconcileCheckedHook != nil {
		m.reconcileCheckedHook(candidate.key)
	}
	if len(expectation.Days) == 0 {
		// 空源（无非空天）：没有可对账的面，既不计缺失也不重发。
		result.Skipped++
		return
	}
	m.mu.Lock()
	saved := m.state.Sources[candidate.key]
	inflight := m.publicationInflight[candidate.key]
	m.mu.Unlock()
	if saved.PublicationPending {
		if inflight != 0 {
			result.Skipped++
			return
		}
		// 遗留 pending 不靠新输入唤醒；这里**只登记**一次「确认 + 必要时重建」的异步任务，
		// 由重建 worker 承担那份 O(全量) 的重活（确认遍历全量事件 + 逐天逐字段查询）。
		//
		// 2026-10-09 改动（原为内联 `tryConfirmPendingPublicationCtx` → `replayMissingDaysCtx`）：
		// 生产现场该段跑在对账轮 goroutine 上，单次「读 57 万条 + 逐天校验」以分钟计，
		// 期间与采集轮争算力与 VL 容量（`inst:220/file` 46 次 `publication_pending` 回退）。
		// 登记是 O(1) ⇒ 对账轮（跑在进程内、与采集同档）不再为重建买单。
		//
		// **不静默**：登记失败（退避/已放弃/队列满）一律记进本轮 Errors，语义与「重发失败」同类
		// ——既不假装已重建，也不丢证据。
		days := reconcileExpectationDays(expectation)
		ok, skipReason := m.scheduleRebuildConfirmPendingAsync(candidate.source, days, "pending_unconfirmed")
		if !ok {
			result.Errors = appendReconcileError(result.Errors,
				"pending_rebuild_scheduled=false:"+candidate.key+":"+skipReason)
			return
		}
		// **只计源、不计天**：此刻只知道「可能缺失的天」（`days` 是该源有内容的全部天），
		// 真正要重发哪几天由 worker 侧的内容级确认决定。把 `len(days)` 记成 ResentDays 会
		// 虚报重发量（读数必须如实，否则排障时会被"重发了 N 天却没写入"误导）。
		// 实际发布天数见 `RebuildLastResult`（真实结论）与 `RebuildStats`（累计读数）。
		result.ResentSources++
		return
	}
	report := m.reconcileWithExpectation(roundCtx, candidate.source, candidate.key, expectation, queryCfg)
	result.CheckedDays += len(report.Days)
	switch {
	case report.Fallback:
		// 查询不可信：不重发（见文件头说明），记错误等下一轮重新判定。
		result.Errors = appendReconcileError(result.Errors,
			"reconcile_untrusted:"+candidate.key+":"+report.FallbackReason)
	case len(report.MissingDays) == 0:
		result.Skipped++
	default:
		for _, day := range report.MissingDays {
			result.Missing = append(result.Missing, candidate.key+":"+day)
		}
		sort.Strings(result.Missing)
		// 缺失天同样**只登记**：`replayMissingDaysCtx` 内联时会做「取权威全量 + 逐天写 VL +
		// 逐字段校验」，在生产单源 57 万条时以十分钟计（`inst:220/file` 的循环重建正是它）。
		ok, skipReason := m.scheduleRebuildAsync(candidate.source, report.MissingDays, "reconcile_missing")
		if !ok {
			result.Errors = appendReconcileError(result.Errors,
				"replay_scheduled=false:"+candidate.key+":"+skipReason)
			return
		}
		result.ResentSources++
		result.ResentDays += len(report.MissingDays)
	}
}

// reconcileExpectationDays 返回该源「有内容的 UTC 天」列表（升序）。
//
// 用途：pending 分支的重建面必须覆盖「可能缺失的全部天」——判定尚未做（确认在 worker 上），
// 故取有内容的全部天。这比凭空猜一天更保守：多算的天在 worker 侧会先经
// `tryConfirmPendingPublicationCtx` 的内容级取证，已完整可见的天不会产生任何写入。
func reconcileExpectationDays(expectation reconcileExpectation) []string {
	out := make([]string, 0, len(expectation.Days))
	for _, day := range expectation.Days {
		if expectation.Counts[day] > 0 {
			out = append(out, day)
		}
	}
	return sortUTCDays(out)
}

// appendReconcileError 追加一条错误分类（有界，超出部分只累计计数）。
func appendReconcileError(list []string, item string) []string {
	if len(list) >= maxReconcileRoundErrors {
		return list
	}
	return append(list, item)
}

// reconcileCandidate 是一个待对账的源（键 + 配置快照）。
type reconcileCandidate struct {
	key    string
	source SourceConfig
}

// nextReconcileCandidates 取本轮的候选源：全源按键排序后**轮转**取 limit 个。
//
// 轮转游标存在 Manager 上（进程内状态）：每轮从上次结束处继续，保证「无论源多少、
// 每轮成本有界」与「所有源都会被覆盖到」两条同时成立。
// nextReconcileCandidates 取候选（加锁版；既有调用方与测试沿用）。
// reconcileCursorKeyOfSnapshot 返回"对账游标当前所指源"的**无锁快照**（未初始化时空串）。
//
// 为什么无锁（2026-10-04 父级裁定 ①）：`config_lock_timeout`/`candidate_lock_timeout` 的快速
// 放弃路径**拿不到 m.mu**——若在这里读游标又会阻塞，观测面重新回到"只剩错误码、点名被掩蔽"。
// 快照由持锁路径在"取候选/推进游标"时写入（atomic.Value），读侧零依赖、零阻塞 ✓。
func (m *Manager) reconcileCursorKeyOfSnapshot() string {
	if m == nil {
		return ""
	}
	v := m.reconcileCursorKeySnapshot.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func (m *Manager) nextReconcileCandidates(limit int) []reconcileCandidate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nextReconcileCandidatesLocked(limit)
}

// nextReconcileCandidatesBounded 有界获取候选：m.mu 上最多等 lockBudget（TryLock 轮询）。
//
// 为什么（2026-10-04 收敛加速器 ✓）：原 `nextReconcileCandidates` **无界等 m.mu**（深积压下实测
// 4m38s——"长持 m.mu 的规划者"，与 runtime.go 同源诊断一致），而轮预算（`roundCtx`，45s）在
// **取候选之前**建立 ⇒ 等锁时间把整轮预算吃光、拿到锁时 ctx 已过期 ⇒ 循环头立即 break
// （checked=0、detail 仅 `round_budget_exhausted`）——**"轮从未开始"的总根源**、duration 波动的
// 直接来源。现在：等锁**有界**且**不消耗轮预算**（roundCtx 移到取候选之后），锁长期紧张时本轮
// 快速放弃并留痕 `candidate_lock_timeout`（下一轮再来），预算全数花在"处理源"上 ✓。
func (m *Manager) nextReconcileCandidatesBounded(limit int, lockBudget time.Duration) ([]reconcileCandidate, bool) {
	// **候选 1 主路径（2026-10-04）**：只读视图零取锁 —— 锁被长持时对账仍能推进（视图由采集链
	// 与源变更持续刷新）。视图不可用（未发布/过期）时才回退到下面的有界 TryLock 路径。
	if out, ok := m.nextReconcileCandidatesFromView(limit); ok {
		return out, true
	}
	if lockBudget <= 0 {
		return m.nextReconcileCandidates(limit), true
	}
	deadline := time.Now().Add(lockBudget)
	for {
		if m.mu.TryLock() {
			out := m.nextReconcileCandidatesLocked(limit)
			m.publishReconcileViewLocked(false) // 顺带刷新只读视图（节流）
			m.mu.Unlock()
			return out, true
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// nextReconcileCandidatesLocked 是持锁主体（调用方必须已持 m.mu）。
func (m *Manager) nextReconcileCandidatesLocked(limit int) []reconcileCandidate {
	keys := make([]string, 0, len(m.pipes))
	for key := range m.pipes {
		if _, ok := m.sources[key]; !ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return nil
	}
	cur := int(m.reconcileLoopCursor.Load())
	if cur >= len(keys) || cur < 0 {
		cur = 0
		m.reconcileLoopCursor.Store(0)
	}
	count := limit
	if count <= 0 || count > len(keys) {
		count = len(keys)
	}
	out := make([]reconcileCandidate, 0, count)
	for i := 0; i < count; i++ {
		key := keys[(cur+i)%len(keys)]
		out = append(out, reconcileCandidate{key: key, source: m.sources[key]})
	}
	// 无锁快照：游标当前所指源（供"锁超时"快速放弃路径带进度，见 reconcileCursorKeySnapshot）。
	m.reconcileCursorKeySnapshot.Store(keys[cur])
	// **取候选只读、不推进游标**（2026-10-03 检查点化修复）：原实现在"取走候选"时就前进 8，
	// 而轮预算（45s，深积压下单源即可吃满）常常只完成 0–1 个源 ⇒ 未处理的 7–8 个源**被跳过**，
	// 要等整圈（⌈源数/8⌉ 轮）才再被取到——"取消即失"的实质。现在游标改由
	// advanceReconcileCursor 在**每个源处理结束后**逐源推进 ⇒ 取消时停在"第一个未处理源"，
	// 已完成的不重做、未完成的下一轮继续（跨轮累积覆盖 ✓）。
	return out
}

// advanceReconcileCursor 在**单个源处理结束后**推进游标一格（逐源检查点，替代"取即推进"）。
func (m *Manager) advanceReconcileCursor() {
	// 候选 1：优先用只读视图零锁推进；无视图时回退持锁（并顺带发布视图）。
	if view := m.currentReconcileView(); view != nil {
		if len(view.keys) == 0 {
			return
		}
		next := (int(m.reconcileLoopCursor.Load()) + 1) % len(view.keys)
		m.reconcileLoopCursor.Store(int64(next))
		m.reconcileCursorKeySnapshot.Store(view.keys[next])
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.pipes))
	for key := range m.pipes {
		if _, ok := m.sources[key]; !ok {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return
	}
	next := (int(m.reconcileLoopCursor.Load()) + 1) % len(keys)
	m.reconcileLoopCursor.Store(int64(next))
	m.reconcileCursorKeySnapshot.Store(keys[next])
	m.publishReconcileViewLocked(false)
}

// reconcileDayCountsCache 是「该源按 UTC 天的权威条数」的进程内缓存。
//
// 为什么必须缓存：权威集合重建是 O(段行数)（生产单源段可达数十万行），而常驻对账是周期任务；
// 没有缓存时每轮都要把参与轮转的源全部重建一遍，把安全网变成常态负担。
//
// 指纹由三个**单调不减**的水位组成（段已覆盖水位 + durable 水位 + delivery 水位）：
// 任一推进都可能改变权威集合（段追加 / WAL durable / 投递推进去重面），此时缓存失效。
// 三者都不动 ⇒ 该源的权威集合逐字节未变 ⇒ 天条数可安全复用。
type reconcileDayCountsCache struct {
	fingerprint string
	days        []string
	counts      map[string]int
}

// canonicalDayCounts 返回该源权威集合「按 UTC 天的条数」（缓存命中零成本）。
func (m *Manager) canonicalDayCounts(key string, source SourceConfig) (reconcileExpectation, error) {
	m.mu.Lock()
	saved := m.state.Sources[key]
	p := m.pipes[key]
	cache, cached := m.reconcileDayCounts[key]
	m.mu.Unlock()

	fingerprint := ""
	if p != nil {
		if positions, ok := p.Positions(); ok {
			fingerprint = fmt.Sprintf("%d|%d|%d", saved.EventsStoredThrough, positions.Durable, positions.Delivery)
		}
	}
	if cached && fingerprint != "" && cache.fingerprint == fingerprint {
		return reconcileExpectation{Days: cache.days, Counts: cache.counts}, nil
	}
	events, err := m.canonicalRecoveryEvents(key, saved)
	if err != nil {
		return reconcileExpectation{}, err
	}
	// CPU 密集对账**在轮内主动让路**（可配：log_reconcile.slice_events / yield）：
	// 现场形态是对账分组与采集轮抢核，采集"没等锁也没等落库"却推进不动 ✗。
	grouped, days, err := m.groupReconcileEvents(source, events)
	if err != nil {
		return reconcileExpectation{}, err
	}
	days = sortUTCDays(days)
	counts := make(map[string]int, len(days))
	for _, day := range days {
		if len(grouped[day]) == 0 {
			continue
		}
		counts[day] = len(grouped[day])
	}
	onlyNonEmpty := make([]string, 0, len(days))
	for _, day := range days {
		if counts[day] > 0 {
			onlyNonEmpty = append(onlyNonEmpty, day)
		}
	}
	m.mu.Lock()
	if m.reconcileDayCounts == nil {
		m.reconcileDayCounts = map[string]reconcileDayCountsCache{}
	}
	m.reconcileDayCounts[key] = reconcileDayCountsCache{
		fingerprint: fingerprint, days: onlyNonEmpty, counts: counts,
	}
	m.mu.Unlock()
	return reconcileExpectation{Days: onlyNonEmpty, Counts: counts}, nil
}

// replayMissingDays 运行期重发一个源的指定 UTC 天（发布语义仍为「取代该天旧代」）。
//
// 复用既有机制（与启动恢复 applyStartupRecovery 的缺失天分支同源）：
//
//	eventsForDays 限定写入面 → writeProjectionPlan（写 VL + 逐字段可见性校验 + 发布 + 落段）
//	→ resolveGapsLandedByDelivery（重发成功即消解被它覆盖的缺口，并登记 VerifiedRuns 区间凭据）
//	→ releaseRecovery（把 WAL 回收责任推进到已发布投影；与投递成功路径同一出口）。
//
// **并发纪律（2026-10-03 收紧，取代此前那条「调用方必须持 cycleMu」的声明）**：
// 此前这里写着「调用方必须持 cycleMu」，但 `ReconcileRoundNow` 从未取过它——声明与实现分叉，
// 谁也拿不到它声称的互斥，而**真按字面实现会把整节点采集堵在重发之后**（现场：54 个未暂停源
// 被串行在 330MB 重发之后）。现在的纪律是 E1 那条：**快照 → 离锁 → 短临界区**——
//   - 重放的读取与 VL 写入（`canonicalRecoveryEvents` / `writeProjectionPlan` 的插入与校验）
//     一律**不持** cycleMu（它们本来就不持：这条纪律只是把它写实）；
//   - 只有代次推进、发布、账本/水位变更走各自既有的短临界区（`m.mu` / `publishMu` / 账本锁）；
//   - 全程受**切片预算**约束（ReplayBudget）并可被 ctx 取消，故「与采集轮并发」是有界且让路的。
//
// 采集轮因此**不再被重发串行在身后**——这也是回归 `TestReplayDoesNotStallCollection` 钉住的性质。
//
// **与 deliveryTailPlan 三条约束的兼容性**：本路径不调用 deliver/planDeliveryTail，
// 构造的 projectionWritePlan 恒有 Replace=true 且 Days 非空（plan.Tail 恒为 nil），
// 因此「重放/待发布语义、事件体已在段、本批区间在水位之后」三条 tail 判据一个都不涉及；
// 段只追加（appendEvents 全量形态）、水位单调、发布在 cycleMu 内串行——三条约束逐字保持。
//
// 为什么 releaseRecovery 传**全量** events 而不是本批：对账保证参与重发的源在其
// 非缺失天上「VL 可见条数 ≥ 应有条数」，缺失天刚被写入并逐字段校验通过——合起来即
// 「该源全量数据已在 VL 可见」，正是 releaseRecovery 要求的责任转移前提。
func (m *Manager) replayMissingDays(source SourceConfig, missingDays []string) error {
	return m.replayMissingDaysCtx(context.Background(), source, missingDays)
}

func (m *Manager) replayMissingDaysCtx(ctx context.Context, source SourceConfig, missingDays []string) error {
	if len(missingDays) == 0 {
		return nil
	}
	// **单实现**：与异步重建 worker 共用 rebuildDaysInner（同一套发布语义、同一套切片预算、
	// 同一套待发布登记）。这里不传 yield —— 同步调用方要求「返回即已推进」，让出会拖长它；
	// 而需要让出以保护采集主线的路径（对账轮 / 启动恢复）已经改走异步调度器
	// （见 reconcileOneCandidate 的 scheduleRebuildAsync），故本入口只剩测试与
	// 需要同步语义的既有调用方。
	//
	// 保留本入口的理由：它是既有回归（d12 / pr61 / gap_autoresolve 等）用来**确定性**断言
	// 「切片预算 / 待发布登记 / 回收责任不提前推进」的同步路径——把它换成异步会让那些断言
	// 变成时序竞猜，那才是真正的覆盖度下降。
	out := m.rebuildDaysInner(ctx, &rebuildRequest{
		key: source.LogSourceID + "/" + source.SourceGeneration, source: source, days: missingDays,
	}, nil)
	if out.Err != nil {
		return out.Err
	}
	if len(out.Days) > 0 {
		slog.Info("常驻增量对账已重发缺失天",
			"source", source.LogSourceID+"/"+source.SourceGeneration, "days", out.Days)
	}
	return nil
}

// reconcileLoopConfig 返回常驻对账配置快照（未启用时 Enabled=false，调用方据此完全不跑）。
// reconcileLoopConfigBounded 有界读取常驻对账配置（锁紧张时 ≤lockBudget 返回 ok=false）。
//
// 为什么（2026-10-04 收敛加速器配套）：`reconcileLoopConfig` 取 m.mu 亦无界——深积压下"读配置"
// 这一步就会吃掉数十秒到数分钟 ⇒ 即使"取候选"有界，轮整体仍无界。配置读取纳入同一有界预算后，
// "轮被锁挡住"有了确定上界且留痕可观测（config_lock_timeout）。
// reconcileLoopConfigFromSnapshot 返回常驻对账配置快照（零锁）；未发布时 ok=false（回退持锁）。
func (m *Manager) reconcileLoopConfigFromSnapshot() (ReconcileLoopConfig, bool) {
	if m == nil {
		return ReconcileLoopConfig{}, false
	}
	v := m.reconcileLoopCfgSnapshot.Load()
	if v == nil {
		return ReconcileLoopConfig{}, false
	}
	cfg, ok := v.(ReconcileLoopConfig)
	if !ok {
		return ReconcileLoopConfig{}, false
	}
	return cfg, true
}

func (m *Manager) reconcileLoopConfigBounded(lockBudget time.Duration) (ReconcileLoopConfig, bool) {
	if m == nil {
		return ReconcileLoopConfig{}, true
	}
	// **候选 1（2026-10-04）**：配置快照零锁直出 —— 配置只在启动/热更时写（写点即时发布），
	// 锁紧张时"配置读"不再是对账轮的第一道坎（此前 config_lock_timeout 正是多轮停死的死因）。
	if cfg, ok := m.reconcileLoopConfigFromSnapshot(); ok {
		return cfg, true
	}
	deadline := time.Now().Add(lockBudget)
	for {
		if m.mu.TryLock() {
			cfg := m.reconcileLoop
			m.reconcileLoopCfgSnapshot.Store(cfg) // 首启兜底：读到即发布（此后零锁）
			m.mu.Unlock()
			return cfg, true
		}
		if !time.Now().Before(deadline) {
			return ReconcileLoopConfig{}, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (m *Manager) reconcileLoopConfig() ReconcileLoopConfig {
	if m == nil {
		return ReconcileLoopConfig{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconcileLoop
}

// SetReconcileLoopConfig 覆盖常驻对账配置（测试与运行期调整用；负值/零值语义同 normalized）。
//
// 与 SetSelfHealInterval 同一风格：测试用小周期/小预算走真实限额路径，
// 而不是只断言常量本身。
func (m *Manager) SetReconcileLoopConfig(cfg ReconcileLoopConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileLoop = cfg
	m.reconcileLoopCfgSnapshot.Store(cfg) // 候选 1：变更即时发布（配置读从此零锁）
}

// recordReconcileRound 把单轮结论并入累计读数（G10 观测面的读数侧）。
func (m *Manager) recordReconcileRound(result ReconcileRoundResult) {
	if m == nil {
		return
	}
	// **TryLock：拿不到即跳过本次统计更新**（2026-10-04 收敛加速器配套）——本方法在"锁紧张"的
	// 快速放弃路径上被调用；若用无界 Lock，会把"有界返回"重新变成无界（实测 202ms → 1.0s ✗）。
	// 代价：锁紧张时少记一次累计（观测面损失，可接受；轮打点仍在日志里 ✓）。
	if !m.mu.TryLock() {
		return
	}
	defer m.mu.Unlock()
	stats := m.reconcileLoopStats
	stats.Rounds++
	stats.CheckedSources += uint64(result.CheckedSources)
	stats.CheckedDays += uint64(result.CheckedDays)
	stats.ResentSources += uint64(result.ResentSources)
	stats.ResentDays += uint64(result.ResentDays)
	stats.SkippedSources += uint64(result.Skipped)
	stats.Errors += uint64(len(result.Errors))
	stats.LastRoundAt = result.RoundAt
	stats.LastDuration = result.Duration
	stats.LastCandidates = result.Candidates
	stats.LastMissing = append([]string(nil), result.Missing...)
	stats.LastErrors = append([]string(nil), result.Errors...)
	m.reconcileLoopStats = stats
}

// logReconcileRound 打印单轮读数（G10 落点风格：每轮一行、字段即读数）。
func (m *Manager) logReconcileRound(result ReconcileRoundResult) {
	if m == nil {
		return
	}
	attrs := []any{
		"candidates", result.Candidates,
		"checkedSources", result.CheckedSources,
		"checkedDays", result.CheckedDays,
		"resentSources", result.ResentSources,
		"resentDays", result.ResentDays,
		"skipped", result.Skipped,
		"errors", len(result.Errors),
		"duration", result.Duration.String(),
	}
	switch {
	case len(result.Errors) > 0:
		slog.Warn("常驻增量对账本轮存在不可信/失败项", append(attrs, "detail", result.Errors)...)
	case len(result.Missing) > 0:
		slog.Info("常驻增量对账发现缺失并已重发", append(attrs, "missing", result.Missing)...)
	default:
		slog.Info("常驻增量对账一致", attrs...)
	}
}

// ReconcileLoopStats 返回常驻对账的累计读数快照（只读观测面）。
func (m *Manager) ReconcileLoopStats() ReconcileLoopStats {
	if m == nil {
		return ReconcileLoopStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := m.reconcileLoopStats
	stats.LastMissing = append([]string(nil), stats.LastMissing...)
	stats.LastErrors = append([]string(nil), stats.LastErrors...)
	return stats
}
