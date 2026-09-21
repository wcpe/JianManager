package process

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-325：Worker 重启接管扫描中「wrapper 存活但 reconnect 拨号失败」的兜底。
// 旧行为只删 PID 文件，活着的 wrapper/Java 从此不可发现（孤儿永久化，真机事故：
// 残留 java 占 Paper session.lock 致实例再也起不来）。现改为：
// 有界重试（期间保留 PID 文件）→ 耗尽后按 PID 记录强杀孤儿进程树 → 死透才清理。

// recoverRetryBackoff reconnect 失败的有界重试间隔（递增，覆盖分钟级瞬时故障，FR-455①）。
// 旧序列仅 {1s,2s,4s}≈7s：交接窗口的 socket 未就绪/资源紧张常是瞬时的，多等一轮即可接管，
// 无需牺牲服务器。扩为 1s→2s→4s→8s→16s→32s→64s（≈127s），显著降低「拨不通即强杀」的误杀面。
var recoverRetryBackoff = []time.Duration{
	time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	16 * time.Second, 32 * time.Second, 64 * time.Second,
}

// errOrphanedWrapperGone 标记「wrapper 已死、仅剩 Java 孤儿」的接管场景：
// 无需再杀 wrapper（已不存在），只需清掉残留的 Java，故跳过其杀树与存活复核。
var errOrphanedWrapperGone = errors.New("wrapper 已不存活，仅剩 Java 孤儿")

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

// reapOrphanWrapper 处置孤儿进程树（FR-325 及同源的 wrapper 已死场景）。
//
// 两种入口：
//   - reconnectErr == errOrphanedWrapperGone：wrapper 已经不在，只剩 Java 孤儿。
//     只清 Java，跳过对已消失 wrapper 的杀树与存活复核。
//   - 其余（reconnect 重试耗尽）：先杀 wrapper 树（防其自动重启 Java），再补杀 Java 树——
//     Unix 上 Java 经 wrapper 的 applyProcAttr 自成进程组，杀 wrapper 组够不到它，
//     而 Java 正是占 session.lock 的真孤儿；Windows 上 taskkill /T 已覆盖子树，
//     补杀已死 PID 报错无害（以存活复核为准）。
//
// PID 文件处置语义：确认全部死透 → 清 PID 文件与残留 socket；仍有存活（权限不足等）→
// 保留 PID 文件，让下次 Worker 重启的接管扫描仍能发现并再次兜底，杜绝孤儿永久失联。
//
// FR-455① 处置前置存活复核：任何 killTree 之前，先以 cmdline/cwd 复核该 PID 是否确属本实例
// （verifyProcessOwnership）。复核不通过（PID 可能已被 OS 复用给无关进程）→ **只告警 + 落审计，
// 不杀**，并保留 PID 文件等下一轮扫描或人工介入，杜绝误杀。
func (m *Manager) reapOrphanWrapper(instanceUUID, pidPath string, rec *daemon.PIDRecord, reconnectErr error) {
	wrapperGone := errors.Is(reconnectErr, errOrphanedWrapperGone)
	if wrapperGone {
		slog.Warn("wrapper 已不存活但 Java 仍活，按 PID 记录强杀 Java 孤儿",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
	} else {
		slog.Warn("reconnect wrapper 重试耗尽，按 PID 记录强杀孤儿进程树",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID,
			"error", reconnectErr)
	}

	// 组装待处置目标：wrapper 已消失时只针对 Java（对已死 PID 杀树只会刷无意义告警，
	// 且其 PGID 可能已被系统复用，误杀无关进程）。仅对仍存活的 PID 建目标。
	type orphanTarget struct {
		pid           int
		expectWrapper bool
		role          string
	}
	targets := make([]orphanTarget, 0, 2)
	if !wrapperGone && rec.WrapperPID > 0 {
		targets = append(targets, orphanTarget{pid: rec.WrapperPID, expectWrapper: true, role: "wrapper"})
	}
	if rec.JavaPID > 0 && rec.JavaPID != rec.WrapperPID && m.pidAlive(rec.JavaPID) {
		targets = append(targets, orphanTarget{pid: rec.JavaPID, expectWrapper: false, role: "java"})
	}

	keptByVerify := false
	killedPIDs := make([]int, 0, len(targets))
	for _, tgt := range targets {
		// FR-455①：处置前 cmdline 存活复核。不通过只告警 + 落审计，不杀。
		if !m.verifyProcessOwnership(tgt.pid, instanceUUID, rec.WorkDir, tgt.expectWrapper) {
			keptByVerify = true
			detail := fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"role":%q,"reason":"ownership_unverified"}`, instanceUUID, tgt.pid, tgt.role)
			m.auditOrphan("orphan.dispose_blocked", instanceUUID, detail, false, "处置前置存活复核不通过，拒绝强杀（PID 可能已被复用）")
			slog.Warn("孤儿处置前置存活复核不通过，只告警不杀，保留 PID 文件",
				"instanceId", instanceUUID, "pid", tgt.pid, "role", tgt.role, "workDir", rec.WorkDir)
			continue
		}
		if err := m.killTree(tgt.pid); err != nil {
			slog.Warn("强杀孤儿进程树报错（可能已死或权限不足，以存活复核为准）",
				"instanceId", instanceUUID, "pid", tgt.pid, "role", tgt.role, "error", err)
		}
		killedPIDs = append(killedPIDs, tgt.pid)
	}

	if len(killedPIDs) > 0 && !m.waitPIDsGone(killedPIDs) {
		slog.Warn("孤儿进程树强杀后仍有存活，保留 PID 文件待下次接管扫描再兜底",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
		return
	}
	if keptByVerify {
		// 有 PID 因复核不通过未处置 → 保留 PID 文件等下一轮或人工介入，绝不因「部分处置」误清。
		return
	}

	_ = os.Remove(pidPath)
	if rec.SocketAddr != "" {
		daemon.RemoveSocket(rec.SocketAddr)
	}
	m.auditOrphan("orphan.dispose_reaped", instanceUUID,
		fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d}`, instanceUUID, rec.WrapperPID, rec.JavaPID),
		true, "")
	slog.Warn("孤儿进程树已强杀并清理 PID 文件",
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
