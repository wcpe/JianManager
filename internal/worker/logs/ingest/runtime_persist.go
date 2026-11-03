// 本文件是**持久化主体**面：persistCtx 的分步让路、planPersist / commitPersist
// 的计划与提交、以及持久化门（persistGate）与让路预算。
//
// 分步持久化的目的不是吞吐，而是「不让一次持久化独占门把采集轮饿死」——
// 见 persistStepYield 附近的说明（2026-10-02 事故）。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// ClearConflictsPersistHandoffCount 返回清空身份冲突"交办后台"的累计次数（观测读数，0 ⇒ 全部同步落库完成）。
func (m *Manager) ClearConflictsPersistHandoffCount() int64 {
	if m == nil {
		return 0
	}
	return m.persistClearConflictsHandoffs.Load()
}

func (m *Manager) requestPersist() {
	if m == nil || m.persistRequests == nil {
		return
	}
	select {
	case m.persistRequests <- struct{}{}:
	default:
	}
}

// persistLoop 是**唯一允许在持久化门上阻塞**的消费者：它把热路径交来的落库请求做完。
//
// 为什么必须有（2026-10-03 现场）：采集/投递侧的落库前置是"先落库再写 VL"的契约，而一次落库的
// 单个单元可能停在一分钟级的 SQL 语句上——把采集侧挂在这条链上，就是现场"VL 零写入 + read_pos
// 冻结"的形态。现在采集侧**有界等待**、超时交办，落库的**真实性**由本 goroutine 与
// `persistCovered` 保证（只在整批真正落库后才推进 ✓ 不谎报）。
func (m *Manager) persistLoop(ctx context.Context) {
	defer close(m.persistWorkerDone)
	if ctx == nil {
		ctx = context.Background()
	}
	// #67-B（b）：受控截断的**锁外执行点** —— 独立 tick（与"周期是否完成"无关 ✓），
	// 每轮只做一次"能不能截断"的判定；真正要求 TryLock 成功 + 无读者 + 残余 ≤ 门限 ✓。
	truncateTicker := time.NewTicker(defaultControlledTruncateInterval)
	defer truncateTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.persistRequests:
			if err := m.persistBackground(); err != nil {
				slog.Warn("后台持久化失败（下一轮重试）", "error", err)
			}
		case <-truncateTicker.C:
			m.tryControlledTruncate(ctx)
		}
	}
}

// persistBackground仅供后台消费者安全合并计划；同步、严格耐久、登记、恢复和Stop入口保持原语义。
func (m *Manager) persistBackground() error {
	return m.persistCtx(context.Background(), persistMode{priority: true, planReuse: true})
}

// persistPollTail 是采集轮尾部的收尾落库；本身不承载任何耐久回执。
//
// 采集轮尾部的返回值本来就被丢弃、预算只有 hotBudget、且允许交办后台（failOnHandoff=false），
// 故它既不保证"返回即已落库"，也不阻止下一轮准入：真正的耐久前置仍是
// CommitAndQualify → persistDurableBeforeDelivery(failOnHandoff=true) 与新批实际完成计划回执。
// 后台持久化模式下这里只唤醒既有消费者，避免主采集线再做一份不会被采纳的冗余计划
// （旧剖析：56 份计划中 13 份未进入 Apply、构造 120121 条 WAL 行）。
// 默认（非 deferredDelivery）保持原同步行为，兼容既有立即 SQL 可见性。
func (m *Manager) persistPollTail() {
	if m == nil {
		return
	}
	if m.deferredDelivery {
		// 通道容量1且永不复位关闭：闭锁后 sending 只落到 default 分支，不会 panic 也不会阻塞。
		m.requestPersist()
		return
	}
	_ = m.persistHot()
}

// defaultControlledTruncateInterval 是受控截断执行点的节拍（#67-B（b））：低频、与持久化周期解耦 ✓。
const defaultControlledTruncateInterval = 60 * time.Second

// noteReclaimStall 是 #179 第 1 步②的**有界判据（本步只告警）**：同一源连续 ≥3 轮"回收未前进"
// 即升级为一条 Warn（含受阻类别读数 ✓），之后每 3 轮再报一次（节流 ✓）。
// **本步不做任何自动动作** ✗（"存量段直投"那一级待 ① 落地后接入 ✓）——目的只是"禁止静默永久钉死" ✓。
func (m *Manager) noteReclaimStall(p *pipeline.Pipeline, source SourceConfig, pos logtypes.Positions) {
	if m == nil || p == nil {
		return
	}
	key := p.Key().LogSourceID + "/" + p.Key().SourceGeneration
	m.reclaimStallMu.Lock()
	if m.reclaimStallRounds == nil {
		m.reclaimStallRounds = make(map[string]int)
		m.reclaimStallLast = make(map[string]uint64)
	}
	if last, ok := m.reclaimStallLast[key]; ok && pos.Reclaim > last {
		m.reclaimStallRounds[key] = 0 // 有进展 ⇒ 计数清零 ✓
	} else if pos.Reclaim < pos.Delivery {
		m.reclaimStallRounds[key]++ // 有欠账且未前进 ⇒ 记一轮 ✓
	} else {
		m.reclaimStallRounds[key] = 0 // 无欠账（reclaim ≥ delivery）⇒ 非停滞 ✓
	}
	rounds := m.reclaimStallRounds[key]
	m.reclaimStallLast[key] = pos.Reclaim
	m.reclaimStallMu.Unlock()
	if rounds >= 3 && rounds%3 == 0 {
		m.blockedStallWarns.Add(1)
		slog.Warn("回收连续多轮无进展（②有界判据：本步只告警，不做自动动作）",
			"logSourceID", source.LogSourceID, "rounds", rounds,
			"read", pos.Read, "durable", pos.Durable, "delivery", pos.Delivery, "reclaim", pos.Reclaim,
			"blocked_seg_hold", m.blockedSegHold.Load(), "blocked_config_hold", m.blockedConfigHold.Load(),
			"blocked_no_material", m.blockedNoMaterial.Load(), "blocked_watermark_moved", m.blockedWatermark.Load(),
			"blocked_no_confirmed_copy", m.blockedNoCopy.Load())
	}
}

// allowReReadGapProbe 是"待重读缝直投"的**每源冷却门**（O(1)，2026-10-06 快验回归修复）：
// 同源 30s 内至多探一次 ⇒ 把该步的代价从"每源每轮 × 全量快照"降到"欠账源 ≤2 次/分钟" ✓。
// 设计取舍：宁可让个别缝多等 ≤30s，也不允许它再占死兜底环（饿死 60 源的代价远大于此 ✗）。
func (m *Manager) allowReReadGapProbe(key ledger.SourceKey) bool {
	if m == nil {
		return false
	}
	k := key.LogSourceID + "/" + key.SourceGeneration
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reReadGapProbeAt == nil {
		m.reReadGapProbeAt = make(map[string]time.Time)
	}
	if last, ok := m.reReadGapProbeAt[k]; ok && now.Sub(last) < 30*time.Second {
		return false
	}
	if len(m.reReadGapProbeAt) > 4096 {
		for kk, ts := range m.reReadGapProbeAt {
			if now.Sub(ts) > 10*time.Minute {
				delete(m.reReadGapProbeAt, kk)
			}
		}
	}
	m.reReadGapProbeAt[k] = now
	return true
}

