package daemon

import (
	"os"
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

// testWorkDir 等价 t.TempDir()，但清理带 Windows 重试退避。
//
// 观察项（2026-09-07）：全量并行负载下本包曾偶发 runtime netpoll fatal
// （单跑/复跑均未复现）。同轮已修复 wrapper 停止后 server 侧连接句柄
// 泄漏与测试 TempDir 句柄残留两类确定性 flake，netpoll 若再复现，
// 从「stop 未等 goroutine 退出即关 pipe」的并发 close 方向深挖。
//
// 只等 javaPID（cmd.exe）退出还不够：taskkill /T /F 异步终止整棵进程树，孙进程
// （ping 等）可能比父进程晚消失几百毫秒，其继承的 CWD 句柄仍占用 pidDir——
// t.TempDir() 的 RemoveAll 届时直接 fatal。故改用自定义清理：RemoveAll 失败时
// 以 100ms 步进重试至 5s；仍失败则放任（进程树终会退出，目录由 OS 临时目录回收），
// 不让清理噪音淹没真实测试结果。
func testWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jm-wrapper-test-")
	require.NoError(t, err)
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := os.RemoveAll(dir)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Logf("测试目录清理失败（放任由 OS 回收）: %v", err)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return dir
}

// TestWrapper_StopControl 验证 wrapper 端到端：
//   - wrapper 监听就绪（就绪信号）
//   - Worker 拨号连接 socket
//   - PID 文件已写入且 wrapper pid 存活
//   - stop 控制命令使 Java 退出、wrapper 结束
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
