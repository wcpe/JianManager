//go:build linux

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 验收缺口 ①：**Java 分支负例**的端到端用例（此前只有函数层，T7 的 C 臂也只覆盖 wrapper 分支）。
//
// 缺口原文：「Java 分支负例未单独构造（T7 的 C 臂）」。当时的现状是：
//   - `TestDefaultVerifyAdoptOwnership_CwdOnlyRejected`（函数层）确实断言了 `weak_evidence_only`，
//     但它直接调函数，不经过 `adoptUnmanagedRecord`，因此「拒绝之后有没有真的不收养 / 不拨号 /
//     不杀 / 保留 PID 文件」这段**动作侧**没有任何证据；
//   - `TestOrphanScan_AutoAdoptRejectsCwdOnlyPair`（端到端）造的两个进程都没有 wrapper 形态，
//     于是**wrapper 分支先拒**（`wrapper_argv_mismatch`），Java 分支根本没被求值；
//   - 唯一走到「Java 分支拒绝」的 `TestOrphanScan_AutoAdoptOwnershipBlocked` 用的是**注入桩**
//     （`adoptVerifyOwner` 直接返回 false），它证明的是「桩返回 false 时代码不收养」，
//     而不是「真实进程形状真的会被 DefaultVerifyAdoptOwnership 拒」。
//
// 本文件的用例补齐这一点：wrapper 用**真实形态且真的能通过复核**（否则走不到 Java 分支），
// Java 侧用会落在 Java 分支判定上的真实进程。断言从审计 evidence 一直覆盖到「零拨号、零杀、PID 文件保留」。

// startJavaDecoy 起一个「形状像 Java、但归属特征不满足」的真实进程（cwd = workDir，可控环境）。
//
// 用 `sleep` 而非 java 是刻意的：本组用例验的是**归属复核判据**，与进程内部做什么无关；
// 用它可以把「复核口径」与「java 夹具行为」两件事解耦，坏掉时能一眼看出是哪一侧的问题。
// （下游链路的行为验收在 adopt_realproc_downstream_linux_test.go，那里才必须用真 java。）
func startJavaDecoy(t *testing.T, workDir string, extraEnv ...string) int {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	cmd.Dir = workDir
	cmd.Env = append(append([]string{}, os.Environ()...), extraEnv...)
	return startIsolated(t, cmd)
}

// writePIDRecordFor 写一条指向指定 PID 的 PID 记录（uuid/workDir/socket 全部由调用方给定）。
func writePIDRecordFor(t *testing.T, pidDir, uuid, workDir string, wrapperPID, javaPID, probePort int) {
	t.Helper()
	require.NoError(t, daemon.NewPIDFile(daemon.PIDFileName(pidDir, uuid)).WriteRecord(daemon.PIDRecord{
		WrapperPID:   wrapperPID,
		JavaPID:      javaPID,
		SocketAddr:   filepath.Join(pidDir, uuid+".sock"),
		InstanceUUID: uuid,
		WorkDir:      workDir,
		ProbePort:    probePort,
	}))
}

// newRealOwnershipFixture 一套「复核走真实现、只桩掉拨号与杀」的夹具。
//
// 与 newRealProcAdoptFixture 的区别：本夹具允许**指定 uuid**（要造「记录里的 UUID 与进程环境里
// 的 UUID 不一致」这类形态），故不复用那个把 adoptUUID 写死的版本。
func newRealOwnershipFixture(t *testing.T) (*adoptFixture, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "adopt-o-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	workDir := filepath.Join(dir, "server")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	f := &adoptFixture{dir: dir, socket: filepath.Join(dir, adoptUUID+".sock")}
	m := NewManager(dir)
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		f.audits = append(f.audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
	// 只桩掉「外部副作用」：拨号与杀/终止。归复核与存活判定都走真实现。
	m.recoverDial = func(_ *daemonStrategy, addr string) error {
		f.dials = append(f.dials, addr)
		return nil
	}
	m.recoverKillTree = func(pid int) error { f.kills = append(f.kills, pid); return nil }
	m.recoverTermTree = func(pid int) error { f.kills = append(f.kills, pid); return nil }

	s := NewOrphanScanner(m, time.Minute, OrphanPolicyWarn)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, nil }
	s.removeContainer = func(context.Context, string) error { return nil }

	f.m, f.s = m, s
	return f, workDir
}

