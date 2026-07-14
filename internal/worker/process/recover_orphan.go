package process

import (
	"log/slog"
	"os"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-325：Worker 重启接管扫描中「wrapper 存活但 reconnect 拨号失败」的兜底。
// 旧行为只删 PID 文件，活着的 wrapper/Java 从此不可发现（孤儿永久化，真机事故：
// 残留 java 占 Paper session.lock 致实例再也起不来）。现改为：
// 有界重试（期间保留 PID 文件）→ 耗尽后按 PID 记录强杀孤儿进程树 → 死透才清理。

// recoverRetryBackoff reconnect 失败的有界重试间隔（递增）。首拨失败后按此序列重试。
var recoverRetryBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// 强杀后存活复核的有界等待：Windows taskkill /T /F 异步终止进程树，
// 杀完立查可能误报存活，故轮询确认（上限 attempts×interval）。
const (
	orphanKillVerifyAttempts = 10
	orphanKillVerifyInterval = 100 * time.Millisecond
)

// dialWrapper 恢复路径的 reconnect 拨号；测试经 recoverDial 注入假拨号。
func (m *Manager) dialWrapper(s *daemonStrategy, addr string) error {
	if m.recoverDial != nil {
		return m.recoverDial(s, addr)
	}
	return s.Reconnect(addr)
}

// pidAlive 进程存活探测；测试经 recoverPIDAlive 注入假读数。
func (m *Manager) pidAlive(pid int) bool {
	if m.recoverPIDAlive != nil {
		return m.recoverPIDAlive(pid)
	}
	return daemon.IsPIDAlive(pid)
}

// killTree 按 PID 强杀整棵进程树；测试经 recoverKillTree 注入假杀手。
func (m *Manager) killTree(pid int) error {
	if m.recoverKillTree != nil {
		return m.recoverKillTree(pid)
	}
	return daemon.KillPIDTree(pid)
}

// retrySleep 重试/复核间隔等待；测试经 recoverSleep 注入免真等待。
func (m *Manager) retrySleep(d time.Duration) {
	if m.recoverSleep != nil {
		m.recoverSleep(d)
		return
	}
	time.Sleep(d)
}

// reconnectWithRetry 对存活 wrapper 做有界重试的 reconnect 拨号（FR-325）。
// 首拨失败后按 recoverRetryBackoff 递增间隔重试；期间不动 PID 文件
// （保证实例在后续扫描中仍可发现）。全部失败返回最后一次错误。
func (m *Manager) reconnectWithRetry(strategy *daemonStrategy, addr, instanceUUID string) error {
	err := m.dialWrapper(strategy, addr)
	if err == nil {
		return nil
	}
	for i, backoff := range recoverRetryBackoff {
		slog.Warn("reconnect wrapper 失败，保留 PID 文件稍后重试",
			"instanceId", instanceUUID, "attempt", i+1, "maxRetries", len(recoverRetryBackoff),
			"backoff", backoff, "error", err)
		m.retrySleep(backoff)
		if err = m.dialWrapper(strategy, addr); err == nil {
			slog.Info("reconnect wrapper 重试成功", "instanceId", instanceUUID, "attempt", i+1)
			return nil
		}
	}
	return err
}

// reapOrphanWrapper 处置 reconnect 重试耗尽后的孤儿 wrapper（FR-325）。
// 先杀 wrapper 树（防其自动重启 Java），再补杀 Java 树——Unix 上 Java 经 wrapper 的
// applyProcAttr 自成进程组，杀 wrapper 组够不到它，而 Java 正是占 session.lock 的真孤儿；
// Windows 上 taskkill /T 已覆盖子树，补杀已死 PID 报错无害（以存活复核为准）。
// PID 文件处置语义：确认全部死透 → 清 PID 文件与残留 socket；仍有存活（权限不足等）→
// 保留 PID 文件，让下次 Worker 重启的接管扫描仍能发现并再次兜底，杜绝孤儿永久失联。
func (m *Manager) reapOrphanWrapper(instanceUUID, pidPath string, rec *daemon.PIDRecord, reconnectErr error) {
	slog.Warn("reconnect wrapper 重试耗尽，按 PID 记录强杀孤儿进程树",
		"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID,
		"error", reconnectErr)

	if err := m.killTree(rec.WrapperPID); err != nil {
		slog.Warn("强杀 wrapper 进程树报错（可能已死或权限不足，以存活复核为准）",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "error", err)
	}
	if rec.JavaPID > 0 && rec.JavaPID != rec.WrapperPID && m.pidAlive(rec.JavaPID) {
		if err := m.killTree(rec.JavaPID); err != nil {
			slog.Warn("强杀 Java 进程树报错（可能已死或权限不足，以存活复核为准）",
				"instanceId", instanceUUID, "javaPid", rec.JavaPID, "error", err)
		}
	}

	if !m.waitPIDsGone([]int{rec.WrapperPID, rec.JavaPID}) {
		slog.Warn("孤儿进程树强杀后仍有存活，保留 PID 文件待下次接管扫描再兜底",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
		return
	}

	_ = os.Remove(pidPath)
	if rec.SocketAddr != "" {
		daemon.RemoveSocket(rec.SocketAddr)
	}
	slog.Warn("孤儿 wrapper 进程树已强杀并清理 PID 文件",
		"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
}

// waitPIDsGone 有界等待一组 PID 全部退出（0/负值跳过），全部退出返回 true。
func (m *Manager) waitPIDsGone(pids []int) bool {
	for attempt := 0; attempt < orphanKillVerifyAttempts; attempt++ {
		if attempt > 0 {
			m.retrySleep(orphanKillVerifyInterval)
		}
		anyAlive := false
		for _, pid := range pids {
			if pid > 0 && m.pidAlive(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return true
		}
	}
	return false
}
