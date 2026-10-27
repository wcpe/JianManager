//go:build linux

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-497③ 归属复核收紧的真实进程用例（Linux：强特征依赖 /proc 的 cmdline 与 environ）。
//
// 缺陷形态（本组用例要挡住的）：PID 被 OS 复用后，一个 **cwd 恰好落在实例 work_dir 下** 的无关进程，
// 仅凭 cwd 单特征就通过宽口径复核 → 被自动收养 → 被 isolateAdoptedWrapper 迁进实例 cgroup，
// 此后 Stop / Kill 打到无关进程上。
//
// 转红方式（改回缺陷行为）：令收养路径改回 DefaultVerifyProcessOwnership（多特征任一命中），
// TestDefaultVerifyAdoptOwnership_CwdOnlyRejected 与 TestOrphanScan_AutoAdoptRejectsCwdOnlyPair
// 立即转红（无关 sleep 会靠 cwd 通过复核并被收养）。

// wrapperConfigEnv 构造与真实 wrapper 同形的环境变量：UUID 只经 JM_DAEMON_WRAPPER_CONFIG 注入，
// argv 不含 UUID（见 daemon.WrapperConfig 与 daemonStrategy.Start 的 cmd.Env）。
func wrapperConfigEnv(uuid, workDir string) string {
	return daemon.EnvWrapperConfig + `={"instance_uuid":"` + uuid + `","work_dir":"` + workDir + `"}`
}

// startIdleProcess 启动一个真实的长寿空闲进程（cwd / 环境可控），用于伪造「无关进程」与「Java 进程」。
// 返回 PID；测试结束按**进程组**精确终止，不波及测试进程自身。
func startIdleProcess(t *testing.T, dir string, env []string) int {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	cmd.Dir = dir
	cmd.Env = env
	return startIsolated(t, cmd)
}

// startIsolated 启动 cmd 并让它自成进程组，返回 PID；cleanup 只终止该组。
//
// 为什么必须自成进程组：① daemon.KillPIDTree 走 `kill(-pgid)`，若子进程与 `go test` 同组会连测试
// 进程一起杀掉，故绝不使用；② 默认同组时终止父 shell 会把脚本里 fork 的 `sleep` 孤儿化（ppid→1），
// 残留数十秒污染后续用例与真机进程视图。自成组后按 `kill(-pid)` 一次收干净。
func startIsolated(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		// 组内全部进程（含脚本 fork 出的 sleep）：pgid == 组首 pid。
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	return pid
}

// startDaemonArgvProcess 启动一个 argv 形如 `<worker 二进制> daemon` 的真实进程（真实 wrapper 形态）。
//
// 手法：把 /bin/sh 以符号链接命名为 worker，并让它在自身 cwd 下执行名为 daemon 的脚本。脚本用
// 「sleep … 再跟一条内置命令」刻意让 sh 本体保持存活——若只写一条 sleep，shell 会把它 exec 掉，
// argv 就变成 `sleep …`，不再是 wrapper 形态。这样 /proc/<pid>/cmdline 正是 `<dir>/worker daemon`。
//
// 注意该进程的 cwd 是 dir 而**不是** workDir：真实 wrapper 由 Worker 以 exec.Command 拉起、从不设置
// cmd.Dir（继承 Worker 的 cwd），故 wrapper 分支必须靠 argv + 环境 UUID 判真，不能靠 cwd。
func startDaemonArgvProcess(t *testing.T, dir string, env []string) int {
	t.Helper()
	link := filepath.Join(dir, "worker")
	require.NoError(t, os.Symlink("/bin/sh", link))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "daemon"), []byte("sleep 120\n:\n"), 0o600))

	cmd := exec.Command(link, "daemon")
	cmd.Dir = dir
	cmd.Env = env
	pid := startIsolated(t, cmd)

	// /proc/<pid>/cmdline 在 exec 完成前可能仍是父进程的副本：等到 argv 已是 wrapper 形态再断言。
	require.Eventually(t, func() bool {
		p, err := psproc.NewProcess(int32(pid))
		if err != nil {
			return false
		}
		cmdline, err := p.Cmdline()
		return err == nil && looksLikeJianManagerDaemon(cmdline)
	}, 3*time.Second, 20*time.Millisecond, "wrapper 形态进程的 argv 未就绪")
	return pid
}