// directDeliverResolvedReReadGap 是 #179-② 的**破环直投步**（红线照批 ✓）：
// 区间上界＝缝 EndPos（目的正是越过当前 delivery ✓）；只投确在 WAL 的 durable 字节（缺 ⇒ 可见缺口+告警 ✓）；
// 不绕导入守卫、不 MarkImported、hold 优先、判据不放宽 ✓；单源互斥复用 #56 在飞表 ✓；
// 上限取配置值（`maxReplayEventsPerDrain`），未配置时回退**导出真源** `DefaultReplayEventsPerDrain` ✓；
// **不做自动终局裁定** ✗（升级梯首级就是本步 ✓）。
func (m *Manager) directDeliverResolvedReReadGap(p *pipeline.Pipeline, source SourceConfig) {
	if m == nil || p == nil || p.Ledger() == nil {
		return
	}
	ctx, release, guardErr := m.acquireSourceDelivery(m.deliveryLifetime(), source)
	if guardErr != nil {
		return
	}
	defer release()
	// 门闸（2026-10-06 探针快验回归修复 ✗✗）：本步原来对**每个源、每轮**先做
	// `Ledger.Get`（深克隆 O(缺口数＋批次数)）＋ `WAL.Snapshot`（全量拷贝 O(WAL 条数)）——
	// 60 源 × 数十万条目实测把兜底环单轮拖到 **48.5 分钟**（采集读面被饿死 ✓）。
	// 两道 O(1) 前置：① 无投递欠账（delivery ≥ read）⇒ 直接返回 ✓；
	//              ② 同源冷却期内不重复探测 ✓（默认 30s，见 allowReReadGapProbe ✓）。
	pos, ok := p.Ledger().Positions(p.Key())
	if !ok || pos.Delivery >= pos.Read {
		return
	}
	if !m.allowReReadGapProbe(p.Key()) {
		return
	}
	entry := p.Ledger().Get(p.Key())
	if entry == nil {
		return
	}
	// 选缝：只取**仍阻挡当前 delivery 水位的待重读缝**（EndPos > delivery ✓）。
	// 落后于水位的旧缝（字节多已被回收）不得被误选——2026-10-05 179 首验实测：旧缝 id=0
	// 被误选 ⇒ 每轮只报"找不到字节"，真正挡路的 id=1 永远轮不到 ✗✗。
	delivery := entry.Positions.Delivery
	type reReadGap struct{ from, to uint64 }
	candidates := make([]reReadGap, 0, 4)
	for _, g := range entry.Gaps {
		if !g.Resolved || !acquire.IsReReadHealableResolution(g.Resolution) {
			continue
		}
		if g.EndPos <= delivery || g.EndPos <= g.StartPos {
			continue
		}
		candidates = append(candidates, reReadGap{g.StartPos, g.EndPos})
	}
	if len(candidates) == 0 {
		return
	}
	limit := m.maxReplayEventsPerDrain
	if limit <= 0 {
		limit = acquire.DefaultReplayEventsPerDrain
	}
	snapshot := p.WAL().Snapshot()
	slice := make([]logtypes.Event, 0, 16)
	picked := false
	tried := make([]bool, len(candidates))
	for {
		// 按 StartPos 升序逐个尝试（离水位最近者优先 ✓），取第一个确有 durable 字节的。
		best := -1
		for i, c := range candidates {
			if tried[i] {
				continue
			}
			if best < 0 || c.from < candidates[best].from {
				best = i
			}
		}
		if best < 0 {
			break
		}
		tried[best] = true
		c := candidates[best]
		slice = slice[:0]
		for _, e := range snapshot {
			if !e.Durable || e.Event.Record.End < c.from || e.Event.Record.Start > c.to {
				continue
			}
			slice = append(slice, e.Event)
			if len(slice) >= limit {
				break
			}
		}
		if len(slice) > 0 {
			picked = true
			break
		}
	}
	if !picked {
		if m.noteSourceError(source.LogSourceID, "reclaim-repair: 缝区间在 WAL 中无 durable 字节") {
			slog.Warn("破环直投：待重读缝区间在 WAL 中找不到 durable 字节（保持可见缺口，不静默）",
				"logSourceID", source.LogSourceID, "candidates", len(candidates))
		}
		return
	}
	// **单源互斥由调用方持有**（INGEST-12）：认领在 `maybeResumePausedSources` 里、经两档
	// O(1) 门闸之后完成，使本函数内的 O(n) 重活（深克隆/全量快照）也只在认领成功后发生。
	// 本函数**不再**自己认领/释放 —— 同 key 二次认领会直接失败而让本步静默空转（自锁）。
	//
	// 增量语义（replay=false ✓）：本步投的是**缝区间的部分切片**，不是整窗——
	// `deliver()` 内 `replace := saved.PublicationPending || replay` ⇒ replay=true 会按
	// 「整窗重发」走权威全量重建（O(段行数)≈秒级）并把全量当写入面 ✗✗（2026-10-05 首验：
	// 该形态带来 202 的「投影可见性期限」重试与时延；部分切片本就不该走整窗语义 ✗）。
	captured, required, capture, captureErr := p.WAL().CaptureDelivery(slice)
	if captureErr != nil {
		m.deferDurableDelivery(source.LogSourceID + "/" + source.SourceGeneration)
		return
	}
	if err := m.persistCommittedBeforeDelivery(ctx, source.LogSourceID+"/"+source.SourceGeneration, p, required); err != nil {
		m.deferDurableDelivery(source.LogSourceID + "/" + source.SourceGeneration)
		return
	}
	slice = captured
	ctx = acquire.WithDeliveryCapture(ctx, capture)
	result, err := m.deliverCtx(ctx, source, slice, false)
	if err != nil {
		slog.Warn("破环直投失败（下轮重试）", "logSourceID", source.LogSourceID, "error", err)
		return
	}
	// 只登记真实结果及实际连续区间并集；ACK丢失或非2xx不能伪造200关闭旧洞。
	if err := p.RecordDeliveryResult(ctx, slice, result); err != nil {
		slog.Warn("破环直投：投递结果登记失败（下轮重试）", "logSourceID", source.LogSourceID, "error", err)
		return
	}
	if result.HTTPStatus < 200 || result.HTTPStatus >= 300 || result.AckLost {
		return
	}
	// #179-③ 收口：缝的"关闭"在本形态是**改判**（待重读类 ⇒ 已投递类）——
	// 既有"未决→已决"通道到不了已决缝 ✗（实测 `缝已关=false` ✓）。
	if n, err := p.Ledger().SupersedeDeliveredReReadGaps(p.Key(), gapRangesOfEvents(slice),
		gapResolutionDeliveryRetry,
		acquire.ResolutionReReadOnResume, acquire.ResolutionReReadOnResumeDelivery); err != nil {
		slog.Warn("破环直投：缝升级失败（数据已投出，下轮重试）", "logSourceID", source.LogSourceID, "error", err)
	} else if n > 0 {
		slog.Info("破环直投：待重读缝已按投递证据升级为终局", "logSourceID", source.LogSourceID, "gaps", n)
	}
}

