package process

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/wxys233/JianManager/internal/worker/daemon"
)

// daemonStrategy 通过独立 wrapper 子进程管理游戏服（ADR-003）。
// Worker spawn wrapper 子进程（脱离 Worker 进程组），wrapper 作为 Java 父进程。
// 两者通过 Unix Socket / Named Pipe + 二进制帧协议通信。
// Worker 退出/重启时 wrapper 继续运行，Worker 重启后通过 PID 文件 reconnect。
type daemonStrategy struct {
	mu      sync.Mutex
	spec    CommandSpec
	mgr     *Manager
	pidDir  string

	wrapperCmd *exec.Cmd
	conn       net.Conn
	state      InstanceState
	closed     bool

	// readWg 跟踪输出读取 goroutine
	readWg sync.WaitGroup
	// connectDone 在尝试连接 wrapper 完成后关闭（成功/失败）
	connectDone chan struct{}
}

// newDaemonStrategy 构造 daemon 策略。
func newDaemonStrategy(mgr *Manager, spec CommandSpec) *daemonStrategy {
	return &daemonStrategy{
		spec:        spec,
		mgr:         mgr,
		pidDir:      mgr.pidDir,
		state:       StateStopped,
		connectDone: make(chan struct{}),
	}
}

func (d *daemonStrategy) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 清理可能残留的旧 socket 文件（Unix）
	addr := daemon.SocketAddr(d.pidDir, d.spec.UUID)
	daemon.RemoveSocket(addr)

	// 构造 wrapper 配置，通过环境变量传递（避免命令行转义问题）
	cfg := daemon.WrapperConfig{
		InstanceUUID: d.spec.UUID,
		StartCommand: d.spec.StartCommand,
		WorkDir:      d.spec.WorkDir,
		EnvVars:      d.spec.EnvVars,
		AutoRestart:  d.spec.AutoRestart,
		PIDDir:       d.pidDir,
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		d.state = StateCrashed
		return fmt.Errorf("编码 wrapper 配置失败: %w", err)
	}

	// wrapper 复用当前 worker 二进制，通过 daemon 子命令模式启动
	exePath, err := os.Executable()
	if err != nil {
		d.state = StateCrashed
		return fmt.Errorf("获取 worker 可执行文件路径失败: %w", err)
	}

	cmd := exec.Command(exePath, "daemon")
	cmd.Env = append(os.Environ(), daemon.EnvWrapperConfig+"="+string(cfgBytes))
	// wrapper 必须脱离 Worker 进程组：Worker 退出时 wrapper 不被牵连。
	// Linux/macOS: setsid 新建会话；Windows: CREATE_NEW_PROCESS_GROUP。
	setDetachedAttr(cmd)

	// wrapper 的日志走独立流，避免与游戏服输出混淆
	cmd.Stdout = newSlogWriter(d.spec.UUID, "wrapper-stdout")
	cmd.Stderr = newSlogWriter(d.spec.UUID, "wrapper-stderr")

	if err := cmd.Start(); err != nil {
		d.state = StateCrashed
		return fmt.Errorf("启动 wrapper 失败: %w", err)
	}
	d.wrapperCmd = cmd
	d.state = StateRunning
	slog.Info("daemon wrapper 已启动", "instanceId", d.spec.UUID, "wrapperPid", cmd.Process.Pid)

	// 异步连接 wrapper 的 socket（wrapper 需要时间监听就绪）
	go d.connectLoop(addr)

	// 回收 wrapper 进程（wrapper 正常运行时不会退出）
	go d.reapWrapper()
	return nil
}

// connectLoop 重试连接 wrapper socket，成功后启动输出读取循环。
func (d *daemonStrategy) connectLoop(addr string) {
	defer close(d.connectDone)

	const maxWait = 10 * time.Second
	deadline := time.Now().Add(maxWait)
	for {
		if time.Now().After(deadline) {
			slog.Warn("连接 wrapper socket 超时", "instanceId", d.spec.UUID)
			return
		}
		conn, err := daemon.Dial(addr)
		if err == nil {
			d.mu.Lock()
			d.conn = conn
			d.mu.Unlock()
			slog.Info("已连接 wrapper socket", "instanceId", d.spec.UUID, "addr", addr)
			d.readWg.Add(1)
			go d.readLoop(conn)
			return
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-d.stopConnectCh():
			return
		}
	}
}

// stopConnectCh 返回一个在 Close 时关闭的信号通道（暂用 nil，连接循环靠超时退出）。
func (d *daemonStrategy) stopConnectCh() <-chan struct{} { return nil }

