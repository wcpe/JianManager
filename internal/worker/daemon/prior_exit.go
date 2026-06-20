package daemon

import (
	"log/slog"
	"os"
	"time"
)

const (
	// startWaitTimeout：重启 daemon 实例前等待上一代进程退出的上限（尽力而为，超时仍继续启动）。
	startWaitTimeout = 15 * time.Second
	// priorExitPollInterval：轮询上一代进程是否退出的间隔。
	priorExitPollInterval = 100 * time.Millisecond
	// envStartWaitTimeout：覆盖上述上限的环境变量（Go duration 文本），供测试/集成缩短。
	envStartWaitTimeout = "JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT"
)

// resolveStartWaitTimeout 返回重启等待上限：环境变量优先（解析失败/非正忽略），否则用默认值。
func resolveStartWaitTimeout() time.Duration {
	if v := os.Getenv(envStartWaitTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return startWaitTimeout
}

// WaitForPriorExit 在（重新）启动 daemon 实例前，按 PID 文件等待上一代 wrapper/Java 进程完全退出。
//
// 背景：快速 stop→start 时，旧实例可能仍在优雅退出，其 Java 仍占着监听端口（如 25566）、
// 其 wrapper 仍占着通信 socket。此时直接 spawn 新 wrapper/Java 会因端口/地址冲突崩溃
// （worker 日志可见 `wrapper 进程退出 err="exit status 1"`）。本函数把这一竞态收敛掉。
//
// 语义：PID 文件不存在（上一代已自清理）即视为已退出，立即返回；wrapper 与 Java PID 均不存活
// 时返回；超时后尽力而为返回（不无限期阻塞启动，避免旧进程卡死时永远起不来）。
func WaitForPriorExit(pidDir, uuid string) {
	waitForPriorExit(pidDir, uuid, resolveStartWaitTimeout())
}

// waitForPriorExit 是 WaitForPriorExit 的内部实现，显式传入超时便于测试。
func waitForPriorExit(pidDir, uuid string, timeout time.Duration) {
	pf := NewPIDFile(PIDFileName(pidDir, uuid))
	deadline := time.Now().Add(timeout)
	for {
		rec, err := pf.ReadRecord()
		if err != nil {
			// PID 文件不存在/不可读：上一代 wrapper 已退出并清理，无需等待。
			return
		}
		wrapperAlive := rec.WrapperPID > 0 && IsPIDAlive(rec.WrapperPID)
		javaAlive := rec.JavaPID > 0 && IsPIDAlive(rec.JavaPID)
		if !wrapperAlive && !javaAlive {
			return
		}
		if !time.Now().Before(deadline) {
			slog.Warn("等待上一代进程退出超时，仍继续启动",
				"instanceId", uuid, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID,
				"wrapperAlive", wrapperAlive, "javaAlive", javaAlive)
			return
		}
		time.Sleep(priorExitPollInterval)
	}
}
