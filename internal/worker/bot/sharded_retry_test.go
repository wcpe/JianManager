package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 分片启动失败自愈（issue #102）的回归测试。
//
// 【现场】节点首次启动时 mineflayer 等依赖未装齐 → 分片 Start 失败、无任何 Ready 片；
// 之后即使依赖装齐，也没有任何路径重新拉起该片（pickShardWithPending 只挑 Ready 片），
// 于是「一切 CreateBot 报分片容量不足」，只能重启整个 worker 才恢复。
//
// 这里用可注入的 StartShard 替身（不 spawn 真 Node 进程）驱动自愈路径：
//   - 依赖未装齐 → 启动失败，且 fail-fast 语义与错误文案保持
//   - 依赖装齐 → 一次普通创建/等待就绪请求即触发重试并恢复容量
//   - 重试带指数退避，退避窗口内不重复 spawn
//   - Stop 之后绝不复活（在途重试的 ctx 全部收束，不泄漏 goroutine）
//   - 部分片失败时，健康片照常承载（不回归既有行为）

// fakeShardEnv 模拟分片启动所依赖的外部环境（依赖是否装齐、某片是否恒故障）。
type fakeShardEnv struct {
	// depsReady 模拟「运维是否已装齐 Bot 运行时依赖」（mineflayer 等）。
	depsReady atomic.Bool
	// brokenShards 恒失败的片（模拟只有某一片装不上，其余正常）。
	brokenShards map[int]bool
	// readyDelay > 0 时模拟「子进程已起、worker-ready 还没到」：启动返回成功但立刻未就绪，
	// 过 readyDelay 才就绪。真机冷启动（起 Node + import mineflayer）正是这个形态。
	readyDelay time.Duration

	mu       sync.Mutex
	attempts []int
	ctxs     []context.Context
}

func newFakeShardEnv(shards int, broken ...int) *fakeShardEnv {
	env := &fakeShardEnv{
		attempts:     make([]int, shards),
		brokenShards: make(map[int]bool, len(broken)),
	}
	for _, i := range broken {
		env.brokenShards[i] = true
	}
	return env
}

// start 是注入到 ShardedManager 的启动动作替身。
func (e *fakeShardEnv) start(ctx context.Context, index int, shard *Manager) error {
	e.mu.Lock()
	e.attempts[index]++
	e.ctxs = append(e.ctxs, ctx)
	broken := e.brokenShards[index]
	e.mu.Unlock()
	if broken || !e.depsReady.Load() {
		// 与 botdist.CheckDeps 同源的 fail-fast 文案：错误里必须带「缺什么」。
		return errors.New("bot 依赖未在同一目录完整安装：缺少 mineflayer、mineflayer-pathfinder")
	}
	if e.readyDelay > 0 {
		// 已 spawn 但未就绪：running=true、Ready=false。
		markShardBooting(shard)
		delay := e.readyDelay
		go func() {
			time.Sleep(delay)
			markShardStarted(shard)
		}()
		return nil
	}
	markShardStarted(shard)
	return nil
}

func (e *fakeShardEnv) attemptsFor(index int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.attempts[index]
}

func (e *fakeShardEnv) totalAttempts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0
	for _, n := range e.attempts {
		total += n
	}
	return total
}

func (e *fakeShardEnv) contexts() []context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]context.Context(nil), e.ctxs...)
}

// newRetryShardStub 构造一片「未启动」的替身分片：running=false、Ready=false，
// 正是 Manager.Start 失败后留下的状态。
func newRetryShardStub(perShardMax int) *Manager {
	m := NewManager(ManagerConfig{BotWorkerPath: "unused"})
	m.capacity.MaxBots = perShardMax
	m.capacity.ObservedAt = time.Now()
	return m
}

