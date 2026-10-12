package ingest

// 本文件实现**启动恢复重发的异步化**（2026-10-10 第二项修复）。
//
// ── 为什么必须再改一次（上一轮打偏了目标）──────────────────────────────────────────
// 上一轮把**常驻对账轮**（`reconcile_loop.reconcileOneCandidate`）从内联执行改为 O(1) 登记，
// 把重活交给 `rebuild_scheduler.go` 的专用 worker。但那只覆盖「运行期对账发现的缺失/pending」。
// 生产实测（`worker.out`，只读统计）表明真正在大量重复的是**启动恢复路径**：
//
//	启动增量对账不可信，回退整窗重发   1658 行（唯一报告 56 个）
//	启动增量对账发现缺失，仅重发缺失天  827 行（唯一报告 25 个）
//	启动增量对账一致，跳过重发         6978 行
//	写入事件总量 2077 万，最大单次 573562（inst:220/file）
//
// 逐行读代码后确认：这条路径（`runStartupRecovery` → `applyStartupRecovery` → `runReplayWindow`
// → `writeProjectionPlan`）**仍在启动驱动的 goroutine 上同步跑完整窗重发**。
// 三个后果都能从现场日志读出来：
//
//	① 驱动内按 `maxStartupReplayPasses=64` 趟反复续跑，期间就绪面恒 not-ready；
//	② 每趟都重新取权威集合（O(段行数)，生产单源 13 万行）再逐天写 VL + 逐字段校验；
//	③ 一旦某源出现**确定性**失败（现场形态：`inst:220/file` 写完 573562 条后校验报
//	   `unexpected or duplicate projection event_id`），它会被下一趟、下一次重启反复重试，
//	   而这条路径**没有任何上限**——现场 `2026-10-05` 被反复判缺失并重发 407 次即此。
//
// ── 本次改法（判定留在启动驱动，重发移出）────────────────────────────────────────
//   - **异步模式（= 生产：`StartupRecoveryBackground=true`）**：判定（对账）仍在本驱动完成，
//     重发**登记**给已有的 `rebuildScheduler`，本驱动立即继续下一批。逐条收益：
//     · **不取 cycleMu**：重建 worker 从不取它，而整轮采集持它 ⇒ 重发无论跑多久都不会把
//       采集挡在门外（这是「不占主线」的根因级保证，不是靠让出凑出来的）。
//     · **有界重试 + 退避 + 放弃**：逐字复用上一轮的 `rebuildFailureClass` / `noteFailure`。
//       确定性失败（写完 VL 但校验不过）达 `rebuildDefaultMaxAttempts` 即**放弃自动重试并打
//       ERROR**；外部条件失败（VL 抖动/ctx 取消）只退避、不判死（频率封顶 `rebuildDefaultBackoffMax`）。
//       两种情况下频率都有上界 ⇒ 不可能再构成无界循环。
//     · **天仍是原子发布单位**：执行体是 `rebuildDaysInner`（与 `replayMissingDaysCtx` 共用同一实现），
//       发布语义一字未改。
//     · **登记与执行都可观测**：登记计入 `RebuildStats.Scheduled`，执行计入
//       Started/Completed/Failed/Abandoned/Partial；跳过记入 `startupRecoveryState.ReplaysSkipped`
//       与 `ReplaySkipReasons`；「已登记未出结论」实时可见于 `ReplaysInFlight`。
//   - **同步模式（`StartupRecoveryBackground=false`）**：**逐字保持**既有内联重发循环
//     （有界趟数 + VL 等待窗口）。理由：这条路径的契约是「New/Start 返回即恢复完成」，调用方
//     （既有回归、需要确定性边界的集成方）依赖它；把它换成异步会把「返回即已推进」变成时序竞猜，
//     那是真实的覆盖度下降。生产不用它（`apps/worker/main.go` 显式传 `StartupRecoveryBackground: true`），
//     本次事故的资源消耗也不来自它。
//
// ── 不静默丢数据（红线，逐条对应）─────────────────────────────────────────────────
//   - 登记失败 ≠ 已重发：`scheduleStartupRebuildTask` 返回 false 时，本文件把「没登记」连同
//     可解释的原因记进读数与日志，**不**把该源标成恢复完成，也**不**推进任何回收责任。
//   - 放弃 ≠ 成功：放弃只停「自动再建一版」；缺口仍留在账本与数据面（`publishedClosedForSource`
//     / `projection_not_published`），就绪面照实报。绝不 `resolveGaps`、绝不 `releaseRecovery`。
//   - 重发没做时谁来兜底：常驻对账（`RunReconcileLoop`）会周期性重新发现该源的缺失天并**重新登记**
//     ——所以「本次没登记」不是数据丢失，只是**推迟到下一个周期**，且有界可观测。
//
// ── 锁序与最坏持有量 ────────────────────────────────────────────────────────────
// 本文件的登记路径只取 `rebuildScheduler.mu`（读 map、写 map、非阻塞发唤醒信号），
// **不取** cycleMu / m.mu / publishMu / registerMu / ledger 锁，故不参与任何锁序，
// 也不可能与它们构成逆序。最坏持有量 = 一次 map 写入，微秒级。

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// startupReplayReason 是启动恢复重发的登记原因（观测用：与常驻对账的
// `reconcile_missing` / `pending_unconfirmed` 区分开，排障时一眼能看出是谁登记的）。
const (
	startupReplayReasonMissing  = "startup_replay_missing"
	startupReplayReasonFallback = "startup_replay_fallback"
)

