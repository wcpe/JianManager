package ingest

// 本文件实现**投影重建（projection rebuild）的异步调度器**：把「重放整段历史 → 写 VL →
// 逐字段校验 → 发布」这条慢路径从采集主线（`pollOnce` 的轮 goroutine）与对账轮
// （`ReconcileRoundNow`）上**移出去**，交给独立的专用 worker，并对其加**限速**与
// **有界重试**。
//
// ── 为什么必须异步（2026-10-09 生产铁证）────────────────────────────────────────────
// `worker.out.20261009-2103`（1.49GB，411 万行）里：
//   · 源 `inst:220/file` 的**同一份 573562 事件、同一个 UTC 天**被写成
//     `projection-115,116,117,…,125,…`（≥70 代次，每 ~1 小时一轮）；
//   · 每轮都是 `投影写入结束 status=200`（写成功）→ `投影校验完成` 报
//     `not fully visible before deadline: context deadline exceeded`（287 簇，queries 从 4 涨到 287）；
//   · 代次因校验不过而**不推进** ⇒ 下一轮对账/重放又判「缺失」⇒ 再取新代次**再写 573562 条**。
// 这条循环此前是**内联同步**跑在 `reconcileOneCandidate`（对账轮）与 `runStartupRecovery`
// 的 goroutine 上：单次「写 57 万条 + 校验 287 簇」耗时以十分钟计，期间它与采集轮争同一条
// 全局校验天花板（`verifyLimiter`，30/s、在飞 6）与持久化门（`persistGate`），
// 直接后果是现场大批源 `acquire: tailer paused (wal backlog limit exceeded (entries=54000/20000))`
// ——**40 个源卡死 13 小时**。用户明确要求「重建不得占用采集主线」，本文件即该要求的落点。
//
// ── 并发边界（谁可与谁并行、哪些必须串行）──────────────────────────────────────────
//   · **可与采集并行**：重建的 VL 插入、逐字段校验、`canonicalRecoveryEvents` 的读段遍历。
//     它们不持 `cycleMu`、不持 `m.mu`（沿用既有重发路径的「快照 → 离锁 → 短临界区」纪律），
//     因此与 `pollOnce` 的采集轮天然并行。
//   · **必须串行**：① 同源**同一时刻至多一个**重建任务（单飞，见 `rebuildScheduler.schedule`）——
//     两次并发的整窗重放会打乱「同源投递串行」这一既有前提（既有的 `replayInflight` 表同款理由）；
//     ② 代次推进 / 发布 / `PublicationPending` / 账本与水位变更仍走各自的**短临界区**
//     （`publishMu` / `m.mu`），与采集径路的对应临界区互斥，但持有时长恒为 O(1)。
//   · **锁序（任何路径都不得逆序）**：`cycleMu → registerMu → pendingMu → m.mu`，
//     `publishMu → m.mu`（见 `publishMu` 注释）。重建 worker **从不取 `cycleMu`** ——
//     这是「不占主线」的根因级保证：整轮采集持 `cycleMu`，只要重建不取它，
//     采集轮就不会被重建挡在门外。
//   · **最坏持有量**：重建路径上唯一的跨调用临界区是 `writeProjectionPlan` 的
//     「`publishMu` + `m.mu` 登记尝试」两段，各自只做 map 读写与值拷贝，**无网络、无磁盘、
//     无 O(事件数) 遍历**；饱和场景下的最坏持有时长为微秒级（与既有投递路径同档）。
//
// ── 不丢数据 / 不把失败伪装成成功（红线）──────────────────────────────────────────
//   · 重建的**发布语义逐字不变**：写 VL → 逐字段可见性校验 → 通过才 `publishSnapshot`。
//     校验不过 ⇒ 该天**不发布**，代次不推进，`PublicationPending` 保持为真。
//   · 放弃自动重试**不改任何投递/回收/水位决定**：只停「自动再建一版」这件事；
//     缺口仍留在账本里、前端仍报「覆盖不完整」，绝不 `resolveGaps`、绝不 `releaseRecovery`、
//     绝不把 `PublicationPending` 置假 —— 因此不存在「把失败伪装成成功」。
//   · 限速让出只改变**节奏**（批与批之间主动交还 CPU），不改变**发布的原子单位**：
//     发布仍以「天」为单位（见 `rebuildDaysInner` 的约束说明），故不会出现
//     「声明整天已发布却只写了一部分」。
//
// ── 防循环重建（用户的第二项要求）────────────────────────────────────────────────
// 见 `rebuildFailureClass` / `noteFailure`：失败按「外部条件 / 确定性」分流，
// 只有**确定性**失败消耗放弃预算（达上限即停止自动重试并打 ERROR）；
// 外部条件失败退避后永远可重试（频率有上界，故同样不是无界循环）。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// ── 配置常量 ────────────────────────────────────────────────────────────────────

const (
	// rebuildDefaultThrottleDelay 是重建**批间让出**的默认时长。
	//
	// 取值依据（对照 `defaultReconcileYield` 的 5ms/4096 条）：重建的批单位是
	// `insertBatchMaxEvents`（500 条），比 4096 条更细，故按「每批让出一点」的口径取更短的值；
	// 目标是让整节点算力**优先给采集**，而不是给重建凑吞吐。让出本身不减少总工作量，
	// 只把这次重建从「连续吃满一个核」摊成「间歇性吃半个核」。
	rebuildDefaultThrottleDelay = 2 * time.Millisecond
	// rebuildDefaultMaxAttempts 是**同一来源同一代次**连续重建失败的上限。
	//
	// 为什么必须有上限：生产实证的循环正是「写成功→校验超时→换新代次→再写」无界重复
	// （573562 条 × ≥70 次，VL 只追加 ⇒ 281GB）。上限让「短期内注定不会成功」的重建停下来，
	// 并把原因**显式打出来**（不静默）。
	rebuildDefaultMaxAttempts = 4
	// rebuildDefaultBackoffBase / rebuildDefaultBackoffMax 是失败后的指数退避。
	//
	// 起点 30s：远大于一轮对账周期（默认 15 分钟周期的最大探测频率），且足以让一次「VL 抖动」
	// 自愈；封顶 10 分钟：即使一直失败，重试频率也不超过每 10 分钟一次（有界且可观测）。
	rebuildDefaultBackoffBase = 30 * time.Second
	rebuildDefaultBackoffMax  = 10 * time.Minute
	// rebuildTaskQueueCap 是待重建队列的长度上限（有界内存 + 有界登记）。
	rebuildTaskQueueCap = 64
)

// ── 任务与调度状态 ──────────────────────────────────────────────────────────────

