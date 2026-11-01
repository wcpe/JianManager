package ingest

// 缺口消解的**异步调度器**（2026-11-02）。
//
// ## 为何要异步
//
// `resolveGapsLandedByDelivery` 内部要调 `landedVisibleInVLCtx` 核对 VL 可见性，
// 而后者的超时是 `verifyBackoffMax*4+1s = 3s`——那是为**退避探测**（0/0.2/0.6/1.1s 四个探测点）
// 设计的，用来压制 2026-09-28「固定轮询到 30 秒超时」的事故。
//
// 但 12KB selector 上限把大批切成 180 条/片（长稳实测 `events=180` 占 15015 次），
// 退避预算被**三次串行 HTTP 往返**吃光 ⇒ 必然 deadline exceeded ⇒ 本轮不消解
// ⇒ WAL 无法回收 ⇒ 积压到上限丢最旧 ⇒ 产生更多缺口 ⇒ **正反馈**。
//
// 长稳第 1 轮实测：1009 次核对超时、599 次丢弃、RSS 4 → 693 MiB。
//
// ## 设计（照 rebuildScheduler 的成熟模式）
//
//   - `wake` 容量 1：多次登记合并成一次唤醒；
//   - `pending` 按源去重：同源多次投递 ⇒ 事件**合并**进同一条待办（而非排 N 份）；
//   - `inflight` 单飞：同源同时至多一个核对任务；
//   - `closed` 关停门：之后不再登记；
//   - 有界：`maxGapResolvePendingSources` 上限，**超限不静默丢**（计数+告警）。
//
// ## 语义红线（未变）
//
// 异步化只挪动「何时查」，**不改变「怎么判」**：
//   - `resolveGapsLandedByDelivery` 一字未改；
//   - `landedVisibleInVL` 的「查不到就不作证据」方向不变；
//   - 绝不在查不到时「先消解、后补证」。

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

const (
	// maxGapResolvePendingSources 是待办源数上限。
	//
	// 为何必须有界：投递成功就登记一次，900 行/s 下登记速率极高。
	// 源数本身有天然上界（本机 60），但这里仍设硬上限：任务说「源数量本身不设上限」，
	// 实践中不能假定，所以超限时**不静默丢**——计数进观测面并告警。
	maxGapResolvePendingSources = 256
	// gapResolveBudget 是单个异步核对任务的超时预算（独立于 3s 的退避预算）。
	//
	// 为何 30s：三次串行 HTTP 往返（512 条切 3 片）× 每片数秒 + VL 索引延迟，
	// 3s 必然不够。30s 给了一个量级的余量，同时仍能响应 ctx 取消。
	gapResolveBudget = 30 * time.Second
	// gapResolveRetryBackoff 是失败后的重试退避（避免重试爆炸）。
	gapResolveRetryBackoff = 30 * time.Second
)

// gapResolveRequest 是一个源的待核对任务。
//
// events 是**累积并集**：同源多次投递成功会把新事件合并进来，
// 而不是新建一条待办。这是「不换种方式炸内存」的关键——
// 待办大小以「一个源一轮的增量」为界，不随登记次数增长。
type gapResolveRequest struct {
	key    string
	source SourceConfig
	events []logtypes.Event
	due    time.Time
}

// GapResolveStats 是异步缺口消解的观测面。
type GapResolveStats struct {
	Scheduled   uint64 // 成功登记的源次（含合并）
	Merged      uint64 // 合并进已有待办的次数
	Skipped     uint64 // 被拒（关停 / 超上限 / 退避中）
	RejectedCap uint64 // 因待办源数超上限而被拒的次数
	Running     uint64 // 已开始执行的次数
	Done        uint64 // 成功跑完的次数
	Failed      uint64 // 失败的次数
	Pending     int    // 当前待办源数
	InFlight    int    // 当前在跑源数
}

type gapResolveScheduler struct {
	mu      sync.Mutex
	wake    chan struct{}
	pending map[string]*gapResolveRequest
	inflight map[string]struct{}
	closed  bool

	scheduled, merged, skipped, rejectedCap atomic.Uint64
	running, done, failed                   atomic.Uint64
}

func newGapResolveScheduler() *gapResolveScheduler {
	return &gapResolveScheduler{
		wake:     make(chan struct{}, 1),
		pending:  make(map[string]*gapResolveRequest),
		inflight: make(map[string]struct{}),
	}
}

