package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ShardedManager 把 N 个独立的 bot-worker 子进程聚合为一个逻辑「Bot 管理器」。
//
// 动机：单 Node 进程承载 mineflayer bot 的数量有硬上限。实测 272 bot 时主线程持续
// 99.9% CPU、RSS 9.2GB，事件循环排不上 10s 心跳定时器 → stdout 停止输出 →
// 控制面判定容量快照过期（CAPACITY_SNAPSHOT_STALE）、availableBots 归零，新 bot 再也起不来。
// 分片后每片只承载 ~150 bot，心跳与事件循环保持可调度。
//
// 设计：组合而非改造 Manager —— 每片就是一个完整的 Manager（各自 spawn 一个 bot-worker
// 子进程、各自持有 IPC 与容量）。ShardedManager 只负责：
//   - 按片容量切分批次（ApplyBotBatch）
//   - 按 bot 归属路由单 bot 操作（StopBotBatch / SignalActions / SetBehavior / ...）
//   - 汇总容量与 fleet 快照
//   - 广播 session 级命令计划（CommandSchedule*）
//
// 片段数 = 1 时行为与直接用 Manager 完全一致（兼容旧部署）。
type ShardedManager struct {
	shards []*Manager
	// shardTotalCap 每片容量上限，用于分配时挑选目标片。
	shardTotalCap int
	maxBots       int

	mu sync.RWMutex
	// botShard 记录每个 bot 归属的片索引。批次分配后写入，停止/操作时据此路由。
	botShard map[string]int

	onEvent EventCallback
	extraMu sync.Mutex

	// shardCancels 各片 Start 时派生的 context 取消函数，Stop 时统一收束。
	// 按片下标存放（自愈重试会替换对应槽位）：一片一个 cancel，重试多少次都不会累积。
	shardCancels []context.CancelFunc

	// shardStates 各片启动健康记账（尝试次数 / 连续失败 / 退避窗口），与 shards 等长。
	// 由 mu 保护：自愈重试据此判断「现在该不该再试一次」，诊断面也读它。
	shardStates []shardHealth

	// stopped 由 Stop 置位：阻止自愈重试把已收束的分片重新拉起（否则会留下孤儿
	// goroutine 与孤儿 bot-worker 子进程）。
	stopped bool

	// startShard 可选的分片启动动作（仅测试注入，nil 时调用 Manager.Start），
	// 与 Manager.startFn 同一惯例：让「启动失败 → 自愈重试」可在不 spawn 真进程的前提下被测。
	startShard func(ctx context.Context, index int, shard *Manager) error

	// recoveryWait 单次自愈等待分片就绪的上限；retryBaseDelay / retryMaxDelay 为退避节奏。
	// 抽成字段而非常量，便于测试把时间窗压到毫秒级（生产取下方默认值）。
	recoveryWait   time.Duration
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration

	// 反查「哪个片持有该 bot」时的兜底：botShard 未命中则遍历各片（重启后重建场景）。
	rootCtx    context.Context
	rootCancel context.CancelFunc
}

// 分片自愈重试参数。
const (
	// shardRetryBaseDelay / shardRetryMaxDelay 是自愈重试的退避节奏（0.5s,1s,2s…上限 30s），
	// 与 Manager.runAutoRestart 的整进程重拉同节奏：既保证「依赖刚装齐」这类瞬时修复
	// 最多 30 秒内被感知到，也不会把 spawn 打成风暴（退避窗口内的请求直接快速失败）。
	shardRetryBaseDelay = 500 * time.Millisecond
	shardRetryMaxDelay  = 30 * time.Second
	// shardRecoveryWait 单次自愈里等待分片打出 worker-ready 的上限。
	// bot-worker 冷启动要先起 Node 再 import mineflayer（真机数百毫秒到数秒），
	// 取 10 秒既覆盖冷启动，又保证 CreateBot 这类同步调用不会被无限挂住。
	shardRecoveryWait = 10 * time.Second
)

