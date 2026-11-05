package ingest

// 缺口消解异步 worker 的运行体与 Manager 接入（2026-11-02）。
//
// 调度器本体见 gap_resolve_async.go；本文件负责：
//   1. 后台 worker 循环（唤醒 / 定时到期 / 关停）；
//   2. Manager 上的登记入口 `scheduleGapResolve`；
//   3. 执行体（判定语义未改，只在“连续多轮查不到”时多一个**有界降级出口**，
//      见 delivery_visibility.go）。

import (
	"context"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// scheduleGapResolve 是异步登记入口（替代原先的同步 `resolveGapsLandedByDelivery` 调用）。
//
// 它只在投递路径上做 **O(1) 的 map 写入 + 非阻塞唤醒**，不做任何网络往返、
// 不取 `m.mu`——这是「不阻塞投递确认与 WAL 回收链」的形式保证。
func (m *Manager) scheduleGapResolve(source SourceConfig, events []logtypes.Event) {
	if m == nil || len(events) == 0 {
		return
	}
	s := m.gapResolves
	if s == nil {
		// 未装配调度器（测试/旧路径）：退回同步，行为与历史一致。
		m.resolveGapsLandedByDelivery(source, events)
		return
	}
	// 惰性启动 worker（2026-11-02）：不能假定调用方一定调了 Start()。
	//
	// 为何必须有：`newTestManager` 只调 `New()` 不调 `Start()`（测试很常见）。
	// 若只靠 Start 启 worker，登记的任务就永远积压——那是个**静默失效**：
	// 缺口永不消解，但没有任何报错。惰性启动把这个口子堵上。
	m.ensureGapResolveWorker()
	key := source.LogSourceID + "/" + source.SourceGeneration
	s.schedule(&gapResolveRequest{
		key: key, source: source, events: events, due: time.Now(),
	})
}

// ensureGapResolveWorker 保证后台 worker 已启动（幂等）。
//
// 用 `sync.Once` 而不是布尔位：并发登记时多个调用同时看到"未启动"会重复 `go`，
// 造成多个 worker 抢同一批任务（单飞 map 会兑付，但白多跑）。
func (m *Manager) ensureGapResolveWorker() {
	m.gapWorkerOnce.Do(func() {
		ctx := m.lifecycleContext()
		if ctx == nil {
			ctx = context.Background()
		}
		if !m.beginWork() {
			return
		}
		go m.runGapResolveWorker(ctx)
	})
}

// GapResolveStats 返回异步缺口消解的观测快照（排障与回归用）。
func (m *Manager) GapResolveStats() GapResolveStats {
	if m == nil || m.gapResolves == nil {
		return GapResolveStats{}
	}
	return m.gapResolves.stats()
}

// runGapResolveWorker 是后台 worker。它不阻塞任何调用方，也不持有 m.mu。
func (m *Manager) runGapResolveWorker(ctx context.Context) {
	s := m.gapResolves
	if s == nil {
		return
	}
	defer m.workWG.Done()

	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		req := s.pickLocked(time.Now())
		var waitUntil time.Time
		var hasDue bool
		if req == nil {
			waitUntil, hasDue = s.nextDueLocked()
		}
		s.mu.Unlock()

		if req == nil {
			timer := time.NewTimer(gapResolveIdleTick)
			if hasDue {
				if d := time.Until(waitUntil); d < gapResolveIdleTick {
					timer.Reset(d)
				}
			}
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.wake:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}

		s.running.Add(1)
		m.executeGapResolve(ctx, req)
	}
}

// gapResolveIdleTick 是无到期任务时的最长休眠（保证不忙等，也能及时看到新登记）。
const gapResolveIdleTick = time.Second

