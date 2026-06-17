package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WrapperConfig 是 daemon wrapper 子进程的启动配置。
// 通过环境变量从 Worker 传递给 wrapper 子进程（避免命令行长度/转义问题）。
type WrapperConfig struct {
	InstanceUUID  string            `json:"instance_uuid"`
	StartCommand  string            `json:"start_command"`
	WorkDir       string            `json:"work_dir"`
	EnvVars       map[string]string `json:"env_vars"`
	AutoRestart   bool              `json:"auto_restart"`
	PIDDir        string            `json:"pid_dir"`
	StartTimeout  time.Duration     `json:"start_timeout"` // Java 启动到首字节输出的等待（0=不限）
}

// 环境变量键名约定。Worker spawn wrapper 时写入这些变量。
const (
	EnvWrapperConfig = "JM_DAEMON_WRAPPER_CONFIG" // JSON 编码的 WrapperConfig
)

// controlCommand 是 Worker 通过 ChannelControl 下发的控制命令。
// 约定 payload 为 "stop" / "kill" / "ping" 文本。
const (
	CtrlStop  = "stop"
	CtrlKill  = "kill"
	CtrlPing  = "ping"
)

// ControlStop / ControlKill / ControlPing 是导出的控制命令常量，
// 供 daemonStrategy 跨包引用。
const (
	ControlStop = CtrlStop
	ControlKill = CtrlKill
	ControlPing = CtrlPing
)

// Wrapper 是 daemon wrapper 子进程的运行体。
// 它作为 Java 进程的父进程，负责：启动/重启 Java、监听 socket、
// 与 Worker 双向帧通信（转发 stdio + 接收控制命令）、维护 PID 文件。
// 参见 ADR-003: 守护进程 Wrapper 模式。
type Wrapper struct {
	cfg      WrapperConfig
	pidFile  *PIDFile
	addr     string

	mu          sync.Mutex
	javaCmd     *exec.Cmd
	javaStdin   io.WriteCloser
	listener    net.Listener
	workerConn  netConn
	state       InstanceState
	closed      bool
	closing     chan struct{}
	crashCount  int
}

// netConn 别名避免直接依赖 net（便于测试替换）。
type netConn = io.ReadWriteCloser

// Run 启动 wrapper 主循环，阻塞直到收到 stop/kill 或 Java 永久退出且无自动重启。
func Run(cfg WrapperConfig) error {
	return RunWithReady(cfg, nil)
}

// RunWithReady 同 Run，并在 wrapper 监听就绪后向 ready 发送信号（非阻塞）。
// ready 为 nil 时忽略。供测试/集成方等待 wrapper 可被拨号后再连接，消除竞态。
func RunWithReady(cfg WrapperConfig, ready chan<- struct{}) error {
	w := &Wrapper{
		cfg:      cfg,
		pidFile:  NewPIDFile(PIDFileName(cfg.PIDDir, cfg.InstanceUUID)),
		addr:     SocketAddr(cfg.PIDDir, cfg.InstanceUUID),
		state:    StateStopped,
		closing:  make(chan struct{}),
	}
	return w.run(ready)
}

// InstanceState 复用 process 包的状态语义（此处独立定义避免循环依赖）。
type InstanceState string

const (
	StateStopped  InstanceState = "STOPPED"
	StateStarting InstanceState = "STARTING"
	StateRunning  InstanceState = "RUNNING"
	StateStopping InstanceState = "STOPPING"
	StateCrashed  InstanceState = "CRASHED"
)

func (w *Wrapper) run(ready chan<- struct{}) error {
	ln, err := Listen(w.addr)
	if err != nil {
		return fmt.Errorf("wrapper 监听失败 %s: %w", w.addr, err)
	}
	w.listener = ln
	defer ln.Close()
	slog.Info("wrapper 监听就绪", "instanceId", w.cfg.InstanceUUID, "addr", w.addr)

	// 通知监听就绪，可被拨号
	if ready != nil {
		select {
		case ready <- struct{}{}:
		default:
		}
	}

	// 写 PID 文件（wrapper pid + socket 地址，java pid 启动后补写）
	if err := w.pidFile.WriteRecord(PIDRecord{
		WrapperPID:   os.Getpid(),
		InstanceUUID: w.cfg.InstanceUUID,
		SocketAddr:   w.addr,
	}); err != nil {
		slog.Warn("写 PID 文件失败", "instanceId", w.cfg.InstanceUUID, "error", err)
	}
	defer w.cleanupPIDFile()

	// 持续接受 Worker 连接：Worker 重启后可 reconnect。
	// 每次接受后替换旧连接并启动读循环处理控制帧。
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				slog.Info("wrapper Accept 退出", "instanceId", w.cfg.InstanceUUID, "error", err)
				return
			}
			w.mu.Lock()
			// 关闭旧连接（若存在），避免并发写
			if w.workerConn != nil {
				_ = w.workerConn.Close()
			}
			w.workerConn = conn
			w.mu.Unlock()
			slog.Info("wrapper 接受 Worker 连接", "instanceId", w.cfg.InstanceUUID)
			go w.readLoop(conn)
		}
	}()

	// 启动 Java
	if err := w.startJava(); err != nil {
		return fmt.Errorf("启动 Java 失败: %w", err)
	}

	// 主循环：阻塞直到 closing 信号（Java 永久退出 / 收到 stop/kill）。
	for {
		select {
		case <-w.closing:
			return nil
		}
	}
}

