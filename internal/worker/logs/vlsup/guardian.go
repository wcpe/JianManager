package vlsup

// 运行期自愈看护（runtime guardian）。
//
// 为什么需要：Supervisor 过去只在**启动路径**做一次孤儿回收（见 reap.go），运行期没有任何
// 探活。`reconcileExitedLocked` 只在有人读状态时把账面改成 FAILED，并不会拉起进程。于是
// VictoriaLogs 因 OOM、段损坏等原因在运行期崩溃后，采集与查询会**长期降级**，直到人工重启
// Worker——日志文件本身还在磁盘上，缺的只是"把进程重新拉起来"这一步。
//
// 设计取向：**保守优先，宁可不重启也不放大故障**。
//   - 只重启「此前健康、现已退出」的实例：本代进程必须稳定存活超过 StableWindow，
//     存活不足窗口的（启动即崩/秒退）按启动失败处理，不进重启循环；
//   - 指数退避 + 连续重启上限：达上限后熔断，明确告警并停手，绝不无限重试；
//   - 与显式操作互斥：Start/Stop 用 epoch 与 opMu 排序，停止中/刚切换资产时绝不拉起；
//   - 可关停：不调用 StartGuardian 就是完全关闭（零 goroutine、零定时器）。
//
// 不做什么：不做 HTTP 探活。进程存活与本包既有的 Health 语义是两件事（契约 §5.3），
// 把健康探测塞进重启判据会让"VL 正在重建索引但进程活着"被误判成崩溃并杀死。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// 看护默认参数。取值理由见 GuardianOptions 各字段注释。
const (
	// DefaultGuardianInterval 探活间隔。
	//
	// 15s 是"崩溃后恢复时延"与"空转开销"的折中：判活只是读一个原子布尔（Process.Exited），
	// 不碰磁盘也不发 HTTP，15s 一次的开销可以忽略；更短则对恢复速度没有实质帮助
	// （VL 冷启动本身就要数秒）。
	DefaultGuardianInterval = 15 * time.Second
	// DefaultGuardianStableWindow 判定「此前健康」的最短存活时间。
	//
	// 30s 排除掉"启动→秒退"的一类失败：这类问题该由启动路径的存活窗口（750ms）与上游
	// 资产校验处理，若让看护也参与，就会变成"启动失败 → 看护重启 → 再失败"的自激循环。
	DefaultGuardianStableWindow = 30 * time.Second
	// DefaultGuardianMaxRestarts 连续自动重启次数上限。
	//
	// 5 次配合下面的退避，最坏情况下 5 分钟内试完就停手熔断：既给瞬时故障留出恢复机会，
	// 又不会让"段损坏导致必然启动失败"的场景把 CPU 与磁盘打满。
	DefaultGuardianMaxRestarts = 5
	// DefaultGuardianBaseBackoff 首次退避。
	DefaultGuardianBaseBackoff = 5 * time.Second
	// DefaultGuardianMaxBackoff 退避封顶。
	//
	// 封顶 5 分钟：即便上限被调得很大，单实例的重启频率也永远不会高于每 5 分钟一次。
	DefaultGuardianMaxBackoff = 5 * time.Minute
)

// ErrGuardianRunning 表示看护已在运行；重复启动被拒绝，避免两条循环并发重启同一实例。
var ErrGuardianRunning = errors.New("vlsup: 运行期看护已在运行")

// errGuardAborted 表示本轮自愈在拿到操作锁后发现"人工操作已接管"，应静默放弃。
// 它不是故障：不能计入重启失败次数，否则人工正常停止会被看护记成一次失败尝试。
var errGuardAborted = errors.New("vlsup: 看护重启被显式操作打断")