// tryControlledTruncate 在持久化环的 tick 上、**锁外**尝试一次受控截断（#67-B（b））。
//
// 为什么放在这里：TRUNCATE 会等读者并做页拷贝 —— 在持 `stateindex.Store.mu` 时做会把落库/采样/读面
// 一起冻住（2026-10-05 生产 58min 停摆的机制本体 ✗✗）。本入口只在**环的节拍**上调用，且真正截断
// 由 `Store.TryControlledTruncate` 用 `TryLock` 自证"没有在飞 apply/读"后才执行 ✓。
func (m *Manager) tryControlledTruncate(ctx context.Context) {
	if m == nil {
		return
	}
	m.persistPlanMu.Lock()
	plan := m.persistPlan
	m.persistPlanMu.Unlock()
	if plan == nil || plan.store == nil {
		return
	}
	plan.store.TryControlledTruncate(ctx)
}

// persistCtx 是 persist 的**可取消 + 分步 + 显式公平**实现。
//
// 公平性（2026-10-03 现场：门闩饿死采集轮）：等待者**显式入队**（优先/普通两条 FIFO），只有队首
// 能被授予门；持门者做完一步后必须**重新排到队尾**，并受「让路窗口」约束（刚交门的调用者在窗口内
// 不得再取门）——公平性不再交给 `sync.Mutex` 的抢锁与裸 Broadcast ✗。
//
// 续跑为什么不重不漏：`ApplyScoped` 的镜像**按提交单元增量更新**，与库内容逐单元一致；因此下一步
// 重算差异时看到的只剩尚未处理的行。ctx 取消时已提交单元保留（状态一致），未提交部分下次继续。
func (m *Manager) persistCtx(ctx context.Context, mode persistMode) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.persistGateEnsure()
	// 合并（coalescing）：本调用按到达顺序声明一个序号；任何「构建发生在声明之后」的落库都会
	// 覆盖它（见下方 covered 的读取位置），因此一个周期可被任意多个调用方共享，短变更调用者
	// （实例登记）不必排到队尾各自再执行一遍。
	seq := m.persistJoin.Add(1)
	if m.persistCovered.Load() >= seq {
		return nil
	}
	// **铁律：规划在门外**（2026-10-03 现场）：持门者只做提交、不做规划 ✗✗。
	// 规划合并（2026-10-03 回退修复 ①）：**只对热路径**（采集/投递，mode.planReuse）在窗口内
	// 复用既有计划 —— 原实现让每个未 covered 的调用者各自跑 planPersist（持 m.mu 遍历全源 +
	// 重建镜像），60 源并发下每轮 N 倍冗余规划 + 锁争抢，实测吞吐 3.3k→1.5k 行/s。
	// 强一致路径（登记/关停/恢复/测试）保持"每次重新规划"⇒"返回即落库"语义零变化 ✓。
	// 覆盖水位按**计划自带**的 plan.covered（构建前读到的 persistJoin 水位）推进，绝不越推 ✓。
	var (
		plan    *persistPlan
		planErr error
	)
	if mode.planReuse {
		plan, planErr = m.planPersistReuse(ctx, seq)
	} else {
		plan, planErr = m.planPersist(ctx)
	}
	if planErr != nil {
		// 规划失败：不阻塞调用方（热路径语义），交给后台通道重试；错误照旧上报 ✓。
		m.requestPersist()
		return planErr
	}
	g := &m.persistGate
	g.mu.Lock()
	defer g.mu.Unlock()
	waitStarted := time.Now()
	g.enqueueLocked(seq, mode.priority)
	// 入队序观测（契约字面，2026-10-05 A 裁定）：记录本条在门内的真实入队顺序。
	// 契约 = "门按其队列顺序（入队序）服务"；seq 是门外取号，并发下可与入队序不同
	// （旧行为里长 m.mu 段恰好串行化了并发调用 ⇒ 隐式维持"取号序==入队序"；A-Step2 拆掉
	// 该隐式耦合后，观测面必须记录真实入队序，不得再拿取号序当契约 ✗）。
	if m.persistGateStage != nil {
		m.persistGateStage(ctx, "enqueue", seq, mode.priority)
	}
	// 出队同样要 Broadcast：**排队结构的变化会改变「谁是队首」**——一个等待者返回（出队）后，
	// 新的队首可能因此变得可授予，而它此刻正睡在 cond.Wait 上。少了这一次唤醒就是全体沉睡
	// （2026-10-03 实测：12 个 persistCtx 全在 Cond.Wait、门是空的）。
	defer func() {
		g.dequeueLocked(seq)
		g.cond.Broadcast()
	}()
	for {
		if m.persistCovered.Load() >= seq {
			return g.err
		}
		if mode.maxWait > 0 && time.Since(waitStarted) >= mode.maxWait {
			// 热路径等不到门：交给后台通道（保证"最终会落库"），本调用立即返回 ✓ 采集不受阻。
			m.notePersistHandoff(mode)
			m.requestPersist()
			if mode.failOnHandoff {
				return ErrPersistDeferred
			}
			return nil
		}
		if !g.running && g.grantableLocked(seq, mode.priority, m.persistPriorityStreakMax()) {
			if plan != nil && plan.snapshot < m.persistAccepted.Load() {
				// 尚未授予写入步骤，离门重新规划但保留真实排队位置，不能让同档后来者插队。
				g.mu.Unlock()
				if mode.planReuse {
					plan, planErr = m.planPersistReuse(ctx, seq)
				} else {
					plan, planErr = m.planPersist(ctx)
				}
				g.mu.Lock()
				if planErr != nil {
					return planErr
				}
				continue
			}
			g.running = true
			g.dequeueLocked(seq)
			if m.persistGateStage != nil {
				m.persistGateStage(ctx, "acquire", seq, mode.priority)
			}
			g.mu.Unlock()

			// 覆盖水位与计划都在**门外**取好（见入口处）：门内只提交 ✗规划。
			if m.persistStepHold != nil {
				m.persistStepHold()
			}
			incomplete, err := m.commitPersist(ctx, plan)
			if err == nil && !incomplete {
				if mode.qualification != nil && plan.provesCommit(mode.qualification) {
					mode.qualification.confirmed = true
				}
				// 只有**完整落库**才推进覆盖水位：部分完成就宣称覆盖会静默丢更新。
				// 推进值 = 计划自带水位（构建前读）：复用场景下它可能小于本次声明号 ⇒ 不越推 ✓。
				for prev := m.persistCovered.Load(); plan.covered > prev; prev = m.persistCovered.Load() {
					if m.persistCovered.CompareAndSwap(prev, plan.covered) {
						break
					}
				}
			}

			g.mu.Lock()
			g.running = false
			g.err = err
			if mode.priority {
				g.priorityStreak++
			} else {
				g.priorityStreak = 0
			}
			g.cond.Broadcast()
			if m.persistGateStage != nil {
				m.persistGateStage(ctx, "release", seq, mode.priority)
			}
			if errors.Is(err, errPersistPlanStale) {
				// 旧计划整件拒写后释放门，重新规划不能挪回门内。
				g.mu.Unlock()
				plan, planErr = m.planPersist(ctx)
				g.mu.Lock()
				if planErr != nil {
					return planErr
				}
				g.enqueueLocked(seq, mode.priority)
				continue
			}
			if err != nil {
				return err
			}
			if !incomplete {
				return nil
			}
			if mode.maxWait > 0 {
				// 热路径最多一步：剩余差异交给后台通道（它允许阻塞），采集继续 ✓。
				m.notePersistHandoff(mode)
				m.requestPersist()
				if mode.failOnHandoff {
					return ErrPersistDeferred
				}
				return nil
			}
			if mode.oneStep {
				// 恢复/重放链：一步一让——剩余差异交给驱动链的下一趟（它们本来就让路）。
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// 本步有界但差异未完：重新排队（回到队尾）+ **显式让路**——睡满一个调度量级的窗口，
			// 让排队者（含采集轮）在窗口内把门拿走；本调用随后按队列顺序再取门。
			g.enqueueLocked(seq, mode.priority)
			yield := m.persistYieldOf()
			g.mu.Unlock()
			if yield > 0 {
				time.Sleep(yield)
			}
			g.mu.Lock()
			continue
		}
		if mode.maxWait > 0 {
			// 热路径：cond 不支持超时，用「短睡 + 重检」实现有界等待（上限 mode.maxWait ✓）。
			g.mu.Unlock()
			time.Sleep(time.Millisecond)
			g.mu.Lock()
			continue
		}
		g.cond.Wait()
	}
}