// waitProcessCwd 等到子进程的 cwd 可读且等于 want（exec 完成前读到的可能是父进程副本）。
func waitProcessCwd(t *testing.T, pid int, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		p, err := psproc.NewProcess(int32(pid))
		if err != nil {
			return false
		}
		cwd, err := p.Cwd()
		return err == nil && samePath(cwd, want)
	}, 3*time.Second, 20*time.Millisecond, "子进程 cwd 未就绪")
}

// waitEnvHasUUID 等到 /proc/<pid>/environ 里能看到本实例 UUID（exec 完成前读到的是父进程副本）。
func waitEnvHasUUID(t *testing.T, pid int, uuid string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return processEnvMatchesInstance(pid, uuid)
	}, 3*time.Second, 20*time.Millisecond, "子进程 environ 未就绪")
}

// TestDefaultVerifyAdoptOwnership_CwdOnlyRejected 缺陷复现：cwd 恰好等于 workDir、其余特征全不符的
// 无关进程，**必须**被判不属（旧宽口径会判属）。
func TestDefaultVerifyAdoptOwnership_CwdOnlyRejected(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	workDir := t.TempDir()

	pid := startIdleProcess(t, workDir, os.Environ()) // cwd=workDir，环境无 UUID
	waitProcessCwd(t, pid, workDir)

	// 前提确认：弱特征确实成立（cwd 就是 workDir），否则本用例证明不了任何事。
	// 旧宽口径在弱特征下单特征通过——这正是缺陷；此处一并钉住，防止有人把宽口径接回收养路径。
	require.True(t, DefaultVerifyProcessOwnership(pid, uuid, workDir, false),
		"前提：宽口径（强杀路径）确实会按 cwd 单特征判真")

	ok, evidence := DefaultVerifyAdoptOwnership(pid, uuid, workDir, false)
	require.False(t, ok, "cwd 等于 workDir 但无实例 UUID 强特征 → 不得判属（否则会被误收养）")
	require.Equal(t, "weak_evidence_only", evidence)

	ok, evidence = DefaultVerifyAdoptOwnership(pid, uuid, workDir, true)
	require.False(t, ok, "无关 sleep 的 argv 不是 daemon → wrapper 分支不得判属")
	require.Equal(t, "wrapper_argv_mismatch", evidence)
}

// TestDefaultVerifyAdoptOwnership_RealWrapperShapeAccepted 真实 wrapper 形态仍然通过：
// argv `<worker> daemon` + 环境携带本实例 UUID（argv 不含 UUID）。
func TestDefaultVerifyAdoptOwnership_RealWrapperShapeAccepted(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	workDir := t.TempDir()
	dir := t.TempDir() // wrapper 的 cwd ≠ workDir（真实情形：继承 Worker 的 cwd）

	pid := startDaemonArgvProcess(t, dir, append(os.Environ(), wrapperConfigEnv(uuid, workDir)))
	waitEnvHasUUID(t, pid, uuid)

	ok, evidence := DefaultVerifyAdoptOwnership(pid, uuid, workDir, true)
	require.True(t, ok, "真实 wrapper 形态（argv daemon + 环境 UUID）必须判属")
	require.Equal(t, "daemon_argv+env_uuid", evidence)

	// 同一进程按 Java 分支亦判属（环境携带本实例 UUID）——环境才是强特征，argv 不是。
	ok, evidence = DefaultVerifyAdoptOwnership(pid, uuid, workDir, false)
	require.True(t, ok)
	require.Equal(t, "uuid_evidence", evidence)

	// 他人实例的 UUID：环境里没有 → 不判属（F11：argv 对任何实例的 wrapper 都相同）。
	ok, evidence = DefaultVerifyAdoptOwnership(pid, "99999999-8888-7777-6666-555555555555", workDir, true)
	require.False(t, ok, "不得把本实例 wrapper 判成他人实例")
	require.Equal(t, "env_uuid_missing", evidence)
}