// shardHealth 单片的启动健康记账（受 ShardedManager.mu 保护）。
type shardHealth struct {
	// launchAttempts 累计启动尝试次数（首次 Start + 各次自愈重试），诊断用。
	launchAttempts int
	// consecutiveFailures 连续失败次数，驱动指数退避；就绪后归零。
	consecutiveFailures int
	// lastError 最近一次启动失败原因；启动成功并确认就绪后清空。
	lastError string
	// lastAttemptAt 最近一次启动尝试时刻（诊断用）。
	lastAttemptAt time.Time
	// nextRetryAt 下一次允许自愈重试的时刻；退避窗口内到达的请求直接判定不可用。
	nextRetryAt time.Time
	// retrying 自愈重试进行中：防并发重入（多个 CreateBot 同时到达时只 spawn 一次）。
	retrying bool
}

// ShardHealthInfo 单片健康快照，供节点诊断面（运行时快照 / 排障日志）读取。
type ShardHealthInfo struct {
	Index               int
	Running             bool
	Ready               bool
	ActiveBots          int
	UnavailableReason   string
	LaunchAttempts      int
	ConsecutiveFailures int
	LastError           string
	LastAttemptAt       time.Time
	NextRetryAt         time.Time
	Retrying            bool
}

// ShardedManagerConfig 构造 ShardedManager。
type ShardedManagerConfig struct {
	// Shards 分片数；<=1 时为单进程（与现状一致）。
	Shards int
	// MaxBots 本节点总容量（各片均分）。
	MaxBots int
	// NewShard 构造单个分片 Manager 的工厂；由调用方注入（便于测试与复用装配逻辑）。
	NewShard func(index int, perShardMax int) *Manager
	// StartShard 可选：替换单片启动动作，nil 时直接调用 Manager.Start。
	// 仅测试注入用（与 Manager.startFn 同一惯例），生产路径不需要。
	StartShard func(ctx context.Context, index int, shard *Manager) error
}

// NewShardedManager 创建分片管理器。
func NewShardedManager(cfg ShardedManagerConfig) *ShardedManager {
	shards := cfg.Shards
	if shards <= 0 {
		shards = 1
	}
	maxBots := cfg.MaxBots
	if maxBots <= 0 {
		maxBots = 50
	}
	// 向上取整：500/3 => 167，3 片共 501 的容量，多出的一格无害（谁先用谁得）。
	perShard := (maxBots + shards - 1) / shards

	rootCtx, rootCancel := context.WithCancel(context.Background())
	s := &ShardedManager{
		shards:        make([]*Manager, 0, shards),
		shardTotalCap: perShard,
		maxBots:       maxBots,
		botShard:      make(map[string]int),
		rootCtx:       rootCtx,
		rootCancel:    rootCancel,
		// 与 shards 等长：启动记账与 cancel 槽位都按片下标寻址（见 launchShard）。
		shardStates:    make([]shardHealth, shards),
		shardCancels:   make([]context.CancelFunc, shards),
		startShard:     cfg.StartShard,
		recoveryWait:   shardRecoveryWait,
		retryBaseDelay: shardRetryBaseDelay,
		retryMaxDelay:  shardRetryMaxDelay,
	}
	for i := 0; i < shards; i++ {
		s.shards = append(s.shards, cfg.NewShard(i, perShard))
	}
	return s
}

// ShardCount 返回分片数（供日志与测试断言）。
func (s *ShardedManager) ShardCount() int { return len(s.shards) }

// SetBotWorkerPath 转发到所有分片。
func (s *ShardedManager) SetBotWorkerPath(p string) {
	for _, sh := range s.shards {
		sh.SetBotWorkerPath(p)
	}
}

// SetEventCallback 记录回调并转发到所有分片。
//
// 事件里补充 shardIndex，便于排障时区分事件来自哪一片。
func (s *ShardedManager) SetEventCallback(cb EventCallback) {
	s.extraMu.Lock()
	s.onEvent = cb
	s.extraMu.Unlock()
	for i, sh := range s.shards {
		idx := i
		sh.SetEventCallback(func(evt *BotWorkerEvent) {
			if evt != nil {
				evt.ShardIndex = idx
			}
			s.extraMu.Lock()
			inner := s.onEvent
			s.extraMu.Unlock()
			if inner != nil {
				inner(evt)
			}
		})
	}
}

