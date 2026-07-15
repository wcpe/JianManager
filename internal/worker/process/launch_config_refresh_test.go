package process

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type launchConfigStrategy struct {
	state   InstanceState
	stopped bool
	closed  bool
}

func (s *launchConfigStrategy) Start(context.Context) error { return nil }
func (s *launchConfigStrategy) Stop() error {
	s.stopped = true
	s.state = StateStopped
	return nil
}
func (s *launchConfigStrategy) Kill() error              { return nil }
func (s *launchConfigStrategy) SendCommand(string) error { return nil }
func (s *launchConfigStrategy) State() InstanceState     { return s.state }
func (s *launchConfigStrategy) Close() error {
	s.closed = true
	return nil
}
func (s *launchConfigStrategy) GetPID() int { return 0 }

// TestManager_SetLaunchConfigDefersRunningStrategyReplacement 验证 FR-233：保存新启动规格时
// 不关闭当前运行策略；完成 Stop 后丢弃旧策略，下一次 Start 必须从最新实例字段重建。
// TestManager_StartDropsLateStaleStrategy 模拟 Stop 已完成过期检查后，配置更新才在
// STOPPING→STOPPED 收尾窗口到达：下一次 Start 仍须关闭旧策略并按新规格重建。
func TestManager_StartDropsLateStaleStrategy(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "late-launch-refresh"
	workDir := t.TempDir()
	require.NoError(t, m.Create(uuid, "并发窗口实例", "echo old", "stop", workDir,
		map[string]string{"OLD": "1"}, false, ProcessTypeDirect, "", "", 0, 0))

	oldStrategy := &launchConfigStrategy{state: StateStopped}
	m.mu.Lock()
	inst := m.instances[uuid]
	inst.State = StateStopping
	inst.strategy = oldStrategy
	inst.strategyStale = false // 模拟 Stop 已完成 stale 检查。
	m.mu.Unlock()

	newEnv := map[string]string{"NEW": "2"}
	m.SetLaunchConfig(uuid, "echo new", "", "", newEnv, false)
	m.mu.Lock()
	inst.State = StateStopped // 模拟 Stop 随后完成状态收尾，但未再次检查 stale。
	m.mu.Unlock()

	require.NoError(t, m.Start(uuid))
	require.True(t, oldStrategy.closed, "Start 必须关闭收尾窗口遗留的旧策略")

	m.mu.RLock()
	current := inst.strategy
	m.mu.RUnlock()
	newStrategy, ok := current.(*directStrategy)
	require.True(t, ok, "下一次 Start 必须重建 direct 策略")
	require.Equal(t, "echo new", newStrategy.spec.StartCommand)
	require.Equal(t, newEnv, newStrategy.spec.EnvVars)
	require.NoError(t, m.Kill(uuid))
}

func TestManager_SetLaunchConfigDefersRunningStrategyReplacement(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "launch-refresh"
	require.NoError(t, m.Create(uuid, "测试实例", "old-command", "stop", t.TempDir(),
		map[string]string{"OLD": "1"}, false, ProcessTypeDirect, "old-jdk", "old-bin", 0, 0))

	oldStrategy := &launchConfigStrategy{state: StateRunning}
	m.mu.Lock()
	inst := m.instances[uuid]
	inst.State = StateRunning
	inst.strategy = oldStrategy
	m.mu.Unlock()

	newEnv := map[string]string{"NEW": "2"}
	m.SetLaunchConfig(uuid, "new-command", "new-jdk", "new-bin", newEnv, true)

	require.Same(t, oldStrategy, inst.strategy, "保存配置不得替换或关闭当前运行策略")
	require.False(t, oldStrategy.closed, "保存配置不得中断当前运行 daemon")
	require.Equal(t, "new-command", inst.StartCommand)
	require.Equal(t, "new-jdk", inst.JDKPath)
	require.Equal(t, "new-bin", inst.JDKBinPath)
	require.Equal(t, newEnv, inst.EnvVars)
	require.True(t, inst.AutoRestart)

	require.NoError(t, m.Stop(uuid))
	require.True(t, oldStrategy.stopped)
	require.True(t, oldStrategy.closed)
	require.Nil(t, inst.strategy, "停止后必须丢弃缓存策略，让下一次启动采用新规格")
}
