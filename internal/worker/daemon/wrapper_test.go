package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 测试替身进程（ping/sleep）不响应 stdin "stop"，缩短优雅停止超时让其快速回退强杀，
// 避免每个停止用例等满 30s。
func init() {
	_ = os.Setenv(envGracefulStopTimeout, "1s")
}

// keepAliveCommand 返回一个跨平台的「保持存活」命令。
// wrapper 用它作为 Java 进程的替身，便于测试控制停止与 PID 恢复。
// 注意：buildJavaCmd 已用 cmd.exe /c（Windows）或 sh -c（unix）包裹，
// 此处只给原始命令体，不要重复 shell 前缀。
func keepAliveCommand() string {
	if runtime.GOOS == "windows" {
		// ping 保持存活 30s
		return "ping -n 30 127.0.0.1 > nul"
	}
	return "sleep 30"
}

// echoForeverCommand 返回一个持续周期性输出一行的命令，
// 用于验证 wrapper 把 Java stdout 转发为帧给 Worker。
func echoForeverCommand() string {
	if runtime.GOOS == "windows" {
		// 输出 5 次 java-mark，每次间隔 1s，共 ~5s（ping -n 2 ≈ 1s）
		return `for /l %i in (1,1,5) do @echo java-mark & ping -n 2 127.0.0.1 > nul`
	}
	return `for i in 1 2 3 4 5 6 7 8 9 10; do echo java-mark; sleep 0.3; done`
}

// runWrapperWithReady 在后台启动 wrapper，返回就绪通道与退出通道。
func runWrapperWithReady(t *testing.T, cfg WrapperConfig) (ready <-chan struct{}, done <-chan error) {
	t.Helper()
	r := make(chan struct{}, 1)
	d := make(chan error, 1)
	go func() { d <- RunWithReady(cfg, r) }()
	return r, d
}

// captureJavaPID 从 PID 文件读取 Java pid（stop 后 wrapper 会清理 PID 文件，届时无法再读）。
func captureJavaPID(t *testing.T, pidDir, uuid string) int {
	t.Helper()
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	var javaPID int
	require.Eventually(t, func() bool {
		rec, err := pf.ReadRecord()
		if err != nil {
			return false
		}
		javaPID = rec.JavaPID
		return javaPID != 0
	}, 3*time.Second, 50*time.Millisecond, "应能读到 Java pid")
	return javaPID
}

// waitForProcGone 轮询等待 pid 对应进程真正退出。Windows 上 taskkill /T /F 异步终止
// 进程树，子进程（及其 CWD 句柄）可能在 wrapper 退出后仍短暂占用 pidDir，导致
// t.TempDir() 的 RemoveAll 清理失败（偶发 fatal）。测试结束前显式等待进程消失、
// 句柄释放后再返回（复用 TestWrapper_PIDFileCleanup 的既有等待模式）。
func waitForProcGone(t *testing.T, pid int) {
	t.Helper()
	if pid == 0 {
		return
	}
	require.Eventually(t, func() bool {
		return !IsPIDAlive(pid)
	}, 5*time.Second, 50*time.Millisecond, "子进程应已退出")
}

// testWorkDir 返回本用例专属工作目录，由 testing 框架（t.TempDir）负责回收，
// 不再用 os.MkdirTemp("") 往系统 /tmp 堆积目录（开发机 /tmp 常为 tmpfs）。
//
// 观察项（2026-09-07）：全量并行负载下本包曾偶发 runtime netpoll fatal
// （单跑/复跑均未复现）。同轮已修复 wrapper 停止后 server 侧连接句柄
// 泄漏与测试 TempDir 句柄残留两类确定性 flake，netpoll 若再复现，
// 从「stop 未等 goroutine 退出即关 pipe」的并发 close 方向深挖。
func testWorkDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestWrapper_StopControl 验证 wrapper 端到端：
//   - wrapper 监听就绪（就绪信号）
//   - Worker 拨号连接 socket
//   - PID 文件已写入且 wrapper pid 存活
//   - stop 控制命令使 Java 退出、wrapper 结束
//
// 参见 ADR-003。
func TestWrapper_StopControl(t *testing.T) {
	pidDir := testWorkDir(t)
	uuid := "test-stop"
	cfg := WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: keepAliveCommand(),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}

	ready, done := runWrapperWithReady(t, cfg)
	addr := SocketAddr(pidDir, uuid)

	// 等待 wrapper 监听就绪
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("wrapper 在就绪前退出: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper 监听就绪超时")
	}

	conn, err := Dial(addr)
	require.NoError(t, err)
	defer conn.Close()

	// PID 文件已写入，wrapper pid 存活（顺带捕获 Java pid，stop 后无法再读）
	var javaPID int
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	require.Eventually(t, func() bool {
		rec, err := pf.ReadRecord()
		if err != nil {
			return false
		}
		javaPID = rec.JavaPID
		return IsPIDAlive(rec.WrapperPID)
	}, 3*time.Second, 100*time.Millisecond, "wrapper pid 应存活")

	// 下发 stop 控制命令
	stopFrame := &Frame{
		Header:  Header{Channel: ChannelControl, Type: TypeCommand},
		Payload: []byte(ControlStop),
	}
	require.NoError(t, stopFrame.Encode(conn))

	// wrapper 应在 stop 后退出
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("wrapper 在 stop 后未退出")
	}
	conn.Close()

	// Windows 上 taskkill /T /F 异步终止进程树：等待 Java pid 真正消失、
	// 句柄释放后再让 t.TempDir() 的 RemoveAll 清理 pidDir（否则偶发清理失败 fatal）。
	waitForProcGone(t, javaPID)
}

