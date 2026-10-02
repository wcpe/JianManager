package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// 本文件实现**常驻增量对账**（后续项③）：把启动期一次性的「源 × UTC 天」条数级对账
// （FR-497，见 reconcile.go）做成周期性、有界、可配的常驻协程。
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

// RunReconcileLoop 常驻对账循环：按 interval 周期执行一轮对账，直到 ctx 结束。
//
// 与采集轮共用同一 ctx 生命周期（见 Manager.Start 的启动点）；关闭配置时立即返回
// （不打日志风暴、不查 VL、不推进任何状态）。
func (m *Manager) RunReconcileLoop(ctx context.Context) {
	if m == nil {
		return
	}
	cfg := m.reconcileLoopConfig()
	if !cfg.Enabled {
		slog.Info("常驻增量对账未启用（关闭即不跑）")
		return
	}
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
	cfg := m.reconcileLoopConfig()
	if !cfg.Enabled {
		// 关闭即不跑：连统计都不推进，保证「关闭」与「跑过但无结论」可区分。
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	roundCtx, cancel := context.WithTimeout(ctx, cfg.RoundBudget)
	defer cancel()

	candidates := m.nextReconcileCandidates(cfg.MaxSourcesPerRound)
	result.Candidates = len(candidates)
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
		// VL 未就绪：本轮不判、更不重发——「不可信查询不重发」的既有取舍同样适用于「端点不可用」，
		// 而且把必然失败的重发挡在门外（2026-10-03 现场：worker 与 VL 同步重启时对账抢跑）。
		if err := m.ensureVLReady(roundCtx, candidate.source); err != nil {
			result.Errors = appendReconcileError(result.Errors, "vl_not_ready:"+candidate.key+":"+err.Error())
			continue
		}
		expectation, err := m.canonicalDayCounts(candidate.key, candidate.source)
		if err != nil {
			result.Errors = appendReconcileError(result.Errors, "canonical_counts:"+err.Error())
			continue
		}
		result.CheckedSources++
		if len(expectation.Days) == 0 {
			// 空源（无非空天）：没有可对账的面，既不计缺失也不重发。
			result.Skipped++
			continue
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
			if err := m.replayMissingDays(candidate.source, report.MissingDays); err != nil {
				result.Errors = appendReconcileError(result.Errors,
					"replay_failed:"+candidate.key+":"+err.Error())
				continue
			}
			result.ResentSources++
			result.ResentDays += len(report.MissingDays)
		}
	}
	result.Duration = time.Since(started)
	m.recordReconcileRound(result)
	m.logReconcileRound(result)
	return result
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
func (m *Manager) nextReconcileCandidates(limit int) []reconcileCandidate {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	if m.reconcileLoopCursor >= len(keys) || m.reconcileLoopCursor < 0 {
		m.reconcileLoopCursor = 0
	}
	count := limit
	if count <= 0 || count > len(keys) {
		count = len(keys)
	}
	out := make([]reconcileCandidate, 0, count)
	for i := 0; i < count; i++ {
		key := keys[(m.reconcileLoopCursor+i)%len(keys)]
		out = append(out, reconcileCandidate{key: key, source: m.sources[key]})
	}
	m.reconcileLoopCursor = (m.reconcileLoopCursor + count) % len(keys)
	return out
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
	grouped, days, err := groupEventsByUTCDay(source, events)
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
	if len(missingDays) == 0 {
		return nil
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	events, err := m.canonicalRecoveryEvents(key, saved)
	if err != nil {
		return fmt.Errorf("ingest: 常驻对账重发取权威集合失败: %w", err)
	}
	if len(events) == 0 {
		return nil
	}
	writeEvents, err := eventsForDays(source, events, missingDays)
	if err != nil {
		return err
	}
	if len(writeEvents) == 0 {
		// 计划的天在权威集合里没有任何事件：不生成空代次（与启动恢复的语义一致）。
		return nil
	}
	generation := m.nextFreeProjectionGeneration(source, saved.ProjectionGeneration)
	m.mu.Lock()
	saved = m.state.Sources[key]
	saved.ProjectionGeneration = generation
	m.state.Sources[key] = saved
	m.mu.Unlock()
	// 按**天**切片推进（预算可配）：每天写完即发布该天；预算用尽的剩余天留给下一轮——下一轮
	// 的对账会重新发现它们仍是缺失天（已发布的天条数已够，不会再被判缺失），因此**无需游标状态**，
	// 天然可续且不重不漏。整窗完成前**不推进回收责任**（releaseRecovery 要求全窗口内容级保证）。
	budget := m.replayBudget
	startedAt := time.Now()
	published := make([]string, 0, len(missingDays))
	remaining := sortUTCDays(missingDays)
	for len(remaining) > 0 {
		if sent := len(published); sent >= budget.MaxDays || time.Since(startedAt) >= budget.MaxDuration {
			slog.Info("常驻对账重发受切片预算约束提前收束（剩余天下一轮续跑）",
				"source", key, "publishedDays", published, "remainingDays", remaining)
			return nil
		}
		day := remaining[0]
		if err := m.ensureVLReady(context.Background(), source); err != nil {
			return fmt.Errorf("ingest: 常驻对账重发前 VL 未就绪（本轮不重发）: %w", err)
		}
		dayEvents, err := eventsForDays(source, events, []string{day})
		if err != nil {
			return err
		}
		if len(dayEvents) == 0 {
			remaining = remaining[1:]
			continue
		}
		plan := projectionWritePlan{Write: dayEvents, Replace: true, Days: []string{day}}
		if _, err := m.writeProjectionPlan(source, events, generation, plan); err != nil {
			return fmt.Errorf("ingest: 常驻对账重发失败: %w", err)
		}
		published = append(published, day)
		remaining = remaining[1:]
	}
	if len(remaining) > 0 {
		// 切片没做完：数据面已发布完成的天是确定的；回收责任留到整窗完成之后再推进。
		return nil
	}
	m.resolveGapsLandedByDelivery(source, writeEvents)
	if err := m.releaseRecovery(source, events); err != nil {
		// best-effort：数据已落库这一事实不受影响，下一轮采集路径会再试。
		slog.Warn("常驻对账重发后推进回收责任失败（下轮重试）", "source", key, "error", err)
	}
	slog.Info("常驻增量对账已重发缺失天",
		"source", key, "days", published, "events", len(writeEvents), "generation", generation)
	return nil
}

// reconcileLoopConfig 返回常驻对账配置快照（未启用时 Enabled=false，调用方据此完全不跑）。
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
}

// recordReconcileRound 把单轮结论并入累计读数（G10 观测面的读数侧）。
func (m *Manager) recordReconcileRound(result ReconcileRoundResult) {
	if m == nil {
		return
	}
	m.mu.Lock()
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