// rebuildRequest 是一次「请重建该源」的请求（登记是 O(1)，不等任何慢操作）。
type rebuildRequest struct {
	// key 是「源键」：LogSourceID/SourceGeneration（与 m.pipes / m.sources 的键同口径）。
	key    string
	source SourceConfig
	// days 是本次重建要覆盖的 UTC 天（升序，非空）。
	days []string
	// confirmPending 为 true 时，worker 先做一次「发布确认」只读取证：若 VL 里其实已经完整可见，
	// 就只清 `PublicationPending`（零写入），只有在确认不通过时才落回 `days` 的重建。
	//
	// 为什么把它并入同一任务：两者都是「O(全量事件) 的重活」，必须一起移出对账轮——
	// 生产现场 `inst:220/file` 的 46 次 `publication_pending` 回退正是这条路径，
	// 而它在原文里是**内联**跑在对账 goroutine 上的。
	confirmPending bool
	// reason 是登记原因（观测用：reconcile_missing / pending_unconfirmed / startup_fallback）。
	reason string
	// startupReplay 为 true 表示本次是**启动恢复**登记的重发（区别于常驻对账登记的）。
	//
	// 为什么要显式标记而不是从 reason 反查：完成时必须**精确**地把它从未完成读数里扣掉
	// （见 Manager.startupReplayPending），而 reason 是自由文本、且会被 schedule 覆盖成
	// 触发方的值——用文本匹配来记账在第一次改动文案时就会静默失效。
	startupReplay bool
	// enqueuedAt 是登记时刻（用于统计排队时延）。
	enqueuedAt time.Time
}

// rebuildOutcome 是一次重建推进的结论（同时作为观测面经 `RebuildLastResult` 暴露）。
type rebuildOutcome struct {
	// Days 是本次真正完成（写入 + 校验通过 + 发布）的 UTC 天。
	Days []string
	// Pending 为 true 表示本次有界推进**没做完**剩下的天（让路/预算/失败），
	// 剩余天留给下一轮——注意这**不是**「已发布」，不代表数据已可见。
	Pending bool
	// ConfirmedOnly 为 true 表示本次**零写入**收尾：发布确认只读取证认定 VL 里其实已完整可见，
	// 故只清 `PublicationPending`，不重建任何天。它与「写成功」是两种不同的完成形态，
	// 分开记账才不会把「确认」误读成「又写了一遍」。
	ConfirmedOnly bool
	// Err 是本次失败原因（nil 表示完整成功）。
	Err error
}

// rebuildAttemptState 是**单源单代次**的重建尝试台账（防循环重建的核心状态）。
//
// 为什么键里必须带 generation：生产循环的形态正是「每次失败都换一个新代次」——
// 若只按源计数，换代次就会把计数清零，上限便形同虚设。这里同时记**代次**：
//   - 连续失败会一直累积到 attempts 上限（跨代次累积，见 noteRebuildFailure）；
//   - 而一旦**成功发布**（校验通过）就整体清零（真进展）。
type rebuildAttemptState struct {
	// attempts 是连续失败次数（成功后清零）。
	attempts int
	// lastErr 是最近一次失败原因（放弃时打出来，绝不静默）。
	lastErr error
	// lastFailureAt 是最近一次失败时刻。
	lastFailureAt time.Time
	// nextAllowedAt 是退避解禁时刻；在此之前所有登记一律跳过并计数。
	nextAllowedAt time.Time
	// abandoned 为 true 表示已达到上限并**停止自动重试**（需人工介入或源换代）。
	abandoned bool
	// abandonReason 是放弃原因（含最后一次失败原因与代次列表），排障与运维面据此定性。
	abandonReason string
	// generations 是本轮连续失败期间**已尝试过的投影代次**（有界，用于排障看清
	// 「它到底建了多少版」，这正是生产现场最难判的一点）。
	generations []string
}

// rebuildStats 是重建调度器的累计读数（G10 观测面风格：读数 + 只读访问器）。
type rebuildStats struct {
	Scheduled       atomic.Int64 // 登记次数（真正入队的那些）
	Coalesced       atomic.Int64 // 因「同源已有待办/在飞/队列满」而未入队的次数
	Started         atomic.Int64 // 真正开始执行的任务数
	Completed       atomic.Int64 // 完整成功（所有天都发布）的任务数
	Partial         atomic.Int64 // 有界推进但没做完（让路/预算）的任务数
	Failed          atomic.Int64 // 失败的任务数
	Abandoned       atomic.Int64 // 因达到上限而停止自动重试的次数
	BackoffSkipped  atomic.Int64 // 因退避窗口未到而跳过的登记次数
	AbandonedSkips  atomic.Int64 // 因已放弃而跳过的登记次数（与 BackoffSkipped 分开：语义不同）
	ThrottledYields atomic.Int64 // 批间让出的累计次数（限速生效的直接读数）
}

// ── 调度器主体 ──────────────────────────────────────────────────────────────────

// rebuildScheduler 是重建的异步执行器：单 goroutine 串行消费「按源去重」的任务表。
//
// 为什么是**多 goroutine 但每源单飞**而不是「无限制并发」：重建的瓶颈是 VL 与磁盘，
// 提高并发只会把争抢变成互相拖慢（既有 `verifyLimiter` 的 30/s 天花板是同一取舍）。
// 这里默认并发 1，保证重建**永远不会**把采集轮之外的所有资源一口气吃光。
type rebuildScheduler struct {
	mu sync.Mutex
	// wake 是「有新任务」的非阻塞唤醒通道（容量 1，多次登记合并成一次唤醒）。
	// 与 ctx 一起在 waitReady 的 select 里等待，故关停不依赖下一次唤醒（见 waitReady 的说明）。
	wake    chan struct{}
	pending map[string]*rebuildRequest // 待办（按源去重：同源只留一条，days 取并集）
	order   []string                   // 待办顺序（FIFO，便于测试与排障观察）
	// inflight 是「正在执行」的源键集合（单飞保证：同源同时至多一个）。
	inflight map[string]struct{}
	// startupPending 是「启动恢复已登记、但尚未出结论」的源键集合。
	//
	// 为什么需要它（2026-10-10 第二项修复）：重发移出启动驱动后，就绪面不能再靠「驱动跑完了」
	// 来判断恢复是否结束——判定与登记是秒级的，而重发可能在几分钟后才收束。没有这个集合，
	// 平台会在重发还在路上时就宣告就绪（正是 2026-10-02 事故的语义侧：就绪判据必须显式）。
	// 语义边界：**只登记、只在执行体给结论时清除**；放弃（确定性失败达上限）按**失败**计入
	// 启动恢复状态而不是清除——「不再重试」不等于「已完成」。
	startupPending map[string]struct{}
	// attempts 是每源的重试台账（见 rebuildAttemptState）。
	attempts map[string]*rebuildAttemptState
	// lastResults 是每源**最近一次**重建结论（观测用：让「上一次重建到底做了哪几天、
	// 是失败还是有界收束」可被运维直接取证，而不是只能从 VL 写入量反推）。
	lastResults map[string]rebuildOutcome
	// closed 在生命周期 ctx 结束后置位：此后不再接受新登记（防止关停期堆积）。
	closed bool
	// throttle 是批间让出时长（<=0 ⇒ 默认）。
	throttle time.Duration
	// maxAttempts 是连续失败上限（<=0 ⇒ 默认）。
	maxAttempts int
	stats       rebuildStats
	// yieldNotify 是批间让出的观测口（生产 nil，零开销；测试用它做确定性断言）。
	yieldNotify func()
	// hookRun 是「任务开始执行」的观测口（仅测试用，生产 nil）。
	hookRun func(key string)
}