// persistPriorityStreakMax 是「连续授予优先队列」的上限（老化阈值）：达上限后强制让一个普通队首
// 通过一次，避免持续到达的采集轮把恢复/重放链饿死（反方向饥饿）。
//
// 默认 8：按「采集轮 250ms 一轮、恢复链每步 ≤3s」估算，8 次优先授予 ≈ 数秒内必然放行一次普通等待者，
// 恢复链仍能收敛；配 0 或负值表示**关闭老化**（仅用于回归里复刻「无反饿保护」的形态）。
func (m *Manager) persistPriorityStreakMax() int {
	if m == nil {
		return defaultPersistPriorityStreakMax
	}
	if m.persistPriorityStreak < 0 {
		// 负值 = **关闭老化**（仅用于回归复刻「无反饿保护」的形态；生产不这样配）。
		return 0
	}
	if m.persistPriorityStreak == 0 {
		return defaultPersistPriorityStreakMax
	}
	return m.persistPriorityStreak
}

// verifyRoundBudgetOf 返回生效的轮级校验预算。
func (m *Manager) verifyRoundBudgetOf() time.Duration {
	if m != nil && m.verifyRoundBudget > 0 {
		return m.verifyRoundBudget
	}
	return defaultVerifyRoundBudget
}

// VerifyDeferredCount 返回"因轮级预算而本轮放弃校验"的累计次数（观测面 ✓）。
func (m *Manager) VerifyDeferredCount() int64 {
	if m == nil {
		return 0
	}
	return m.verifyDeferred.Load()
}

// reconcileCandidateLockBudget 是「取对账候选等 m.mu」的默认墙钟上限。
const reconcileCandidateLockBudget = 2 * time.Second

// reconcileLockBudgetOf 返回生效的「取候选等 m.mu」有界上限（≤0 用默认常量）。
func (m *Manager) reconcileLockBudgetOf() time.Duration {
	if m != nil && m.reconcileLockBudget > 0 {
		return m.reconcileLockBudget
	}
	return reconcileCandidateLockBudget
}

// verifyVisibilityFloorOf 返回生效的"可见性错开"等待（≤0 = 关）。
func (m *Manager) verifyVisibilityFloorOf() time.Duration {
	if m != nil && m.verifyVisibilityFloor > 0 {
		return m.verifyVisibilityFloor
	}
	return 0
}

// persistYieldOf 返回生效的让路窗口：默认 max(persistStepYield, GOMAXPROCS × 5ms)。
//
// 为什么与 GOMAXPROCS 挂钩：「一次完整调度」的量级取决于并行度——核越多，被唤醒的等待者越多，
// 单个持门者越容易靠"回来得快"压制它们（现场 8 个等待者等 2+ 分钟）。
func (m *Manager) persistYieldOf() time.Duration {
	if m != nil && m.persistYield > 0 {
		return m.persistYield
	}
	window := time.Duration(runtime.GOMAXPROCS(0)) * 5 * time.Millisecond
	if window < persistStepYield {
		window = persistStepYield
	}
	return window
}

// persistGateEnsure 惰性初始化持久化门（兼容直接构造 Manager 的测试路径）。
func (m *Manager) persistGateEnsure() {
	g := &m.persistGate
	g.mu.Lock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	g.mu.Unlock()
}

// persistSnapshot 在**持持久化门（本轮唯一执行者）**的前提下执行一次完整持久化：m.mu 下
// 构建期望状态，释放 m.mu 后再落库（SQLite 事务不再阻塞登记的短临界区；顺序由门保证，见 persist）。
// persistSnapshotStep 执行**一步**持久化：构建期望状态（m.mu 下）→ 释放 m.mu → 一次有界落库
// （受每周期总预算约束）。返回 incomplete=true 表示差异尚未做完（镜像已按提交单元更新，
// 下一步重算即续跑）。
// persistPlan 是**门外的规划结果**：门内只允许做提交（ApplyScopedCtx）与水位推进 ✗规划。
//
// 为什么必须拆开（2026-10-03 现场 X 光片）：持门者在做 CPU 密集规划
// （persistSnapshotStep→planState→mirrorKey 复刻整源镜像行），于是 6 个投递/采集单元全部卡在
// sync.Cond.Wait、pollOnce 的 chan send 卡 4 分钟 ✗✗。铁律：**持门者只做提交、不做规划** ✓。
type persistPlan struct {
	store    *stateindex.Store
	desired  stateindex.State
	changed  []string
	advances []persistAdvance
	// covered 是**构建本计划前**读到的 persistJoin 水位：提交成功后覆盖水位最多推进到这里
	// （计划只保证覆盖"构建前声明的变更"；复用场景下它可能小于调用者声明号 ⇒ 绝不越推 ✓）。
	covered int64
	// snapshot 是实际快照顺序；较新快照一经接纳，整个旧计划都失效。
	snapshot  uint64
	committed map[string]*committedSourceReceipt
}

// 每份回执与生成真实计划的 Pipeline 实例绑定；err/Incomplete/未执行计划均不得发布。
type committedSourceReceipt struct {
	pipe       *pipeline.Pipeline
	identities map[acquire.CommitIdentity]struct{}
	err        error
}

// liveIdentities 返回当前 WAL 中仍存活（durable）的完整身份集合。
//
// 2026-10-29：原实现有“复用本计划已算身份”的轻量索引（WALCommitIndex/Snapshot）以免重复
// 编码正文——那套东西唯一目的是省 WAL 行的 SQL 编码，随 `source_wal` 表一起下线。
// 现在一律严格全量重算（与旧的 nil 索引回退路径逐字一致）。
func (receipt *committedSourceReceipt) liveIdentities() (map[acquire.CommitIdentity]struct{}, error) {
	return receipt.pipe.WAL().CommittedIdentities()
}

var errPersistPlanStale = errors.New("ingest: 持久化计划已被较新快照超越")