// maxStartupReplaySkipReasons 是就绪/跳过原因的有界条数（避免逐源刷屏；总量由计数表达）。
const maxStartupReplaySkipReasons = 16

// startupReplaySummary 是一次「启动恢复重发登记」的结论（观测 + 就绪面输入）。
//
// 为什么要分开记 Scheduled / Skipped：两者语义**相反**——Scheduled 表示「已在路上」，
// Skipped 表示「本次没有重发」。把它们合并成一个计数会让现场无法判断「到底有没有在重发」，
// 而那正是本轮修复要回答的问题。
type startupReplaySummary struct {
	// Scheduled 是成功登记到调度器的源数。
	Scheduled int
	// Skipped 是未登记的源数（退避窗口内/已放弃/队列已满/在飞合并/天不可判定）。
	Skipped int
	// SkipReasons 是逐源跳过原因（有界）。
	SkipReasons []string
	// ResolveUnknown 是「对账已证明全部天可见、因而按既有语义解算 UNKNOWN 投递」的源数。
	// 这一支**不写 VL**（零重发），故与上面两个计数分开记。
	ResolveUnknown int
}

// startupReplayDays 返回该源本次需要重发的 UTC 天（升序）。
//
// 语义与 `prepareReplayWindow` 完全一致（那里是同一判据的另一处入口）：
//   - 回退整窗 ⇒ 权威集合覆盖到的**全部** UTC 天；
//   - 仅缺失天 ⇒ 报告里的 MissingDays。
//
// 返回 needs=false 表示「不需要重发」（无缺失天且未回退）。注意这里**不分配代次**：
// 代次在真正执行时由 `rebuildDaysInner` 分配（它会在写入前做 VL 占用探测），
// 这样「登记」就真的是 O(1)（O(天数)，不是 O(事件数)）且不产生任何副作用。
func (m *Manager) startupReplayDays(item startupSource, report ReconcileReport) ([]string, bool, error) {
	if !report.needsReplay() {
		return nil, false, nil
	}
	if !report.Fallback {
		return sortUTCDays(append([]string(nil), report.MissingDays...)), true, nil
	}
	daySet := make(map[string]struct{}, 8)
	for _, event := range item.events {
		day, err := eventUTCDay(item.source, event)
		if err != nil {
			// 天不可判定：与老路径 prepareReplayWindow 的处置一致（如实报错，绝不假装已恢复）。
			return nil, true, err
		}
		daySet[day] = struct{}{}
	}
	days := make([]string, 0, len(daySet))
	for day := range daySet {
		days = append(days, day)
	}
	return sortUTCDays(days), true, nil
}