func newRebuildScheduler() *rebuildScheduler {
	s := &rebuildScheduler{
		pending:     make(map[string]*rebuildRequest),
		inflight:    make(map[string]struct{}),
		attempts:    make(map[string]*rebuildAttemptState),
		lastResults: make(map[string]rebuildOutcome),
		throttle:    rebuildDefaultThrottleDelay,
		maxAttempts: rebuildDefaultMaxAttempts,
	}
	s.wake = make(chan struct{}, 1)
	return s
}

// throttleOf 归一化批间让出时长。
func (s *rebuildScheduler) throttleOf() time.Duration {
	if s == nil || s.throttle <= 0 {
		return rebuildDefaultThrottleDelay
	}
	return s.throttle
}

// maxAttemptsOf 归一化连续失败上限。
func (s *rebuildScheduler) maxAttemptsOf() int {
	if s == nil || s.maxAttempts <= 0 {
		return rebuildDefaultMaxAttempts
	}
	return s.maxAttempts
}

// scheduleStartup 登记一次**启动恢复**重发，并在同一临界区内把它记入 startupPending 台账。
//
// 为什么必须在 s.mu 内一起做（而不是"先 schedule 再记 pending"）：worker 可能在两步之间就把
// 任务跑完，那时 `doneLocked` 清 pending 会先于本次写入发生 ⇒ pending 永挂 ⇒ 启动恢复永远不就绪。
// 反之若"先记 pending 再 schedule"，登记被拒时又会留下一个永远等不到结论的 pending。
// 一个临界区同时决定"排不排队"和"记不记账"，两种失败模式都不存在。
//
// 语义边界：`startupPending` 的存在**不等于**「缺口已解决」，只表示「启动恢复对它的结论还没出」。
// 缺口是否解决仍由发布账本（`PublicationPending` / 就绪面）判定，本台账只负责「不要提前宣告完成」。
func (s *rebuildScheduler) scheduleStartup(req *rebuildRequest, reason string) bool {
	if s == nil || req == nil || req.key == "" || len(req.days) == 0 {
		return false
	}
	req.startupReplay = true
	req.reason = reason
	if req.enqueuedAt.IsZero() {
		req.enqueuedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 已放弃的源不再登记，也**不**记 pending（它已经有确定结论：放弃；记成 pending 会让
	// 启动恢复永远等一个不会再来的结论）——放弃本身在就绪面按失败呈现，见 startupReplayPending。
	if st := s.attempts[req.key]; st != nil && st.abandoned {
		return false
	}
	if !s.scheduleLocked(req) {
		return false
	}
	if s.startupPending == nil {
		s.startupPending = make(map[string]struct{})
	}
	s.startupPending[req.key] = struct{}{}
	return true
}

// schedule 登记一次重建请求。**O(1)、不等待任何慢操作、不取 m.mu**（这是「不占主线」的形式保证）。
//
// 合并语义（同 `schedulePendingReplay`）：同源已有待办 ⇒ 不新建队列项，只把 days **并集**
// 进已有待办并唤醒 worker。这样即使对账每轮都发现同一批缺失天，队列也不会膨胀。
//
// 返回 ok=false 表示本次登记被丢弃（队列已满或已关停）；调用方据此把这件事记进读数，
// **不得**据此认为「数据已重建」——重建没做，缺口照旧可见。
func (s *rebuildScheduler) schedule(req *rebuildRequest, reason string) bool {
	if s == nil || req == nil || req.key == "" || len(req.days) == 0 {
		return false
	}
	req.reason = reason
	if req.enqueuedAt.IsZero() {
		req.enqueuedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scheduleLocked(req)
}

// scheduleLocked 是排队的实际逻辑（调用方需持 s.mu）——被 `schedule` 与 `scheduleStartup`
// 共用，后者借它把「排队」与「记 pending」放进同一临界区。
func (s *rebuildScheduler) scheduleLocked(req *rebuildRequest) bool {
	if s.closed {
		return false
	}
	// 退避窗口内：跳过（不排队），但**把这件事记成一次可观测的跳过**——静默跳过会让
	// 「为什么没重建」在现场无从判断。
	if st := s.attempts[req.key]; st != nil {
		if st.abandoned {
			return false
		}
		if now := time.Now(); now.Before(st.nextAllowedAt) {
			return false
		}
	}
	// 单飞 + 合并：在飞的任务不另起一份；已有待办则并入 days。
	if existing, ok := s.pending[req.key]; ok {
		existing.days = unionSortedDays(existing.days, req.days)
		s.signalLocked()
		return true
	}
	if _, busy := s.inflight[req.key]; busy {
		return false // 在飞：本次由在飞那次的下一轮续做（days 由下一轮对账重新发现）
	}
	if len(s.pending) >= rebuildTaskQueueCap {
		return false
	}
	req.days = sortUTCDays(append([]string(nil), req.days...))
	s.pending[req.key] = req
	s.order = append(s.order, req.key)
	s.signalLocked()
	return true
}

// pickLocked 取出下一个可执行的任务（调用方需持 s.mu）。
func (s *rebuildScheduler) pickLocked() *rebuildRequest {
	if len(s.order) == 0 {
		return nil
	}
	key := s.order[0]
	s.order = s.order[1:]
	req, ok := s.pending[key]
	if !ok {
		return nil
	}
	delete(s.pending, key)
	s.inflight[key] = struct{}{}
	return req
}

// doneLocked 标记某源执行结束（调用方需持 s.mu）。
//
// 一并清 `startupPending`：该源的启动恢复重发**已经有了结论**（成功/有界推进/失败/放弃都算），
// 结论本身记在 `lastResults` 与就绪面上。此处只负责"不再阻塞就绪判定"。
// 注意清除**不等于**成功：失败/放弃的结论由 `startupReplayPending` 按失败计入。
func (s *rebuildScheduler) doneLocked(key string) {
	delete(s.inflight, key)
	delete(s.startupPending, key)
}

// startupPendingKeys 返回「启动恢复已登记、尚无结论」的源键（升序，便于断言与日志稳定）。
func (s *rebuildScheduler) startupPendingKeys() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.startupPending))
	for key := range s.startupPending {
		keys = append(keys, key)
	}
	s.mu.Unlock()
	sort.Strings(keys)
	return keys
}