// GuardianOptions 是运行期看护的参数。零值表示全部取默认值。
//
// 关闭方式：不调用 StartGuardian 即可（Supervisor 本身不会自动启动看护）。
type GuardianOptions struct {
	// Interval 探活间隔；0 用 DefaultGuardianInterval。
	Interval time.Duration
	// StableWindow 进程至少存活这么久才被视为「此前健康」；0 用 DefaultGuardianStableWindow。
	StableWindow time.Duration
	// MaxRestarts 连续自动重启上限；0 用 DefaultGuardianMaxRestarts。
	// 达到上限后看护熔断停手并告警，直到显式 Start（或 Worker 重启）才重新武装。
	MaxRestarts int
	// BaseBackoff 首次失败后的退避；0 用 DefaultGuardianBaseBackoff。
	BaseBackoff time.Duration
	// MaxBackoff 退避封顶；0 用 DefaultGuardianMaxBackoff。
	MaxBackoff time.Duration
	// Now 时钟注入（测试用假时钟）；nil 用 time.Now。
	Now func() time.Time
}

// withDefaults 补齐默认值；非法值（负数、过短）按默认处理而不是报错——看护不应成为启动阻碍。
func (o GuardianOptions) withDefaults() GuardianOptions {
	if o.Interval <= 0 {
		o.Interval = DefaultGuardianInterval
	}
	if o.StableWindow <= 0 {
		o.StableWindow = DefaultGuardianStableWindow
	}
	if o.MaxRestarts <= 0 {
		o.MaxRestarts = DefaultGuardianMaxRestarts
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = DefaultGuardianBaseBackoff
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = DefaultGuardianMaxBackoff
	}
	if o.MaxBackoff < o.BaseBackoff {
		o.MaxBackoff = o.BaseBackoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// now 返回当前时刻（假时钟注入点）。
func (o GuardianOptions) now() time.Time { return o.Now() }

// backoff 返回第 attempt 次失败后的退避时长（attempt 从 1 起）。
//
// 指数增长并封顶：5s → 10s → 20s → 40s → 80s …（不超过 MaxBackoff）。
// 为什么要退避：VL 崩溃常见诱因（段损坏、端口被占）不会自愈，没有退避时看护会把
// "必然失败的启动"变成高频重试，反而拖慢同机其它进程。
func (o GuardianOptions) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := o.BaseBackoff
	for i := 1; i < attempt; i++ {
		if d >= o.MaxBackoff/2 {
			return o.MaxBackoff
		}
		d *= 2
	}
	if d > o.MaxBackoff {
		d = o.MaxBackoff
	}
	return d
}

// StartGuardian 启动运行期看护循环（幂等保护：已在运行则返回 ErrGuardianRunning）。
//
// ctx 取消即停止（Worker 退出时不留残余 goroutine）。返回 nil 只表示循环已启动，
// 不代表任何实例健康。
func (s *Supervisor) StartGuardian(ctx context.Context, opts GuardianOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = opts.withDefaults()
	s.guardMu.Lock()
	if s.guardCancel != nil {
		s.guardMu.Unlock()
		return ErrGuardianRunning
	}
	s.guardGen++
	gen := s.guardGen
	gctx, cancel := context.WithCancel(ctx)
	s.guardCancel = cancel
	s.guardOpts = opts
	s.guardMu.Unlock()

	slog.Info("VictoriaLogs 运行期看护已启用",
		"interval", opts.Interval,
		"stableWindow", opts.StableWindow,
		"maxRestarts", opts.MaxRestarts,
		"baseBackoff", opts.BaseBackoff,
		"maxBackoff", opts.MaxBackoff)
	go s.guardLoop(gctx, opts, gen)
	return nil
}

// StopGuardian 停止看护循环（幂等）。停止不会改动任何实例的进程状态。
func (s *Supervisor) StopGuardian() {
	s.guardMu.Lock()
	cancel := s.guardCancel
	s.guardCancel = nil
	s.guardMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// guardLoop 是看护的周期主体。
func (s *Supervisor) guardLoop(ctx context.Context, opts GuardianOptions, gen int) {
	// 只清理「本代」的句柄：旧循环延迟退出时不能把新一代的 cancel 抹成 nil，
	// 否则新一代将无法被 StopGuardian 停止（goroutine 泄漏）。
	defer func() {
		s.guardMu.Lock()
		if s.guardGen == gen {
			s.guardCancel = nil
		}
		s.guardMu.Unlock()
	}()
	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkOnce(ctx, opts)
		}
	}
}