// persistAdvance 记录「本轮构建过的源」及其构建时的修订号/覆盖签名，**只在落库成功后才推进**。
//
// 为什么不能在构建期就推进（用户质疑 2，2026-10-02 复核）：一次落库失败（磁盘满 / 库被
// 换成只读 / 进程在 apply 之前被杀）会把这些源永久标记成「已持久化」——下一轮不再重建，
// 其陈旧行再也不会被重试，索引与内存就此静默分叉。现场形态正是「部分持久化残留的悬挂源
// 被当成已全量落库」：重启后该源按陈旧水位重读（重复投递），或整行缺失被当成新源
// （先前已投递的数据无人认账）。失败必须让水位**留在原处**，下一轮自动重试同一批源。
type persistAdvance struct {
	key   string
	rev   uint64
	cover coverSignature
}

// persistRootSnapshot 把根状态深拷贝定格，供门外规划使用。
func persistRootSnapshot(st persistedState) persistedState {
	out := persistedState{Sources: make(map[string]persistedSource, len(st.Sources)), SourceConfigs: make(map[string]SourceConfig, len(st.SourceConfigs)), Instances: make(map[string]InstanceBinding, len(st.Instances))}
	for key, saved := range st.Sources {
		saved.VerifiedRuns = append([]ledger.PositionRange(nil), saved.VerifiedRuns...)
		saved.IdentityConflicts = append([]identityConflictRecord(nil), saved.IdentityConflicts...)
		// 清空凭据与冲突记录同族：快照必须**深拷贝**。浅拷贝会让计划构建期间发生的清空
		// 直接改写已定格快照里的凭据切片（两者共享底层数组），使"计划描述的变更是哪一次"
		// 变得不可断言——这与既有 VerifiedRuns/IdentityConflicts 的拷贝理由完全同型。
		saved.IdentityConflictClearances = append([]identityConflictClearance(nil), saved.IdentityConflictClearances...)
		out.Sources[key] = saved
	}
	for key, value := range st.SourceConfigs {
		out.SourceConfigs[key] = value
	}
	for key, value := range st.Instances {
		out.Instances[key] = value
	}
	return out
}

func (m *Manager) planPersist(ctx context.Context) (*persistPlan, error) {
	// covered 是**构建本计划前**读到的持久化声明水位：提交成功后覆盖水位最多推进到这里
	// （计划只保证覆盖"构建前声明的变更"；复用路径下它可能小于调用者声明号 ⇒ 绝不越推 ✓）。
	covered := m.persistJoin.Load()
	if m.persistPlanHold != nil {
		// 测试钩子：模拟"长规划"。它必须在**门外**睡眠——变异成门内规划则采集/投递单元被卡 ✓。
		m.persistPlanHold()
	}
	m.mu.Lock()
	store := m.index
	if store == nil {
		// 直接构造 Manager 的测试路径：按需打开索引（不触发迁移）。
		opened, err := stateindex.OpenInMemory(m.indexPath(), m.indexCommit)
		if err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("ingest: 打开采集索引失败: %w", err)
		}
		store = opened
		m.index = store
	}
	if m.persistedRev == nil {
		m.persistedRev = make(map[string]uint64)
	}
	if m.persistedCover == nil {
		m.persistedCover = make(map[string]coverSignature)
	}
	m.persistSnapshotSerial++
	snapshot := m.persistSnapshotSerial
	planState := persistRootSnapshot(m.state)
	changed := make([]string, 0, len(m.pipes))
	// 源集合 = 管道（生产全部源都有管道）∪ 内存状态（测试可能直接构造只有状态的管理器）。
	// 只有状态的源没有账本修订号可查（管道才是权威），按「每轮都变更」处理，
	// 与旧的全量写入行为等价——这是测试路径的正确性底线，生产路径不经过这里。
	keys := make(map[string]struct{}, len(m.pipes)+len(m.state.Sources))
	for key := range m.pipes {
		keys[key] = struct{}{}
	}
	for key := range m.state.Sources {
		keys[key] = struct{}{}
	}
	// advanced 记录「本轮构建过的源」及其构建时的修订号/覆盖签名，**只在落库成功后才推进**。
	//
	// 为什么不能在构建期就推进（用户质疑 2，2026-10-02 复核）：一次落库失败（磁盘满 / 库被
	// 换成只读 / 进程在 apply 之前被杀）会把这些源永久标记成「已持久化」——下一轮不再重建，
	// 其陈旧行再也不会被重试，索引与内存就此静默分叉。现场形态正是「部分持久化残留的悬挂源
	// 被当成已全量落库」：重启后该源按陈旧水位重读（重复投递），或整行缺失被当成新源
	// （先前已投递的数据无人认账）。失败必须让水位**留在原处**，下一轮自动重试同一批源。
	advanced := make([]persistAdvance, 0, len(keys))
	// #55-B（父级裁定，三段式）：第一段锁内只做**守卫判定 + O(1) 输入快照**；重建重活
	//（Ledger 快照 / WAL 分片 / 内联正文拷贝）移到锁外；第三段锁内**复验守卫**后写回（防丢失更新）。
	rebuilds := make([]rebuildTask, 0, len(keys))
	committed := make(map[string]*committedSourceReceipt)
	for key := range keys {
		p := m.pipes[key]
		if p == nil {
			// 无管道：没有修订号可依据，按变更处理（重建该源的账本派生行）。
			changed = append(changed, key)
			continue
		}
		rev := p.Ledger().Revision(p.Key())
		saved := m.state.Sources[key]
		cover := coverSignatureOf(saved)
		if m.persistedRev[key] == rev && m.persistedCover[key] == cover {
			continue // 账本与段覆盖都未变：该源本轮不参与行规划（只写变更行的前提）
		}
		receipt := &committedSourceReceipt{pipe: p, identities: make(map[acquire.CommitIdentity]struct{})}
		committed[key] = receipt
		rebuilds = append(rebuilds, rebuildTask{
			key: key, pipe: p, rev: rev, cover: cover,
			eventsStored: saved.EventsStored, through: saved.EventsStoredThrough, inline: saved.Events,
			committed: receipt,
		})
		// 覆盖签名只取决于 EventsStored / EventsStoredThrough，而重建**原样保留**这两个字段
		//（见 commitRebuiltSource）⇒ 计划时读到与重建后读到逐字相同，故记账可与守卫同临界区。
		advanced = append(advanced, persistAdvance{key: key, rev: rev, cover: cover})
		changed = append(changed, key)
	}
	// 第二段（锁外）：逐源构建重建体。现场此处是 m.mu 内除逐条 marshal 之外最大的整块耗时之一
	//（Ledger 快照逐条 clone + WAL 分片的内联正文拷贝，逐条镜像重建阶段达 260–632ms）。
	m.mu.Unlock()
	for i := range rebuilds {
		rebuilds[i].rebuilt = m.buildRebuiltSourceOutsideLock(rebuilds[i])
		if rebuilds[i].committed.err != nil {
			return nil, rebuilds[i].committed.err
		}
	}
	m.mu.Lock()
	// 第三段（锁内）：**复验守卫**后写回。锁外算重建体期间采集轮照常推进：账本修订号或段覆盖
	// 若已变化，直接写回就会用**计划时的旧水位**覆盖新水位（丢失更新）⇒ 整件丢弃、留待下一轮
	//（守卫在下轮必然重新命中；无需重试环）。丢弃不改变水位记账：advanced 记的是计划时的
	// rev/cover，若它们已然陈旧 ⇒ 下轮守卫必命中 ⇒ 自愈 ✓。
	for i := range rebuilds {
		t := rebuilds[i]
		p := m.pipes[t.key]
		if p == nil || p != t.pipe {
			continue
		}
		// 计划私有副本使用实际重建体；实时 revision 变化只阻止写回共享镜像，不吞掉已取得的快照。
		private := planState.Sources[t.key]
		private.Ledger = t.rebuilt.Ledger
		planState.Sources[t.key] = private
		if p.Ledger().Revision(p.Key()) != t.rev || coverSignatureOf(m.state.Sources[t.key]) != t.cover {
			continue
		}
		m.commitRebuiltSource(t.key, t.rebuilt)
	}
	// 增量状态：账本派生行只按变更源构建，根行恒为全量（行数为 O(源数)，便宜且兜住
	// 元数据变更）；配合 ApplyScoped 让未变更源的账本派生行根本不参与规划。
	//
	// 2026-10-29：WAL 不再落盘（source_wal 表已删）⇒ 原「①水位化差异删除提示」与
	// 「A-Step2 锁外构建 WAL 行」整条链一并下线。现在锁内只做账本派生行 + 根行的规划，
	// 没有锁外补建的步骤。
	desired, err := m.stateToIndexStateScoped(&planState, changed)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	// 规划完成：释放 m.mu 并返回计划。**规划到此结束**（门外）✓；提交由持门者 commitPersist ✓。
	m.mu.Unlock()
	return &persistPlan{store: store, desired: desired, changed: changed, advances: advanced, covered: covered, snapshot: snapshot, committed: committed}, nil
}

