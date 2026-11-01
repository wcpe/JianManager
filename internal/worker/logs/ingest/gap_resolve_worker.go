package ingest

// 缺口消解异步 worker 的运行体与 Manager 接入（2026-11-02）。
//
// 调度器本体见 gap_resolve_async.go；本文件负责：
//   1. 后台 worker 循环（唤醒 / 定时到期 / 关停）；
//   2. Manager 上的登记入口 `scheduleGapResolve`；
//   3. 执行体 —— **直接调用一字未改的 `resolveGapsLandedByDelivery`**。

import (
	"context"
	"time"

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
// **关键**：这里调用的是**一字未改**的 `resolveGapsLandedByDelivery`，
// 所以「区间必须落在某一段逐字段校验通过的写入区间内」这个判定语义完全不变。
// 异步化只改变了「何时调」。
func (m *Manager) executeGapResolve(parent context.Context, req *gapResolveRequest) {
	s := m.gapResolves
	defer func() {
		// 无论成败都释放单飞位，并检查运行期间是否有新事件被合并进来。
		s.mu.Lock()
		var requeue *gapResolveRequest
		if pending := s.pending[req.key]; pending != nil {
			// 运行期间登记的新事件：要再跑一轮（否则这段增量就丢了）。
			requeue = pending
			delete(s.pending, req.key)
		}
		s.doneLocked(req.key, requeue)
		s.mu.Unlock()
	}()

	// 独立预算：不复用 3s 的退避探测预算（那个是为“探测点”设计的，切片后必然不够）。
	ctx, cancel := context.WithTimeout(parent, gapResolveBudget)
	defer cancel()

	m.resolveGapsLandedByDeliveryCtx(ctx, req.source, req.events)
	s.done.Add(1)
}