// readLoop 从 wrapper 读取帧：stdout/stderr 转发到 onOutput，control 响应忽略。
func (d *daemonStrategy) readLoop(conn net.Conn) {
	defer d.readWg.Done()
	for {
		fr, err := daemon.Decode(conn)
		if err != nil {
			// 连接断开：wrapper 重启或退出
			slog.Info("wrapper socket 连接断开", "instanceId", d.spec.UUID, "error", err)
			return
		}
		switch fr.Channel {
		case daemon.ChannelStdout, daemon.ChannelStderr:
			if d.mgr.onOutput != nil {
				stream := "stdout"
				if fr.Channel == daemon.ChannelStderr {
					stream = "stderr"
				}
				data := fr.Payload
				// Windows 上 Java 输出可能为 GBK，这里复用 direct 的转换逻辑
				if runtime.GOOS == "windows" {
					data = decodeGBK(fr.Payload)
				}
				d.mgr.onOutput(d.spec.UUID, stream, data)
			}
		}
	}
}

// reapWrapper 回收 wrapper 进程（仅在 wrapper 退出时触发）。
func (d *daemonStrategy) reapWrapper() {
	cmd := d.wrapperCmd
	if cmd == nil {
		return
	}
	err := cmd.Wait()
	slog.Info("wrapper 进程退出", "instanceId", d.spec.UUID, "err", err)
	d.mu.Lock()
	if !d.closed {
		d.state = StateCrashed
	}
	d.mu.Unlock()
}

func (d *daemonStrategy) Stop() error {
	d.mu.Lock()
	d.state = StateStopping
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return nil
	}
	// 通过控制帧通知 wrapper 停止 Java
	return d.sendControl(daemon.ControlStop)
}

func (d *daemonStrategy) Kill() error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn != nil {
		_ = d.sendControl(daemon.ControlKill)
	}
	// 同时直接 kill wrapper（兜底）
	d.mu.Lock()
	cmd := d.wrapperCmd
	d.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func (d *daemonStrategy) SendCommand(command string) error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("实例 %s 未连接 wrapper", d.spec.UUID)
	}
	f := &daemon.Frame{
		Header:  daemon.Header{Channel: daemon.ChannelStdin, Type: daemon.TypeData},
		Payload: []byte(command + "\n"),
	}
	return f.Encode(conn)
}

func (d *daemonStrategy) State() InstanceState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// Close 断开与 wrapper 的连接，不杀游戏服（daemon 优雅退出语义）。
// wrapper 进程继续托管 Java，Worker 重启后可 reconnect。
func (d *daemonStrategy) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	conn := d.conn
	d.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	// 等待读取 goroutine 退出（带超时，避免卡住）
	done := make(chan struct{})
	go func() {
		d.readWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// sendControl 向 wrapper 下发控制命令。
func (d *daemonStrategy) sendControl(cmd string) error {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("wrapper 未连接")
	}
	f := &daemon.Frame{
		Header:  daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand},
		Payload: []byte(cmd),
	}
	return f.Encode(conn)
}

// Reconnect 在 Worker 重启后重新连接已存活的 wrapper。
// 成功返回 true。由 Manager.RecoverDaemonInstances 调用。
func (d *daemonStrategy) Reconnect(addr string) error {
	conn, err := daemon.Dial(addr)
	if err != nil {
		return fmt.Errorf("reconnect wrapper 失败: %w", err)
	}
	d.mu.Lock()
	d.conn = conn
	d.state = StateRunning
	d.closed = false
	d.connectDone = make(chan struct{})
	close(d.connectDone)
	d.mu.Unlock()
	d.readWg.Add(1)
	go d.readLoop(conn)
	slog.Info("已恢复 daemon wrapper 连接", "instanceId", d.spec.UUID, "addr", addr)
	return nil
}

// SetWrapperPID 供恢复时记录 wrapper 进程句柄（用于 Kill 兜底）。
func (d *daemonStrategy) SetWrapperPID(pid int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if pid > 0 {
		proc, err := os.FindProcess(pid)
		if err == nil {
			d.wrapperCmd = exec.Command("") // 占位，进程已存在
			if d.wrapperCmd.Process == nil {
				d.wrapperCmd.Process = proc
			}
		}
	}
}

// decodeGBK 将 GBK 字节解码为 UTF-8（Windows Java 输出常见）。
func decodeGBK(p []byte) []byte {
	// 复用 direct 包内转换器，避免重复导入 transform
	return gbkToUTF8(p)
}

// newSlogWriter 把 wrapper 子进程的标准输出/错误写到 slog。
type slogWriter struct {
	instanceID string
	stream     string
}

func newSlogWriter(instanceID, stream string) io.Writer {
	return &slogWriter{instanceID: instanceID, stream: stream}
}

func (w *slogWriter) Write(p []byte) (int, error) {
	slog.Debug("wrapper 输出", "instanceId", w.instanceID, "stream", w.stream, "data", string(p))
	return len(p), nil
}