// TestWrapper_StopDuringJavaStartDoesNotOrphanJava 锁定真机验收（FR-475 轮次）发现的产品缺陷：
// stop/kill 落在「startJava 已发起、javaCmd 尚未登记」的窗口内时，旧实现直接 signalClose 收摊退出，
// 而 startJava 随后拉起的 Java 无人托管——真机表现为 wrapper 已退出、Java 仍在跑并占着实例工作目录
// （Worker 侧显示已停止；用例侧则表现为 t.TempDir 的 RemoveAll 撞上该进程的 CWD 句柄而失败）。
//
// 修复后：启动窗口内的停止请求被暂存，javaCmd 登记完成即补发停止——wrapper 退出前 Java 必已终止。
func TestWrapper_StopDuringJavaStartDoesNotOrphanJava(t *testing.T) {
	workDir := t.TempDir()
	cfg := WrapperConfig{
		InstanceUUID: "test-stop-race",
		StartCommand: keepAliveCommand(),
		WorkDir:      workDir,
		AutoRestart:  false,
		PIDDir:       workDir,
	}
	// 直接构造 Wrapper（不起监听/不跑 run）：本用例只验「启动窗口 + 停止」这段状态机。
	w := &Wrapper{
		cfg:     cfg,
		pidFile: NewPIDFile(PIDFileName(cfg.PIDDir, cfg.InstanceUUID)),
		addr:    SocketAddr(cfg.PIDDir, cfg.InstanceUUID),
		state:   StateStopped,
		closing: make(chan struct{}),
	}

	// 模拟 startJava 已进入启动临界区：javaStarting=true，javaCmd 仍为 nil。此时收到 stop。
	w.mu.Lock()
	w.state = StateStarting
	w.javaStarting = true
	w.mu.Unlock()
	w.stopJava(false)

	if w.isClosed() {
		t.Fatal("启动窗口内的 stop 不得让 wrapper 收摊退出：随后拉起的 Java 会无人托管")
	}
	w.mu.Lock()
	deferred := w.stopDeferred
	w.mu.Unlock()
	require.True(t, deferred, "启动窗口内的 stop 必须被暂存，待 javaCmd 登记后补发")

	// 模拟 startJava 走出启动临界区（登记 javaCmd/javaStdin，清 javaStarting）并补发暂存请求，
	// 随后像 startJava 一样起 javaWait 善后（补发把 state 置为 StateStopping → 不误判崩溃重启）。
	javaCmd := buildJavaCmd(cfg)
	stdin, err := javaCmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, javaCmd.Start())
	t.Cleanup(func() {
		// 兜底：用例提前失败时终止进程树，避免残留子进程占住工作目录（回收交给 javaWait）。
		killProcessTree(javaCmd)
	})
	w.mu.Lock()
	w.javaCmd = javaCmd
	w.javaStdin = stdin
	w.state = StateRunning
	w.javaStarting = false
	w.mu.Unlock()

	w.applyDeferredStop()
	go w.javaWait(javaCmd)

	// 补发后 Java 必须被终止（优雅停止超时 1s → 强杀），wrapper 才能在收摊后退出。
	waitForProcGone(t, javaCmd.Process.Pid)
	select {
	case <-w.closing:
	case <-time.After(5 * time.Second):
		t.Fatal("补发停止后 wrapper 未收摊退出")
	}
}