// TestDefaultVerifyAdoptOwnership_WrapperArgvAloneRejected 只有 argv 形如 daemon、环境无 UUID → 不判属。
// 这是 F11 的原始约束（argv 对所有实例的 wrapper 都相同），收紧后必须仍然成立。
func TestDefaultVerifyAdoptOwnership_WrapperArgvAloneRejected(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	workDir := t.TempDir()
	dir := t.TempDir()

	pid := startDaemonArgvProcess(t, dir, os.Environ()) // argv 为 wrapper 形态，但无配置环境

	ok, evidence := DefaultVerifyAdoptOwnership(pid, uuid, workDir, true)
	require.False(t, ok, "单凭 argv 形如 daemon 不得判属")
	require.Equal(t, "env_uuid_missing", evidence)
}

// TestDefaultVerifyAdoptOwnership_RealJavaShapeAccepted 真实 Java 形态仍然通过：
// cwd = workDir（wrapper 以 cmd.Dir 拉起）**且** 环境继承 `JM_DAEMON_WRAPPER_CONFIG`（强特征）。
func TestDefaultVerifyAdoptOwnership_RealJavaShapeAccepted(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	workDir := t.TempDir()

	pid := startIdleProcess(t, workDir, append(os.Environ(), wrapperConfigEnv(uuid, workDir)))
	waitProcessCwd(t, pid, workDir)
	waitEnvHasUUID(t, pid, uuid)

	ok, evidence := DefaultVerifyAdoptOwnership(pid, uuid, workDir, false)
	require.True(t, ok, "真实 Java 形态（cwd=workDir + 环境 UUID）必须判属")
	require.Equal(t, "uuid_evidence", evidence)
}