// markShardStarted 把替身分片置为「已启动且已就绪」（不 spawn 真进程）。
func markShardStarted(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = true
	m.stdin = json.NewEncoder(io.Discard)
	m.capacity.Ready = true
	m.capacity.UnavailableReason = ""
	m.capacity.WorkerEpoch = "epoch-test"
	m.capacity.ObservedAt = time.Now()
	if m.capacity.CapacityGeneration == 0 {
		m.capacity.CapacityGeneration = 1
	}
	// 与 Manager.closeReadySignalLocked 同语义：就绪信号必须真正关闭，
	// 否则 WaitReady 会一直等到 ctx 超时。
	if m.readyCh != nil {
		select {
		case <-m.readyCh:
		default:
			close(m.readyCh)
		}
	}
}

// markShardBooting 把替身分片置为「子进程已起、但还没打出 worker-ready」。
// 真机冷启动（起 Node、import mineflayer）就是这段窗口——此前这段窗口里的 CreateBot
// 会被直接判成「分片容量不足」。
func markShardBooting(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = true
	m.stdin = json.NewEncoder(io.Discard)
	m.capacity.Ready = false
	m.capacity.UnavailableReason = "bot-worker 正在启动"
	m.capacity.ObservedAt = time.Now()
}

// newRetrySharded 构造可注入启动动作的分片管理器，并把自愈时间窗压到毫秒级。
//
// 生产默认是「单次等待就绪 10s + 0.5s 起步、30s 封顶的退避」；测试压小以免用例变慢，
// 各用例按需再覆盖 retryBaseDelay / retryMaxDelay 来断言退避窗口。
func newRetrySharded(t *testing.T, shards, maxBots int, env *fakeShardEnv) *ShardedManager {
	t.Helper()
	s := NewShardedManager(ShardedManagerConfig{
		Shards:     shards,
		MaxBots:    maxBots,
		NewShard:   func(_ int, perShardMax int) *Manager { return newRetryShardStub(perShardMax) },
		StartShard: env.start,
	})
	s.recoveryWait = 300 * time.Millisecond
	s.retryBaseDelay = time.Millisecond
	s.retryMaxDelay = time.Millisecond
	// 收尾只取消 rootCtx，不走 Stop：替身分片没有真实 waitDone，Stop 会等不到子进程退出。
	// rootCancel 与生产 Stop 的第一步等价，足以收束所有自愈 goroutine。
	t.Cleanup(s.rootCancel)
	return s
}

// TestShardedRetry_RecoversAfterDepsInstalled 覆盖 issue #102 的原始现场：
// 依赖未装齐 → 首次启动失败；装齐后一次普通创建请求即恢复，无需重启 worker。
func TestShardedRetry_RecoversAfterDepsInstalled(t *testing.T) {
	env := newFakeShardEnv(2)
	s := newRetrySharded(t, 2, 20, env)

	// 1) 依赖未装齐：fail-fast，错误里带出「缺什么」。
	err := s.Start(context.Background())
	require.Error(t, err, "依赖未装齐时必须 fail-fast，而不是静默降级")
	assert.Contains(t, err.Error(), "缺少 mineflayer", "错误必须说清缺哪个依赖")
	assert.Equal(t, 1, env.attemptsFor(0), "首次 Start 每片只应尝试一次")
	assert.False(t, s.IsRunning(), "全部片启动失败时不算在运行")

	// 2) 此刻创建必然拿不到容量，且错误里应带出根因（而不是笼统的「容量不足」）。
	err = s.CreateBots([]BotConfig{{ID: "bot-a"}})
	require.Error(t, err, "没有就绪分片时不应创建成功")
	assert.Contains(t, err.Error(), "分片容量不足")
	assert.Contains(t, err.Error(), "缺少 mineflayer", "容量不足的错误必须带出真实根因，否则运维会去查容量")

	// 3) 运维装齐依赖后，一次普通创建请求即可触发自愈并成功（无需重启 worker）。
	env.depsReady.Store(true)
	// 上次失败已建立退避窗口（测试里压到 1ms），等它过期再发请求——生产上限是 30s，
	// 也就是说「装齐依赖 → 最多 30 秒后的下一次创建即恢复」，不再需要重启进程。
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, s.CreateBots([]BotConfig{{ID: "bot-b"}}), "依赖装齐后应自愈成功")

	assert.True(t, s.IsRunning(), "自愈后分片应在运行")
	c := s.CapacitySnapshot()
	assert.True(t, c.Ready, "自愈后容量应重新可用")
	assert.GreaterOrEqual(t, env.attemptsFor(0), 2, "片 0 应经历「首次失败 + 自愈重试」")
	assert.GreaterOrEqual(t, env.attemptsFor(1), 1, "片 1 同样应被自愈拉起")

	// 自愈成功后失败记账必须清空，否则诊断面会一直显示旧错误。
	for _, info := range s.ShardHealth() {
		assert.Empty(t, info.LastError, "分片 %d 就绪后不应再残留失败原因", info.Index)
		assert.Zero(t, info.ConsecutiveFailures, "分片 %d 就绪后连续失败计数应归零", info.Index)
	}
}

