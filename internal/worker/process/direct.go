package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// directStrategy 直接子进程启动方式。
// Worker 作为游戏服进程的父进程，Worker 退出时游戏服随之退出。
// 适用于开发/测试，或不需要进程隔离的场景。与 ADR-003 的 daemon 方式互补。
type directStrategy struct {
	mu     sync.Mutex
	spec   CommandSpec
	mgr    *Manager
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	state  InstanceState
	// crashCount 记录连续崩溃次数，用于指数退避。成功重启后不清零，
	// 持续崩溃退避逐步加长；正常停止时由 Manager 重置实例账面 CrashCount。
	crashCount int
	wg         sync.WaitGroup
	closed     bool
}

// newDirectStrategy 构造 direct 策略。
func newDirectStrategy(mgr *Manager, spec CommandSpec) *directStrategy {
	return &directStrategy{
		spec:  spec,
		mgr:   mgr,
		state: StateStopped,
	}
}

func (d *directStrategy) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 跨平台 shell 命令执行
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// 不加 chcp — instanceWriter 会将 GBK 输出转换为 UTF-8
		cmd = exec.Command("cmd.exe", "/c", d.spec.StartCommand)
	} else {
		cmd = exec.Command("sh", "-c", d.spec.StartCommand)
	}
	cmd.Dir = d.spec.WorkDir
	cmd.Stdout = &instanceWriter{manager: d.mgr, instanceID: d.spec.UUID, stream: "stdout"}
	cmd.Stderr = &instanceWriter{manager: d.mgr, instanceID: d.spec.UUID, stream: "stderr"}

	// 创建 stdin 管道（必须在 Start 前）
	stdin, err := cmd.StdinPipe()
	if err != nil {
		d.state = StateCrashed
		return fmt.Errorf("创建 stdin 管道失败: %w", err)
	}

	cmd.Env = ComposeEnv(os.Environ(), d.spec)

	if err := cmd.Start(); err != nil {
		stdin.Close()
		d.state = StateCrashed
		return fmt.Errorf("启动进程失败: %w", err)
	}

	d.cmd = cmd
	d.stdin = stdin
	d.state = StateRunning
	slog.Info("direct 实例已启动", "instanceId", d.spec.UUID, "pid", cmd.Process.Pid)

	d.wg.Add(1)
	go d.waitLoop()
	return nil
}

// waitLoop 等待进程退出并按需触发指数退避重启。
// 重启通过回调 Manager.Start 完成，使状态机统一在 Manager 层记账。
func (d *directStrategy) waitLoop() {
	defer d.wg.Done()
	cmd := d.cmd
	err := cmd.Wait()

	d.mu.Lock()
	if d.stdin != nil {
		d.stdin.Close()
	}
	// Stopping/Stopped 视为正常停止（Windows Kill 返回非零退出码）
	if d.state == StateStopping || d.state == StateStopped {
		d.state = StateStopped
		d.cmd = nil
		d.mu.Unlock()
		slog.Info("direct 实例已停止", "instanceId", d.spec.UUID)
		return
	}
	d.state = StateCrashed
	d.cmd = nil
	d.crashCount++
	crashCount := d.crashCount
	d.mu.Unlock()

	slog.Warn("direct 实例崩溃", "instanceId", d.spec.UUID, "err", err, "crashCount", crashCount)

	// 指数退避自动重启交给 Manager.Start（它会在 CRASHED 状态下允许重启）
	if d.spec.AutoRestart {
		delay := backoffDelay(crashCount)
		slog.Info("将在延迟后自动重启", "instanceId", d.spec.UUID, "delay", delay, "crashCount", crashCount)
		time.Sleep(delay)

		d.mu.Lock()
		currentState := d.state
		d.mu.Unlock()
		if currentState == StateCrashed {
			if restartErr := d.mgr.Start(d.spec.UUID); restartErr != nil {
				slog.Error("自动重启失败", "instanceId", d.spec.UUID, "error", restartErr)
			}
		}
	}
}

func (d *directStrategy) Stop() error {
	d.mu.Lock()
	cmd := d.cmd
	d.state = StateStopping
	d.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Windows 上 os.Interrupt 对多数进程无效，直接 Kill 更可靠
	if runtime.GOOS == "windows" {
		return cmd.Process.Kill()
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func (d *directStrategy) Kill() error {
	d.mu.Lock()
	cmd := d.cmd
	d.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func (d *directStrategy) SendCommand(command string) error {
	d.mu.Lock()
	stdin := d.stdin
	d.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("实例 %s 的 stdin 不可用", d.spec.UUID)
	}
	_, err := fmt.Fprintln(stdin, command)
	return err
}

func (d *directStrategy) State() InstanceState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

func (d *directStrategy) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.mu.Unlock()
	// direct 模式没有持久连接，Close 不终止进程（由 Stop/Kill 负责）
	return nil
}

// GetPID 返回实例进程的 PID，未启动或已退出时返回 0。
func (d *directStrategy) GetPID() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil || d.cmd.Process == nil {
		return 0
	}
	return d.cmd.Process.Pid
}

// instanceWriter 将进程输出路由到 Manager 的 onOutput 回调。
// Windows 上自动将 GBK 编码转换为 UTF-8。
type instanceWriter struct {
	manager    *Manager
	instanceID string
	stream     string // "stdout" or "stderr"
}

func (w *instanceWriter) Write(p []byte) (n int, err error) {
	if w.manager != nil && w.manager.onOutput != nil {
		// Windows cmd.exe 子进程输出默认 GBK，转换为 UTF-8
		w.manager.onOutput(w.instanceID, w.stream, gbkToUTF8(p))
	}
	return len(p), nil
}

// backoffDelay 计算指数退避延迟。
// 1s → 2s → 4s → 8s → 16s → 30s (上限)。
func backoffDelay(crashCount int) time.Duration {
	delay := time.Second * time.Duration(1<<uint(crashCount-1))
	maxDelay := 30 * time.Second
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}