// newRealProcAdoptFixture 构造一套「真实 PID」收养夹具：**不注入归属复核**（走
// DefaultVerifyAdoptOwnership），**不注入存活判定**（走真实 /proc），只桩掉拨号与杀/终止动作
// 以便断言「有没有拨号、有没有动进程」。
func newRealProcAdoptFixture(t *testing.T) (*adoptFixture, string) {
	t.Helper()
	dir := t.TempDir()
	workDir := filepath.Join(dir, "server")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	f := &adoptFixture{dir: dir, socket: filepath.Join(dir, adoptUUID+".sock")}
	m := NewManager(dir)
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		f.audits = append(f.audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
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

// writePIDRecord 写入一条指向真实 PID 的 PID 记录（wrapper/java 均真实存活）。
func writePIDRecord(t *testing.T, dir, workDir string, wrapperPID, javaPID int) {
	t.Helper()
	require.NoError(t, daemon.NewPIDFile(filepath.Join(dir, adoptUUID+".pid")).WriteRecord(daemon.PIDRecord{
		WrapperPID:   wrapperPID,
		JavaPID:      javaPID,
		SocketAddr:   filepath.Join(dir, adoptUUID+".sock"),
		InstanceUUID: adoptUUID,
		WorkDir:      workDir,
		ProbePort:    25566,
	}))
}

// TestOrphanScan_AutoAdoptRejectsCwdOnlyPair 端到端复现缺陷：PID 记录指向两个 cwd 落在 workDir 下、
// 但无任何强特征的无关进程（PID 复用形态）→ 拒绝收养、零拨号、零杀、保留 PID 文件、落审计。
func TestOrphanScan_AutoAdoptRejectsCwdOnlyPair(t *testing.T) {
	f, workDir := newRealProcAdoptFixture(t)

	wrapperPID := startIdleProcess(t, workDir, os.Environ())
	javaPID := startIdleProcess(t, workDir, os.Environ())
	waitProcessCwd(t, wrapperPID, workDir)
	waitProcessCwd(t, javaPID, workDir)
	writePIDRecord(t, f.dir, workDir, wrapperPID, javaPID)

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1)
	require.False(t, findings[0].Adopted, "cwd 单特征不得收养")
	require.False(t, f.m.instanceKnown(adoptUUID), "不得纳管无关进程")
	require.Empty(t, f.dials, "复核不过不得拨号（拨号会顶掉该 wrapper 的控制连接）")
	require.Empty(t, f.kills, "复核不过绝不动进程")
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
	require.Equal(t, 1, f.countAudit("orphan.adopt_blocked"))
	require.Contains(t, f.audits[0].detail, `"evidence":"wrapper_argv_mismatch"`,
		"审计须解释拦截口径")
	require.True(t, pidFileExists(filepath.Join(f.dir, adoptUUID+".pid")), "保留 PID 文件等下一轮/人工介入")
}

// TestOrphanScan_AutoAdoptAcceptsRealWrapperShape 端到端正向：真实 wrapper 形态（argv + 环境 UUID）
// 与真实 Java 形态（cwd=workDir + 环境 UUID）的进程对**仍然能被收养**——收紧不能把正常收养也拦掉。
func TestOrphanScan_AutoAdoptAcceptsRealWrapperShape(t *testing.T) {
	f, workDir := newRealProcAdoptFixture(t)

	wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
	javaPID := startIdleProcess(t, workDir, append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
	waitEnvHasUUID(t, wrapperPID, adoptUUID)
	waitProcessCwd(t, javaPID, workDir)
	waitEnvHasUUID(t, javaPID, adoptUUID)
	writePIDRecord(t, f.dir, workDir, wrapperPID, javaPID)

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1)
	require.True(t, findings[0].Adopted, "真实 wrapper/Java 形态必须收养成功")
	inst, ok := f.m.GetInstance(adoptUUID)
	require.True(t, ok)
	require.Equal(t, StateRunning, inst.State)
	require.Equal(t, workDir, inst.WorkDir)
	require.NotNil(t, inst.strategy)
	require.Equal(t, wrapperPID, inst.strategy.GetPID(), "收养只重连，PID 不变")
	require.Equal(t, []string{filepath.Join(f.dir, adoptUUID+".sock")}, f.dials, "应且仅应拨号一次")
	require.Empty(t, f.kills, "收养路径不得调用任何杀/终止动作")
	require.Equal(t, 1, f.countAudit("orphan.auto_adopted"))
	require.Equal(t, 0, f.countAudit("orphan.adopt_blocked"))
}

// TestOrphanScan_AutoAdoptDisabledKeepsRejecting 开关关闭时，即使是真实 wrapper 形态也不收养
// （开关语义优先，且不因收紧而改变）。
func TestOrphanScan_AutoAdoptDisabledKeepsRejecting(t *testing.T) {
	f, workDir := newRealProcAdoptFixture(t)

	wrapperPID := startDaemonArgvProcess(t, t.TempDir(), append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
	javaPID := startIdleProcess(t, workDir, append(os.Environ(), wrapperConfigEnv(adoptUUID, workDir)))
	waitEnvHasUUID(t, wrapperPID, adoptUUID)
	waitProcessCwd(t, javaPID, workDir)
	waitEnvHasUUID(t, javaPID, adoptUUID)
	writePIDRecord(t, f.dir, workDir, wrapperPID, javaPID)

	f.s.SetAutoAdopt(false)
	findings := f.s.ScanOnce()

	for _, fd := range findings {
		require.False(t, fd.Adopted, "auto_adopt=false 时任何 finding 都不得被收养")
	}
	require.False(t, f.m.instanceKnown(adoptUUID))
	require.Empty(t, f.dials)
	require.Empty(t, f.kills)
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
}
