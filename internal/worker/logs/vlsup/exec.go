package vlsup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Process 是已启动 VL 进程的最小句柄。
type Process interface {
	// PID 返回操作系统进程号；未启动时为 0。
	PID() int
	// Stop 优雅停止：先尝试中断，超过 grace 后强制结束。可幂等调用。
	Stop(grace time.Duration) error
	// Exited 非阻塞报告进程是否已退出（含启动后立即失败的情形）。
	//
	// 必要性：进程可能在 Start 成功后立刻退出（真机实测：数据目录缺失时 VL 立即退出、
	// 变成僵尸），若只依据 Start 无错就置 RUNNING，控制面会报「启动成功」而实际已死。
	// 同时其 wait goroutine 负责回收子进程，避免僵尸堆积。
	Exited() bool
}

// CmdFactory 构造并启动进程。生产使用 RealFactory；单元测试注入 fake，不依赖真实 VL 二进制。
type CmdFactory interface {
	Start(ctx context.Context, bin string, args []string) (Process, error)
}

// RealFactory 基于 os/exec 启动真实 victoria-logs 进程。
type RealFactory struct {
	// Grace 是 Stop 默认优雅窗口；0 表示使用 DefaultStopGrace。
	Grace time.Duration
	// Env 是追加到进程环境的额外变量（如 GOMEMLIMIT）。nil 表示不追加。
	Env []string
}

// DefaultStopGrace 优雅停止默认窗口。
const DefaultStopGrace = 5 * time.Second

// Start 实现 CmdFactory。
func (f RealFactory) Start(ctx context.Context, bin string, args []string) (Process, error) {
	if bin == "" {
		return nil, fmt.Errorf("vlsup: binary path is empty")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// 追加额外环境变量（如 VL 的 GOMEMLIMIT 软内存上限），保留继承环境。
	if len(f.Env) > 0 {
		cmd.Env = append(os.Environ(), f.Env...)
	}
	// 失败日志不进入 VL 管道：stdout/stderr 丢弃或由上层接独立 sink 文件。
	// foundation 阶段不绑定 VL 日志采集，避免递归。
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("vlsup: start %s: %w", bin, err)
	}
	grace := f.Grace
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	p := &realProcess{cmd: cmd, grace: grace, done: make(chan struct{})}
	// 单一 Wait 回收者：子进程无论被 Stop 还是自行退出都由它回收，避免僵尸堆积。
	go func() {
		_ = cmd.Wait()
		p.mu.Lock()
		p.exited = true
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

type realProcess struct {
	cmd     *exec.Cmd
	grace   time.Duration
	mu      sync.Mutex
	exited  bool
	stopped bool
	done    chan struct{}
}

func (p *realProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Exited 报告子进程是否已退出。
func (p *realProcess) Exited() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited
}

func (p *realProcess) Stop(grace time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	if grace <= 0 {
		grace = p.grace
	}
	exited := p.exited
	p.mu.Unlock()
	if exited {
		return nil
	}
	// 先尝试中断（Unix SIGINT / Windows 上可能等价于 Kill）。
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
		return nil
	case <-time.After(grace):
		_ = p.cmd.Process.Kill()
		<-p.done
		return nil
	}
}