// TestShardedRetry_WaitReadyTriggersRecovery 覆盖 fleet 路径（prepareBotFleet → WaitReady）。
func TestShardedRetry_WaitReadyTriggersRecovery(t *testing.T) {
	env := newFakeShardEnv(1)
	s := newRetrySharded(t, 1, 10, env)

	require.Error(t, s.Start(context.Background()), "依赖未装齐时启动应失败")
	env.depsReady.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, s.WaitReady(ctx), "WaitReady 应先自愈再等待，而不是直接报未就绪")
	assert.Equal(t, 2, env.attemptsFor(0))
}

// TestShardedRetry_BackoffThrottlesRetries 断言重试有退避：窗口内的重复请求不再 spawn。
func TestShardedRetry_BackoffThrottlesRetries(t *testing.T) {
	env := newFakeShardEnv(1)
	s := newRetrySharded(t, 1, 10, env)
	// 放大到 200ms，才能在测试里稳定区分「窗口内」与「窗口外」。
	s.retryBaseDelay = 200 * time.Millisecond
	s.retryMaxDelay = 200 * time.Millisecond

	require.Error(t, s.Start(context.Background()))
	assert.Equal(t, 1, env.attemptsFor(0))

	// 首次失败不留退避窗口：「刚装齐依赖」是最常见现场，允许紧跟着的请求立刻再试。
	require.Error(t, s.CreateBots([]BotConfig{{ID: "bot-a"}}))
	assert.Equal(t, 2, env.attemptsFor(0), "首次失败后应允许立即重试一次")

	// 第二次失败起进入退避：窗口内的连续请求一律跳过，不 spawn。
	for i := 0; i < 3; i++ {
		require.Error(t, s.CreateBots([]BotConfig{{ID: "bot-b"}}))
	}
	assert.Equal(t, 2, env.attemptsFor(0), "退避窗口内的请求不得重复 spawn（否则 CreateBot 会打成 spawn 风暴）")

	// 窗口过去后允许再试一次。
	time.Sleep(250 * time.Millisecond)
	require.Error(t, s.CreateBots([]BotConfig{{ID: "bot-c"}}))
	assert.Equal(t, 3, env.attemptsFor(0), "退避窗口结束后应重新尝试")
}

// TestShardedRetry_CreateWaitsForBootingShard 覆盖「分片刚 spawn、worker-ready 还没到」的窗口。
//
// 这是 issue #102 现场另一个真机症状：依赖装齐后 Start 已经不报错了，但分片要过一会儿
// 才 Ready，期间 CreateBot 一率被报「分片容量不足」。自愈路径里的「等就绪」把这段窗口
// 吸收掉，而不是让运维以为容量真的不够。
func TestShardedRetry_CreateWaitsForBootingShard(t *testing.T) {
	env := newFakeShardEnv(1)
	env.depsReady.Store(true)
	env.readyDelay = 40 * time.Millisecond
	s := newRetrySharded(t, 1, 10, env)
	s.recoveryWait = time.Second

	require.NoError(t, s.Start(context.Background()))
	require.False(t, s.CapacitySnapshot().Ready, "刚 spawn 的分片还没就绪")

	start := time.Now()
	require.NoError(t, s.CreateBots([]BotConfig{{ID: "bot-a"}}), "启动窗口内不应误报容量不足")
	assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond, "应等待分片就绪而不是直接判容量不足")
	assert.True(t, s.CapacitySnapshot().Ready)
	assert.Equal(t, 1, env.attemptsFor(0), "已 running 的分片只等就绪，不重复 spawn")
}

