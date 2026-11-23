package vlsup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Process 是受管进程的兼容最小接口；退出证据通过可选ExecHandle获取。
type Process interface {
	PID() int
	Stop(grace time.Duration) error
	Exited() bool
}

// ProcessSnapshot 保存单一Wait观察到的退出证据，不含命令行或原始Wait错误。
type ProcessSnapshot struct {
	StartedAt, ExitedAt            time.Time
	ExitCode                       int
	ExitCodeKnown, Exited, Stopped bool
	Diagnostics                    []string
	DroppedLines, TruncatedLines   int
}

// ExecHandle 是真实进程的可选扩展；不能把读取时墙钟当成进程活过的时长。
type ExecHandle interface {
	Process
	Snapshot() ProcessSnapshot
}

// CmdFactory 可注入测试替身，不要求替身实现额外退出证据。
type CmdFactory interface {
	Start(context.Context, string, []string) (Process, error)
}

// namespaceCmdFactory 允许真实工厂按namespace注入独立Go软限制。
type namespaceCmdFactory interface {
	StartNamespace(context.Context, Namespace, string, []string) (Process, error)
}

// RealFactory 以非shell方式启动并持续排空两个输出流到独立有界脱敏落点。
type RealFactory struct {
	Grace          time.Duration
	Env            []string
	NamespaceEnv   map[Namespace][]string
	DiagnosticSink Sink
	Secrets        []string
}

// DefaultStopGrace 是优雅停止后转为强杀的默认有界等待。
const DefaultStopGrace = 5 * time.Second

// Start 保留通用工厂接口，默认独立诊断不会递归写回VL。
func (f RealFactory) Start(ctx context.Context, bin string, args []string) (Process, error) {
	return f.start(ctx, "managed", bin, args, f.Env)
}

// StartNamespace 仅为各层选择独立环境，不改变兼容Process接口。
func (f RealFactory) StartNamespace(ctx context.Context, ns Namespace, bin string, args []string) (Process, error) {
	env := f.Env
	if v, ok := f.NamespaceEnv[ns]; ok {
		env = v
	}
	return f.start(ctx, string(ns), bin, args, env)
}

func (f RealFactory) start(ctx context.Context, namespace, bin string, args, env []string) (Process, error) {
	if bin == "" {
		return nil, fmt.Errorf("vlsup: binary path is empty")
	}
	secrets := append(append([]string(nil), f.Secrets...), argumentSecrets(args, append(os.Environ(), env...))...)
	d := newProcessDiagnostics(namespace, f.DiagnosticSink, secrets)
	stdout := &diagnosticWriter{d: d, stream: "stdout"}
	stderr := &diagnosticWriter{d: d, stream: "stderr"}
	cmd := exec.CommandContext(ctx, bin, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// 后代异常继承管道时也不能无限等待；正常VL的两个流由exec拷贝协程持续排空。
	cmd.WaitDelay = time.Second
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		d.close()
		return nil, fmt.Errorf("vlsup: 启动失败: %s", redact(err.Error(), secrets))
	}
	grace := f.Grace
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	p := &realProcess{cmd: cmd, grace: grace, done: make(chan struct{}), diagnostics: d, snapshot: ProcessSnapshot{StartedAt: startedAt}}
	// 唯一Wait回收者，无论自行退出、正常Stop或强杀都保存同一份终态。
	go func() {
		err := cmd.Wait()
		exitedAt := time.Now()
		stdout.flush()
		stderr.flush()
		code := -1
		known := cmd.ProcessState != nil
		if known {
			code = cmd.ProcessState.ExitCode()
		}
		// 锁序仅process.mu，常量字段赋值；不在锁内调用诊断sink。
		p.mu.Lock()
		p.snapshot.Exited = true
		p.snapshot.ExitedAt = exitedAt
		p.snapshot.ExitCode = code
		p.snapshot.ExitCodeKnown = known
		stopped := p.snapshot.Stopped
		p.mu.Unlock()
		if err != nil && !stopped {
			d.record("exit", fmt.Sprintf("进程退出 code=%d", code), false)
		}
		d.close()
		close(p.done)
	}()
	return p, nil
}

type realProcess struct {
	cmd         *exec.Cmd
	grace       time.Duration
	mu          sync.Mutex
	snapshot    ProcessSnapshot
	done        chan struct{}
	diagnostics *processDiagnostics
}

func (p *realProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
func (p *realProcess) Exited() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot.Exited
}
func (p *realProcess) Snapshot() ProcessSnapshot {
	if p == nil {
		return ProcessSnapshot{Exited: true}
	}
	p.mu.Lock()
	out := p.snapshot
	p.mu.Unlock()
	out.Diagnostics, out.DroppedLines, out.TruncatedLines = p.diagnostics.snapshot()
	return out
}
func (p *realProcess) Stop(grace time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	p.mu.Lock()
	if p.snapshot.Stopped {
		p.mu.Unlock()
		<-p.done
		return nil
	}
	p.snapshot.Stopped = true
	exited := p.snapshot.Exited
	if grace <= 0 {
		grace = p.grace
	}
	p.mu.Unlock()
	if exited {
		<-p.done
		return nil
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
		return nil
	}
}
