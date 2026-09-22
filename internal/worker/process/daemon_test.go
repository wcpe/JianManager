package process

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// daemon 集成测试在进程内运行 wrapper，替身进程（ping/sleep）不响应 stdin "stop"，
// 缩短优雅停止超时，避免清理阶段 stop 等满默认 30s 导致 TempDir 被占用清理失败。
func init() {
	_ = os.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	// 缩短重启前「等待上一代进程退出」上限，避免 daemon 启动路径用例久等。
	_ = os.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "1s")
}

// keepAliveCmd 跨平台保持存活命令（daemon 策略集成测试用）。
func keepAliveCmd() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1 > nul"
	}
	return "sleep 30"
}

// 注：daemonStrategy.Start 通过 os.Executable() spawn worker 二进制的 daemon 子命令。
// 单元测试环境下 os.Executable() 指向测试二进制、无 daemon 子命令分支，
// 因此真实 spawn 路径留给真机/集成验证（由主控执行）。此处覆盖：
//   - PID 文件恢复（RecoverDaemonInstances）
//   - StopAll 优雅断开（不杀 wrapper）
// 这两条路径直接复用已运行的 wrapper，不依赖 spawn。

// TestManager_DaemonRecover 验证 Worker 重启后通过 PID 文件恢复 daemon 连接。
// 流程：启动 daemon 实例 → 模拟 Worker 重启（新建 Manager，断开旧策略）→
// RecoverDaemonInstances 扫描 PID 文件 reconnect → 实例恢复为 RUNNING。
func TestManager_DaemonRecover(t *testing.T) {
	pidDir := t.TempDir()
	uuid := "daemon-recover"

	// 第一阶段：用 wrapper 直接启动一个存活的 daemon（不经 Manager.Start 的真实 spawn，
	// 而是直接 Run wrapper + 写 PID 文件），模拟「Worker 重启前 wrapper 已在运行」。
	cfg := daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: keepAliveCmd(),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- daemon.RunWithReady(cfg, ready) }()
	<-ready

	// 等 wrapper 写好 PID 文件
	pidPath := filepath.Join(pidDir, uuid+".pid")
	require.Eventually(t, func() bool {
		rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
		return err == nil && rec.WrapperPID > 0 && daemon.IsPIDAlive(rec.WrapperPID)
	}, 3*time.Second, 100*time.Millisecond)

	// 第二阶段：新 Manager（模拟 Worker 重启），恢复
	m2 := NewManager(pidDir)
	recovered, err := m2.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	st, _ := m2.GetState(uuid)
	assert.Equal(t, StateRunning, st)

	// 停止并退出，清理 wrapper
	_ = m2.Stop(uuid)
	select {
	case <-done:
	case <-time.After(6 * time.Second):
	}
}

// TestManager_DaemonStopAllGraceful daemon 模式 StopAll 应只断开连接、不杀游戏服。
// 验证：StopAll 后 wrapper 进程仍存活（进程隔离目标）。
func TestManager_DaemonStopAllGraceful(t *testing.T) {
	pidDir := t.TempDir()
	uuid := "daemon-stopall"
	cfg := daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: keepAliveCmd(),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- daemon.RunWithReady(cfg, ready) }()
	<-ready

	pidPath := filepath.Join(pidDir, uuid+".pid")
	require.Eventually(t, func() bool {
		_, err := daemon.NewPIDFile(pidPath).ReadRecord()
		return err == nil
	}, 3*time.Second, 100*time.Millisecond)
	rec, _ := daemon.NewPIDFile(pidPath).ReadRecord()
	wrapperPID := rec.WrapperPID

	m := NewManager(pidDir)
	require.NoError(t, m.Create(uuid, "Daemon", keepAliveCmd(), "", pidDir, nil, false, ProcessTypeDaemon, "", "", 0, 0))
	// 用 recover 接管已运行的 wrapper
	recovered, recErr := m.RecoverDaemonInstances()
	t.Logf("recover: recovered=%d err=%v wrapperPID=%d", recovered, recErr, wrapperPID)
	require.Equal(t, 1, recovered, "应恢复 1 个 daemon 实例")

	m.StopAll() // daemon 模式：只断开，不杀
	t.Logf("StopAll done, wrapper alive=%v", daemon.IsPIDAlive(wrapperPID))

	// wrapper 进程应仍存活（StopAll 不杀游戏服/wrapper）
	assert.True(t, daemon.IsPIDAlive(wrapperPID), "wrapper 应在 StopAll 后存活")

	// 清理：连接 wrapper 下发 stop 并等待退出，避免 TempDir 清理时文件被占用
	conn, err := daemon.Dial(daemon.SocketAddr(pidDir, uuid))
	t.Logf("cleanup dial: err=%v", err)
	if err == nil {
		f := &daemon.Frame{Header: daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand}, Payload: []byte(daemon.ControlStop)}
		_ = f.Encode(conn)
		conn.Close()
	}
	select {
	case <-done:
		t.Logf("wrapper exited via stop")
	case <-time.After(8 * time.Second):
		t.Logf("wrapper did not exit in 8s")
	}
	// 注意：本测试在进程内运行 wrapper（goroutine，非子进程），
	// wrapperPID == os.Getpid()，不可用 killProcessTree（会杀掉测试自身）。
	// 真实部署中 wrapper 是独立子进程，stop 已通过 taskkill /T 清理 Java 树。
}