func (w *Wrapper) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// startJava 启动 Java 进程并写入 java pid。
func (w *Wrapper) startJava() error {
	w.mu.Lock()
	w.state = StateStarting
	w.mu.Unlock()

	cmd := buildJavaCmd(w.cfg)
	javaStdin, err := cmd.StdinPipe()
	if err != nil {
		w.setState(StateCrashed)
		return fmt.Errorf("创建 Java stdin 管道失败: %w", err)
	}

	// Java 输出转发到 Worker（若已连接）
	cmd.Stdout = &wrapperOutput{w: w, stream: ChannelStdout}
	cmd.Stderr = &wrapperOutput{w: w, stream: ChannelStderr}

	if err := cmd.Start(); err != nil {
		w.setState(StateCrashed)
		return fmt.Errorf("Java 启动失败: %w", err)
	}

	w.mu.Lock()
	w.javaCmd = cmd
	w.javaStdin = javaStdin
	w.state = StateRunning
	javaPID := cmd.Process.Pid
	w.mu.Unlock()

	// 补写 java pid 到 PID 文件
	rec, _ := w.pidFile.ReadRecord()
	if rec == nil {
		rec = &PIDRecord{}
	}
	rec.JavaPID = javaPID
	rec.WrapperPID = os.Getpid()
	rec.SocketAddr = w.addr
	rec.InstanceUUID = w.cfg.InstanceUUID
	if err := w.pidFile.WriteRecord(*rec); err != nil {
		slog.Warn("更新 PID 文件 java pid 失败", "error", err)
	}
	slog.Info("Java 已启动", "instanceId", w.cfg.InstanceUUID, "javaPid", javaPID)

	// 等待 Java 退出
	go w.javaWait(cmd)
	return nil
}

// javaWait 等待 Java 进程退出，按自动重启策略处理。
func (w *Wrapper) javaWait(cmd *exec.Cmd) {
	err := cmd.Wait()
	w.mu.Lock()
	w.javaCmd = nil
	if w.javaStdin != nil {
		w.javaStdin.Close()
		w.javaStdin = nil
	}
	prevState := w.state
	w.mu.Unlock()

	slog.Warn("Java 退出", "instanceId", w.cfg.InstanceUUID, "err", err)

	// 主动停止（stop/kill）→ 不重启
	if prevState == StateStopping || prevState == StateStopped {
		w.setState(StateStopped)
		w.signalClose()
		return
	}

	w.mu.Lock()
	w.crashCount++
	crashCount := w.crashCount
	w.state = StateCrashed
	w.mu.Unlock()
	w.setState(StateCrashed)

	if !w.cfg.AutoRestart || w.isClosed() {
		w.signalClose()
		return
	}

	delay := backoffDelay(crashCount)
	slog.Info("wrapper 将延迟重启 Java", "instanceId", w.cfg.InstanceUUID, "delay", delay, "crashCount", crashCount)
	select {
	case <-time.After(delay):
	case <-w.closing:
		return
	}
	if w.isClosed() {
		return
	}
	if err := w.startJava(); err != nil {
		slog.Error("wrapper 重启 Java 失败", "instanceId", w.cfg.InstanceUUID, "error", err)
		w.signalClose()
	}
}

func (w *Wrapper) setState(s InstanceState) {
	w.mu.Lock()
	w.state = s
	w.mu.Unlock()
}

func (w *Wrapper) signalClose() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.closing)
}