// currentGuardOptions 返回当前生效的看护参数（未启动时给默认值，便于外部手动触发一轮）。
func (s *Supervisor) currentGuardOptions() GuardianOptions {
	s.guardMu.Lock()
	defer s.guardMu.Unlock()
	return s.guardOpts.withDefaults()
}

// CheckOnce 手动执行一轮看护。供后台循环与运维/测试复用（逐轮推进，不依赖真实 ticker）。
func (s *Supervisor) CheckOnce(ctx context.Context) {
	s.checkOnce(ctx, s.currentGuardOptions())
}

// checkOnce 是单轮探活：遍历全部受管 namespace，对判定为「崩溃」的实例尝试重启。
//
// opts 作为参数传入而非读字段，是为了让测试能用假时钟确定性地推进，不受后台循环参数影响。
func (s *Supervisor) checkOnce(ctx context.Context, opts GuardianOptions) {
	now := opts.now()
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		s.checkNamespace(ctx, ns, opts, now)
	}
}

// checkNamespace 处理单个 namespace 的一轮看护。
func (s *Supervisor) checkNamespace(ctx context.Context, ns Namespace, opts GuardianOptions, now time.Time) {
	s.mu.Lock()
	inst, ok := s.instances[ns]
	if !ok {
		s.mu.Unlock()
		return
	}

	// ① 先处理"已经稳定存活"的实例：这是重启计数唯一的归零条件。
	//    启动成功不清零——否则"启动→秒崩"的循环每次都会把计数抹回 0，退避与上限全部失效。
	if inst.state == StateRunning && inst.proc != nil && !inst.proc.Exited() &&
		!inst.startedAt.IsZero() && now.Sub(inst.startedAt) >= opts.StableWindow {
		inst.stableObserved = true
		if inst.restartAttempts != 0 || inst.restartsSuspended || inst.lastRestartErr != "" {
			inst.restartAttempts = 0
			inst.restartsSuspended = false
			inst.nextRestartAt = time.Time{}
			inst.lastRestartErr = ""
			s.syncGuardStatusLocked(inst)
		}
	}

	// ② 崩溃判定：账面还持有进程句柄、句柄已退出，且账面不是"显式停止"。
	//    允许 FAILED：reconcileExitedLocked 会在状态被读取时先把 RUNNING 改成 FAILED，
	//    两种账面都必须能触发自愈，否则"控制面刚查过状态"反而会让自愈失效。
	crashed := inst.proc != nil && inst.proc.Exited() &&
		(inst.state == StateRunning || inst.state == StateFailed)
	if !crashed || inst.restartsSuspended || inst.startedAt.IsZero() {
		s.mu.Unlock()
		return
	}

	// ③ 资格仅来自活态曾过窗或真实退出证据；死亡后经过的墙钟不计为存活。
	stable := inst.stableObserved
	if handle, ok := inst.proc.(ExecHandle); ok {
		snapshot := handle.Snapshot()
		stable = stable || (!snapshot.StartedAt.IsZero() && !snapshot.ExitedAt.IsZero() && snapshot.ExitedAt.Sub(snapshot.StartedAt) >= opts.StableWindow)
	}
	if !stable {
		s.mu.Unlock()
		return
	}

	// ④ 退避未到期则跳过（退避在每次尝试前就写入，见 ⑥）。
	if now.Before(inst.nextRestartAt) {
		s.mu.Unlock()
		return
	}

	// ⑤ 达上限：熔断 + 明确告警（含尝试次数与最后错误），不静默。
	if inst.restartAttempts >= opts.MaxRestarts {
		inst.restartsSuspended = true
		attempts := inst.restartAttempts
		lastErr := inst.lastRestartErr
		msg := fmt.Sprintf("vlsup: %s 自动重启已达上限（%d 次），已停止自愈，需人工介入", ns, attempts)
		if lastErr != "" {
			msg += "；最后一次错误：" + lastErr
		}
		inst.status.LastError = msg
		s.syncGuardStatusLocked(inst)
		s.mu.Unlock()

		slog.Error("VictoriaLogs 自动重启熔断",
			"namespace", ns, "attempts", attempts, "maxRestarts", opts.MaxRestarts, "lastError", lastErr)
		s.reportGuardFailure(ns, errors.New(msg))
		return
	}

	// ⑥ 先记账再动手：尝试次数与退避在任何 I/O 之前写好，即使 Start 长时间阻塞
	//    （例如磁盘卡住）也不会让同一崩溃被反复计成"还没试过"。
	attempt := inst.restartAttempts + 1
	epoch := inst.epoch
	inst.restartAttempts = attempt
	inst.nextRestartAt = now.Add(opts.backoff(attempt))
	s.syncGuardStatusLocked(inst)
	s.mu.Unlock()

	err := s.safeError(s.restartCrashed(ctx, ns, epoch))

	switch {
	case errors.Is(err, errGuardAborted):
		// 期间被显式 Stop/Start 接管：撤回本轮计数，避免把人工操作记成看护失败。
		s.mu.Lock()
		if cur, ok := s.instances[ns]; ok && cur.epoch == epoch {
			cur.restartAttempts = attempt - 1
			s.syncGuardStatusLocked(cur)
		}
		s.mu.Unlock()
		return

	case err != nil:
		s.mu.Lock()
		if cur, ok := s.instances[ns]; ok {
			cur.lastRestartErr = err.Error()
			cur.status.LastError = fmt.Sprintf("vlsup: %s 自动重启失败（第 %d/%d 次）：%v",
				ns, attempt, opts.MaxRestarts, err)
			s.syncGuardStatusLocked(cur)
		}
		s.mu.Unlock()
		slog.Error("VictoriaLogs 自动重启失败",
			"namespace", ns, "attempt", attempt, "maxRestarts", opts.MaxRestarts, "error", err)
		s.reportGuardFailure(ns, fmt.Errorf("vlsup: %s 自动重启失败（第 %d/%d 次）：%w",
			ns, attempt, opts.MaxRestarts, err))

	default:
		s.mu.Lock()
		if cur, ok := s.instances[ns]; ok {
			cur.lastRestartErr = ""
			s.syncGuardStatusLocked(cur)
		}
		s.mu.Unlock()
		slog.Warn("VictoriaLogs 已在崩溃后自动重启",
			"namespace", ns, "attempt", attempt, "maxRestarts", opts.MaxRestarts)
	}
}