// planPersistReuse 是热路径的"规划合并"门面（**只供 persistMode.planReuse**，见其注释）。
//
// 背景（2026-10-03 回退定位）：f237ae32 把规划移出持久化门后，**每个未被 covered 短路的
// 调用者都各自跑一遍 planPersist**（持 m.mu 遍历全源 + 重建变更源镜像）——60 源并发下
// 每轮 N 倍冗余规划 + m.mu 争抢，实测吞吐 3.3k→1.5k 行/s、verify 98% 停摆。
//
// 合并判据（正确性）：**只复用"构建于本调用声明之后"的计划** —— 计划自带 plan.covered
// （构建前读到的 persistJoin 水位），`plan.covered >= seq` 意味着规划发生在本次声明之后，
// 而本次的状态变更完成于声明之前 ⇒ 该计划必然已包含本次变更 ✓（不漏写）。
// 等待期间（persistPlanMu 上排队）由先到的调用者完成规划，其 covered 覆盖排队者的声明号
// ⇒ 排队者醒来即复用 —— 与旧实现"持门者的规划覆盖排队者"同效，但规划仍在**门外** ✓。
//
// 本函数不改变门内提交的原子边界（门内仍一份计划/一次落盘）✓，不依赖任何调用侧串行 ✗。
func (m *Manager) planPersistReuse(ctx context.Context, seq int64) (*persistPlan, error) {
	m.persistPlanMu.Lock()
	defer m.persistPlanMu.Unlock()
	if m.persistPlan != nil && m.persistPlan.covered >= seq && m.persistPlan.snapshot >= m.persistAccepted.Load() {
		m.persistPlanReuses.Add(1)
		return m.persistPlan, nil
	}
	plan, err := m.planPersist(ctx)
	if err != nil {
		return nil, err
	}
	m.persistPlan = plan
	m.persistPlanAt = time.Now()
	m.persistPlanBuilds.Add(1)
	return plan, nil
}

// PersistPlanStats 导出"规划次数/复用次数"（复验指标：builds/轮 ≈1；只增不减的累计值）。
func (m *Manager) PersistPlanStats() (builds, reuses int64) {
	if m == nil {
		return 0, 0
	}
	return m.persistPlanBuilds.Load(), m.persistPlanReuses.Load()
}

// commitPersist 在**门内**提交一份计划：只做提交与水位推进，不做任何规划 ✗。
//
// 新鲜度过滤（为什么必须有）：规划发生在门外 ⇒ 同一源可能已被**更新的计划**先提交 ✗。
// 直接套用旧计划会写回陈旧行；更糟的是若把 persistedRev 往回写，陈旧行就**永远不会**被重建 ✗✗。
// 因此提交前用 O(1) 的修订号/覆盖签名复读一遍：只提交**仍然新鲜**的源，其余丢弃（水位不推进
// ⇒ 下一轮自然重建）✓。这是廉价一致性校验，不是规划 ✓（不含镜像重建/状态转换）。
func (m *Manager) commitPersist(ctx context.Context, plan *persistPlan) (bool, error) {
	if plan == nil || plan.store == nil {
		return false, nil
	}
	m.persistCommitMu.Lock()
	defer m.persistCommitMu.Unlock()
	if plan.snapshot < m.persistAccepted.Load() {
		return false, errPersistPlanStale
	}
	// 部分提交和失败同样建立门槛，旧根行及剪枝绝不允许穿过。
	m.persistAccepted.Store(plan.snapshot)
	// 提交**全部**构建过的源（不清空提交 ✗）：清空会在"状态持续变更"时让每次提交都被丢弃 ⇒ 饿死 ✗
	// （实测：把过滤当成"提交条件"会同时打断 delivery_batch 剪枝与默认值恢复用例）。
	// 只按新鲜度决定**是否推进水位**：不新鲜的源水位留在原处 ⇒ 下一轮必然重建并重写它
	// （即使本轮写进了略旧的行，也会被下一轮覆盖）✓ —— 收敛性不依赖"本轮是否恰好赶上" ✓。
	m.mu.Lock()
	advances := make([]persistAdvance, 0, len(plan.advances))
	for _, a := range plan.advances {
		pp := m.pipes[a.key]
		if pp == nil {
			continue
		}
		if pp.Ledger().Revision(pp.Key()) != a.rev || coverSignatureOf(m.state.Sources[a.key]) != a.cover {
			continue // 规划后被更新：写行但不推进水位 ⇒ 下一轮重建 ✓（绝不把陈旧行标成已落库 ✗）
		}
		advances = append(advances, a)
	}
	m.mu.Unlock()
	stats, err := plan.store.ApplyScopedCtx(ctx, plan.desired, plan.changed)
	if err == nil {
		// 观测面：记录本次读数（含分步未完成标志），供排障与回归取证。
		m.mu.Lock()
		m.lastPersistStats = stats
		m.mu.Unlock()
	}
	if err != nil {
		// 不推进水位：本轮构建的源在下一轮会被重新构建并重试落库，悬挂源不会静默滞留。
		return false, fmt.Errorf("ingest: 持久化采集索引失败（水位未推进，下一轮自动重试）: %w", err)
	}
	if stats.Incomplete {
		// 本步受预算约束提前返回：**不推进水位**，下一步重建同一批源并续做剩余差异
		// （镜像已按提交单元更新 ⇒ 重算结果只剩未处理的行）。
		return true, nil
	}
	completed := make(map[string]*committedSourceReceipt, len(plan.committed))
	for key, receipt := range plan.committed {
		if receipt.err != nil {
			return false, receipt.err
		}
		live, liveErr := receipt.liveIdentities()
		if liveErr != nil {
			return false, liveErr
		}
		kept := &committedSourceReceipt{pipe: receipt.pipe, identities: make(map[acquire.CommitIdentity]struct{})}
		for id := range receipt.identities {
			if _, ok := live[id]; ok {
				kept.identities[id] = struct{}{}
			}
		}
		completed[key] = kept
	}
	// 完整落库后才推进水位。需要重新取锁：规划期已释放 m.mu，期间可能有新的变更被声明；
	// 即便账本在这段时间里又前进，这里记下的是**更旧**的修订号，下一轮会因不等而重建（安全方向）。
	m.mu.Lock()
	for _, a := range advances {
		m.persistedRev[a.key] = a.rev
		m.persistedCover[a.key] = a.cover
	}
	if m.committedReceipts == nil {
		m.committedReceipts = make(map[string]*committedSourceReceipt)
	}
	for key, receipt := range completed {
		if m.pipes[key] == receipt.pipe {
			m.committedReceipts[key] = receipt
		}
	}
	for key, receipt := range m.committedReceipts {
		if m.pipes[key] != receipt.pipe {
			delete(m.committedReceipts, key)
		}
	}
	m.mu.Unlock()
	return false, nil
}

