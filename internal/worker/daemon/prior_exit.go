package daemon

import (
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
	return waitForPriorExit(pidDir, uuid, timeout)
}

// waitForPriorExit 是 WaitForPriorExit 的内部实现，显式传入超时便于测试。
func waitForPriorExit(pidDir, uuid string, timeout time.Duration) error {
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	deadline := time.Now().Add(timeout)
	for {
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
		time.Sleep(priorExitPollInterval)
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
func SocketServed(pidDir, uuid string) bool {
	addr := SocketAddr(pidDir, uuid)
	if !socketDialable(addr) {
		return false
	}
	time.Sleep(socketProbeGap)
	return socketDialable(addr)
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