// noteSuccess 记一次成功：清空该源的重试台账（真进展 ⇒ 计数归零是可验证的语义）。
func (s *rebuildScheduler) noteSuccess(key string) {
	s.mu.Lock()
	delete(s.attempts, key)
	s.mu.Unlock()
}

// rebuildFailureExternal / rebuildFailureDeterministic 是重建失败的**两个类别**。
//
// 为什么要分类（而不是「失败就计数、够数就放弃」）：两类失败的**正确处置相反**。
//
//   - **external（外部条件）**：VL 未就绪、网络往返失败、本轮 ctx 取消。这类失败**会在外部
//     条件恢复后自愈**。若也计入放弃上限，则「VL 抖一次 ⇒ 该源永远不再重建 ⇒ 数据长期不可见」
//     ——那是可用性回归，而不是止血。对它的正确处置是**退避 + 永不放弃**：重试频率有上界
//     （封顶 rebuildDefaultBackoffMax），故同样**不可能构成无界循环**。
//   - **deterministic（确定性）**：刚重建过、写完 VL 却「校验仍不通过」（内容不一致 / 多余或
//     重复 / 永久失败），以及事件集非法。这类失败**重试不会自愈**——它正是 2026-10-09 生产
//     循环的形态（`inst:220/file` 同一份 573562 条写成 ≥70 个代次，每轮 1 小时）。
//     对它的正确处置是**计上限并放弃**（有界尝试 + 显式 ERROR），把算力与 VL 容量还给采集。
//
// **可见性**：两类都打日志（external 为 WARN + 退避，deterministic 达上限为 ERROR + 放弃原因），
// 因此「为什么这个源不再重建」永远可判读。
//
// **恢复路径**（放弃之后怎么回来，必须说清）：尝试台账是**进程内内存态**（`newRebuildScheduler`
// 每次 New 重建），故「重启 Worker」即重新武装；此外源换代次（`SourceGeneration` 变化）后
// 该源键随之改变，自然也重新武装。两者都是运维既有动作，无需额外开关。
type rebuildFailureClass int

const (
	// rebuildFailureExternal：外部条件所致，退避后**永远允许**重试（频率有上界）。
	rebuildFailureExternal rebuildFailureClass = iota
	// rebuildFailureDeterministic：确定性失败，达上限即放弃自动重试。
	rebuildFailureDeterministic
)

// classifyRebuildFailure 判定一次重建失败的类别（判据全部取自既有实现，不新增启发式）。
func (m *Manager) classifyRebuildFailure(key string, err error) rebuildFailureClass {
	if err == nil {
		return rebuildFailureExternal
	}
	// ① **确定性**：本次已把数据写进 VL，但校验未通过。
	//
	// 这是生产循环的形态，必须优先于下面所有"外部/超时"判据：它的错误文案恰好是
	// `not fully visible before deadline: context deadline exceeded`（超时类），若被归到
	// external 就会**永不放弃** ⇒ 循环重建复发（这正是本项修复要消除的现象）。
	if errors.Is(err, errProjectionVerifyFailed) {
		return rebuildFailureDeterministic
	}
	// ② VL 未就绪：外部条件（既有 notReady 台账就是为此存在的），会在恢复后自愈。
	if m.vlNotReadyFor(key) {
		return rebuildFailureExternal
	}
	// ③ ctx 取消/超时（不含 ①，①已在上面拦下）：本轮让路或关停，属外部条件。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return rebuildFailureExternal
	}
	// ④ 校验的确定性判定（内容不一致 / 多余或重复 / 事件集非法）与 vlsup 的永久失败：
	// 这两类重试不会自愈。
	if verifyErrorIsSemantic(err) || vlsup.IsPermanent(err) {
		return rebuildFailureDeterministic
	}
	// ⑤ 其余（插入 5xx、连接被拒、读段失败等）：外部条件，按可自愈处理。
	return rebuildFailureExternal
}

// noteFailure 记一次失败并推进退避；**确定性**失败达到上限即放弃自动重试。
//
// 返回（是否放弃, 退避解禁时刻, 累计确定性失败次数）。
//
// 关键：这里的「放弃」只停**自动再建一版**，绝不改善任何发布/水位/缺口状态
// （见文件头「不丢数据 / 不把失败伪装成成功」）。因此放弃是安全的：数据仍在账本、
// 缺口仍可见、前端仍报「覆盖不完整」。
func (s *rebuildScheduler) noteFailure(key, generation string, err error, class rebuildFailureClass) (abandoned bool, nextAllowed time.Time, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.attempts[key]
	if st == nil {
		st = &rebuildAttemptState{}
		s.attempts[key] = st
	}
	// 只有确定性失败消耗「放弃预算」：外部条件失败不消耗，故 VL 抖动不会把源判死。
	if class == rebuildFailureDeterministic {
		st.attempts++
	}
	st.lastErr = err
	st.lastFailureAt = time.Now()
	if generation != "" {
		// 有界记录已尝试代次：它正是现场最难判的「到底建了多少版」。
		if len(st.generations) < 8 {
			st.generations = append(st.generations, generation)
		}
	}
	if class == rebuildFailureDeterministic && st.attempts >= s.maxAttemptsOf() {
		st.abandoned = true
		st.abandonReason = fmt.Sprintf("连续 %d 次重建失败后停止自动重试；已尝试代次 %v；最后失败：%v",
			st.attempts, st.generations, err)
		return true, time.Time{}, st.attempts
	}
	// 指数退避：base * 2^(attempts-1)，封顶 max。
	//
	// 对**外部条件**失败：attempts 不增长 ⇒ 恒以 base 退避——「VL 抖一次不必等十分钟」，
	// 同时频率仍有上界（每 base 一次，且仍受对账周期约束）⇒ 不构成无界循环。
	delay := rebuildDefaultBackoffBase
	for i := 1; i < st.attempts; i++ {
		delay *= 2
		if delay >= rebuildDefaultBackoffMax {
			delay = rebuildDefaultBackoffMax
			break
		}
	}
	st.nextAllowedAt = time.Now().Add(delay)
	return false, st.nextAllowedAt, st.attempts
}

