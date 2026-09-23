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

// observeOrphanAtStartup 在 Worker 启动恢复路径上「只观测、不处置」一条孤儿（FR-471）。
//
// 为什么启动路径必须非破坏：换二进制/重启 Worker 是高频运维动作，而 PID 目录里可能残留
// 陈旧记录（wrapper 已死、Java PID 已被复用或本就属他人）。旧行为在此按记录强杀 Java 树，
// 真机事故（2026-09-23）一次打断 54 台在跑农场。既然 FR-471 已提供启动冲突预检（防双开）与
// 漂移呈现 + 接管入口，启动路径就不再需要任何静默破坏：
//
//   - 只记审计（`orphan.startup_detected_not_reaped`，success=true 表示「观测成功」而非「已处置」）
//     并打 WARN，**保留 PID 文件**；
//   - 交周期孤儿扫描按 `orphan.dispose_policy` 处置（默认 warn 只告警；显式 auto 才强杀）；
//   - FR-471 的漂移观测与 `adopt-runtime` 接管是人工/受控的解决出口。
//
// reason 用于审计定位触发分支（startup_wrapper_gone_java_alive / startup_reconnect_failed）。
func (m *Manager) observeOrphanAtStartup(instanceUUID string, rec *daemon.PIDRecord, reason string) {
	if rec == nil {
		return
	}
	detail := fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"kind":%q,"policy":"observe","workDir":%q}`,
		instanceUUID, rec.WrapperPID, rec.JavaPID, reason, rec.WorkDir)
	m.auditOrphan("orphan.startup_detected_not_reaped", instanceUUID, detail, true, "")
	slog.Warn("启动期发现 daemon 孤儿：按 FR-471 非破坏策略只观测不强杀（保留 PID 文件，交由周期扫描与接管流程处置）",
		"instanceId", instanceUUID, "reason", reason,
		"wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "workDir", rec.WorkDir)
}

// reapOrphanWrapper 处置孤儿进程树（FR-325 及同源的 wrapper 已死场景）。
//
// 仅由**显式 auto 策略**的周期孤儿扫描调用（FR-471 起，启动恢复路径不再调用本函数）。
// // 两种入口：
//   - reconnectErr == errOrphanedWrapperGone：wrapper 已经不在，只剩 Java 孤儿（**真孤儿**）。
//     只清 Java，跳过对已消失 wrapper 的杀树与存活复核。
//   - 其余（reconnect 重试耗尽）：先判定 wrapper 是否仍存活——
//     · wrapper 仍存活：属「确属本实例、仅 socket 瞬时不可达/暂不健康」（ADR-093 决策 1），
//     **只告警 + 落审计、保留 PID 文件、不杀**，等下一轮扫描或人工介入（强杀会误杀一个
//     wrapper 与 Java 都健康、只是 socket 一时拨不通的运行中服务器）；
//     · wrapper 已在重试期间死亡：退化为真孤儿场景，按 PID 记录强杀 Java 树。
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

	// ADR-093 决策 1（覆盖 spec §2.1 旧表述「复核不通过→只告警」）：接管重试耗尽但 wrapper 仍存活，
	// 判为「确属本实例、仅瞬时不可达」——只告警、保留 PID 文件、不杀。只有当 wrapper 已确证死亡
	// （真孤儿）才进入处置。存活判据即「wrapper 进程仍存活」，与 socket 是否拨通无关。
	if !wrapperGone && rec.WrapperPID > 0 && m.pidAlive(rec.WrapperPID) {
		detail := fmt.Sprintf(`{"instanceUuid":%q,"wrapperPid":%d,"javaPid":%d,"reason":"alive_unreachable"}`,
			instanceUUID, rec.WrapperPID, rec.JavaPID)
		m.auditOrphan("orphan.dispose_blocked", instanceUUID, detail, false,
			"接管重试耗尽但 wrapper 仍存活（仅瞬时不可达），按 ADR-093 只告警不杀")
		slog.Warn("接管重试耗尽但 wrapper 仍存活，按 ADR-093 只告警不杀，保留 PID 文件等下一轮/人工介入",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID, "error", reconnectErr)
		return
	}
	if !wrapperGone {
		// wrapper 已在重试期间死亡：退化为「wrapper 死 / Java 活」真孤儿场景，仅处置 Java。
		slog.Warn("reconnect 重试期间 wrapper 已死亡，退化为 Java 真孤儿处置",
			"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
	}

	// 到达此处仅剩「wrapper 已不存活、Java 仍活」的真孤儿场景（wrapper 存活的瞬时不可达已在上面
	// 只告警返回）。
	slog.Warn("wrapper 已不存活但 Java 仍活，按 PID 记录强杀 Java 孤儿",
		"instanceId", instanceUUID, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)

	// 组装待处置目标：wrapper 已消失，只针对 Java（对已死 PID 杀树只会刷无意义告警，
	// 且其 PGID 可能已被系统复用，误杀无关进程）。仅对仍存活的 Java 建目标。
	type orphanTarget struct {
		pid           int
		expectWrapper bool
		role          string
	}
	targets := make([]orphanTarget, 0, 1)
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