// TestJavaBranchRejectsCwdOnly_EndToEnd 端到端：**wrapper 分支真实通过**，Java 分支因只有
// 弱特征（cwd == workDir、环境无本实例 UUID）被拒为 `weak_evidence_only`。
//
// 这条是 T7 的 C 臂缺失的那一格：先前的端到端负例都停在 wrapper 分支（`wrapper_argv_mismatch`），
// 从来没走到过 Java 分支的判定；函数层虽然有断言，但没证明「拒绝之后动作侧真的什么都没做」。
//
// 转红方式：把 adopt_orphan.go 里的 Java 分支复核删掉（只复核 wrapper），或在
// DefaultVerifyAdoptOwnership 的 Java 分支里把弱特征也判真 —— 本用例立刻转红（会被收养）。
func TestJavaBranchRejectsCwdOnly_EndToEnd(t *testing.T) {
	f, workDir := newRealOwnershipFixture(t)

	// wrapper：真实形态（argv `<worker> daemon` + 环境带本实例 UUID）→ wrapper 分支通过。
	wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
	waitEnvHasUUID(t, wrapperPID, adoptUUID)
	ok, ev := DefaultVerifyAdoptOwnership(wrapperPID, adoptUUID, workDir, true)
	require.True(t, ok, "前提：wrapper 分支必须真实通过，否则本用例根本走不到 Java 分支")
	require.Equal(t, "daemon_argv+env_uuid", ev)

	// Java：cwd == workDir（弱特征）但环境无 UUID、cmdline 无 UUID → Java 分支必须拒绝。
	javaPID := startJavaDecoy(t, workDir)
	waitProcessCwd(t, javaPID, workDir)
	ok, ev = DefaultVerifyAdoptOwnership(javaPID, adoptUUID, workDir, false)
	require.False(t, ok, "前提：Java 分支必须真实拒绝")
	require.Equal(t, "weak_evidence_only", ev, "拦截口径必须是「只有弱特征」")

	writePIDRecordFor(t, f.dir, adoptUUID, workDir, wrapperPID, javaPID, 25566)

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1)
	require.False(t, findings[0].Adopted, "Java 分支复核不过 → 不得收养")
	require.False(t, f.m.instanceKnown(adoptUUID), "不得纳管")
	require.Empty(t, f.dials, "复核不过不得拨号（拨号会顶掉该 wrapper 的控制连接）")
	require.Empty(t, f.kills, "复核不过绝不动进程")

	// 审计必须能解释「拦在 Java 分支、口径是 weak_evidence_only」——不是笼统的一句失败。
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
	require.Equal(t, 1, f.countAudit("orphan.adopt_blocked"))
	detail := f.audits[0].detail
	require.Contains(t, detail, `"role":"java"`, "审计须表明拦在 Java 分支：%s", detail)
	require.Contains(t, detail, `"evidence":"weak_evidence_only"`, "审计须写明拦截口径：%s", detail)
	require.Contains(t, detail, `"pid":`+strconv.Itoa(javaPID), "审计须记录被拒的 Java PID：%s", detail)

	// 两个真实进程都还活着（收养是非破坏动作），PID 文件保留等下一轮/人工介入。
	require.True(t, isPIDAliveLocal(wrapperPID))
	require.True(t, isPIDAliveLocal(javaPID), "复核不过绝不能杀进程（护栏五.1）")
	require.True(t, pidFileExists(daemon.PIDFileName(f.dir, adoptUUID)), "保留 PID 文件")
}