// snapshot 返回某源重试台账的只读副本（测试与运维面取证用）。
func (s *rebuildScheduler) snapshot(key string) (rebuildAttemptState, bool) {
	if s == nil {
		return rebuildAttemptState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.attempts[key]
	if !ok || st == nil {
		return rebuildAttemptState{}, false
	}
	out := *st
	out.generations = append([]string(nil), st.generations...)
	return out, true
}

// waitReady 阻塞等待「有任务可领」或 ctx 结束（worker 主循环的等待点）。
//
// 为什么用「Cond + 唤醒通道」而不是纯 Cond：Cond 无法被 ctx 中断，纯 Cond 会让关停依赖
// 「下一次唤醒」，而关停时可能永远没有下一次。这里用**非阻塞唤醒通道**（容量 1，合并唤醒）
// 承担「有活干」的信号、用 `select` 同时听 ctx —— 两者都不需要轮询，关停延迟为 O(1)。
// 同型实现见既有 `schedulePendingReplay` / `replayWakeCh`（同一取舍：容量 1 合并多次唤醒）。
func (s *rebuildScheduler) waitReady(ctx context.Context) *rebuildRequest {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil
		}
		if req := s.pickLocked(); req != nil {
			s.mu.Unlock()
			return req
		}
		wake := s.wake
		if wake == nil {
			// 懒创建（容量 1）：多次登记合并成一次唤醒。
			wake = make(chan struct{}, 1)
			s.wake = wake
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
			return nil
		case <-wake:
			// 有新登记：回到循环头领任务。
		}
	}
}

// signalLocked 非阻塞地唤醒 worker（调用方需持 s.mu）。
func (s *rebuildScheduler) signalLocked() {
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	select {
	case s.wake <- struct{}{}:
	default: // 已有待唤醒信号：合并
	}
}

// RunRebuildWorker 是重建的专用执行 goroutine：由 `Manager.Start` 启动，随生命周期 ctx 退出。
//
// 与采集轮的关系：**完全独立的一条 goroutine**。它不取 `cycleMu`，因此无论重建跑多久，
// `pollOnce` 都能照常取锁推进采集——这正是「重建不得占用主线」的实现方式。
func (m *Manager) RunRebuildWorker(ctx context.Context) {
	if m == nil || m.rebuilds == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		req := m.rebuilds.waitReady(ctx)
		if req == nil {
			return // 关停
		}
		m.runRebuildRequest(ctx, req)
	}
}

// runRebuildRequest 执行一次重建请求（在工作 goroutine 上，绝不阻塞采集主线）。
func (m *Manager) runRebuildRequest(ctx context.Context, req *rebuildRequest) {
	if m.rebuilds == nil {
		return
	}
	if hook := m.rebuilds.hookRun; hook != nil {
		hook(req.key)
	}
	m.rebuilds.stats.Started.Add(1)
	started := time.Now()
	outcome := m.rebuildDays(ctx, req)
	// 记录最近结论（观测面）：无论成功/失败/有界收束都写一条，供运维直接取证。
	m.rebuilds.mu.Lock()
	m.rebuilds.lastResults[req.key] = outcome
	m.rebuilds.mu.Unlock()
	if outcome.Err != nil {
		m.rebuilds.stats.Failed.Add(1)
		gen := m.currentProjectionGeneration(req.key)
		class := m.classifyRebuildFailure(req.key, outcome.Err)
		abandoned, nextAt, attempts := m.rebuilds.noteFailure(req.key, gen, outcome.Err, class)
		if abandoned {
			m.rebuilds.stats.Abandoned.Add(1)
			st, _ := m.rebuilds.snapshot(req.key)
			// 放弃必须**可见**：级别 ERROR + 完整原因（含已尝试代次与最后失败原因），
			// 并明确写出「数据未被当作成功」这一事实，避免运维误判。
			slog.Error("投影重建已停止自动重试（达到确定性失败上限；数据仍视为未发布，缺口保持可见，需人工介入）",
				"source", req.key, "reason", req.reason,
				"attempts", st.attempts, "generations", st.generations,
				"recoverableBy", "重启 Worker（尝试台账为进程内态）或源换代次",
				"lastError", outcome.Err)
		} else {
			// 两类失败分别留痕：`class` 让现场一眼分清「等外部条件自愈」与「正在消耗放弃预算」。
			slog.Warn("投影重建失败（指数退避后重试；数据仍视为未发布，缺口保持可见）",
				"source", req.key, "reason", req.reason, "days", req.days,
				"class", rebuildFailureClassName(class), "deterministicAttempts", attempts,
				"maxAttempts", m.rebuilds.maxAttemptsOf(),
				"elapsedMs", time.Since(started).Milliseconds(),
				"nextAttemptAt", nextAt.Format(time.RFC3339), "error", outcome.Err)
		}
	} else if outcome.Pending {
		m.rebuilds.stats.Partial.Add(1)
		slog.Info("投影重建本轮有界推进未完成（剩余天留给下一轮，数据仍视为未发布）",
			"source", req.key, "reason", req.reason,
			"publishedDays", outcome.Days, "elapsedMs", time.Since(started).Milliseconds())
	} else {
		m.rebuilds.stats.Completed.Add(1)
		m.rebuilds.noteSuccess(req.key)
		if outcome.ConfirmedOnly {
			// 零写入形态：与「写成功」分开说，避免运维以为"又写了一遍"。
			slog.Info("投影重建以「发布确认」收尾（VL 已完整可见，零写入，未生成新代次）",
				"source", req.key, "reason", req.reason, "days", outcome.Days,
				"elapsedMs", time.Since(started).Milliseconds())
		} else {
			slog.Info("投影重建完成（写入 + 逐字段校验 + 发布均已通过）",
				"source", req.key, "reason", req.reason, "days", outcome.Days,
				"elapsedMs", time.Since(started).Milliseconds())
		}
	}
	m.rebuilds.mu.Lock()
	m.rebuilds.doneLocked(req.key)
	m.rebuilds.mu.Unlock()
}

// rebuildFailureClassName 返回失败类别的可读名（日志字段用）。
func rebuildFailureClassName(class rebuildFailureClass) string {
	if class == rebuildFailureDeterministic {
		return "deterministic"
	}
	return "external"
}

// currentProjectionGeneration 读取某源当前代次（仅观测用；零锁失败时返回空串）。
func (m *Manager) currentProjectionGeneration(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Sources[key].ProjectionGeneration
}

// RebuildStats 返回重建调度器的累计读数快照（只读观测面）。
func (m *Manager) RebuildStats() rebuildStatsView {
	if m == nil || m.rebuilds == nil {
		return rebuildStatsView{}
	}
	s := m.rebuilds
	return rebuildStatsView{
		Scheduled: s.stats.Scheduled.Load(), Coalesced: s.stats.Coalesced.Load(),
		Started: s.stats.Started.Load(), Completed: s.stats.Completed.Load(),
		Partial: s.stats.Partial.Load(), Failed: s.stats.Failed.Load(),
		Abandoned: s.stats.Abandoned.Load(), BackoffSkipped: s.stats.BackoffSkipped.Load(),
		AbandonedSkips:  s.stats.AbandonedSkips.Load(),
		ThrottledYields: s.stats.ThrottledYields.Load(),
	}
}