// TestShardedRetry_BackoffSchedule 直接校验退避曲线（0.5s 起步、30s 封顶）。
func TestShardedRetry_BackoffSchedule(t *testing.T) {
	s := NewShardedManager(ShardedManagerConfig{
		Shards:   1,
		MaxBots:  10,
		NewShard: func(_ int, perShardMax int) *Manager { return newRetryShardStub(perShardMax) },
	})
	// 用生产默认节奏断言曲线本身。
	s.retryBaseDelay = shardRetryBaseDelay
	s.retryMaxDelay = shardRetryMaxDelay

	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, time.Duration(0), s.backoffLocked(1), "首次失败不留窗口")
	assert.Equal(t, shardRetryBaseDelay, s.backoffLocked(2), "第二次失败起按 0.5s 起步")
	assert.Equal(t, 2*shardRetryBaseDelay, s.backoffLocked(3))
	assert.Equal(t, 4*shardRetryBaseDelay, s.backoffLocked(4))
	// 大次数必须封顶而不是移位溢出成负数。
	assert.Equal(t, shardRetryMaxDelay, s.backoffLocked(9))
	assert.Equal(t, shardRetryMaxDelay, s.backoffLocked(64))
	assert.Equal(t, shardRetryMaxDelay, s.backoffLocked(1<<20))
}

// TestShardedRetry_StartIsIdempotentForRunningShards 断言重复 Start 不会打扰已运行分片。
//
// 必要性：上层 ensureBotManager 只在 IsRunning()==false 时调 Start，而 IsRunning 是
// 「任一片在跑」——部分成功后就再也不会被调用；即便被调用，对已运行片重复 Start
// 只会拿到「Bot 管理器已在运行」并把整体误判为失败。
func TestShardedRetry_StartIsIdempotentForRunningShards(t *testing.T) {
	env := newFakeShardEnv(2)
	s := newRetrySharded(t, 2, 20, env)
	env.depsReady.Store(true)

	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Start(context.Background()), "重复 Start 应补齐失败片而不是整体报错")
	assert.Equal(t, 1, env.attemptsFor(0), "已运行的分片不应被重复启动")
	assert.Equal(t, 1, env.attemptsFor(1))
}

// TestShardedRetry_StopPreventsRevival 断言 Stop 之后自愈不会复活分片（不泄漏 goroutine）。
func TestShardedRetry_StopPreventsRevival(t *testing.T) {
	env := newFakeShardEnv(2)
	s := newRetrySharded(t, 2, 20, env)

	require.Error(t, s.Start(context.Background()))
	attemptsBeforeStop := env.totalAttempts()

	// 替身分片都是「未运行」状态，Stop 会立即返回（不会等真实子进程）。
	s.Stop()

	// Stop 之后即便依赖已装齐，任何入口都不得再拉起子进程。
	env.depsReady.Store(true)
	require.Error(t, s.CreateBots([]BotConfig{{ID: "bot-a"}}), "已停止的管理器不应创建成功")
	assert.Error(t, s.Start(context.Background()), "已停止的管理器不应被重新启动")
	assert.Equal(t, attemptsBeforeStop, env.totalAttempts(), "Stop 之后不得再有任何启动尝试")

	// 在途自愈 goroutine 的 ctx 必须已收束——这是「不泄漏」的可判定证据：
	// 所有重试 ctx 都派生自 rootCtx，rootCancel 后它们必然 Done。
	for _, ctx := range env.contexts() {
		assert.Error(t, ctx.Err(), "分片启动 ctx 必须随 Stop 收束，否则会留下悬挂 goroutine")
	}
}

