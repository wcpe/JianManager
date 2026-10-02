package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

const (
	// startWaitTimeout：等待上一代进程退出预算的下限。
	startWaitTimeout = 15 * time.Second
	// priorExitPollInterval：轮询上一代进程是否退出的间隔。
	priorExitPollInterval = 100 * time.Millisecond
	// priorExitMargin：强杀兜底之后的收尾余量（Java 退出、wrapper 退出并清理 PID 文件）。
	priorExitMargin = 10 * time.Second
	// envStartWaitTimeout：覆盖上述预算的环境变量（Go duration 文本），供测试/集成缩短。
	envStartWaitTimeout = "JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT"
)

// ErrPriorExitTimeout 表示上一代 wrapper/Java 未在预算内退出，本次启动已被拒绝。
var ErrPriorExitTimeout = errors.New("上一代实例进程未在预算内退出")

// ErrStartWaitCanceled 表示「等待上一代进程退出」被调用方 ctx 取消（CP 的委托 RPC 被取消/超时）。
//
// 它与 ErrPriorExitTimeout 语义完全不同，必须区别对待：
//   - ErrPriorExitTimeout 是**确定性结论**：旧进程仍在，本次启动被拒绝；
//   - 本错误只说明「还没来得及判定」（本次既未启动、也未拒绝），属于**可重试**的中间状态，
//     不得据此把实例记为崩溃——否则一次取消会污染实例状态（与 CP 侧「假失败」同源）。
var ErrStartWaitCanceled = errors.New("等待上一代进程退出被取消（本次未启动，可重试）")

// PriorExitBudget 返回「等待上一代进程退出」的预算。
//
// 预算必须覆盖 wrapper 的优雅停止强杀兜底（gracefulStopSeconds，未下发时按 wrapper 默认
// 30s，平台设置 graceful_stop.timeout 可放大）再加收尾余量。
//
// 旧实现把上限硬编码为 15s 且超时后「仍继续启动」，而强杀兜底默认就有 30s：于是任何
// 「优雅停止耗时 > 15s」的实例重启时必然双开——旧进程仍在关服，新 wrapper/Java 已被拉起并
// 因端口/锁冲突秒崩，旧进程则继续用旧配置跑满兜底时长（实例 153 / beacon-main 实证：
// `等待上一代进程退出超时，仍继续启动 wrapperAlive=true javaAlive=true` 后 4 次 exitCode=1，
// 旧 wrapper 直到 30s 强杀兜底到点才退出）。
//
// 环境变量优先，供测试/集成缩短。
func PriorExitBudget(gracefulStopSeconds int) time.Duration {
	if v := os.Getenv(envStartWaitTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	budget := resolveGracefulStopTimeout(gracefulStopSeconds) + priorExitMargin
	if budget < startWaitTimeout {
		budget = startWaitTimeout
	}
	return budget
}

// WaitForPriorExit 在（重新）启动 daemon 实例前，按 PID 文件等待上一代 wrapper/Java 进程完全退出。
//
// 背景：快速 stop→start 时，旧实例可能仍在优雅退出，其 Java 仍占着监听端口（如 25566）、
// 其 wrapper 仍占着通信 socket。此时直接 spawn 新 wrapper/Java 会因端口/地址冲突崩溃
// （worker 日志可见 `wrapper 进程退出 err="exit status 1"`）。
//
// 语义：PID 文件不存在（上一代已自清理）即视为已退出，立即返回 nil；wrapper 与 Java PID 均不
// 存活时返回 nil；预算耗尽而仍有存活进程时返回 ErrPriorExitTimeout——**拒绝启动**，不静默继续。
//
// 调用方必须把该错误上抛（不要吞掉）：本次启动会被置为失败并记录原因，使「旧进程未退、
// 新进程未起」这一事实在实例状态与日志中可见，而不是双开之后仍报告成功。
// 注意区分两类返回：nil 表示安全（可以启动），error 表示旧进程仍在（必须放弃本次启动）。
func WaitForPriorExit(pidDir, uuid string, timeout time.Duration) error {
	return WaitForPriorExitContext(context.Background(), pidDir, uuid, timeout)
}

// WaitForPriorExitContext 是 WaitForPriorExit 的**可取消**形态。
//
// 为什么要可取消（复审 P1-5）：等待预算可达分钟级（平台设优雅停止 120s 时 = 130s），而调用方
// （CP 的委托 RPC）可能提前取消或超时。此前的实现只有固定 time.Sleep 轮询、完全不看 ctx：
// 请求早已取消，Worker 仍会把预算耗尽再去写状态/拒绝启动，于是运维看到的是「CP 超时」与
// 「Worker 拒绝」两个与真因无关的错误。
// ctx 取消时返回 ErrStartWaitCanceled（可重试，不是「旧进程仍在」这一确定性结论）。
func WaitForPriorExitContext(ctx context.Context, pidDir, uuid string, timeout time.Duration) error {
	return waitForPriorExit(ctx, pidDir, uuid, timeout)
}

// waitForPriorExit 是 WaitForPriorExitContext 的内部实现，显式传入超时便于测试。
func waitForPriorExit(ctx context.Context, pidDir, uuid string, timeout time.Duration) error {
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrStartWaitCanceled, err)
		}
		rec, err := pf.ReadRecord()
		if err != nil {
			// PID 文件不存在/不可读：上一代 wrapper 已退出并清理，无需等待。
			return nil
		}
		wrapperAlive := rec.WrapperPID > 0 && IsPIDAlive(rec.WrapperPID)
		javaAlive := rec.JavaPID > 0 && IsPIDAlive(rec.JavaPID)
		if !wrapperAlive && !javaAlive {
			return nil
		}
		if !time.Now().Before(deadline) {
			// 拒绝启动：宁可本次启动失败（旧进程可能仍在正常服务），也不允许新旧进程并存。
			slog.Error("等待上一代进程退出超时，拒绝启动以避免新旧进程并存",
				"instanceId", uuid, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID,
				"wrapperAlive", wrapperAlive, "javaAlive", javaAlive, "waited", timeout)
			return fmt.Errorf("%w: instanceId=%s wrapperPid=%d(活=%t) javaPid=%d(活=%t) 已等待 %s",
				ErrPriorExitTimeout, uuid, rec.WrapperPID, wrapperAlive, rec.JavaPID, javaAlive, timeout)
		}
		// 轮询间隔也必须可取消：固定 time.Sleep 会让取消最多要等一个完整间隔才被观察到。
		timer := time.NewTimer(priorExitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w: %v", ErrStartWaitCanceled, ctx.Err())
		case <-timer.C:
		}
	}
}

