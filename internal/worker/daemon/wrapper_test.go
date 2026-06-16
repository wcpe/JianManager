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

// TestWrapper_StopControl 验证 wrapper 端到端：
//   - wrapper 监听就绪（就绪信号）
//   - Worker 拨号连接 socket
//   - PID 文件已写入且 wrapper pid 存活
//   - stop 控制命令使 Java 退出、wrapper 结束
// 参见 ADR-003。
func TestWrapper_StopControl(t *testing.T) {
	pidDir := t.TempDir()
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
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper 监听就绪超时")
	}

	conn, err := Dial(addr)
	require.NoError(t, err)
	defer conn.Close()

	// PID 文件已写入，wrapper pid 存活
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	require.Eventually(t, func() bool {
		rec, err := pf.ReadRecord()
		return err == nil && IsPIDAlive(rec.WrapperPID)
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
}

// TestWrapper_StdoutForward 验证 wrapper 把 Java 的 stdout 转发为帧给 Worker。
func TestWrapper_StdoutForward(t *testing.T) {
	pidDir := t.TempDir()
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

	// 停止 wrapper，回收
	stopFrame := &Frame{Header: Header{Channel: ChannelControl, Type: TypeCommand}, Payload: []byte(ControlStop)}
	_ = stopFrame.Encode(conn)
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

// TestWrapper_PIDFileCleanup wrapper 退出后 PID 文件应被清理。
func TestWrapper_PIDFileCleanup(t *testing.T) {
	pidDir := t.TempDir()
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
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	require.Eventually(t, func() bool {
		rec, err := pf.ReadRecord()
		return err == nil && rec.JavaPID != 0
	}, 3*time.Second, 50*time.Millisecond, "应能读到 Java pid")
	rec, _ := pf.ReadRecord()
	javaPID := rec.JavaPID

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
	// 让 TempDir 的 RemoveAll 能成功。
	if javaPID != 0 {
		require.Eventually(t, func() bool {
			return !IsPIDAlive(javaPID)
		}, 5*time.Second, 50*time.Millisecond, "Java 进程应已退出")
	}
}