// Start 启动所有分片。返回错误若任一片启动失败（已成功的片保持运行，便于部分可用）。
//
// 关键：每片用各自的 context（派生自 ShardedManager 的 rootCtx），而非共用调用方传入的 ctx。
// 原因：Manager.Start 内部会 context.WithCancel(ctx) 并在隔离不健康子进程时 cancel 它；
// 若各片共用同一父 ctx，任何一片的隔离操作都会波及所有片（真机表现：启动后仅剩 1 个
// bot-worker 进程，日志出现「stdin 阻塞，已隔离不健康子进程 error=context canceled」）。
// 分片生命周期归属 ShardedManager，Stop 时经 rootCancel 统一收束。
//
// 重复调用是幂等的「补齐失败片」：已运行的分片直接跳过。
// 必要性：上层 ensureBotManager 只在 IsRunning()==false 时才调 Start，而 IsRunning 是
// 「任一片在跑」——部分片启动成功后 Start 就再也不会被调用（issue #102 的永久性来源之一）；
// 即便被调用，对已运行片重复 Start 只会拿到「Bot 管理器已在运行」并让整体误判为失败。
func (s *ShardedManager) Start(_ context.Context) error {
	s.mu.RLock()
	stopped := s.stopped
	s.mu.RUnlock()
	if stopped {
		return fmt.Errorf("分片管理器已停止")
	}
	var firstErr error
	for i := range s.shards {
		if s.shards[i].IsRunning() {
			continue
		}
		if err := s.launchShardIfDue(i); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("分片 %d 启动失败: %w", i, err)
			}
		}
	}
	return firstErr
}

// launchShardIfDue 在退避窗口之外才真正启动分片；窗口内直接返回上次的真实失败原因。
//
// 为什么 Start 也要吃退避：上层 ensureBotManager 只在 IsRunning()==false 时调 Start，
// 若 Start 无条件尝试，则「全部片起不来」时每个 CreateBot 都会 spawn 一次——
// 退避就形同虚设。窗口内直接失败既不打爆进程，也仍然把「缺什么」告诉调用方。
func (s *ShardedManager) launchShardIfDue(i int) error {
	s.mu.RLock()
	h := s.shardStates[i]
	s.mu.RUnlock()
	if wait := time.Until(h.nextRetryAt); wait > 0 {
		reason := h.lastError
		if reason == "" {
			reason = "分片未就绪"
		}
		return fmt.Errorf("%s（%.0f 秒后自动重试）", reason, wait.Seconds())
	}
	return s.launchShard(i)
}

// launchShard 为单片派生独立 ctx 并启动，同时把失败记进健康账本（含退避窗口）。
//
// ctx 必须派生自 rootCtx：既不能沿用调用方 ctx（会重演「任一片取消波及全部片 /
// gRPC 请求结束即杀子进程」事故），也不能用自愈等待的超时 ctx——后者会成为子进程
// ctx 的祖先，超时一到 exec.CommandContext 会直接把刚拉起来的 bot-worker kill 掉。
func (s *ShardedManager) launchShard(i int) error {
	sh := s.shards[i]
	shardCtx, cancel := context.WithCancel(s.rootCtx)

	s.mu.Lock()
	if s.stopped {
		// Stop 与重试并发：已收束就不能再起子进程（否则留下孤儿进程/goroutine）。
		s.mu.Unlock()
		cancel()
		return fmt.Errorf("分片管理器已停止")
	}
	// 替换而非追加：一片始终只有一个有效 cancel，重试多少次都不会累积泄漏。
	s.shardCancels[i] = cancel
	h := &s.shardStates[i]
	h.launchAttempts++
	h.lastAttemptAt = time.Now()
	start := s.startShard
	s.mu.Unlock()

	var err error
	if start != nil {
		err = start(shardCtx, i, sh)
	} else {
		err = sh.Start(shardCtx)
	}
	if err != nil {
		// 「已在运行」不是失败：并发的 ensureBotManager 或另一路自愈已经把它拉起来了。
		// 记成失败会让诊断面误报，并平白给它加一段退避窗口。
		if strings.Contains(err.Error(), "已在运行") {
			// 本次派生的 ctx 没有被任何人采用（Manager 在采用前就返回了），直接收掉；
			// 真正在跑的那份 ctx 仍由 rootCtx 级联管理，Stop 不会漏掉它。
			cancel()
			return nil
		}
		s.recordShardFailure(i, err)
		return err
	}
	s.mu.Lock()
	s.shardStates[i].lastError = ""
	s.mu.Unlock()
	return nil
}