// rebuildStatsView 是 RebuildStats 的返回值（值快照，避免调用方读到撕裂的读数）。
type rebuildStatsView struct {
	Scheduled       int64 `json:"scheduled"`
	Coalesced       int64 `json:"coalesced"`
	Started         int64 `json:"started"`
	Completed       int64 `json:"completed"`
	Partial         int64 `json:"partial"`
	Failed          int64 `json:"failed"`
	Abandoned       int64 `json:"abandoned"`
	BackoffSkipped  int64 `json:"backoff_skipped"`
	AbandonedSkips  int64 `json:"abandoned_skips"`
	ThrottledYields int64 `json:"throttled_yields"`
}

// RebuildAbandonedReason 返回某源「已放弃自动重试」的原因（未放弃时 ok=false）。
//
// 暴露它的理由与 `LastReconcileReports` 同源：让「为什么这个源不再重建」可被运维与回归
// 直接取证，而不是只能从 VL 写入量反推。
func (m *Manager) RebuildAbandonedReason(key string) (string, bool) {
	if m == nil || m.rebuilds == nil {
		return "", false
	}
	st, ok := m.rebuilds.snapshot(key)
	if !ok || !st.abandoned {
		return "", false
	}
	return st.abandonReason, true
}

// unionSortedDays 求两个 UTC 天集合的并集（升序、去重）。
func unionSortedDays(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, day := range list {
			if day == "" {
				continue
			}
			if _, dup := seen[day]; dup {
				continue
			}
			seen[day] = struct{}{}
			out = append(out, day)
		}
	}
	sort.Strings(out)
	return out
}

// ── 重建执行体（限速 + 有界推进）────────────────────────────────────────────────

// rebuildDays 在工作 goroutine 上推进一次重建：按 UTC 天逐个「写 VL + 逐字段校验 + 发布」。
//
// **原子单位是「天」，不是「批」** —— 这是从既有代码事实反推出来的硬约束，不能按"限速"的
// 直觉去拆：`writeProjectionPlan` 用 `plan.Write` 当写入面、却用 `grouped[day]`（该天**全量**）
// 当发布/白名单依据（见 writeProjectionPlan → writeProjectionDay → publishSnapshot）。
// 若把一天拆成多批各自发布，就会出现「发布声称整天可见、实际只写了一部分」——那正是
// 「把失败伪装成成功」。故限速只作用在**天内的插入批之间**与**天与天之间**，发布语义一字不动。
//
// 返回的 outcome.Pending=true 表示有界推进没做完剩余天（让路窗口/ctx 取消/中途失败）；
// 剩余天留给下一轮对账重新发现——**不是**「已发布」，调用方不得据此认为数据可见。
//
// 与 `replayMissingDaysCtx` 是**同一套发布语义的两个入口**：本函数是异步 worker 用的形态
// （带 yield 与 outcome），后者是既有同步调用方的形态（返回 error）。两者共用本函数实现，
// 因此不存在「两条路径语义分叉」的风险。
func (m *Manager) rebuildDays(ctx context.Context, req *rebuildRequest) rebuildOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	res := m.rebuildDaysInner(ctx, req, m.rebuildYieldFn())
	return res
}

// rebuildDaysInner 是重建的执行主体（yield 由调用方给定：worker 传让出钩子，同步入口传 nil）。
func (m *Manager) rebuildDaysInner(ctx context.Context, req *rebuildRequest, yield func()) rebuildOutcome {
	key := req.key
	// 「先确认、后重建」：pending 的语义是「上次发布**未获确认**」，而不是「数据一定没写成功」。
	// 先做一次只读取证（成本 O(全量)，与重建同级，故同样必须由本 worker 承担），
	// 确认通过即零写入收尾——这正是既有 `tryConfirmPendingPublication` 的语义，逐字保留。
	if req.confirmPending {
		confirmed, missingDays, err := m.confirmPendingOutsideLine(ctx, req.source, key)
		if err != nil {
			return rebuildOutcome{Pending: true, Err: err}
		}
		if confirmed {
			// 已完整可见：**不做任何重建**（零写入）。这不是「跳过校验」，而是校验已经通过
			// （判据来自既有 `tryConfirmPendingPublicationCtx` 的内容级取证）。
			return rebuildOutcome{Days: append([]string(nil), req.days...), ConfirmedOnly: true}
		}
		// **重建面收敛为「实测缺失天」**（而不是调用方登记时的全部天）：
		// 已完整可见的天不再被重写——这既是既有语义（原内联路径同样只重发 confirmed 缺失天），
		// 也是「不重复写已经有数据的天」的省流要求（生产单源 57 万条/天，多写一天即多一份 VL 体积）。
		if len(missingDays) > 0 {
			req.days = missingDays
		}
	}
	if len(req.days) == 0 {
		return rebuildOutcome{}
	}
	// 权威集合与发布快照都在**锁外**取（快照必须先于权威读取，见 publicationSnapshot 的契约）。
	snapshot := m.capturePublicationSnapshot(req.source, ctx)
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	events, err := m.canonicalRecoveryEvents(key, saved)
	if err != nil {
		return rebuildOutcome{Err: fmt.Errorf("ingest: 投影重建取权威集合失败: %w", err)}
	}
	if len(events) == 0 {
		// 权威集合为空：没有可重建的面（与既有重发语义一致：不生成空代次）。
		return rebuildOutcome{}
	}
	// 代次只保存在本计划，实际接纳后由 writeProjectionPlan 原子登记尝试状态。
	//
	// 取名为**预留**语义（2026-10-10 缺陷修复，与 deliver 路径同一机制）：本函数在整窗期间
	// 持有同一个代次名，而它的兄弟入口 `replayMissingDaysCtx` 与采集轮投递**没有**共享单飞
	// 保证（重建 worker 的单飞只覆盖自己那张 inflight 表）⇒ 并发取名会撞名。
	// 释放必须覆盖全部返回路径（含预算提前收束、ctx 取消、写失败），故用 defer 收口。
	generation, genRes := m.reserveProjectionGeneration(req.source, saved.ProjectionGeneration,
		"rebuild:"+req.reason)
	// 释放收口到函数出口（预算提前收束 / ctx 取消 / 写失败一并覆盖）。genRes==nil 时是空操作。
	defer m.releaseProjectionGeneration(genRes)
	out := rebuildOutcome{}
	// 按**天**切片推进（预算可配）：每天写完即发布该天；预算用尽的剩余天留给下一轮——下一轮
	// 的对账会重新发现它们仍是缺失天（已发布的天条数已够，不会再被判缺失），因此**无需游标状态**，
	// 天然可续且不重不漏。整窗完成前**不推进回收责任**（releaseRecovery 要求全窗口内容级保证）。
	budget := m.replayBudget
	startedAt := time.Now()
	remaining := sortUTCDays(append([]string(nil), req.days...))
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			out.Pending = true
			out.Err = err
			return out
		}
		if sent := len(out.Days); sent >= budget.MaxDays || time.Since(startedAt) >= budget.MaxDuration {
			// 预算用尽：如实登记「该代次的发布尚未完成」，剩余天留给下一轮。
			// 这里**不宣称任何天已发布**，也**不推进回收责任**（见函数尾部的整窗判据）。
			m.markPublicationPending(key, generation)
			slog.Info("投影重建受切片预算约束提前收束（剩余天下一轮续跑）",
				"source", key, "publishedDays", out.Days, "remainingDays", remaining)
			out.Pending = true
			return out
		}
		day := remaining[0]
		// VL 未就绪时**不重建**（与既有重发纪律一致）：记错误交给退避，绝不把几十万事件
		// 打在未就绪的 VL 上反复失败重试（现场触发因之一）。
		if err := m.ensureVLReady(ctx, req.source); err != nil {
			out.Pending = true
			out.Err = fmt.Errorf("ingest: 投影重建前 VL 未就绪: %w", err)
			return out
		}
		dayEvents, err := eventsForDays(req.source, events, []string{day})
		if err != nil {
			out.Pending = true
			out.Err = err
			return out
		}
		if len(dayEvents) == 0 {
			// 该天在权威集合里没有事件：不生成空代次（既有语义），直接推进到下一
			// 天——这一步不算预算用量（它不产生任何写入）。
			remaining = remaining[1:]
			continue
		}
		plan := projectionWritePlan{
			Write: dayEvents, Replace: true, Days: []string{day}, Snapshot: snapshot,
			// 限速：天内的插入批之间与校验簇之间主动让出 CPU（yield 为 nil 时零行为差异）。
			Yield: yield,
		}
		if _, err := m.writeProjectionPlan(req.source, events, generation, plan); err != nil {
			out.Pending = true
			out.Err = fmt.Errorf("ingest: 投影重建失败: %w", err)
			return out
		}
		out.Days = append(out.Days, day)
		remaining = remaining[1:]
		// 天与天之间让出：本 goroutine 不持任何锁，故这里的让出只影响重建自身节奏，
		// 采集轮完全不受影响（这正是「重建慢速但采集照常」的形态）。
		if yield != nil {
			yield()
		}
	}
	// 整窗完成（本次请求覆盖的全部天都已发布）：与既有重发路径同一出口——
	// 消解被本次写入面覆盖的缺口、推进回收责任。**未完成时绝不走到这里**
	// （releaseRecovery 要求全窗口内容级保证，见其注释）。
	writeEvents, werr := eventsForDays(req.source, events, req.days)
	if werr != nil {
		out.Err = werr
		return out
	}
	m.resolveGapsLandedByDelivery(req.source, writeEvents)
	if err := m.releaseRecovery(req.source, events); err != nil {
		// best-effort：数据已落库这一事实不受影响，下一轮采集路径会再试。
		slog.Warn("投影重建后推进回收责任失败（下轮重试）", "source", key, "error", err)
	}
	return out
}