// persistSnapshotStep 是「规划 + 提交」的兼容壳（供已在门外的调用方与测试使用）。
//
// 调用方若**持门**，必须改调 planPersist（门外）+ commitPersist（门内）✗ 否则违反铁律 ✗。
func (m *Manager) persistSnapshotStep(ctx context.Context) (bool, error) {
	plan, err := m.planPersist(ctx)
	if err != nil {
		return false, err
	}
	return m.commitPersist(ctx, plan)
}

// LastPersistStats 返回最近一次落库的读数（含谓词范围删除是否启用、是否分步未完成）。
func (m *Manager) LastPersistStats() stateindex.Stats {
	if m == nil {
		return stateindex.Stats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := m.lastPersistStats
	stats.Written = cloneCountMap(m.lastPersistStats.Written)
	stats.Deleted = cloneCountMap(m.lastPersistStats.Deleted)
	stats.Planned = cloneCountMap(m.lastPersistStats.Planned)
	return stats
}

func cloneCountMap(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// coverSignature 是「WAL 以何种形态落库」的判据（B1a 按条切分的内联/引用决策输入）。
type coverSignature struct {
	eventsStored bool
	through      uint64
}

func coverSignatureOf(saved persistedSource) coverSignature {
	return coverSignature{eventsStored: saved.EventsStored, through: saved.EventsStoredThrough}
}

// rebuildTask 是一件「锁外重建」任务（#55-B）：
//   - 锁内产出：O(1) 输入快照（账本修订号 / 覆盖签名 / 段覆盖水位 / 内联正文切片头）
//     —— `inline` 是**切片头值拷贝**，其底层数组只被整体置 nil 或重建为新切片、
//     从不原地改写元素（见 appendEvents / appendEventTail / migrateInlineEvents）⇒ 锁外只读安全；
//   - 锁外产出：rebuilt（重建体）；
//   - 第三段复验 rev/cover/pipe 身份后写回（复验不过 ⇒ 丢弃留待下轮）。
type rebuildTask struct {
	key   string
	pipe  *pipeline.Pipeline
	rev   uint64
	cover coverSignature

	eventsStored bool
	through      uint64
	inline       []logtypes.Event

	rebuilt   persistedSource
	committed *committedSourceReceipt
}

// buildRebuiltSourceOutsideLock 在**锁外**构建一个源的重建体（#55-B，从原 rebuildSource 拆出）。
//
// 现场：这段是 m.mu 内最大的一块耗时（Ledger().Snapshot() 逐条 clone + WAL 快照），
// 逐条镜像重建阶段达 260–632ms ⇒ 整段持锁时，投递/登记/停止/解算全部一起排队。
//
// 锁外安全性：Ledger.Snapshot / WAL.Snapshot / WAL.CommittedIdentities 各自持自己的锁
// （ledger.go:599 `l.mu.RLock`、wal.go `w.mu.Lock`）且产物都是**全新切片**；本函数不触碰
// m.state / m.pipes。
//
// 2026-10-29：WAL 不再落盘（`source_wal` 表已删），因此这里不再做「内联/引用分流」与「SQL 行
// 字节编码」——只剩三件仍需要的事：账本快照、WAL 内存镜像（供 canonical 重建补全）、
// 本计划捕获的已提交身份（供投递前的提交资格验证）。
func (m *Manager) buildRebuiltSourceOutsideLock(t rebuildTask) persistedSource {
	if hook, _ := m.rebuildSourceBuildHook.Load().(func()); hook != nil {
		hook()
	}
	entry := persistedSource{
		Ledger:              t.pipe.Ledger().Snapshot(),
		EventsStored:        t.eventsStored,
		EventsStoredThrough: t.through,
		// WAL 镜像只是"本进程内还排队的正文"的副本，不落盘。
		WAL: t.pipe.WAL().Snapshot(),
	}
	// 事件体已在磁盘段时不写回内联切片——这正是原先 state 涨到 180MB、每次 persist
	// 触发 130MB MarshalIndent 的根源（FR-484 阶段0 量测）。
	if !entry.EventsStored {
		entry.Events = append(entry.Events, t.inline...)
	}
	if t.committed != nil {
		// 提交资格：本计划捕获时仍为 durable 的完整身份集合（内存态，不碰磁盘）。
		identities, err := t.pipe.WAL().CommittedIdentities()
		t.committed.err = err
		t.committed.identities = identities
	}
	return entry
}

// commitRebuiltSource 把锁外算好的重建体写回 state（**调用方必须持 m.mu**，且已复验守卫）。
//
// 保留字段（投影代次 / 待发布 / 逐字段凭据）取**提交时**的现值：它们由别的路径维护
// （publish / recordVerifiedRuns），用提交时的读数比用计划时的读数**减少**丢失更新的窗口；
// 重建体自身的字段（账本 / WAL / 覆盖水位）仍来自计划时的 O(1) 快照，与改前逐字段一致。
//
// VerifiedRuns 必须原样保留（既有事故）：它由投递路径写入（见 recordVerifiedRuns），
// 重建时若清空，每次 persist 都会把刚登记的区间级凭据抹掉 —— 那会使
// autoResolveGapsFromPublished 永远拿不到凭据而**静默失效**。
func (m *Manager) commitRebuiltSource(key string, rebuilt persistedSource) {
	cur := m.state.Sources[key]
	cur.Ledger = rebuilt.Ledger
	cur.WAL = rebuilt.WAL
	cur.EventsStored = rebuilt.EventsStored
	cur.EventsStoredThrough = rebuilt.EventsStoredThrough
	cur.Events = rebuilt.Events
	// ProjectionGeneration / PublicationPending / VerifiedRuns 原样保留（cur 的现值）。
	m.state.Sources[key] = cur
}

func newWAL(led *ledger.Ledger, key ledger.SourceKey) *acquire.WAL {
	return acquire.NewWAL(led, key)
}

// multilineUnclosedTimeoutOf 归一化未闭合超时配置。
//
// 口径：0 ⇒ 默认（normalize.DefaultUnclosedTimeout，5s，零配置零行为变化）；
// 负值 ⇒ 关闭（返回 0，调用方据此跳过强制冲刷）。为什么不把 0 也当成「关闭」：
// 0 是 Go 的零值，无法与「未配置」区分，而「未配置」必须是安全默认（会冲刷）。
// 要关闭请显式写一个负值（配置侧 0 与负值都映射为关闭，见 Config.IngestMultilineUnclosedTimeout）。
func multilineUnclosedTimeoutOf(v time.Duration) time.Duration {
	if v < 0 {
		return 0
	}
	if v == 0 {
		return normalize.DefaultUnclosedTimeout
	}
	return v
}

// ErrVerifyDeferred 是"本轮预算用尽、校验延后到下一轮"的**可分类哨兵**（用户 2026-10-05 ④ ✓）。
//
// 为什么需要：它是**设计内的正常让路**（同源 INFO 往往显示投影已写入 status=200/err=nil ✓），
// 但此前只以裸文本返回 ⇒ 调用方无从分类 ⇒ 被逐级打成 **ERROR** ✗（现场 `inst:142` 的分级误报 ✓）。
// 有哨兵后，调用方 `errors.Is(err, ErrVerifyDeferred)` 即可降级为 WARN/INFO ✓ —— 语义零变化 ✓。
var ErrVerifyDeferred = errors.New("verification deferred to next round")

// errProjectionVerifyFailed 标记「本次已把数据写进 VL，但逐字段可见性校验未通过」。
//
// 为什么需要这个显式标记（2026-10-09 生产循环的止血点）：
// 该形态是「写完 → 校验不过 → 换新代次再写一遍」的**自锁循环**——现场 `inst:220/file` 的同一份
// 573562 事件被写成 ≥70 个代次（每轮 ~1 小时），VL 累积 281GB、并把 40 个源的采集挤死 13 小时。
// 这类失败**重试不会自愈**（数据本身有问题，或该代次永远凑不齐），故重建调度器必须把它判为
// **确定性失败**并按上限放弃自动重试；而「VL 未就绪 / 网络抖动」这类外部失败必须继续重试。
//
// **为什么不靠错误串匹配**：该形态的错误文案恰是 `... not fully visible before deadline:
// context deadline exceeded`（超时类），既有 `verifyErrorIsSemantic` **刻意**把它判为非语义
// （那是为了「不取消同批其余簇」的正确取舍，不能改）；而 `errors.Is(err, context.DeadlineExceeded)`
// 又会把它归到"超时类"。用**写入已成功**这个事实做判据（在返回路径上显式包装）比匹配文案更稳，
// 也不会随文案演进静默失效（静默失效的后果就是循环重建复发）。
var errProjectionVerifyFailed = errors.New("projection verify failed after write")

// defaultSweepStepBudget 是兜底环**单源单步**的有界等待预算（0 ⇒ 默认 150ms ✓）。
//
// 为什么必须有（2026-10-05 现场 X 光片 ✓）：环第一轮卡在 `DeliverPending→deliver` 的 `m.mu`
// 上 ⇒ `ticks=1` 冻结、`candidates` 恒定 ⇒ 外部看起来像"没扫到"，其实是"**第一轮就挂住**" ✗✗。
// 维护环的职责是**周期性巡检**，不是等待重活完成 ⇒ 单步必须能被放弃 ✓。
const defaultSweepStepBudget = 150 * time.Millisecond

// defaultSweepInflightCap 是在飞被放弃调用的**并发上限**（0 ⇒ 默认 12 ✓）。
//
// 为什么需要上限（新账 2 ✓）：超时只放弃"等"、不放弃"做" ✗ ⇒ 每个被放弃的调用仍持有载荷在后台
//
//	等 `m.mu`。上限把"同时在飞的这类调用"钉住 ⇒ 内存不再随轮数叠加 ✓（超出即本轮不再发起新步 ✓）。
//
// 2026-10-06 #6 复盘（父令定点）：原默认 4 ⇒ 95 源规模下 `inflight` 长期贴 1–4、`skip_inflight_cap`
// 单调增（实测 0→363）、单轮 300 s+、排水停摆（WAL 760 020 悬空、read 900 000 vs reclaim 0）✗。
// 提到 12：只放开**跨源**并发；**同源仍严格单飞**（tryClaimReplayInflight 逐源互斥 ✗）；
// 有界执行器语义不变（超预算只不等待、后台继续 ✓）。
const defaultSweepInflightCap = 12

func (m *Manager) sweepInflightCapOf() int {
	if m == nil || m.sweepInflightCap <= 0 {
		return defaultSweepInflightCap
	}
	return m.sweepInflightCap
}

func (m *Manager) sweepStepBudgetOf() time.Duration {
	if m == nil || m.sweepStepBudget <= 0 {
		return defaultSweepStepBudget
	}
	return m.sweepStepBudget
}

// runSweepStepBounded 执行一步；**超预算即返回 false**（不等它 ✓）。被放弃的那次调用会在后台自行
// 完成（`m.mu` 释放后即返回 ✓）——因此"跳过"只表示"本轮不等它"，**不丢工作** ✓。
func (m *Manager) runSweepStepBounded(budget time.Duration, fn func()) bool {
	// 读数与占额必须是同一次 CAS；超时只停止等待，真实后台完成才归还。
	limit := int64(m.sweepInflightCapOf())
	current := m.sweepInflight.Load()
	if m.sweepAdmissionHook != nil {
		m.sweepAdmissionHook()
	}
	for {
		if current >= limit {
			m.skipInflightCap.Add(1)
			return false
		}
		if m.sweepInflight.CompareAndSwap(current, current+1) {
			break
		}
		current = m.sweepInflight.Load()
	}
	if !m.beginWork() {
		m.sweepInflight.Add(-1)
		return false
	}
	done := make(chan struct{})
	go func() {
		defer func() { m.sweepInflight.Add(-1); close(done); m.workWG.Done() }() // 在飞数随调用真正结束而减 ✓
		// 注入点必须在**有界区之内**（2026-10-05 实测教训 ✗✓）：放在 goroutine 之外会把**调用者**
		// 自己按住 ⇒ 预算再紧也救不了 ⇒ 红证会测出"钩子位置错"而不是"有界失效" ✓。
		if m.sweepStepHold != nil {
			m.sweepStepHold()
		}
		fn()
	}()
	select {
	case <-done:
		return true
	case <-time.After(budget):
		// 放弃**等待**，但调用**仍在飞** ⇒ 在飞计数要到它真正结束才回落 ✓（这是定界的关键 ✓）。
		return false
	}
}

// startupRecoveryFailureLevel 决定"启动恢复：源恢复失败"这条日志的**级别**（用户 2026-10-05 ✓）。
//
// 为什么要抽成函数（可直接测 ✓）：延后是**设计内的正常让路**（`ErrVerifyDeferred` ✓），真失败不是 ✗；
// 抽出来后用例可以断言两侧（延期 ⇒ WARN ✓ / 真失败 ⇒ ERROR ✓），而不再依赖"读源码 + 人眼判" ✓。
//
// **纪律第 8 条（推广版，用户 2026-10-05 ✓）**：凡新增导出/哨兵，必须**同批带【消费点 + 可达性红证】**
// ✗ —— 本次的"哨兵造好没人用"正是它的反面教材 ✓（`ErrVerifyDeferred` 导出后，此处才是它的第一个消费点 ✓）。
func startupRecoveryFailureLevel(err error) (slog.Level, string) {
	if errors.Is(err, ErrVerifyDeferred) {
		return slog.LevelWarn, "启动恢复：源校验延后（设计内让路，下轮重试）"
	}
	return slog.LevelError, "启动恢复：源恢复失败（其余源照常继续）"
}
