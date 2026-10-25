package vlsup

// 运行期看护的用例：用假进程（fakeFactory/fakeProc）与假时钟逐轮推进，
// 不依赖真实 VictoriaLogs 二进制，也不依赖真实等待（退避/稳定窗口都是纯时间判定）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock 是可推进的假时钟；看护的时间判定全部走 GuardianOptions.Now。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// guardianTestOptions 是测试用看护参数：稳定窗口 30s、上限 3 次、首次退避 5s、封顶 1min。
func guardianTestOptions(clock *fakeClock) GuardianOptions {
	return GuardianOptions{
		Interval:     time.Second,
		StableWindow: 30 * time.Second,
		MaxRestarts:  3,
		BaseBackoff:  5 * time.Second,
		MaxBackoff:   time.Minute,
		Now:          clock.Now,
	}.withDefaults()
}

// TestGuardianRestartsPreviouslyHealthyCrashedInstance 覆盖「退出 → 重启成功」。
func TestGuardianRestartsPreviouslyHealthyCrashedInstance(t *testing.T) {
	factory := &fakeFactory{}
	sink := &RecordingSink{}
	s := newTestSupervisor(t, factory, sink, false)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx, NamespaceHot))
	require.Equal(t, 1, factory.startCount())

	clock := newFakeClock()
	opts := guardianTestOptions(clock)

	// 必须在活态观察到越过稳定窗口，不能在死后等待墙钟补足资格。
	clock.Advance(31 * time.Second)
	s.checkOnce(ctx, opts)
	factory.procs[0].exitImmediately()
	s.checkOnce(ctx, opts)
	require.Equal(t, 2, factory.startCount(), "崩溃后应自动重启一次")
	require.Len(t, factory.procs, 2)
	require.False(t, factory.procs[1].Exited(), "新进程应为存活句柄")

	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateRunning, status.State)
	require.Equal(t, 1, status.RestartAttempts, "重启成功但尚未稳定存活时，计数应保留")
	require.False(t, status.RestartsSuspended)
	require.Empty(t, status.LastRestartError)
}

// TestGuardianStopsAfterMaxRestartsAndAlerts 覆盖「连续失败达上限 → 停止重试并告警」。
func TestGuardianStopsAfterMaxRestartsAndAlerts(t *testing.T) {
	factory := &fakeFactory{}
	sink := &RecordingSink{}
	s := newTestSupervisor(t, factory, sink, false)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx, NamespaceHot))
	clock := newFakeClock()
	clock.Advance(31 * time.Second)
	opts := guardianTestOptions(clock)
	s.checkOnce(ctx, opts)
	factory.procs[0].exitImmediately()

	// 第 1 次尝试失败。
	factory.failNext = errors.New("boom-1")
	s.checkOnce(ctx, opts)
	require.Equal(t, 1, factory.startCount())
	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, 1, status.RestartAttempts)
	require.Contains(t, status.LastRestartError, "boom-1")

	// 退避期内再扫一轮：不应立刻重试（这就是「退避」的断言点）。
	s.checkOnce(ctx, opts)
	require.Equal(t, 1, factory.startCount(), "退避未到期不应重试")

	// 第 2、3 次尝试失败（每次推进足够越过 10s/20s 的退避）。
	clock.Advance(6 * time.Second)
	factory.failNext = errors.New("boom-2")
	s.checkOnce(ctx, opts)
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, 2, status.RestartAttempts, "第 2 次尝试应已记账")

	clock.Advance(11 * time.Second)
	factory.failNext = errors.New("boom-3")
	s.checkOnce(ctx, opts)
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, 3, status.RestartAttempts, "第 3 次尝试应已记账")

	// 第 4 轮：已达上限（3 次）⇒ 熔断，不再尝试启动。
	clock.Advance(21 * time.Second)
	factory.failNext = errors.New("should-not-be-used")
	s.checkOnce(ctx, opts)
	require.Equal(t, 1, factory.startCount(), "失败的启动不计入成功启动次数，且熔断后不会再调工厂")

	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.True(t, status.RestartsSuspended, "达上限应标记熔断")
	require.Equal(t, 3, status.RestartAttempts)
	require.Contains(t, status.LastError, "已达上限")
	require.Contains(t, status.LastError, "boom-3")

	// 告警不静默：最后一次上报必须说清熔断与尝试次数。
	reports := sink.Snapshot()
	require.NotEmpty(t, reports)
	last := reports[len(reports)-1]
	require.Contains(t, last.Err.Error(), "已达上限")
	require.Contains(t, last.Err.Error(), "3 次")
	require.Equal(t, string(NamespaceHot), last.Instance)

	// 熔断后即使时钟继续推进也不再重启。
	clock.Advance(10 * time.Minute)
	s.checkOnce(ctx, opts)
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, 3, status.RestartAttempts)
	require.True(t, status.RestartsSuspended)
}

// TestGuardianDoesNotRestartAfterExplicitStop 覆盖「正常停止不触发重启」。
func TestGuardianDoesNotRestartAfterExplicitStop(t *testing.T) {
	factory := &fakeFactory{}
	s := newTestSupervisor(t, factory, &RecordingSink{}, false)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx, NamespaceHot))

	clock := newFakeClock()
	clock.Advance(31 * time.Second)
	opts := guardianTestOptions(clock)

	// 显式停止后，即便进程句柄看起来"已退出"，看护也不得拉起。
	require.NoError(t, s.Stop(ctx, NamespaceHot))
	s.checkOnce(ctx, opts)
	require.Equal(t, 1, factory.startCount(), "显式停止的实例不得被看护重启")

	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateStopped, status.State)
	require.Zero(t, status.RestartAttempts)

	// StopAll（Worker 退出路径）同样不得触发重启。
	require.NoError(t, s.Start(ctx, NamespaceHot))
	require.Equal(t, 2, factory.startCount())
	clock.Advance(31 * time.Second)
	s.StopAll(ctx)
	s.checkOnce(ctx, opts)
	require.Equal(t, 2, factory.startCount(), "StopAll 之后不得被看护重启")
}