// TestJavaBranchRejectsForeignInstanceUUID Java 环境携带**别的实例**的 UUID → 不判属。
//
// 形态来源：同一台机器上跑着多个实例，Java 进程从 wrapper 继承的 `JM_DAEMON_WRAPPER_CONFIG` 是
// **它自己那个实例**的。若 PID 记录串号（记录说 javaPid=X 属实例 A，而 X 其实是实例 B 的 Java），
// 而复核只看「环境里有没有 UUID」，就会把 B 的 Java 认成 A 的 —— 收养后 A 取得 B 的控制权，
// Stop 会停掉别人的服。wrapper 分支早有此类断言（`env_uuid_missing`），Java 分支此前零覆盖。
//
// 实测判据（见断言，非按规格想象）：Java 分支的通过条件是「环境含**本实例** UUID 或 cmdline 含
// UUID」；他实例 UUID 两个条件都不满足，落到弱特征检查——cwd 若等于 workDir 则是
// `weak_evidence_only`，否则 `uuid_evidence_missing`。本用例把两种 cwd 形态都钉住。
func TestJavaBranchRejectsForeignInstanceUUID(t *testing.T) {
	const otherUUID = "99999999-8888-7777-6666-555555555555"

	t.Run("cwd 落在 workDir 下", func(t *testing.T) {
		f, workDir := newRealOwnershipFixture(t)
		wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
		waitEnvHasUUID(t, wrapperPID, adoptUUID)
		// Java 继承了**他实例**的配置（真实形态：B 实例的 Java 被误记成 A 的 Java）。
		javaPID := startJavaDecoy(t, workDir, wrapperConfigEnv(otherUUID, workDir))
		waitProcessCwd(t, javaPID, workDir)
		waitEnvHasMaybe(t, javaPID, otherUUID)

		ok, ev := DefaultVerifyAdoptOwnership(javaPID, adoptUUID, workDir, false)
		require.False(t, ok, "他实例的 Java 不得被认成本实例")
		require.Equal(t, "weak_evidence_only", ev,
			"实际判据：cwd 命中弱特征 → weak_evidence_only（不是 uuid_evidence_missing）")

		writePIDRecordFor(t, f.dir, adoptUUID, workDir, wrapperPID, javaPID, 25566)
		findings := f.s.ScanOnce()
		require.Len(t, findings, 1)
		require.False(t, findings[0].Adopted)
		require.False(t, f.m.instanceKnown(adoptUUID))
		require.Empty(t, f.dials)
		require.Empty(t, f.kills)
		require.Contains(t, f.audits[0].detail, `"role":"java"`)
		require.Contains(t, f.audits[0].detail, `"evidence":"weak_evidence_only"`)
	})

	t.Run("cwd 不在 workDir 下", func(t *testing.T) {
		f, workDir := newRealOwnershipFixture(t)
		wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
		waitEnvHasUUID(t, wrapperPID, adoptUUID)
		// cwd 与 workDir 无关、环境携带他实例 UUID → 连通弱特征都没有。
		javaPID := startJavaDecoy(t, t.TempDir(), wrapperConfigEnv(otherUUID, workDir))
		waitEnvHasMaybe(t, javaPID, otherUUID)

		ok, ev := DefaultVerifyAdoptOwnership(javaPID, adoptUUID, workDir, false)
		require.False(t, ok, "他实例的 Java 不得被认成本实例")
		require.Equal(t, "uuid_evidence_missing", ev,
			"实际判据：无任何本实例特征 → uuid_evidence_missing")

		writePIDRecordFor(t, f.dir, adoptUUID, workDir, wrapperPID, javaPID, 25566)
		require.Len(t, f.s.ScanOnce(), 1)
		require.False(t, f.m.instanceKnown(adoptUUID))
		require.Empty(t, f.dials)
		require.Empty(t, f.kills)
		require.Contains(t, f.audits[0].detail, `"evidence":"uuid_evidence_missing"`)
	})
}