// confirmPendingOutsideLine 在**重建 worker goroutine 上**做一次「发布确认」只读取证。
//
// 返回（是否已完整可见, 实测缺失天, error）。判据全部来自既有实现
// `tryConfirmPendingPublicationCtx`（一字未改）；本包装只负责：取当前 state、判定在飞、
// 把「缺失天」如实回传。
//
// 为什么必须由本 worker 承担：它内部会遍历全量事件（`canonicalRecoveryEvents`，生产单源
// 57 万条）并对每一天做逐字段可见性查询——这正是 O(全量) 的重活。原先它内联跑在对账轮
// goroutine 上（跑在采集同档），生产现场该源 46 次 `publication_pending` 回退即此。
func (m *Manager) confirmPendingOutsideLine(ctx context.Context, source SourceConfig, key string) (bool, []string, error) {
	m.mu.Lock()
	saved := m.state.Sources[key]
	inflight := m.publicationInflight[key]
	m.mu.Unlock()
	if !saved.PublicationPending {
		// 已被别人确认/写成功清掉了：无需再确认（也不等于「本次重建成功」，
		// 调用方按 days 继续重建即可——重建本身幂等）。
		return false, nil, nil
	}
	if inflight != 0 {
		// 该源还有在飞的写入：此刻取证不可信（可能读到写一半的中间态）。留给下一轮。
		return false, nil, fmt.Errorf("ingest: 投影重建前发布确认不可用：同源有在飞写入")
	}
	missingDays := []string{}
	confirmed, err := m.tryConfirmPendingPublicationCtx(ctx, source, key, saved, &missingDays)
	if err != nil {
		return false, nil, fmt.Errorf("ingest: 投影重建前发布确认失败: %w", err)
	}
	if confirmed {
		return true, nil, nil
	}
	return false, sortUTCDays(missingDays), nil
}

// rebuildYieldFn 返回本次重建的「批间让出」函数（nil 表示不让出）。
func (m *Manager) rebuildYieldFn() func() {
	if m == nil || m.rebuilds == nil {
		return nil
	}
	return m.rebuildYield
}

// rebuildYield 执行一次批间让出：睡眠固定时长并记一次读数。
//
// 为什么是"主动 sleep"而不是"让渡调度器"：重建是纯用户态计算 + 网络等待，Go 的调度器
// 本就会在系统调用处让出；真正的争抢点是 **CPU 与 VL/磁盘的算力**。短暂 sleep 能保证
// 采集轮（同一进程、同一批核）拿到稳定的时间片，代价是重建总时长线性变长——这正是
// 「慢速重建」要的取舍。让出期间本 goroutine 不持任何锁（包括 cycleMu 与 m.mu）。
func (m *Manager) rebuildYield() {
	if m == nil || m.rebuilds == nil {
		return
	}
	delay := m.rebuilds.throttleOf()
	m.rebuilds.stats.ThrottledYields.Add(1)
	if notify := m.rebuilds.yieldNotify; notify != nil {
		notify()
	}
	if delay <= 0 {
		return
	}
	time.Sleep(delay)
}

// scheduleRebuildAsync 是采集主线/对账轮登记重建的**唯一入口**：O(1)、不取 m.mu、
// 不等任何慢操作，登记完立即返回（这正是「重建不占主线」的形式保证）。
//
// opts.confirmPending 为 true 时，任务先做一次「发布确认」只读取证（见 rebuildRequest）——
// 这是 pending 分支的形态；缺失天分支传 false（它已经知道确切缺失面）。
//
// 返回 ok=false 的三种情形都必须被调用方当成「本次没重建」处理（数据不会被当作已重建）：
//   - 队列已满（rebuildTaskQueueCap）
//   - 同源在飞/正在退避/已放弃
//   - 调度器已关停
//
// 第二个返回值是**可解释的跳过原因**（进对账读数，便于事后排障）。
func (m *Manager) scheduleRebuildAsync(source SourceConfig, days []string, reason string) (bool, string) {
	return m.scheduleRebuildTask(source, days, reason, false)
}