// TestGuardianAbortsWhenOperatorStopsDuringRestart 覆盖「看护已判定崩溃、但人工停止先落地」的竞争。
//
// 用 epoch 模拟：先 Stop（人工接管），再让看护拿着旧 epoch 去重启 ⇒ 必须静默放弃，
// 且不得把这次放弃计入重启失败次数。
func TestGuardianAbortsWhenOperatorStopsDuringRestart(t *testing.T) {
	factory := &fakeFactory{}
	sink := &RecordingSink{}
	s := newTestSupervisor(t, factory, sink, false)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx, NamespaceHot))
	factory.procs[0].exitImmediately()

	clock := newFakeClock()
	clock.Advance(31 * time.Second)

	s.mu.Lock()
	staleEpoch := s.instances[NamespaceHot].epoch
	s.mu.Unlock()

	// 人工停止先发生（epoch 自增、进程句柄清空）。
	require.NoError(t, s.Stop(ctx, NamespaceHot))

	// 看护拿着过期 epoch 尝试重启 ⇒ 拒绝。
	require.ErrorIs(t, s.restartCrashed(ctx, NamespaceHot, staleEpoch), errGuardAborted)
	require.Equal(t, 1, factory.startCount(), "过期 epoch 的重启必须被拒绝")

	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateStopped, status.State)
	require.Zero(t, status.RestartAttempts)
	require.False(t, status.RestartsSuspended)
}

// TestGuardianResetsAfterStableRecoveryAndManualStartRearms 覆盖「稳定存活后清零」与「人工 Start 重新武装」。
func TestGuardianResetsAfterStableRecoveryAndManualStartRearms(t *testing.T) {
	factory := &fakeFactory{}
	sink := &RecordingSink{}
	s := newTestSupervisor(t, factory, sink, false)
	ctx := context.Background()
	require.NoError(t, s.Start(ctx, NamespaceHot))

	clock := newFakeClock()
	clock.Advance(31 * time.Second)
	opts := guardianTestOptions(clock)

	// 活态过窗后再制造失败尝试（计数1）。
	s.checkOnce(ctx, opts)
	factory.procs[0].exitImmediately()
	factory.failNext = errors.New("boom")
	s.checkOnce(ctx, opts)
	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, 1, status.RestartAttempts)

	// 再成功重启一次：启动成功本身不清零（否则崩溃循环打不满上限）。
	clock.Advance(6 * time.Second)
	s.checkOnce(ctx, opts)
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateRunning, status.State)
	require.Equal(t, 2, status.RestartAttempts)

	// 新进程稳定存活超过窗口 ⇒ 计数清零。
	clock.Advance(31 * time.Second)
	s.checkOnce(ctx, opts)
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Zero(t, status.RestartAttempts)
	require.False(t, status.RestartsSuspended)
	require.Empty(t, status.LastRestartError)

	// 熔断后人工 Start 重新武装。
	s.mu.Lock()
	s.instances[NamespaceHot].restartsSuspended = true
	s.instances[NamespaceHot].restartAttempts = 3
	s.syncGuardStatusLocked(s.instances[NamespaceHot])
	s.mu.Unlock()
	require.NoError(t, s.Stop(ctx, NamespaceHot))
	require.NoError(t, s.Start(ctx, NamespaceHot))
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.False(t, status.RestartsSuspended)
	require.Zero(t, status.RestartAttempts)
}

// TestGuardianStartStopLifecycle 覆盖看护循环自身的启停（不泄漏 goroutine、拒绝重复启动）。
func TestGuardianStartStopLifecycle(t *testing.T) {
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, s.StartGuardian(ctx, GuardianOptions{Interval: 5 * time.Millisecond}))
	require.ErrorIs(t, s.StartGuardian(ctx, GuardianOptions{}), ErrGuardianRunning)

	s.StopGuardian()
	// 停止后可以重新启动（Worker 热更新/测试场景）。
	require.NoError(t, s.StartGuardian(ctx, GuardianOptions{Interval: 5 * time.Millisecond}))
	s.StopGuardian()

	cancel()
	// ctx 取消后循环退出；再次启动应成功（guardCancel 已由 defer 清空，这里只验证不 panic）。
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, s.StartGuardian(context.Background(), GuardianOptions{Interval: 5 * time.Millisecond}))
	s.StopGuardian()
}

// TestGuardianOptionsBackoff 固定退避曲线（防止变异时曲线被静默改掉）。
func TestGuardianOptionsBackoff(t *testing.T) {
	opts := GuardianOptions{BaseBackoff: 5 * time.Second, MaxBackoff: 30 * time.Second}.withDefaults()
	require.Equal(t, 5*time.Second, opts.backoff(1))
	require.Equal(t, 10*time.Second, opts.backoff(2))
	require.Equal(t, 20*time.Second, opts.backoff(3))
	require.Equal(t, 30*time.Second, opts.backoff(4))
	require.Equal(t, 30*time.Second, opts.backoff(99))

	// 默认时钟应接近真实时间（Now 未注入时用 time.Now）。
	require.WithinDuration(t, time.Now(), opts.Now(), time.Second)
}