// scheduleStartupReplays 把一批源的启动恢复重发**登记**出去（异步模式 = 生产路径）。
//
// 调用方是启动驱动本身；本方法**不做任何重活**：对每个源只做 O(天数) 的登记（或对
// 「无需重发」的源做一次投递状态检查）。因此驱动可以立刻继续下一批，
// 而重发在重建 worker 上按有界重试 + 退避 + 放弃跑完。
func (m *Manager) scheduleStartupReplays(loaded []startupSource, reports []ReconcileReport) startupReplaySummary {
	summary := startupReplaySummary{}
	if len(loaded) == 0 {
		return summary
	}
	if m == nil {
		return summary
	}
	for index, item := range loaded {
		if index >= len(reports) {
			break
		}
		report := reports[index]
		days, needs, err := m.startupReplayDays(item, report)
		if err != nil {
			// 与老路径同型：判不了天就记一次启动恢复失败（可见），不当成已恢复。
			m.noteStartupRecoveryFailure(item.key, err)
			summary.Skipped++
			summary.SkipReasons = appendBoundedReason(summary.SkipReasons, item.key+":days_unavailable:"+err.Error())
			continue
		}
		if !needs {
			// 对账已证明该源全部天的数据在 VL 中可见 ⇒ 按既有语义解算 UNKNOWN 投递。
			// **不做** releaseRecovery：回收责任推进需要内容级保证，留给运行期正常路径
			//（与 `applyStartupRecovery` 的零重发分支逐字一致）。
			if err := m.resolveUnknownAfterConsistent(item); err != nil {
				m.noteStartupRecoveryFailure(item.key, err)
				summary.Skipped++
				summary.SkipReasons = appendBoundedReason(summary.SkipReasons, item.key+":resolve_unknown:"+err.Error())
				continue
			}
			summary.ResolveUnknown++
			continue
		}
		if len(days) == 0 {
			// 判「需要重发」却算不出任何天（权威集合里该源为空）：**不登记、不静默**。
			// 这不是失败（没有任何可写的数据），但必须可见——否则现场会以为"已经在重发了"。
			summary.Skipped++
			summary.SkipReasons = appendBoundedReason(summary.SkipReasons, item.key+":no_replayable_days")
			continue
		}
		reason := startupReplayReasonMissing
		if report.Fallback {
			reason = startupReplayReasonFallback
		}
		if ok, skipReason := m.scheduleStartupRebuildTask(item.source, days, reason); !ok {
			summary.Skipped++
			summary.SkipReasons = appendBoundedReason(summary.SkipReasons, item.key+":"+skipReason)
			if skipReason == "rebuild_abandoned" {
				// 已放弃 = 需要人工介入：记入启动恢复失败原因（可见且阻塞就绪面），
				// 绝不把它当成"稍后会自动好"。
				m.noteStartupRecoveryFailure(item.key,
					fmt.Errorf("ingest: 启动恢复重发已放弃自动重试（确定性失败达上限，需重启 Worker 或源换代次）: %s", reason))
				continue
			}
			// 其余（退避/在飞/队列满）：本轮的缺失天推迟到常驻对账下一轮重新发现并重新登记；
			// 数据仍视为未发布，缺口由数据面（projection_not_published）照实呈现。
			slog.Warn("启动恢复重发未登记（推迟到常驻对账下一轮重新发现；数据仍视为未发布，缺口保持可见）",
				"source", item.key, "reason", skipReason, "days", days, "replayReason", reason)
			continue
		}
		summary.Scheduled++
	}
	return summary
}

// resolveUnknownAfterConsistent 在「对账证明该源全部天可见」时解算 UNKNOWN 投递
// （与 `applyStartupRecovery` 的零重发分支同一处置，逐字保留）。
func (m *Manager) resolveUnknownAfterConsistent(item startupSource) error {
	m.mu.Lock()
	p := m.pipes[item.key]
	m.mu.Unlock()
	if p == nil || p.DeliveryState() != logtypes.DeliveryUnknown {
		return nil
	}
	return p.ResolveUnknownThroughProjection(item.events)
}