// recordShardFailure 记一次启动失败并算出下一次允许重试的时刻。
func (s *ShardedManager) recordShardFailure(i int, err error) {
	s.mu.Lock()
	h := &s.shardStates[i]
	h.consecutiveFailures++
	h.lastError = err.Error()
	h.nextRetryAt = time.Now().Add(s.backoffLocked(h.consecutiveFailures))
	attempts, failures, nextRetryAt := h.launchAttempts, h.consecutiveFailures, h.nextRetryAt
	s.mu.Unlock()
	slog.Warn("bot-worker 分片启动失败，已进入退避窗口",
		"shard", i, "attempts", attempts, "consecutiveFailures", failures,
		"nextRetryAt", nextRetryAt, "error", err)
}

// recordShardReady 清空失败记账：分片已经就绪，失败计数与退避窗口都不再有意义。
func (s *ShardedManager) recordShardReady(i int) {
	s.mu.Lock()
	h := &s.shardStates[i]
	h.consecutiveFailures = 0
	h.lastError = ""
	h.nextRetryAt = time.Time{}
	s.mu.Unlock()
}

// backoffLocked 指数退避：0.5s,1s,2s,4s…上限 30s（须持有 s.mu）。
//
// 首次失败不留窗口（返回 0）：节点「刚装齐依赖」是最常见的现场，允许紧跟着的那次
// 请求立刻再试一把；从第二次失败起才进入退避，避免每次 CreateBot 都狂 spawn。
func (s *ShardedManager) backoffLocked(failures int) time.Duration {
	if failures <= 1 {
		return 0
	}
	base, maxDelay := s.retryBaseDelay, s.retryMaxDelay
	if base <= 0 || maxDelay <= 0 {
		return 0
	}
	// 位移前先封顶，避免 failures 很大时移位溢出算出负数。
	if failures-2 >= 8 {
		return maxDelay
	}
	delay := base << (failures - 2)
	if delay > maxDelay || delay <= 0 {
		return maxDelay
	}
	return delay
}

// recoverUnreadyShards 对未就绪分片做一次惰性自愈（带退避），并等待其就绪。
//
// 为什么惰性而不是常驻后台重试：
//   - 触发点就是「真的要容量」的入口（ApplyBotBatch / CreateBots / WaitReady），没有 Bot
//     需求时不会白白起进程；也就不需要额外的后台 goroutine 及其生命周期管理。
//   - 收束简单：所有重试都派生自 rootCtx，Stop 一取消全部退出，不存在泄漏。
//
// 为什么不会打爆：每片独立记账，同一退避窗口内的重复调用直接跳过（不发 spawn）；
// 同时用 retrying 标记做单飞，多个 CreateBot 并发到达也只会触发一次启动尝试。
// 为什么不会卡死调用方：整次自愈的等待上限是 recoveryWait（默认 10s），且调用方 ctx
// 结束时立刻放弃等待；这段等待也不等同于子进程 ctx——超时只放弃等待，不会 kill 已拉起的
// bot-worker（若让等待 ctx 当子进程的祖先，超时一到 exec.CommandContext 会直接杀进程）。
func (s *ShardedManager) recoverUnreadyShards(parent context.Context, trigger string) {
	if len(s.shards) == 0 {
		return
	}
	now := time.Now()
	// 先在无锁状态读各片就绪位：Manager 的容量快照要拿 Manager 自己的锁，而 Manager.Start
	// 会在 spawn 期间长时间持有它。若在持有 s.mu 时去读，整个路由/记账面都会被一次
	// spawn 卡住（心跳、容量快照、归属登记全部排队）。
	ready := make([]bool, len(s.shards))
	for i := range s.shards {
		ready[i] = s.shards[i].CapacitySnapshot().Ready
	}

	var pending []int
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	for i := range s.shards {
		// 就绪的片直接跳过：健康路径零副作用，既有行为完全不变。
		if ready[i] {
			continue
		}
		h := &s.shardStates[i]
		if h.retrying {
			// 已有调用在重试这一片：让它负责，本次不重复 spawn。
			continue
		}
		if now.Before(h.nextRetryAt) {
			// 退避窗口内：本次直接判定不可用，由调用方快速失败（错误里会带根因）。
			continue
		}
		h.retrying = true
		pending = append(pending, i)
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}

	// 等待窗口派生自 rootCtx：Stop 时立刻收束在途等待，不留悬挂 goroutine。
	waitCtx, cancel := context.WithTimeout(s.rootCtx, s.recoveryWait)
	defer cancel()
	if parent != nil {
		// 尊重调用方 ctx：请求已被取消时不再替它挡在这里等自愈。
		stop := context.AfterFunc(parent, cancel)
		defer stop()
	}
	var wg sync.WaitGroup
	for _, idx := range pending {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.retryShard(waitCtx, i, trigger)
		}(idx)
	}
	wg.Wait()
}