// schedule 登记一次核对。同源已有待办 ⇒ 合并事件（并集）而不是新建一条。
//
// 返回 false 表示被拒（已关停 / 超上限 / 该源正在飞）。
func (s *gapResolveScheduler) schedule(req *gapResolveRequest) bool {
	if s == nil || req == nil || req.key == "" || len(req.events) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.skipped.Add(1)
		return false
	}
	if _, running := s.inflight[req.key]; running {
		// 正在飞：先把事件记进待办，worker 跑完会再看一次（否则这段时间的增量就丢了）。
		if existing := s.pending[req.key]; existing != nil {
			existing.events = mergeGapResolveEvents(existing.events, req.events)
			s.merged.Add(1)
		} else {
			if len(s.pending) >= maxGapResolvePendingSources {
				s.rejectedCap.Add(1)
				s.skipped.Add(1)
				slog.Warn("缺口消解待办已满，本轮登记被拒（周期环会兜底重试）",
					"logSourceID", req.source.LogSourceID, "pending", len(s.pending), "cap", maxGapResolvePendingSources)
				return false
			}
			s.pending[req.key] = req
			s.scheduled.Add(1)
		}
		s.signalLocked()
		return true
	}
	if existing := s.pending[req.key]; existing != nil {
		existing.events = mergeGapResolveEvents(existing.events, req.events)
		s.merged.Add(1)
		s.signalLocked()
		return true
	}
	if len(s.pending) >= maxGapResolvePendingSources {
		s.rejectedCap.Add(1)
		s.skipped.Add(1)
		slog.Warn("缺口消解待办已满，本轮登记被拒（周期环会兜底重试）",
			"logSourceID", req.source.LogSourceID, "pending", len(s.pending), "cap", maxGapResolvePendingSources)
		return false
	}
	s.pending[req.key] = req
	s.scheduled.Add(1)
	s.signalLocked()
	return true
}

// signalLocked 非阻塞唤醒（容量 1 ⇒ 多次登记合并成一次唤醒）。
func (s *gapResolveScheduler) signalLocked() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// pickLocked 取一个到期且不在飞的任务，并把它移出待办、记入 inflight。
func (s *gapResolveScheduler) pickLocked(now time.Time) *gapResolveRequest {
	for key, req := range s.pending {
		if now.Before(req.due) {
			continue
		}
		delete(s.pending, key)
		s.inflight[key] = struct{}{}
		return req
	}
	return nil
}

// nextDueLocked 返回最近的到期时刻（用于定时器）。
func (s *gapResolveScheduler) nextDueLocked() (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, req := range s.pending {
		if !found || req.due.Before(earliest) {
			earliest = req.due
			found = true
		}
	}
	return earliest, found
}

// doneLocked 把源移出 inflight；若期间又合并了新事件，重新入队。
func (s *gapResolveScheduler) doneLocked(key string, requeue *gapResolveRequest) {
	delete(s.inflight, key)
	if requeue != nil && len(requeue.events) > 0 {
		s.pending[key] = requeue
	}
}

// close 置关停门并唤醒 worker（让其退出）。
func (s *gapResolveScheduler) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.pending = make(map[string]*gapResolveRequest)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// stats 返回观测快照。
func (s *gapResolveScheduler) stats() GapResolveStats {
	if s == nil {
		return GapResolveStats{}
	}
	s.mu.Lock()
	pending, inflight := len(s.pending), len(s.inflight)
	s.mu.Unlock()
	return GapResolveStats{
		Scheduled:   s.scheduled.Load(),
		Merged:      s.merged.Load(),
		Skipped:     s.skipped.Load(),
		RejectedCap: s.rejectedCap.Load(),
		Running:     s.running.Load(),
		Done:        s.done.Load(),
		Failed:      s.failed.Load(),
		Pending:     pending,
		InFlight:    inflight,
	}
}

// mergeGapResolveEvents 把新增事件合并进已有集合（按 EventID 去重）。
//
// 为何按 EventID 去重而不是直接 append：同一批事件可能被重复登记（投递重试、
// 重建路径与投递路径都调），不去重会让待办随登记次数线性膨胀——那正是要避免的。
func mergeGapResolveEvents(existing, incoming []logtypes.Event) []logtypes.Event {
	if len(existing) == 0 {
		return incoming
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]logtypes.Event, 0, len(existing)+len(incoming))
	for _, e := range existing {
		if _, ok := seen[e.EventID]; ok {
			continue
		}
		seen[e.EventID] = struct{}{}
		out = append(out, e)
	}
	for _, e := range incoming {
		if _, ok := seen[e.EventID]; ok {
			continue
		}
		seen[e.EventID] = struct{}{}
		out = append(out, e)
	}
	return out
}