// readLoop 处理 Worker 下发的帧：stdin 数据 / 控制命令。
func (w *Wrapper) readLoop(conn netConn) {
	for {
		fr, err := Decode(conn)
		if err != nil {
			// 连接断开（Worker 重启/退出）— 不杀 Java，wrapper 继续托管
			slog.Info("Worker 连接断开，wrapper 继续托管 Java", "instanceId", w.cfg.InstanceUUID, "error", err)
			return
		}
		switch fr.Channel {
		case ChannelStdin:
			w.mu.Lock()
			stdin := w.javaStdin
			w.mu.Unlock()
			if stdin != nil {
				if _, err := stdin.Write(fr.Payload); err != nil {
					slog.Warn("写入 Java stdin 失败", "error", err)
				}
			}
		case ChannelControl:
			w.handleControl(string(fr.Payload))
		}
	}
}

// handleControl 处理控制命令。
func (w *Wrapper) handleControl(cmd string) {
	switch strings.TrimSpace(cmd) {
	case CtrlStop:
		slog.Info("wrapper 收到 stop", "instanceId", w.cfg.InstanceUUID)
		w.stopJava(false)
	case CtrlKill:
		slog.Info("wrapper 收到 kill", "instanceId", w.cfg.InstanceUUID)
		w.stopJava(true)
	case CtrlPing:
		// 心跳：回写控制响应
		w.mu.Lock()
		conn := w.workerConn
		w.mu.Unlock()
		if conn != nil {
			resp := &Frame{Header: Header{Channel: ChannelControl, Type: TypeResponse}, Payload: []byte("pong")}
			_ = resp.Encode(conn)
		}
	default:
		slog.Warn("wrapper 收到未知控制命令", "cmd", cmd)
	}
}

// stopJava 停止 Java 进程树并通知 wrapper 主循环退出。
// force=true 强制 Kill；否则先尝试 Interrupt（unix），失败回退 Kill。
// Windows 上 Kill 仅终止 cmd.exe，其子进程（如 ping）会继承句柄继续运行，
// 导致 cmd.Wait 阻塞；因此用 taskkill /T 递归终止进程树。
// stop 后 wrapper 应退出，直接 signalClose 不依赖 javaWait。
func (w *Wrapper) stopJava(force bool) {
	w.mu.Lock()
	cmd := w.javaCmd
	w.state = StateStopping
	w.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		w.signalClose()
		return
	}
	if runtime.GOOS == "windows" {
		// taskkill /T /F 递归终止 Java 进程树，避免子进程句柄导致 Wait 阻塞
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	} else if force {
		_ = cmd.Process.Kill()
	} else {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			_ = cmd.Process.Kill()
		}
	}
	// stop 语义：wrapper 应退出，通知主循环
	w.signalClose()
}

func (w *Wrapper) cleanupPIDFile() {
	_ = w.pidFile.Remove()
	RemoveSocket(w.addr)
}

// wrapperOutput 把 Java 的 stdout/stderr 作为帧转发给已连接的 Worker。
type wrapperOutput struct {
	w      *Wrapper
	stream Channel
}

func (o *wrapperOutput) Write(p []byte) (int, error) {
	o.w.mu.Lock()
	conn := o.w.workerConn
	o.w.mu.Unlock()
	if conn != nil {
		f := &Frame{Header: Header{Channel: o.stream, Type: TypeData}, Payload: append([]byte(nil), p...)}
		_ = f.Encode(conn)
	}
	return len(p), nil
}

// buildJavaCmd 构造 Java 进程命令。跨平台：Windows 用 cmd.exe /s /c，其他用 sh -c。
func buildJavaCmd(cfg WrapperConfig) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// /s /c 让 cmd.exe 先剥掉外层引号再解析，避免路径引号被当作可执行文件名的一部分
		cmd = exec.Command("cmd.exe", "/s", "/c", `"`+cfg.StartCommand+`"`)
	} else {
		cmd = exec.Command("sh", "-c", cfg.StartCommand)
	}
	cmd.Dir = cfg.WorkDir
	for k, v := range cfg.EnvVars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd
}

// backoffDelay 指数退避：1s→2s→4s→...→30s 上限。
func backoffDelay(crashCount int) time.Duration {
	delay := time.Second * time.Duration(1<<uint(crashCount-1))
	maxDelay := 30 * time.Second
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// ParseWrapperConfigFromEnv 从环境变量解析 wrapper 配置。
// 供 cmd/worker 的 daemon 子命令调用。
func ParseWrapperConfigFromEnv() (WrapperConfig, error) {
	raw := os.Getenv(EnvWrapperConfig)
	if raw == "" {
		return WrapperConfig{}, fmt.Errorf("环境变量 %s 为空", EnvWrapperConfig)
	}
	var cfg WrapperConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return WrapperConfig{}, fmt.Errorf("解析 wrapper 配置失败: %w", err)
	}
	return cfg, nil
}

// ParseAutoRestart 辅助从字符串解析布尔（供环境变量传递）。
func ParseAutoRestart(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}