// retryShard 单片自愈：未运行则重新拉起，然后等它打出 worker-ready。
func (s *ShardedManager) retryShard(ctx context.Context, idx int, trigger string) {
	sh := s.shards[idx]
	defer func() {
		// 无论成败都要放掉单飞标记，否则该片再也不会被重试。
		s.mu.Lock()
		s.shardStates[idx].retrying = false
		s.mu.Unlock()
	}()

	if !sh.IsRunning() {
		if err := s.launchShard(idx); err != nil {
			// launchShard 已记账并写好退避窗口，这里只留一条带触发来源的日志。
			slog.Warn("bot-worker 分片自愈重试失败，等待退避后再次尝试",
				"shard", idx, "trigger", trigger, "error", err)
			return
		}
		slog.Info("bot-worker 分片自愈已重新拉起，等待就绪", "shard", idx, "trigger", trigger)
	}
	// 已运行但未就绪（例如刚 spawn、正在 import mineflayer，或上层刚拉起来）：
	// 这里只等就绪、不重复启动，避免把正在启动的进程打成一团。
	if err := sh.WaitReady(ctx); err != nil {
		if s.rootCtx.Err() != nil {
			// 管理器已收束（Stop）：这不是分片的错，别把「context canceled」记成启动失败，
			// 否则诊断面会残留一条误导性的失败原因。
			return
		}
		s.recordShardFailure(idx, fmt.Errorf("等待分片就绪失败: %w", err))
		slog.Warn("bot-worker 分片自愈后仍未就绪", "shard", idx, "trigger", trigger, "error", err)
		return
	}
	s.recordShardReady(idx)
	slog.Info("bot-worker 分片已恢复就绪，容量重新可用", "shard", idx, "trigger", trigger)
}

// ShardHealth 返回各片健康与启动重试状态（只读，不触发任何重试）。
//
// 存在的意义：issue #102 的现场是「分片启动失败后容量永久为 0，但诊断面只能看到
// 笼统的容量不足」。把「哪一片、试了几次、上次为什么失败、下次何时重试」暴露出来，
// 运维无需重启 worker 就能判断是依赖未装齐还是别的原因。
func (s *ShardedManager) ShardHealth() []ShardHealthInfo {
	// 先复制记账再释放锁：查询分片要拿 Manager 的锁（可能正被一次 spawn 持有），
	// 不能在持有 s.mu 时去等它。
	s.mu.RLock()
	states := append([]shardHealth(nil), s.shardStates...)
	s.mu.RUnlock()

	out := make([]ShardHealthInfo, 0, len(s.shards))
	for i, sh := range s.shards {
		c := sh.CapacitySnapshot()
		info := ShardHealthInfo{
			Index:             i,
			Running:           sh.IsRunning(),
			Ready:             c.Ready,
			ActiveBots:        c.ActiveBots,
			UnavailableReason: c.UnavailableReason,
		}
		if i < len(states) {
			h := states[i]
			info.LaunchAttempts = h.launchAttempts
			info.ConsecutiveFailures = h.consecutiveFailures
			info.LastError = h.lastError
			info.LastAttemptAt = h.lastAttemptAt
			info.NextRetryAt = h.nextRetryAt
			info.Retrying = h.retrying
		}
		out = append(out, info)
	}
	return out
}

