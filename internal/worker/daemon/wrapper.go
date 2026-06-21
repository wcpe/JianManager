package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WrapperConfig 是 daemon wrapper 子进程的启动配置。
// 通过环境变量从 Worker 传递给 wrapper 子进程（避免命令行长度/转义问题）。
// WrapperConfig 是 daemon wrapper 子进程的启动配置。
// 通过环境变量从 Worker 传递给 wrapper 子进程（避免命令行长度/转义问题）。
type WrapperConfig struct {
	InstanceUUID string `json:"instance_uuid"`
	StartCommand string `json:"start_command"`
	// StopCommand 优雅停止时写入进程 stdin 的命令（不含换行）。
	// 由 Control Plane 按实例角色派生（MC 后端用 stop，代理用 end）。
	// 为空时 wrapper 回退到 MC 的 "stop"，保证旧实例/恢复路径行为不变。
	StopCommand  string            `json:"stop_command,omitempty"`
	WorkDir      string            `json:"work_dir"`
	EnvVars      map[string]string `json:"env_vars"`
	JavaHome     string            `json:"java_home,omitempty"`
	JDKBinPath   string            `json:"jdk_bin_path,omitempty"`
	AutoRestart  bool              `json:"auto_restart"`
	PIDDir       string            `json:"pid_dir"`
	StartTimeout time.Duration     `json:"start_timeout"` // Java 启动到首字节输出的等待（0=不限）
	// ProbePort 透传到 PID 记录，使 Worker 重启恢复后心跳继续自采该实例 ServerProbe 指标（FR-060）。
	ProbePort int `json:"probe_port,omitempty"`
	// GracefulStopTimeoutSeconds 优雅停止后等待进程自行退出的上限（秒，CP 从平台设置
	// graceful_stop.timeout 取生效值后于启动时下发，FR-063）。>0 时 wrapper 用它做超时强杀兜底；
	// 0=未指定，回退环境变量/默认。值在启动时定型，故对设置变更后「新启动」的实例生效。
	GracefulStopTimeoutSeconds int `json:"graceful_stop_timeout_seconds,omitempty"`
}

// 环境变量键名约定。Worker spawn wrapper 时写入这些变量。
const (
	EnvWrapperConfig = "JM_DAEMON_WRAPPER_CONFIG" // JSON 编码的 WrapperConfig
)

// controlCommand 是 Worker 通过 ChannelControl 下发的控制命令。
// 约定 payload 为 "stop" / "kill" / "ping" 文本。
const (
	CtrlStop = "stop"
	CtrlKill = "kill"
	CtrlPing = "ping"
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
	cfg     WrapperConfig
	pidFile *PIDFile
	addr    string

	mu            sync.Mutex
	javaCmd       *exec.Cmd
	javaStdin     io.WriteCloser
	listener      net.Listener
	workerConn    netConn
	state         InstanceState
	closed        bool
	closing       chan struct{}
	crashCount    int
	fastCrashes   int       // 连续「快速崩溃」（启动后很快退出）计数
	javaStartedAt time.Time // 本次 Java 启动时刻，用于判断是否快速崩溃
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
		cfg:     cfg,
		pidFile: NewPIDFile(PIDFileName(cfg.PIDDir, cfg.InstanceUUID)),
		addr:    SocketAddr(cfg.PIDDir, cfg.InstanceUUID),
		state:   StateStopped,
		closing: make(chan struct{}),
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
		WorkDir:      w.cfg.WorkDir,
		ProbePort:    w.cfg.ProbePort,
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
	rec.ProbePort = w.cfg.ProbePort
	if err := w.pidFile.WriteRecord(*rec); err != nil {
		slog.Warn("更新 PID 文件 java pid 失败", "error", err)
	}
	slog.Info("Java 已启动", "instanceId", w.cfg.InstanceUUID, "javaPid", javaPID)

	// 记录启动时刻并等待 Java 退出（用于快速崩溃判定）
	w.mu.Lock()
	w.javaStartedAt = time.Now()
	w.mu.Unlock()
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
	if time.Since(w.javaStartedAt) < minHealthyUptime {
		w.fastCrashes++
	} else {
		w.fastCrashes = 0 // 曾健康运行过，重置快速崩溃计数
	}
	fastCrashes := w.fastCrashes
	w.state = StateCrashed
	w.mu.Unlock()
	w.setState(StateCrashed)

	// 持续快速崩溃（缺核心 jar / JDK 不兼容等）：放弃自动重启、退出 wrapper，
	// 实例落到 CRASHED 而非永远 RUNNING（否则状态误导且无法删除）。
	if fastCrashes >= maxFastCrashes {
		slog.Warn("Java 连续快速崩溃，停止自动重启", "instanceId", w.cfg.InstanceUUID, "fastCrashes", fastCrashes)
		w.signalClose()
		return
	}

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

// stopJava 停止 Java 进程。
// force=true（kill）：直接强杀进程树并立即退出，不等关服序列。
// force=false（stop）：向 MC 服务器 stdin 写 "stop" 命令触发优雅关服，
// 让其保存世界并输出完整停止日志；Java 自行退出后由 javaWait 善后 signalClose。
// 这样终端能看到「Stopping the server / Saving worlds」等停止日志，而非被瞬间杀掉。
// 超时仍未退出则强杀兜底，避免 wrapper 永不退出。
func (w *Wrapper) stopJava(force bool) {
	w.mu.Lock()
	cmd := w.javaCmd
	stdin := w.javaStdin
	w.state = StateStopping
	w.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		w.signalClose()
		return
	}

	if force {
		w.forceKill(cmd)
		w.signalClose()
		return
	}

	// 优雅停止：向进程 stdin 写关服命令。命令按实例角色派生（MC 后端 stop / 代理 end），
	// 代理不认 "stop"，若误发会一直挂到超时强杀、且重启时旧进程仍占端口导致崩溃。
	// 无 stdin（非交互进程）时回退信号/强杀。
	stopCmd := resolveStopCommand(w.cfg.StopCommand)
	wrote := false
	if stdin != nil {
		if _, err := stdin.Write([]byte(stopCmd + "\n")); err == nil {
			wrote = true
		}
	}
	if !wrote {
		if runtime.GOOS != "windows" {
			_ = cmd.Process.Signal(os.Interrupt)
		} else {
			w.forceKill(cmd)
			w.signalClose()
			return
		}
	}
	// 超时兜底：关服序列卡死时强杀（javaWait 在 Java 退出时把 javaCmd 置 nil）。
	go func() {
		time.Sleep(w.resolveGracefulStopTimeout())
		w.mu.Lock()
		still := w.javaCmd
		w.mu.Unlock()
		if still != nil && still.Process != nil {
			slog.Warn("优雅停止超时，强制终止 Java", "instanceId", w.cfg.InstanceUUID)
			w.forceKill(still)
		}
	}()
}