// appendBoundedReason 追加一条跳过原因（有界：超出上限只保留前若干条）。
//
// 为什么要界：一次启动可能登记上百个源，退避期间全部跳过——无界收集会把就绪原因与内存
// 一起撑大；而被截断的部分仍有计数（`startupReplaySummary.Skipped`）可判读，不会静默。
func appendBoundedReason(list []string, item string) []string {
	if len(list) >= maxStartupReplaySkipReasons {
		return list
	}
	return append(list, item)
}

// noteStartupReplaySummary 把登记结论落进启动恢复读数并打一行日志（**可观测**落点）。
//
// 为什么必须打这一行：改动前「启动恢复在重发什么」只能从 VL 写入量反推；而本次改动把重发
// 移出启动驱动后，「登记了几个源、跳过了几个、为什么跳过」成了唯一的直接证据。
func (m *Manager) noteStartupReplaySummary(summary startupReplaySummary) {
	if summary.Scheduled == 0 && summary.Skipped == 0 && summary.ResolveUnknown == 0 {
		return
	}
	m.mu.Lock()
	m.startupRecovery.ReplaysScheduled += summary.Scheduled
	m.startupRecovery.ReplaysSkipped += summary.Skipped
	for _, reason := range summary.SkipReasons {
		if len(m.startupRecovery.ReplaySkipReasons) >= maxStartupReplaySkipReasons {
			break
		}
		m.startupRecovery.ReplaySkipReasons = append(m.startupRecovery.ReplaySkipReasons, reason)
	}
	m.mu.Unlock()
	slog.Info("启动恢复重发已登记（重发在重建 worker 上执行，不占采集主线）",
		"scheduled", summary.Scheduled, "skipped", summary.Skipped,
		"resolveUnknown", summary.ResolveUnknown, "skipReasons", summary.SkipReasons)
}

// startupReplayInFlight 返回「启动恢复已登记、尚无结论」的源键（升序）。
//
// 用途：把「重发还在路上」这件事显式暴露给观测面（`StartupRecoveryStatus`）与就绪面
// （`CutoverReadiness` 的 `startup_replay_in_flight`），避免现场把「登记完成」误读成
// 「恢复完成」。
func (m *Manager) startupReplayInFlight() []string {
	if m == nil || m.rebuilds == nil {
		return nil
	}
	return m.rebuilds.startupPendingKeys()
}

// WaitStartupReplayIdle 等待「已登记的启动恢复重发全部出结论」（ctx 取消即返回 ctx.Err()）。
//
// 为什么需要（**两阶段契约**）：改动后启动恢复分成两段——
//   - 阶段一（判定 + 登记）：秒级，由 `WaitStartupRecovery` 覆盖；
//   - 阶段二（重发本身）：分钟级，跑在重建 worker 上，由本方法覆盖。
//
// 就绪面同时受两段约束（见 `CutoverReadiness`），因此任何「等恢复彻底落定」的调用方
// 都必须等两段——本方法就是阶段二的等待点。生产**不调用**它（生产不等重发，
// 只是不许在此期间宣告就绪）；它是测试与需要确定性边界的集成方的显式等待点。
//
// **有界性**：轮询间隔固定（不受重发规模影响），且等待完全由传入 ctx 约束——
// 调用方给出上界，本方法绝不自行无限等（外部条件类失败永不放弃，故这里的 ctx 上界是必须的）。
// 超时**不**改任何状态：缺口仍由数据面照实呈现，不会因为"等超时了"就被当成成功。
func (m *Manager) WaitStartupReplayIdle(ctx context.Context) error {
	if m == nil || m.rebuilds == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 先判一次再进循环：绝大多数调用（无缺失天）当场返回，不引入任何等待。
	if len(m.startupReplayInFlight()) == 0 {
		return nil
	}
	ticker := time.NewTicker(startupReplayIdlePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if len(m.startupReplayInFlight()) == 0 {
				return nil
			}
		}
	}
}

// startupReplayIdlePoll 是「等重发出结论」的轮询间隔。
//
// 取值理由：重发的收束粒度是「单天写完 + 逐字段校验 + 发布」，生产单天最长到分钟级，
// 100ms 的轮询既不引入可感延迟，也不会在被测进程里烧 CPU（这是等待点，不是检查点）。
const startupReplayIdlePoll = 100 * time.Millisecond