// scheduleRebuildConfirmPendingAsync 登记一次「先确认发布、必要时重建」的异步任务。
func (m *Manager) scheduleRebuildConfirmPendingAsync(source SourceConfig, days []string, reason string) (bool, string) {
	return m.scheduleRebuildTask(source, days, reason, true)
}

// scheduleStartupRebuildTask 登记**启动恢复**路径的重发（除「不占主线」外还要额外满足两件事）。
//
// 与 `scheduleRebuildAsync` 的三点差异，逐条说明为什么必须存在：
//  1. `startupReplay=true` ⇒ 该源进入 `startupPending` 台账：重发未出结论之前，启动恢复的
//     就绪面**不得**宣告完成（判定是秒级的、重发是分钟级的，二者必须分别记账）。
//  2. 需要 `confirmPending=true`：启动恢复面对的是「刚重启、账本说该天待发布」的形态。
//     若只登记一次纯缺失天重发，而 VL 里其实已经完整可见（上一趟写进去了、只是账本没合上），
//     `rebuildDaysInner` 会走「发布确认」零写入分支收尾——这正是 2026-10-09 现场
//     `inst:220/file` 需要的行为（写完 573562 条后校验报重复 event_id，不能再写一遍）。
//  3. 台账登记与排队**同一临界区**：若先排队再记 pending，worker 可能在这两步之间就把任务跑完
//     并清掉 pending（那时 `delete` 打到空集合，pending 永挂）——启动恢复会永远不就绪。
//     故本入口把 req 交给 `scheduleStartup` 由它在 s.mu 内一起完成，杜绝该竞态。
func (m *Manager) scheduleStartupRebuildTask(source SourceConfig, days []string, reason string) (bool, string) {
	if m == nil || m.rebuilds == nil || len(days) == 0 {
		return false, "scheduler_unavailable"
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	req := &rebuildRequest{
		key: key, source: source, days: days,
		confirmPending: true, startupReplay: true,
	}
	if ok := m.rebuilds.scheduleStartup(req, reason); !ok {
		return false, m.rebuilds.skipReason(key)
	}
	m.rebuilds.stats.Scheduled.Add(1)
	return true, ""
}

// skipReason 把「为什么没登记」翻译成可解释的短标识（调用方据此记读数与日志）。
//
// 三档与 `schedule` 的三个拒绝点一一对应：已放弃 / 退避窗口内 / 在飞或队列已满。
// 前两档是**有台账**的确定原因，第三档是「稍后仍会由对账重新发现」的软原因——
// 现场排障需要的正是这个区分（"为什么这个源没在重发"）。
func (s *rebuildScheduler) skipReason(key string) string {
	if s == nil {
		return "scheduler_unavailable"
	}
	if st, exists := s.snapshot(key); exists {
		if st.abandoned {
			s.stats.AbandonedSkips.Add(1)
			return "rebuild_abandoned"
		}
		if time.Now().Before(st.nextAllowedAt) {
			s.stats.BackoffSkipped.Add(1)
			return "rebuild_backoff"
		}
	}
	s.stats.Coalesced.Add(1)
	return "rebuild_coalesced_or_full"
}

func (m *Manager) scheduleRebuildTask(source SourceConfig, days []string, reason string, confirmPending bool) (bool, string) {
	if m == nil || m.rebuilds == nil || len(days) == 0 {
		return false, "scheduler_unavailable"
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	req := &rebuildRequest{
		key: key, source: source, days: days,
		confirmPending: confirmPending,
	}
	if ok := m.rebuilds.schedule(req, reason); !ok {
		// 区分"为什么没登记"：退避/放弃是可解释的（有台账），其余按队列/在飞归类。
		if st, exists := m.rebuilds.snapshot(key); exists {
			if st.abandoned {
				m.rebuilds.stats.AbandonedSkips.Add(1)
				return false, "rebuild_abandoned"
			}
			if time.Now().Before(st.nextAllowedAt) {
				m.rebuilds.stats.BackoffSkipped.Add(1)
				return false, "rebuild_backoff"
			}
		}
		m.rebuilds.stats.Coalesced.Add(1)
		return false, "rebuild_coalesced_or_full"
	}
	m.rebuilds.stats.Scheduled.Add(1)
	return true, ""
}

// markPublicationPending 如实登记「该代次的发布尚未完成」。
//
// 只在代次仍是本次尝试的那一个时才写（否则说明期间别人已经成功发布到更新的代次，
// 把 pending 写回去会让状态倒退——那会把已成功的事情说成未成功）。
func (m *Manager) markPublicationPending(key, generation string) {
	m.mu.Lock()
	cur := m.state.Sources[key]
	if cur.ProjectionGeneration == generation {
		cur.PublicationPending = true
		m.state.Sources[key] = cur
	}
	m.mu.Unlock()
	m.requestPersist()
}

// drainRebuilds 在**调用方 goroutine 上**同步排空待办重建任务，返回真正执行的任务数。
//
// 为什么需要它（而不是让测试"等一会儿再看"）：生产路径把重建交给 worker 是为了不占主线；
// 而回归要断言的是「重建**做了什么**」（切片、待发布登记、回收责任不提前推进），这类断言
// 需要确定性。本方法让测试在**同一套执行体**（`runRebuildRequest` → `rebuildDays`）上同步推进，
// 而不是换一份实现——因此它测的仍然是生产代码路径，只是换了执行者。
//
// 与 worker 的关系：两者**不得同时运行**。生产不调用本方法；测试不启动 worker
// （`Start` 才会起 worker，而绝大多数用例只调 `New` + 直接驱动）。若确实同时存在，
// `pickLocked` 的认领是互斥的（同一条待办只会被其中一方取走），故不会重复执行。
func (m *Manager) drainRebuilds(ctx context.Context) int {
	if m == nil || m.rebuilds == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ran := 0
	for {
		m.rebuilds.mu.Lock()
		req := m.rebuilds.pickLocked()
		m.rebuilds.mu.Unlock()
		if req == nil {
			return ran
		}
		m.runRebuildRequest(ctx, req)
		ran++
	}
}

// RebuildLastResult 返回某源**最近一次**重建的结论（观测面；无记录时 ok=false）。
//
// 为什么需要：生产现场最难的判读是「上一次重建到底做完了没、卡在哪一天」——原文只能从
// VL 写入量与时序反推。本访问器把结论直接暴露出来（与 `LastReconcileReports` 同一取舍）。
func (m *Manager) RebuildLastResult(key string) (rebuildOutcome, bool) {
	if m == nil || m.rebuilds == nil {
		return rebuildOutcome{}, false
	}
	m.rebuilds.mu.Lock()
	defer m.rebuilds.mu.Unlock()
	out, ok := m.rebuilds.lastResults[key]
	if !ok {
		return rebuildOutcome{}, false
	}
	out.Days = append([]string(nil), out.Days...)
	return out, true
}