// TestWrapper_StdoutForward 验证 wrapper 把 Java 的 stdout 转发为帧给 Worker。
func TestWrapper_StdoutForward(t *testing.T) {
	pidDir := testWorkDir(t)
	uuid := "test-stdout"
	cfg := WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: echoForeverCommand(),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}

	ready, done := runWrapperWithReady(t, cfg)
	addr := SocketAddr(pidDir, uuid)

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper 监听就绪超时")
	}

	conn, err := Dial(addr)
	require.NoError(t, err)
	defer conn.Close()

	// 读取帧，期望收到含 java-mark 的 stdout
	var got string
	deadline := time.After(6 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("等待 Java 输出超时，已收: %q", got)
		default:
		}
		fr, err := Decode(conn)
		if err != nil {
			t.Fatalf("读取帧失败: %v", err)
		}
		if fr.Channel == ChannelStdout {
			got += string(fr.Payload)
			if assert.Contains(t, got, "java-mark") {
				break
			}
		}
	}

	// 停止 wrapper，回收。先捕获 Java pid（stop 后 PID 文件被清理无法再读），
	// 结束前等待进程真正退出，避免 Windows 句柄占用 pidDir 致 TempDir 清理失败。
	javaPID := captureJavaPID(t, pidDir, uuid)
	stopFrame := &Frame{Header: Header{Channel: ChannelControl, Type: TypeCommand}, Payload: []byte(ControlStop)}
	_ = stopFrame.Encode(conn)
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
	conn.Close()
	waitForProcGone(t, javaPID)
}

// TestWrapper_PIDFileCleanup wrapper 退出后 PID 文件应被清理。
func TestWrapper_PIDFileCleanup(t *testing.T) {
	pidDir := testWorkDir(t)
	uuid := "test-cleanup"
	cfg := WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: keepAliveCommand(),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}

	ready, done := runWrapperWithReady(t, cfg)
	addr := SocketAddr(pidDir, uuid)
	<-ready

	conn, err := Dial(addr)
	require.NoError(t, err)

	// 捕获 Java pid：stop 后 wrapper 会清理 PID 文件，届时无法再读。
	javaPID := captureJavaPID(t, pidDir, uuid)

	stopFrame := &Frame{Header: Header{Channel: ChannelControl, Type: TypeCommand}, Payload: []byte(ControlStop)}
	require.NoError(t, stopFrame.Encode(conn))
	// 等待 wrapper 处理 stop 后再关闭连接，避免 EOF 抢先于 stop 帧
	select {
	case <-done:
		conn.Close()
	case <-time.After(8 * time.Second):
		conn.Close()
		t.Fatal("wrapper 未退出")
	}

	// PID 文件应已清理
	_, err = os.Stat(filepath.Join(pidDir, uuid+".pid"))
	assert.True(t, os.IsNotExist(err), "PID 文件应已被清理")

	// Windows 上 taskkill /T /F 异步终止进程树，Java 进程的句柄可能仍占用
	// 工作目录导致 t.TempDir() 清理失败。等待 Java pid 真正消失后再返回，
	// 让 TempDir 的 RemoveAll 能成功（统一走 waitForProcGone helper）。
	waitForProcGone(t, javaPID)
}

// crashImmediatelyCommand 返回一个「启动即崩溃」的命令（跨平台），
// 用于验证 FR-459 崩溃熔断的 daemon 生效路径：wrapper 收到「禁用自动重启」后不得再拉起 Java。
func crashImmediatelyCommand() string {
	if runtime.GOOS == "windows" {
		return "exit /b 1"
	}
	return "exit 1"
}

// TestWrapper_DisableRestartStopsAutoRestart 验证 FR-459 blocker 的 daemon 生效路径：
// AutoRestart=true 且 Java 启动即崩溃时，wrapper 本会按退避自动重启；一旦 Worker 下发
// ControlDisableRestart（崩溃熔断），wrapper 必须停止自动重启并自行收摊退出。
//
// 判定依据：wrapper 的 fastCrashes 放弃阈值是 5 次、退避序列 1s+2s+4s+8s（≥15s）才会自退，
// 故「5s 内退出」只可能来自熔断禁用帧，而非 wrapper 自身的快速崩溃放弃逻辑。
func TestWrapper_DisableRestartStopsAutoRestart(t *testing.T) {
	pidDir := testWorkDir(t)
	uuid := "test-disable-restart"
	cfg := WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: crashImmediatelyCommand(),
		WorkDir:      pidDir,
		AutoRestart:  true,
		PIDDir:       pidDir,
	}

	ready, done := runWrapperWithReady(t, cfg)
	addr := SocketAddr(pidDir, uuid)
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("wrapper 在就绪前退出: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper 监听就绪超时")
	}

	conn, err := Dial(addr)
	require.NoError(t, err)
	defer conn.Close()

	// 下发「禁用自动重启」（崩溃熔断）。
	frame := &Frame{Header: Header{Channel: ChannelControl, Type: TypeCommand}, Payload: []byte(ControlDisableRestart)}
	require.NoError(t, frame.Encode(conn))

	// wrapper 应在退避窗口内复核熔断标志并收摊退出（不再拉起 Java）。
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("收到禁用自动重启后 wrapper 仍在自动重启（未退出）")
	}
}