// TestJavaBranchSubstringPollutionRejected **子串污染**负例：环境里的 UUID 只是本实例 UUID
// 的一段更长的十六进制/连字符串的前缀（或紧邻别的 hex 字符），不构成本实例身份。
//
// 这是补验时发现并修掉的真实缺陷：`processEnvMatchesInstance` 过去对「NUL 换空格后的整块
// 环境」做 `strings.Contains`，于是任何一处**出现过**本实例 UUID 这段字符的变量值都会被判成
// 「我就是这个实例」——最典型的就是他实例 UUID 以本实例 UUID 为前缀（UUID 文本长度可变，
// 前缀相同完全可能），或某个变量把两个 UUID 拼在一起。
//
// 后果不是「少收养一个」而是「认错实例」：收养会取得该 PID 的控制权，Stop 打到别人身上。
//
// 转红方式（改回缺陷行为）：把 processEnvMatchesInstance 改回对整块环境 `strings.Contains`，
// 本用例立即转红（Java 分支会被判通过 → 进程被收养）。
func TestJavaBranchSubstringPollutionRejected(t *testing.T) {
	t.Run("他实例 UUID 以本实例 UUID 为前缀", func(t *testing.T) {
		f, workDir := newRealOwnershipFixture(t)
		wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
		waitEnvHasUUID(t, wrapperPID, adoptUUID)

		// 他实例的 UUID = 本实例 UUID + 额外后缀（纯 hex/连字符，与 UUID 字符集同构）。
		// 旧实现：整块环境 contains 本实例 UUID → 命中；新实现：后缀紧邻 hex 字符 → 不命中。
		pollutedUUID := adoptUUID + "-aa"
		javaPID := startJavaDecoy(t, workDir, wrapperConfigEnv(pollutedUUID, workDir))
		waitProcessCwd(t, javaPID, workDir)
		waitEnvHasMaybe(t, javaPID, pollutedUUID)

		// 前提确认：环境里确实出现了「本实例 UUID 这段字符」，否则本用例什么也没证明。
		env, err := readProcessEnviron(javaPID)
		require.NoError(t, err)
		require.Contains(t, env, adoptUUID, "前提：环境块里确实出现了本实例 UUID 这段字符（旧实现会命中）")

		require.False(t, processEnvMatchesInstance(javaPID, adoptUUID),
			"UUID 后紧跟 hex 字符 → 不是本实例身份（旧实现在此误判为真）")
		ok, ev := DefaultVerifyAdoptOwnership(javaPID, adoptUUID, workDir, false)
		require.False(t, ok, "子串污染不得判属")
		require.Equal(t, "weak_evidence_only", ev)

		writePIDRecordFor(t, f.dir, adoptUUID, workDir, wrapperPID, javaPID, 25566)
		require.Len(t, f.s.ScanOnce(), 1)
		require.False(t, f.m.instanceKnown(adoptUUID), "不得收养被污染进程")
		require.Empty(t, f.dials)
		require.Empty(t, f.kills)
		require.True(t, isPIDAliveLocal(javaPID))
	})

	t.Run("本实例 UUID 出现在无关变量的值中段", func(t *testing.T) {
		f, workDir := newRealOwnershipFixture(t)
		wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
		waitEnvHasUUID(t, wrapperPID, adoptUUID)

		// 典型真机形态：监控/脚本把某实例 UUID 写进自己的变量值（「看过」而非「就是」）。
		javaPID := startJavaDecoy(t, workDir, "MONITOR_TARGETS=sandbox-"+adoptUUID+"-replica")
		waitProcessCwd(t, javaPID, workDir)

		env, err := readProcessEnviron(javaPID)
		require.NoError(t, err)
		require.Contains(t, env, adoptUUID, "前提：环境块里确实出现了本实例 UUID")

		require.False(t, processEnvMatchesInstance(javaPID, adoptUUID),
			"「值里出现过」不等于「我就是」——前后被 hex/连字符包夹时不得判属")
		ok, ev := DefaultVerifyAdoptOwnership(javaPID, adoptUUID, workDir, false)
		require.False(t, ok)
		require.Equal(t, "weak_evidence_only", ev)

		writePIDRecordFor(t, f.dir, adoptUUID, workDir, wrapperPID, javaPID, 25566)
		require.Len(t, f.s.ScanOnce(), 1)
		require.False(t, f.m.instanceKnown(adoptUUID))
		require.Empty(t, f.dials)
		require.Empty(t, f.kills)
	})

	t.Run("值里以完整 token 出现仍判属（收紧不能误杀真归属）", func(t *testing.T) {
		_, workDir := newRealOwnershipFixture(t)
		// 定界符包裹的完整 UUID（真实 wrapper 配置的 JSON 形态）必须继续通过。
		javaPID := startJavaDecoy(t, workDir, wrapperConfigEnv(adoptUUID, workDir))
		waitProcessCwd(t, javaPID, workDir)
		waitEnvHasUUID(t, javaPID, adoptUUID)

		require.True(t, processEnvMatchesInstance(javaPID, adoptUUID),
			"JSON 里的完整 UUID 自成 token → 必须判属（收紧不得把正常形态一起拦掉）")
		ok, ev := DefaultVerifyAdoptOwnership(javaPID, adoptUUID, workDir, false)
		require.True(t, ok)
		require.Equal(t, "uuid_evidence", ev)
	})
}

// TestWrapperBranchSubstringPollutionRejected wrapper 分支同样不能被子串污染：
// 环境里出现本实例 UUID 的一段更长 hex 串时，wrapper 分支也不得判通过（它用的是同一个谓词）。
func TestWrapperBranchSubstringPollutionRejected(t *testing.T) {
	_, workDir := newRealOwnershipFixture(t)

	wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(),
		wrapperConfigEnv(adoptUUID+"-bb", workDir)))
	waitEnvHasMaybe(t, wrapperPID, adoptUUID+"-bb")

	ok, ev := DefaultVerifyAdoptOwnership(wrapperPID, adoptUUID, workDir, true)
	require.False(t, ok, "子串污染下 wrapper 分支也不得判属")
	require.Equal(t, "env_uuid_missing", ev)
}

// waitEnvHasMaybe 等进程 environ 里能看到 sub（用于「他实例/污染」形态的就绪判定）。
//
// 不能用 waitEnvHasUUID：它断言的是「本实例 UUID 判属为真」，而这些用例的构造恰恰要求它不为真。
func waitEnvHasMaybe(t *testing.T, pid int, sub string) {
	t.Helper()
	require.Eventually(t, func() bool {
		env, err := readProcessEnviron(pid)
		return err == nil && env != ""
	}, 3*time.Second, 20*time.Millisecond, "子进程 environ 未就绪")
}