// unavailableDetail 汇总未就绪分片的根因，供「容量不足」类错误直接带出原因。
// 运维首先要知道的是「哪一片、为什么没起来」，而不是笼统的容量不足。
func (s *ShardedManager) unavailableDetail() string {
	parts := make([]string, 0, len(s.shards))
	for _, info := range s.ShardHealth() {
		if info.Ready {
			continue
		}
		reason := info.LastError
		if reason == "" {
			reason = info.UnavailableReason
		}
		if reason == "" {
			reason = "未就绪"
		}
		parts = append(parts, fmt.Sprintf("分片 %d: %s", info.Index, reason))
	}
	if len(parts) == 0 {
		return ""
	}
	// 只返回「分片 N: 原因」列表，由调用方拼在自己的错误文案后面
	// （如「分片容量不足：分片 0: 依赖未装齐…」）。
	return strings.Join(parts, "；")
}

// Stop 停止所有分片。
func (s *ShardedManager) Stop() {
	s.mu.Lock()
	// 先置位 stopped：在途自愈重试必须在下一步 rootCancel 之前就放弃，
	// 否则它可能刚好在这个时间窗里又拉起一个新子进程。
	s.stopped = true
	cancels := s.shardCancels
	s.shardCancels = make([]context.CancelFunc, len(s.shards))
	s.mu.Unlock()
	s.rootCancel()
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
	for _, sh := range s.shards {
		sh.Stop()
	}
}

// IsRunning 返回是否至少有一片在运行。
func (s *ShardedManager) IsRunning() bool {
	for _, sh := range s.shards {
		if sh.IsRunning() {
			return true
		}
	}
	return false
}

// SubscribeEvents 把所有分片的事件汇聚到单一通道，供上层统一消费。
//
// 各片事件已由 SetEventCallback 补写 ShardIndex，上层可据此区分来源。
// 返回的 cancel 会关闭该汇聚通道并断开所有分片订阅。
func (s *ShardedManager) SubscribeEvents(buffer int) (<-chan *BotWorkerEvent, func()) {
	out := make(chan *BotWorkerEvent, buffer)
	cancels := make([]func(), 0, len(s.shards))
	var closeOnce sync.Once
	var wg sync.WaitGroup

	closeOut := func() {
		closeOnce.Do(func() {
			for _, c := range cancels {
				c()
			}
			wg.Wait()
			close(out)
		})
	}

	for i, sh := range s.shards {
		idx := i
		events, cancel := sh.SubscribeEvents(buffer)
		cancels = append(cancels, cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for evt := range events {
				if evt != nil {
					evt.ShardIndex = idx
				}
				// 上层通道满时丢弃而非阻塞：阻塞会卡住该片的事件泵，
				// 进而拖慢 bot-worker 的 stdout 消费。
				select {
				case out <- evt:
				default:
				}
			}
		}()
	}
	return out, closeOut
}

// WaitReady 等待所有分片就绪。
//
// 未就绪的片先触发一次惰性自愈（带退避），再逐个等待：这样「依赖刚装齐」的节点不必
// 重启 worker，一次正常调用就能把容量救回来。已就绪的片不受影响（recoverUnreadyShards
// 只处理未就绪片），因此健康路径的行为与之前完全一致。
func (s *ShardedManager) WaitReady(ctx context.Context) error {
	s.recoverUnreadyShards(ctx, "wait-ready")
	for i, sh := range s.shards {
		if err := sh.WaitReady(ctx); err != nil {
			// 带上该片的启动失败根因：否则上层只能看到「分片 N 未就绪」，
			// 无法判断是依赖未装齐、node 缺失还是仍在启动。
			return fmt.Errorf("分片 %d 未就绪: %w%s", i, err, s.shardFailureSuffix(i))
		}
	}
	return nil
}