// executeGapResolve 执行一次异步核对。
//
// **关键**：判定语义一字未改（`resolveGapsLandedByDeliveryReport` 即旧
// `resolveGapsLandedByDeliveryScope` 的带状态版本）。异步化只改变了「何时调」；
// 返回值只改变了「下次还要不要重核这批事件」。
func (m *Manager) executeGapResolve(parent context.Context, req *gapResolveRequest) {
	s := m.gapResolves
	// unresolved / probeOK 在 defer 里被读：声明必须在 defer 之前。
	var unresolved []logtypes.Event
	// probeOK 报告本轮**是否真的问过 VL**（见 resolveGapsLandedByDeliveryReport）。
	// 它决定「连续不可见」的计数要不要 +1：查询失败时什么都不知道，不得计入。
	probeOK := false
	defer func() {
		// ── 有界重试 → 降级（2026-11-05，审计 P0-1）──
		//
		// 这条链一直只做了「查不到就留在待办里下一轮再问」，而那是**无界**的：
		// VL 永久拒收（retention 超限 / 字段数超限）时待办只增不减，
		// 直到 `maxGapResolvePendingSources` 把新登记一律拒掉（然后再也不会好转）。
		// 本段补上那个缺失的出口。
		//
		// 判据为什么是「轮次 + 墙钟」两条同时成立，见 delivery_visibility.go 的常量注释；
		// 为什么「查询失败」不计轮次，见 landedVisibleInVLCtxStatus 的注释。
		missRounds, firstMissAt := req.missRounds, req.firstMissAt
		now := time.Now()
		if probeOK && len(unresolved) > 0 {
			missRounds++
			if firstMissAt.IsZero() {
				firstMissAt = now
			}
		}
		missWindow := time.Duration(0)
		if !firstMissAt.IsZero() {
			missWindow = now.Sub(firstMissAt)
		}
		degraded := probeOK && len(unresolved) > 0 &&
			missRounds >= maxGapResolveMissRounds && missWindow >= gapResolveMissWindow
		if degraded {
			// 降级是在 `s.mu` **之外**做的：`recordVisibleDeliveryMiss` 要写账本（取账本锁），
			// 持调度器锁做账本 IO 会让所有源的登记在这段时间里排队。
			key := req.source.LogSourceID + "/" + req.source.SourceGeneration
			var led *ledger.Ledger
			if pipe, ok := m.pipeFor(key); ok && pipe != nil {
				led = pipe.Ledger()
			}
			err := m.recordVisibleDeliveryMiss(req.source, led, sourceKeyOf(key), unresolved, deliveryMissEvidence{
				Missing: len(unresolved), Total: len(req.events),
				Rounds: missRounds, Window: missWindow,
			})
			if err != nil {
				// 记账失败 ⇒ **不能**丢弃待办：那会让该段数据既有缺失、又没有任何痕迹
				// （比不降级更差）。改为再等一轮重试，并让下一轮不满足「已降级」条件。
				slog.Warn("可见性降级登记失败，保留待办下轮重试（不丢弃未登记的事件）",
					"source", req.source.LogSourceID, "events", len(unresolved), "error", err)
				degraded = false
			}
		}
		if degraded {
			// 已落账：这批归入「确定不可见」，从待办里撒掉（不再计入 missRounds）。
			unresolved = nil
		}

		s.mu.Lock()
		// 把**本次仍未确认可见**的事件留在待办里重试（而不是整批丢弃）。
		//
		// 为什么必须这样（2026-11-02 真机缺陷修复）：此前这里把整批事件用完就丢，
		// 于是同源下次登记时 `mergeGapResolveEvents` 把新事件与旧待办做并集 ——
		// 待办规模随运行时间**线性增长**（60 源实测 361 → 5420），
		// 一次探测因此被切成 30+ 片，任何预算都超时 ⇒ 缺口永不消解（正反馈）。
		//
		// 改成「只留未消解的」后，待办规模由**真实未决量**定界，不再随时长增长：
		// 已确认可见的事件由 `recordVerifiedRuns` 持久化成区间凭据，不需要再核。
		var requeue *gapResolveRequest
		if pending := s.pending[req.key]; pending != nil {
			// 运行期间登记的新事件：与本次未消解的合并（去重）。
			// 如果刚才降级了（unresolved 已置 nil），新登记的事件**不带旧计数** ——
			// 它们是新的一批，不能被上一批的 miss 历史连坐。
			r := &gapResolveRequest{
				key:         req.key,
				source:      req.source,
				events:      mergeGapResolveEvents(unresolved, pending.events),
				due:         pending.due,
				missRounds:  pending.missRounds,
				firstMissAt: pending.firstMissAt,
			}
			if !degraded {
				// 计数归本次轮次（含运行期间合并进来的那批），不让降级后的新批被旧账连坐。
				r.missRounds, r.firstMissAt = missRounds, firstMissAt
			}
			requeue = r
			delete(s.pending, req.key)
		} else if len(unresolved) > 0 {
			// 没有新登记，但本次仍有未确认可见的事件 ⇒ 退避后重试（带着本轮计数）。
			requeue = &gapResolveRequest{
				key:         req.key,
				source:      req.source,
				events:      unresolved,
				due:         time.Now().Add(gapResolveRetryBackoff),
				missRounds:  missRounds,
				firstMissAt: firstMissAt,
			}
		}
		s.doneLocked(req.key, requeue)
		s.mu.Unlock()
	}()

	// 独立预算：不复用 3s 的退避探测预算（那个是为“探测点”设计的，切片后必然不够）。
	ctx, cancel := context.WithTimeout(parent, gapResolveBudget)
	defer cancel()

	unresolved, probeOK = m.resolveGapsLandedByDeliveryReport(ctx, req.source, req.events)
	s.done.Add(1)
}