// forceKill 强制终止 Java 进程树。Windows 上 Kill 仅终止 cmd.exe，子进程继承句柄继续运行
// 导致 cmd.Wait 阻塞，故用 taskkill /T 递归终止整棵进程树。
func (w *Wrapper) forceKill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	} else {
		_ = cmd.Process.Kill()
	}
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

// buildJavaCmd 构造 Java 进程命令。跨平台：Windows 用 cmd.exe /c，其他用 sh -c。
func buildJavaCmd(cfg WrapperConfig) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", cfg.StartCommand)
	} else {
		cmd = exec.Command("sh", "-c", cfg.StartCommand)
	}
	cmd.Dir = cfg.WorkDir
	cmd.Env = composeEnv(os.Environ(), cfg)
	return cmd
}

// composeEnv 合成进程环境：基线 (os.Environ) + JAVA_HOME + PATH 前置 + 实例 EnvVars。
// 与 internal/worker/process.ComposeEnv 行为一致，本地复制以避免 daemon ↔ process 包循环依赖。
func composeEnv(base []string, cfg WrapperConfig) []string {
	out := append([]string(nil), base...)

	javaBin := cfg.JDKBinPath
	if javaBin == "" && cfg.JavaHome != "" {
		javaBin = filepath.Join(cfg.JavaHome, "bin")
	}
	pathKey := "PATH"
	if runtime.GOOS == "windows" {
		pathKey = "Path"
	}

	if cfg.JavaHome != "" {
		out = append(out, "JAVA_HOME="+cfg.JavaHome)
	}
	if javaBin != "" {
		replaced := false
		for i, kv := range out {
			if k, v, ok := splitEnvKey(kv); ok && k == pathKey {
				out[i] = k + "=" + javaBin + string(os.PathListSeparator) + v
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, pathKey+"="+javaBin)
		}
	}
	for k, v := range cfg.EnvVars {
		out = append(out, k+"="+v)
	}
	return out
}

func splitEnvKey(kv string) (key, value string, ok bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}

const (
	// minHealthyUptime：Java 存活不足此时长即视为「快速崩溃」（启动即挂）。
	minHealthyUptime = 10 * time.Second
	// maxFastCrashes：连续快速崩溃达到此数即放弃自动重启、wrapper 退出（实例标 CRASHED）。
	maxFastCrashes = 5
	// gracefulStopTimeout：优雅停止（向 MC 发 "stop"）后等待 Java 自行退出的上限，超时强杀兜底。
	gracefulStopTimeout = 30 * time.Second
	// envGracefulStopTimeout：覆盖优雅停止超时的环境变量（Go duration 文本）。
	// 供测试/集成缩短——测试替身进程（ping/sleep）不响应 "stop"，否则每个停止用例都要等满超时。
	envGracefulStopTimeout = "JIANMANAGER_GRACEFUL_STOP_TIMEOUT"
)

// defaultStopCommand 是未指定停止命令时的回退值（Minecraft 服务端关服命令）。
const defaultStopCommand = "stop"

// resolveStopCommand 返回优雅停止时写入 stdin 的命令：配置非空则用配置（去空白），
// 否则回退到 MC 的 "stop"。保证旧实例、PID 恢复等未携带停止命令的路径行为不变。
func resolveStopCommand(cfg string) string {
	if c := strings.TrimSpace(cfg); c != "" {
		return c
	}
	return defaultStopCommand
}

// resolveGracefulStopTimeout 返回优雅停止超时，按优先级解析：
//  1. 启动时下发的 config 值（CP 从平台设置 graceful_stop.timeout 取生效值，FR-063）；
//  2. 环境变量（供测试/集成缩短）；
//  3. 默认值。
//
// config 值在实例启动时定型，故设置变更只对其后「新启动」的实例生效，已运行实例保留启动时的值。
func (w *Wrapper) resolveGracefulStopTimeout() time.Duration {
	if w.cfg.GracefulStopTimeoutSeconds > 0 {
		return time.Duration(w.cfg.GracefulStopTimeoutSeconds) * time.Second
	}
	if v := os.Getenv(envGracefulStopTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return gracefulStopTimeout
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
