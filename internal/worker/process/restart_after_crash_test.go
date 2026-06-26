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
	// onStart 可选钩子：在 Start 记账为 RUNNING 后、返回前同步触发，用于确定性地模拟
	// 「进程在 strategy.Start 返回前就崩溃」的并发时序（启动窗口竞态），无需真实进程与 sleep。
	onStart func()
}

func (f *fakeStrategy) Start(context.Context) error {
	f.startCount++
	f.state = StateRunning
	if f.onStart != nil {
		f.onStart()
	}
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

// TestManager_Start_PreservesCrashDuringStartWindow 确定性复现并回归「启动窗口竞态」：
// 若进程在 strategy.Start 返回前就崩溃（waitLoop 经 markStrategyState 抢先把记账置为 CRASHED），
// Manager.Start 收尾时不得用 RUNNING 覆盖该 CRASHED——否则实例「已死却记 RUNNING」，Start 守卫
// （仅允许 STOPPED/CRASHED 启动）从此拒绝重启，线上须重启整个 Worker 才能恢复。
//
// 这正是 TestManager_RestartAfterCrash 在高负载下偶发 FAIL 的根因：真实进程 spawn 后极速退出，
// waitLoop 与 Manager.Start 收尾在 inst.State 上竞争写入。此处用 fake 在 Start 内同步触发崩溃回调，
// 把负载敏感的时序竞态固化为确定性用例（修复前必失败、修复后必通过）。
func TestManager_Start_PreservesCrashDuringStartWindow(t *testing.T) {
	m := NewManager(t.TempDir())
	uuid := "crash-in-start-window"
	require.NoError(t, createDirect(m, uuid, "Fake", "echo hi", "."))

	// 注入在 Start 内同步崩溃的替身：onStart 把 Manager 记账改写为 CRASHED，
	// 紧接着 Manager.Start 会尝试落 RUNNING——修复前覆盖 CRASHED，修复后保留 CRASHED。
	fake := &fakeStrategy{state: StateStopped}
	fake.onStart = func() { m.markStrategyState(uuid, StateCrashed) }
	m.mu.Lock()
	m.instances[uuid].strategy = fake
	m.mu.Unlock()

	require.NoError(t, m.Start(uuid))

	st, _ := m.GetState(uuid)
	require.Equal(t, StateCrashed, st, "启动窗口内崩溃必须保留 CRASHED，不得被收尾的 RUNNING 覆盖")

	// 既然记账为 CRASHED，实例必须仍可再次启动（守卫允许 CRASHED），证明未卡死在 RUNNING。
	require.NoError(t, m.Start(uuid), "CRASHED 实例应可重启，证明未被卡在 RUNNING")
}

// quickCrashCmd 返回一个立即以非零码退出的命令，用于模拟实例进程崩溃（不依赖真实 MC / 端口）。
// direct 策略按平台用 cmd.exe / sh 执行，故两端均可用 "exit 1"。
func quickCrashCmd() string { return "exit 1" }

// TestManager_RestartAfterCrash 端到端回归（spawn 真实 direct 进程）：实例进程异步崩溃后，
// Manager 记账须同步为 CRASHED，否则 Start() 守卫仍看到 RUNNING 而拒绝重启——线上表现为
// 「崩溃后须重启整个 Worker 才能恢复」。崩溃记账由 waitLoop→markStrategyState 完成。
//
// 本用例改用状态变更回调做确定性同步（见下），不再用对负载敏感的 require.Eventually 轮询；
// 启动窗口竞态（崩溃抢在 Start 收尾前）的根因回归见 TestManager_Start_PreservesCrashDuringStartWindow。
func TestManager_RestartAfterCrash(t *testing.T) {
	m := NewManager(t.TempDir())
	uuid := "crash-restart"
	// AutoRestart=false：只验证「崩溃后可手动重启」，排除自动重启对断言的干扰。
	require.NoError(t, createDirect(m, uuid, "Crash", quickCrashCmd(), t.TempDir()))

	// 确定性同步：用状态变更回调在每次进入 CRASHED 时投递信号，测试据此精确等待
	// 「崩溃已记账」这一事件，而非按固定间隔轮询固定超时（对机器负载敏感、高负载下偶发超时）。
	// 缓冲足够容纳两次崩溃信号，避免回调在 waitLoop goroutine 上阻塞。
	crashed := make(chan struct{}, 4)
	m.SetStateChangeHandler(func(_ string, _, newState InstanceState) {
		if newState == StateCrashed {
			crashed <- struct{}{}
		}
	})

	require.NoError(t, m.Start(uuid))

	// 进程立即退出 → 异步 waitLoop 应把记账同步为 CRASHED（修复前停在 RUNNING）。
	waitForCrash(t, crashed)
	require.Equal(t, StateCrashed, mustGetState(t, m, uuid), "崩溃后 Manager 记账应同步为 CRASHED")

	// 关键回归：CRASHED 实例可直接重启拉起新进程，无需重启 Worker。
	require.NoError(t, m.Start(uuid), "CRASHED 实例应可重新启动")

	// 重启后进程仍会很快再次退出，等待其再次进入 CRASHED，确认重启真的拉起了新进程（而非被守卫拒绝）。
	waitForCrash(t, crashed)
	require.Equal(t, StateCrashed, mustGetState(t, m, uuid), "重启后再次崩溃应再次记为 CRASHED")

	_ = m.Remove(uuid)
}

// waitForCrash 阻塞直到收到一次「实例进入 CRASHED」的状态变更信号，实现对异步崩溃的确定性等待。
// 超时仅作兜底，防止真实回归（崩溃从未发生）时用例永久挂起；正常路径在毫秒级即被信号唤醒。
func waitForCrash(t *testing.T, crashed <-chan struct{}) {
	t.Helper()
	select {
	case <-crashed:
	case <-time.After(10 * time.Second):
		t.Fatal("等待实例崩溃记账为 CRASHED 超时")
	}
}

// mustGetState 读取实例状态，出错即终止用例，便于在断言点直接拿到当前记账状态。
func mustGetState(t *testing.T, m *Manager, uuid string) InstanceState {
	t.Helper()
	st, err := m.GetState(uuid)
	require.NoError(t, err)
	return st
}