// TestShardedRetry_PartialFailureKeepsHealthyShards 断言部分片失败时健康片照常承载。
//
// 既有行为不能回归：一片起不来不应让整个节点丧失容量。
func TestShardedRetry_PartialFailureKeepsHealthyShards(t *testing.T) {
	env := newFakeShardEnv(2, 1) // 片 1 恒故障（例如只有它装不上依赖）
	env.depsReady.Store(true)
	s := newRetrySharded(t, 2, 20, env)

	// 部分失败要如实报错，但成功片保持运行。
	require.Error(t, s.Start(context.Background()))
	assert.True(t, s.IsRunning(), "有一片成功即视为在运行")
	assert.True(t, s.shards[0].CapacitySnapshot().Ready, "健康片应就绪")
	assert.False(t, s.shards[1].CapacitySnapshot().Ready, "故障片应未就绪")

	// 创建请求仍能落到健康片（自愈会先尝试救故障片，但不应影响健康片承载）。
	require.NoError(t, s.CreateBots([]BotConfig{{ID: "bot-a"}}))
	idx, sh := s.routeShard("bot-a")
	require.NotNil(t, sh)
	assert.Equal(t, 0, idx, "创建必须落在唯一健康的片上")

	// 整体容量快照仍是「未就绪」，但原因必须点名故障片与真实错误。
	c := s.CapacitySnapshot()
	assert.False(t, c.Ready, "有片未就绪时整体不算就绪")
	assert.Contains(t, c.UnavailableReason, "分片 1")
	assert.Contains(t, c.UnavailableReason, "缺少 mineflayer", "原因必须带出真实根因")
}

// TestShardedRetry_ShardHealthExposed 断言诊断面能看出「哪片、试了几次、为什么失败」。
func TestShardedRetry_ShardHealthExposed(t *testing.T) {
	env := newFakeShardEnv(2, 1)
	env.depsReady.Store(true)
	s := newRetrySharded(t, 2, 20, env)

	require.Error(t, s.Start(context.Background()))

	health := s.ShardHealth()
	require.Len(t, health, 2)
	assert.True(t, health[0].Ready, "健康片应报就绪")
	assert.Equal(t, 1, health[0].LaunchAttempts)
	assert.Empty(t, health[0].LastError)

	assert.False(t, health[1].Ready, "故障片应报未就绪")
	assert.Equal(t, 1, health[1].ConsecutiveFailures)
	assert.Contains(t, health[1].LastError, "缺少 mineflayer", "诊断面必须能直接看到启动失败原因")
	assert.False(t, health[1].LastAttemptAt.IsZero())

	// 运行时快照应透传同一份分片健康明细（供节点诊断面读取）。
	snap := s.RuntimeSnapshot()
	require.Len(t, snap.Shards, 2)
	assert.Equal(t, health[1].LastError, snap.Shards[1].LastError)
}

// TestShardedRetry_ApplyBatchReportsRootCause 断言 fleet 批次路径的「容量不足」也带根因。
func TestShardedRetry_ApplyBatchReportsRootCause(t *testing.T) {
	env := newFakeShardEnv(2, 0, 1) // 两片都恒故障：没有任何 Ready 片
	s := newRetrySharded(t, 2, 20, env)

	require.Error(t, s.Start(context.Background()))

	// 没有 Ready 片时全部落到 overflow，不需要真实 IPC 回执即可验证错误文案。
	evt, err := s.ApplyBotBatch(context.Background(), "req-1", "batch-1", "idem-1",
		[]BotConfig{{ID: "bot-a"}})
	require.NoError(t, err)
	require.NotNil(t, evt)
	require.Len(t, evt.Results, 1)
	assert.False(t, evt.Results[0].Accepted)
	assert.Equal(t, "capacity_insufficient", evt.Results[0].ErrorCode)
	assert.Contains(t, evt.Results[0].Error, "分片容量不足")
	assert.Contains(t, evt.Results[0].Error, "缺少 mineflayer", "批次回执也必须带出真实根因")
}