// socketProbeGap 是「socket 是否仍被服务」两次探测之间的间隔：用于避开「上一代刚删完 PID 文件、
// listener 尚未关闭」的收尾瞬间，避免把正常重启误判成新旧并存。
const socketProbeGap = 200 * time.Millisecond

// SocketServed 报告该实例的 wrapper socket 是否仍有人监听。
//
// 用途：daemon 策略 spawn 新 wrapper 前的纵深防御。上面的等待只以 PID 文件为依据，而记录可能
// 缺失/损坏（旧 wrapper 被强杀未及清理、记录被误删），此时等待会误判「上一代已清理」直接放行。
// socket 是实例级唯一地址，能拨通就说明确有 wrapper 在托管该实例——调用方此时必须拒绝启动，
// 宁可本次失败，也不允许两个 wrapper 并存抢同一个实例。
//
// 连续两次（间隔 socketProbeGap）都可拨通才判真。
//
// 无 ctx 形态：等价于 SocketServedContext(context.Background(), ...)。
// 注意 bool 语义：**取消**与「未观测到监听者」都会是 false，需要区分时请用 SocketServedContext。
func SocketServed(pidDir, uuid string) bool {
	served, err := SocketServedContext(context.Background(), pidDir, uuid)
	return err == nil && served
}

// SocketServedContext 是 SocketServed 的**可取消**形态。
//
// 返回 (served, err)：err 非 nil（ctx 取消）时服务状态**未知**，调用方绝不能按「无人监听」放行——
// 那正好会退化成双开；应返回可重试错误并保留旧进程（见 process.daemonStrategy.Start）。
func SocketServedContext(ctx context.Context, pidDir, uuid string) (bool, error) {
	addr := SocketAddr(pidDir, uuid)
	if !socketDialable(addr) {
		return false, nil
	}
	// 第二次探测的间隔同样必须可取消（原实现是固定 time.Sleep，最长 200ms 才观察得到取消）。
	timer := time.NewTimer(socketProbeGap)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, fmt.Errorf("%w: %v", ErrStartWaitCanceled, ctx.Err())
	case <-timer.C:
	}
	return socketDialable(addr), nil
}

// socketDialable 尝试拨号一次并立刻关闭，仅用于判断该地址是否有人在监听。
func socketDialable(addr string) bool {
	conn, err := Dial(addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