// TestWrapper_DisableAutoRestartIsSticky 验证禁用自动重启是粘性状态（不复位），
// 且不误伤正在启动中的 Java（避免熔断帧把在启动的 Java 变成孤儿）。
func TestWrapper_DisableAutoRestartIsSticky(t *testing.T) {
	w := &Wrapper{cfg: WrapperConfig{AutoRestart: true}, closing: make(chan struct{}), state: StateStopped}
	require.False(t, w.isAutoRestartOff())

	// Java 启动在飞：不得立即收摊（否则新拉起的 Java 无人托管）。
	w.mu.Lock()
	w.javaStarting = true
	w.mu.Unlock()
	w.disableAutoRestart()
	require.True(t, w.isAutoRestartOff())
	require.False(t, w.isClosed(), "启动在飞时不得立即关闭 wrapper")

	// Java 在跑：同样保持托管。
	w.mu.Lock()
	w.javaStarting = false
	w.javaCmd = &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	w.mu.Unlock()
	w.disableAutoRestart()
	require.False(t, w.isClosed(), "Java 在跑时不得立即关闭 wrapper")

	// 状态变化（崩溃/停止）不得复位熔断标志。
	w.setState(StateCrashed)
	require.True(t, w.isAutoRestartOff(), "禁用自动重启在状态变化后仍保持")

	// 确无 Java 在跑也无启动在飞（崩溃退避窗口）→ 立即收摊。
	w.mu.Lock()
	w.javaCmd = nil
	w.mu.Unlock()
	w.disableAutoRestart()
	require.True(t, w.isClosed(), "空闲态应立即收摊，避免空占进程")
}

// FR-459 终验 Major：对称的 enable_restart 控制帧必须清掉粘性「禁用自动重启」，使 wrapper
// 恢复自动重启（人工解除熔断的 daemon 复位路径）。
func TestWrapper_EnableAutoRestartClearsSticky(t *testing.T) {
	w := &Wrapper{cfg: WrapperConfig{AutoRestart: true}, closing: make(chan struct{}), state: StateStopped}

	// Java 在跑时禁用不立即收摊（保持托管）。
	w.mu.Lock()
	w.javaCmd = &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	w.mu.Unlock()

	w.handleControl(ControlDisableRestart)
	require.True(t, w.isAutoRestartOff(), "禁用帧应置位粘性开关")
	require.False(t, w.isClosed(), "Java 在跑时禁用不得关闭 wrapper")

	// 对称复位帧：清除粘性开关且不得关闭 wrapper。
	w.handleControl(ControlEnableRestart)
	require.False(t, w.isAutoRestartOff(), "enable_restart 必须复位粘性禁用")
	require.False(t, w.isClosed(), "恢复自动重启不得关闭 wrapper")

	// 幂等：重复下发无副作用。
	w.handleControl(ControlEnableRestart)
	require.False(t, w.isAutoRestartOff())
}

// FR-459 终验 Low #5：startJava 在临界区内复核「已关闭/已禁用」，消除 javaWait 复核点与 startJava
// 取锁之间的 TOCTOU——否则该窗口内到达的禁用帧会误判空闲而收摊，紧接着 startJava 却拉起 Java，
// 造成 wrapper 已退出、Java 无人托管的孤儿。
func TestWrapper_StartJavaRefusesWhenAutoRestartDisabled(t *testing.T) {
	w := &Wrapper{cfg: WrapperConfig{AutoRestart: true, StartCommand: crashImmediatelyCommand()},
		closing: make(chan struct{}), state: StateStopped}

	w.mu.Lock()
	w.autoRestartOff = true
	w.mu.Unlock()

	err := w.startJava()
	require.ErrorIs(t, err, errAutoRestartSuppressed, "已禁用自动重启时 startJava 必须拒绝拉起")
	w.mu.Lock()
	defer w.mu.Unlock()
	require.Nil(t, w.javaCmd, "拒绝启动时不得登记 Java 进程（避免孤儿）")
	require.False(t, w.javaStarting, "拒绝启动不得遗留「启动在飞」标记")
	require.Equal(t, StateStopped, w.state, "拒绝启动不得改写状态")
}