// shardFailureSuffix 返回单片最近一次启动失败的补充说明（无失败记录时为空）。
func (s *ShardedManager) shardFailureSuffix(i int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i >= len(s.shardStates) {
		return ""
	}
	h := s.shardStates[i]
	if h.lastError == "" {
		return ""
	}
	if h.nextRetryAt.IsZero() {
		return "（上次启动失败: " + h.lastError + "）"
	}
	return fmt.Sprintf("（上次启动失败: %s；%s 后自动重试）", h.lastError, time.Until(h.nextRetryAt).Round(time.Second))
}

// CapacitySnapshot 汇总各片容量。
//
// 汇总语义：
//   - Max/Active/Connecting/RSS：求和（逻辑总量）
//   - EventLoopP95Ms：取最大值（最差片，用于告警阈值不受单片掩盖）
//   - ObservedAt：取最早（最保守，避免某片停更时整体仍显新鲜）
//   - Ready：全员就绪才算真；部分就绪时保留可用性但标注原因
//   - WorkerEpoch：主片值 + 分片数后缀（字符串比对语义，见设计说明）
func (s *ShardedManager) CapacitySnapshot() BotCapacitySnapshot {
	if len(s.shards) == 0 {
		return BotCapacitySnapshot{}
	}
	out := BotCapacitySnapshot{
		Ready:      true,
		MaxBots:    s.maxBots,
		ObservedAt: time.Time{},
	}
	var epoch string
	allReady := true
	var unreadyDetails []string
	for i, sh := range s.shards {
		c := sh.CapacitySnapshot()
		if i == 0 {
			epoch = c.WorkerEpoch
		}
		out.ActiveBots += c.ActiveBots
		out.ConnectingBots += c.ConnectingBots
		out.RSSBytes += c.RSSBytes
		out.DroppedEvents += c.DroppedEvents
		if c.EventLoopP95Ms > out.EventLoopP95Ms {
			out.EventLoopP95Ms = c.EventLoopP95Ms
		}
		// 取最早的非零时刻：任一片停更都应让整体看起来「不够新」。
		if !c.ObservedAt.IsZero() && (out.ObservedAt.IsZero() || c.ObservedAt.Before(out.ObservedAt)) {
			out.ObservedAt = c.ObservedAt
		}
		if i == 0 || c.WorkerEpochGeneration > out.WorkerEpochGeneration {
			out.WorkerEpochGeneration = c.WorkerEpochGeneration
		}
		// CapacityGeneration 求和保证单调：任一片换代都会让总量增大，CP 侧据此识别计划失效。
		out.CapacityGeneration += c.CapacityGeneration
		if i == 0 {
			out.BotWorkerVersion = c.BotWorkerVersion
			out.Features = append([]string(nil), c.Features...)
			out.Legacy = c.Legacy
		}
		if !c.Ready {
			allReady = false
			// 逐片列出未就绪原因：只报「分片 N 未就绪」会让运维分不清是依赖未装齐、
			// node 缺失，还是仅仅仍在启动。
			reason := c.UnavailableReason
			s.mu.RLock()
			if i < len(s.shardStates) && s.shardStates[i].lastError != "" {
				// 启动失败时容量快照只有「尚未就绪」这种笼统原因，真实错误在启动记账里。
				reason = s.shardStates[i].lastError
			}
			s.mu.RUnlock()
			if reason == "" {
				reason = "未就绪"
			}
			unreadyDetails = append(unreadyDetails, fmt.Sprintf("分片 %d: %s", i, reason))
		}
	}
	out.Ready = allReady
	if !allReady {
		// 部分片未就绪：整体标不可用，原因指出是分片级问题，避免误判为整机故障。
		out.UnavailableReason = fmt.Sprintf("部分 bot-worker 分片未就绪: %s", strings.Join(unreadyDetails, "；"))
	}
	if len(s.shards) > 1 {
		// 复合标识：CP 侧只做字符串比对与展示，加后缀可区分多片部署。
		out.WorkerEpoch = fmt.Sprintf("%s:%d", epoch, len(s.shards))
	} else {
		out.WorkerEpoch = epoch
	}
	return out
}

