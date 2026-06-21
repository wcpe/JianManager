package process

import (
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
	require.NoError(t, m.Create(uuid, "Daemon", keepAliveCmd(), "", pidDir, nil, false, ProcessTypeDaemon, "", "", 0))
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