// restartCrashed 在操作锁内复核后拉起已退出的实例。
//
// 复核是必要的：从"判定崩溃"到真正重启之间可能已有人调过 Stop（例如 StopAll 退出路径、
// 资产切换）。epoch 变了说明人工操作在先，本轮直接放弃；状态已不是 RUNNING/FAILED 同理。
func (s *Supervisor) restartCrashed(ctx context.Context, ns Namespace, epoch uint64) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	inst, ok := s.instances[ns]
	if !ok || inst.epoch != epoch {
		s.mu.Unlock()
		return errGuardAborted
	}
	if inst.state != StateRunning && inst.state != StateFailed {
		s.mu.Unlock()
		return errGuardAborted
	}
	if inst.proc != nil && !inst.proc.Exited() {
		// 已经有存活进程（例如人工刚拉起），不应重复启动。
		s.mu.Unlock()
		return errGuardAborted
	}
	// 旧句柄已退出（其 Wait 回收者已结束），保留引用直到 Start 成功替换，
	// 这样即使本轮启动失败，下一轮仍能凭"proc 非空且已退出"再次识别为崩溃。
	if inst.state == StateRunning {
		inst.state = StateFailed
		inst.status.State = StateFailed
	}
	s.mu.Unlock()

	return s.start(ctx, ns)
}

// syncGuardStatusLocked 把看护账面同步到对外状态快照（调用方须持有 s.mu）。
func (s *Supervisor) syncGuardStatusLocked(inst *instance) {
	inst.status.RestartAttempts = inst.restartAttempts
	inst.status.RestartsSuspended = inst.restartsSuspended
	inst.status.LastRestartError = inst.lastRestartErr
}

// reportGuardFailure 经守卫把看护失败送到独立 sink（与 supervisor 其它失败同口径）。
func (s *Supervisor) reportGuardFailure(ns Namespace, err error) {
	if blocked := reportViaSink(s.sink, s.allowVLRec, string(ns), err); blocked {
		s.mu.Lock()
		s.guardBlocks++
		s.mu.Unlock()
	}
}