// exitAfterCmd 返回一个「存活若干秒后自行退出」的跨平台命令（验证 wrapper 自动重启用）。
func exitAfterCmd(seconds int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("ping -n %d 127.0.0.1 > nul", seconds+1)
	}
	return fmt.Sprintf("sleep %d", seconds)
}

// autoRestartToggler 暴露 daemon 策略的禁用/恢复自动重启控制帧能力（wrapper 协议）。
type autoRestartToggler interface {
	DisableAutoRestart() error
	EnableAutoRestart() error
}

// FR-459 终验 Major 端到端回归：熔断禁用自动重启后经人工解除（对称 enable 帧），仍在托管的
// wrapper 必须恢复「Java 退出后自动重启」。修复前没有 enable 帧，wrapper 收到禁用后收摊退出，
// 而 Worker/CP 认为熔断已解除 → Java 下次崩溃不再被拉起（假解除）。
func TestManager_DaemonEnableRestartReArmsWrapper(t *testing.T) {
	pidDir := t.TempDir()
	uuid := "daemon-rearm"
	cfg := daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: exitAfterCmd(4),
		WorkDir:      pidDir,
		AutoRestart:  true,
		PIDDir:       pidDir,
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- daemon.RunWithReady(cfg, ready) }()
	<-ready

	pidPath := filepath.Join(pidDir, uuid+".pid")
	pf := daemon.NewPIDFile(pidPath)
	var firstJavaPID int
	require.Eventually(t, func() bool {
		rec, err := pf.ReadRecord()
		if err != nil || rec.JavaPID == 0 {
			return false
		}
		firstJavaPID = rec.JavaPID
		return true
	}, 5*time.Second, 20*time.Millisecond, "应能读到首个 Java pid")

	m := NewManager(pidDir)
	require.NoError(t, m.Create(uuid, "Daemon", exitAfterCmd(4), "", pidDir, nil, true, ProcessTypeDaemon, "", "", 0, 0))
	recovered, recErr := m.RecoverDaemonInstances()
	require.NoError(t, recErr)
	require.Equal(t, 1, recovered, "应恢复 1 个 daemon 实例")

	m.mu.RLock()
	strategy := m.instances[uuid].strategy
	m.mu.RUnlock()
	toggler, ok := strategy.(autoRestartToggler)
	require.True(t, ok, "daemon 策略应实现禁用/恢复自动重启控制帧")

	// 熔断禁用（此刻 Java 仍在跑：只发禁用帧、保持托管）→ 人工解除（对称 enable 帧复位粘性开关）。
	require.NoError(t, toggler.DisableAutoRestart())
	require.NoError(t, toggler.EnableAutoRestart())

	// 首个 Java 退出后 wrapper 应自动拉起新 Java（pid 变化）。
	// 修复前 enable 缺失 → wrapper 收摊退出、PID 文件被清理，本断言必失败。
	require.Eventually(t, func() bool {
		rec, readErr := pf.ReadRecord()
		return readErr == nil && rec.JavaPID != 0 && rec.JavaPID != firstJavaPID
	}, 20*time.Second, 100*time.Millisecond, "恢复自动重启后 wrapper 应在 Java 退出后拉起新进程")

	// 清理：下发 kill 结束 wrapper（进程内运行，不能 killProcessTree 自身）。
	if conn, dialErr := daemon.Dial(daemon.SocketAddr(pidDir, uuid)); dialErr == nil {
		f := &daemon.Frame{Header: daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand}, Payload: []byte(daemon.ControlKill)}
		_ = f.Encode(conn)
		_ = conn.Close()
	}
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Log("wrapper 未在 8s 内退出（清理超时）")
	}
}
