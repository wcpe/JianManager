package process

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStrategy 是可控的 IProcessCommand 测试替身：不 spawn 真实进程，便于确定性地验证
// Manager 在「策略异步崩溃 → markStrategyState 同步 → 重启」路径上的记账与守卫行为。
type fakeStrategy struct {
	startCount int
	state      InstanceState
}

func (f *fakeStrategy) Start(context.Context) error {
	f.startCount++
	f.state = StateRunning
	return nil
}
func (f *fakeStrategy) Stop() error              { f.state = StateStopped; return nil }
func (f *fakeStrategy) Kill() error              { f.state = StateStopped; return nil }
func (f *fakeStrategy) SendCommand(string) error { return nil }
func (f *fakeStrategy) State() InstanceState     { return f.state }
func (f *fakeStrategy) Close() error             { return nil }
func (f *fakeStrategy) GetPID() int              { return 0 }

// TestManager_MarkStrategyState_AllowsRestart 确定性单测（不 spawn 进程）：
// 策略异步崩溃经 markStrategyState 同步后，Manager 记账须转为 CRASHED 且可重启（复用已注入策略）。
func TestManager_MarkStrategyState_AllowsRestart(t *testing.T) {
	m := NewManager(t.TempDir())
	uuid := "fake-crash"
	require.NoError(t, createDirect(m, uuid, "Fake", "echo hi", "."))

	// 注入替身策略并置为已运行（模拟 Start 成功后的稳态）。
	fake := &fakeStrategy{state: StateRunning}
	m.mu.Lock()
	inst := m.instances[uuid]
	inst.strategy = fake
	inst.State = StateRunning
	m.mu.Unlock()

	// 模拟「wrapper/子进程异步退出 = 崩溃」回调。
	m.markStrategyState(uuid, StateCrashed)
	st, _ := m.GetState(uuid)
	require.Equal(t, StateCrashed, st, "崩溃后记账应为 CRASHED")

	// 关键：CRASHED 实例可直接重启，复用已注入策略并再次调用其 Start。
	require.NoError(t, m.Start(uuid), "CRASHED 实例应可重新启动")
	st, _ = m.GetState(uuid)
	assert.Equal(t, StateRunning, st)
	assert.GreaterOrEqual(t, fake.startCount, 1, "重启应再次调用策略 Start")
}

// quickCrashCmd 返回一个立即以非零码退出的命令，用于模拟实例进程崩溃（不依赖真实 MC / 端口）。
// direct 策略按平台用 cmd.exe / sh 执行，故两端均可用 "exit 1"。
func quickCrashCmd() string { return "exit 1" }

// TestManager_RestartAfterCrash 复现并回归：实例进程异步崩溃后，Manager 记账须同步为 CRASHED，
// 否则 Start() 守卫仍看到 RUNNING 而拒绝重启——线上表现为「崩溃后须重启整个 Worker 才能恢复」。
//
// 修复前：waitLoop 只更新策略内部状态、不回写 Manager.inst.State，GetState 持续返回 RUNNING，
// 第一处 require.Eventually 会超时失败；即便强行再 Start 也会被守卫拒绝。
func TestManager_RestartAfterCrash(t *testing.T) {
	m := NewManager(t.TempDir())
	uuid := "crash-restart"
	// AutoRestart=false：只验证「崩溃后可手动重启」，排除自动重启对断言的干扰。
	require.NoError(t, createDirect(m, uuid, "Crash", quickCrashCmd(), t.TempDir()))

	require.NoError(t, m.Start(uuid))

	// 进程立即退出 → 异步 waitLoop 应把记账同步为 CRASHED（修复前停在 RUNNING）。
	require.Eventually(t, func() bool {
		st, _ := m.GetState(uuid)
		return st == StateCrashed
	}, 5*time.Second, 20*time.Millisecond, "崩溃后 Manager 记账应同步为 CRASHED")

	// 关键回归：CRASHED 实例可直接重启拉起新进程，无需重启 Worker。
	require.NoError(t, m.Start(uuid), "CRASHED 实例应可重新启动")

	// 重启后进程仍会很快再次退出，等待其回到 CRASHED，确认重启真的拉起了新进程（而非被守卫拒绝）。
	require.Eventually(t, func() bool {
		st, _ := m.GetState(uuid)
		return st == StateCrashed
	}, 5*time.Second, 20*time.Millisecond, "重启后再次崩溃应再次记为 CRASHED")

	_ = m.Remove(uuid)
}