// RuntimeSnapshot 聚合各片运行时视图（PID 取主片，运行态为「任一片在跑」）。
func (s *ShardedManager) RuntimeSnapshot() RuntimeSnapshot {
	if len(s.shards) == 0 {
		return RuntimeSnapshot{}
	}
	primary := s.shards[0].RuntimeSnapshot()
	out := primary
	out.Running = s.IsRunning()
	out.ShardPIDs = make([]int, 0, len(s.shards))
	for _, sh := range s.shards {
		out.ShardPIDs = append(out.ShardPIDs, sh.RuntimeSnapshot().PID)
	}
	out.Capacity = s.CapacitySnapshot()
	// 分片级健康明细：诊断面据此看出「哪一片没起来、失败几次、下次何时重试」。
	out.Shards = s.ShardHealth()
	return out
}

// routeShard 返回 bot 所在的片索引；未登记时遍历各片兜底（重启后重建场景）。
func (s *ShardedManager) routeShard(botID string) (int, *Manager) {
	s.mu.RLock()
	idx, ok := s.botShard[botID]
	s.mu.RUnlock()
	if ok && idx >= 0 && idx < len(s.shards) {
		return idx, s.shards[idx]
	}
	for i, sh := range s.shards {
		if _, exists := sh.GetBot(botID); exists {
			return i, sh
		}
	}
	return -1, nil
}

// rememberShard 记录 bot 归属。
func (s *ShardedManager) rememberShard(botID string, shard int) {
	s.mu.Lock()
	s.botShard[botID] = shard
	s.mu.Unlock()
}

// forgetShard 移除 bot 归属。
func (s *ShardedManager) forgetShard(botID string) {
	s.mu.Lock()
	delete(s.botShard, botID)
	s.mu.Unlock()
}

// pickShard 选当前负载最低且仍有余量的片；返回 nil 表示全满。
//
// 用「ActiveBots 最少」而非轮询：bot 分布天然随批次到达顺序漂移，按实际负载挑选
// 能自动纠偏，且不依赖历史分配记录。
func (s *ShardedManager) pickShard() (int, *Manager) {
	idx := s.pickShardWithPending(nil)
	if idx < 0 {
		return -1, nil
	}
	return idx, s.shards[idx]
}

// pickShardWithPending 在 pickShard 基础上叠加「本批已分配但未下发」的计数。
//
// 必要性：批次内连续分配时，各片 CapacitySnapshot 尚未反映刚分配出去的 bot
// （它们要等 RPC 下发后才计入 ActiveBots），只看快照会把整批塞进同一片。
// pending 为空时退化为普通 pickShard。
func (s *ShardedManager) pickShardWithPending(pending map[int]int) int {
	bestIdx := -1
	bestLoad := 0
	for i, sh := range s.shards {
		c := sh.CapacitySnapshot()
		if !c.Ready {
			continue
		}
		load := c.ActiveBots + c.ConnectingBots
		if pending != nil {
			load += pending[i]
		}
		if load >= s.shardTotalCap {
			continue
		}
		if bestIdx == -1 || load < bestLoad {
			bestIdx, bestLoad = i, load
		}
	}
	return bestIdx
}

// shardFree 返回该片剩余可容纳的 bot 数。
func (s *ShardedManager) shardFree(m *Manager) int {
	c := m.CapacitySnapshot()
	free := s.shardTotalCap - c.ActiveBots - c.ConnectingBots
	if free < 0 {
		return 0
	}
	return free
}

// PendingRequestCount 汇总各片在途请求数。
func (s *ShardedManager) PendingRequestCount() int {
	total := 0
	for _, sh := range s.shards {
		total += sh.PendingRequestCount()
	}
	return total
}

// CircuitOpen 任一片熔断即视为整体熔断（避免向已知有问题的片继续下发）。
func (s *ShardedManager) CircuitOpen() bool {
	for _, sh := range s.shards {
		if sh.CircuitOpen() {
			return true
		}
	}
	return false
}
