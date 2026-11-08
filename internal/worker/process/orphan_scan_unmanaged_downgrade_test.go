package process

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 组合风险回归：`dispose_policy=auto` + `auto_adopt=false` 时，未纳管活进程对不得被当作 direct 孤儿处置。
//
// 缺陷形态：`auto_adopt=false` 使未纳管进程对**不被收养**（不进入内存表 → 不在受管集合中），
// 而第二相 `scanDirectOrphans` 的判据 `directOrphanWorkDir` 只按「cwd / cmdline 落在受管服务器根下」
// 认定 direct 孤儿、**不排除 wrapper 守护进程**，于是这一对会被 auto 档 killTree——等于用处置策略
// 绕过运维「这些进程我来管」的明示意图，最坏后果是杀掉运维正在手动管理的服务器。
//
// 裁定（最强语义）：**开关关闭 ≠ 允许处置**。降级只针对「未纳管 + 活」这一类，且必须留痕。
const (
	// 未纳管活进程对的实例 UUID 与 PID（夹具专用，避免与本包其它用例的常量冲突）。
	dgUnmanagedUUID = "7f7f7f7f-1111-2222-3333-444444444444"
	dgWrapperPID    = 6201
	dgJavaPID       = 6202
	// 已纳管 RUNNING 实例的根 PID。
	dgManagedRunningPID = 7101
	// 已纳管但脱管（STOPPED、无策略）实例目录下的残留 Java PID。
	dgManagedStoppedResidualPID = 7102
	// 与 PID 记录无关的真 direct 孤儿 PID。
	dgStrayOrphanPID = 9301
)

// unmanagedDowngradeFixture 覆盖「未纳管活进程对存在」的夹具：
// PID 目录里有该对的有效记录且两进程均存活，内存表为空，本机进程快照里两进程都在该实例目录下运行。
type unmanagedDowngradeFixture struct {
	m          *Manager
	s          *OrphanScanner
	audits     *[]scanAudit
	serversDir string
	pairDir    string
	// kills 任何 killTree 调用；降级场景必须为空。
	kills []int
	// dials 每次收养拨号；降级场景必须为空（开关关闭不收养）。
	dials []string
}

func newUnmanagedDowngradeFixture(t *testing.T, policy OrphanDisposePolicy, autoAdopt bool) *unmanagedDowngradeFixture {
	t.Helper()
	serversDir := t.TempDir()
	pairDir := filepath.Join(serversDir, "survival-"+dgUnmanagedUUID[:8])
	require.NoError(t, os.MkdirAll(pairDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, policy)
	f := &unmanagedDowngradeFixture{m: m, s: s, audits: audits, serversDir: serversDir, pairDir: pairDir}

	// 存活性与动作全部注入：只有该对的 wrapper/java 认为存活，杀树与拨号都被记录。
	alive := map[int]bool{dgWrapperPID: true, dgJavaPID: true}
	m.recoverPIDAlive = func(pid int) bool { return alive[pid] }
	s.pidAlive = m.recoverPIDAlive
	m.recoverKillTree = func(pid int) error { f.kills = append(f.kills, pid); return nil }
	m.recoverTermTree = func(pid int) error { f.kills = append(f.kills, pid); return nil }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	m.adoptVerifyOwner = func(int, string, string, bool) (bool, string) { return true, "test_stub" }
	m.recoverDial = func(_ *daemonStrategy, addr string) error {
		f.dials = append(f.dials, addr)
		return nil
	}
	m.recoverSleep = func(time.Duration) {}

	// 未纳管活进程对的 PID 记录（wrapper 与 Java 均活、记录有效）。
	require.NoError(t, daemon.NewPIDFile(filepath.Join(serversDir, dgUnmanagedUUID+".pid")).WriteRecord(daemon.PIDRecord{
		WrapperPID:   dgWrapperPID,
		JavaPID:      dgJavaPID,
		SocketAddr:   filepath.Join(serversDir, dgUnmanagedUUID+".sock"),
		InstanceUUID: dgUnmanagedUUID,
		WorkDir:      pairDir,
	}))

	// 本机进程快照：该进程对的 wrapper 与 Java 都在其实例目录下运行（direct 相的真实候选）。
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: dgWrapperPID, Cmdline: "/usr/local/bin/jmworker daemon", Cwd: pairDir},
			{PID: dgJavaPID, Cmdline: "java -jar server.jar nogui", Cwd: pairDir},
		}, nil
	}
	s.SetAutoAdopt(autoAdopt)
	return f
}

// countAudit 统计某 action 的审计条数。
func (f *unmanagedDowngradeFixture) countAudit(action string) int {
	n := 0
	for _, a := range *f.audits {
		if a.action == action {
			n++
		}
	}
	return n
}

// findAudit 返回某 action 的第一条审计。
func (f *unmanagedDowngradeFixture) findAudit(action string) (scanAudit, bool) {
	for _, a := range *f.audits {
		if a.action == action {
			return a, true
		}
	}
	return scanAudit{}, false
}

// directFindings 抽出 direct 相产出的 finding（降级后仍以 direct 形态回报，带实例 UUID）。
func directFindings(findings []OrphanFinding) []OrphanFinding {
	out := make([]OrphanFinding, 0, len(findings))
	for _, f := range findings {
		if f.Kind == OrphanKindDirect {
			out = append(out, f)
		}
	}
	return out
}

// TestOrphanScan_UnmanagedPairDowngradedWhenAutoAdoptDisabled 验收①：
// `dispose_policy=auto` + `auto_adopt=false` + 未纳管活进程对 → **不杀**，且留告警 + 审计说明降级。
func TestOrphanScan_UnmanagedPairDowngradedWhenAutoAdoptDisabled(t *testing.T) {
	f := newUnmanagedDowngradeFixture(t, OrphanPolicyAuto, false)
	logs := captureScanLogs(t)

	findings := f.s.ScanOnce()

	// 核心：一个进程都不许杀（killTree 含整棵进程树）。
	require.Empty(t, f.kills, "auto_adopt=false 时未纳管活进程对不得被处置（含 killTree）")
	// 开关关闭仍不收养：不拨号、不登记、PID 文件保留。
	require.Empty(t, f.dials, "开关关闭不得拨号")
	require.False(t, f.m.instanceKnown(dgUnmanagedUUID), "开关关闭不得登记进内存表")
	require.True(t, pidFileExists(filepath.Join(f.serversDir, dgUnmanagedUUID+".pid")),
		"未处置时 PID 记录必须保留")

	// 发现与判决都保留：第 0 相一条「未纳管活进程」，第二相两条（wrapper/Java）带 UUID 的降级 finding。
	require.Len(t, findings, 3, "第 0 相 1 条 + direct 相 2 条（已降级）")
	require.Equal(t, OrphanKindUnmanagedLiveRuntime, findings[0].Kind)
	direct := directFindings(findings)
	require.Len(t, direct, 2)
	for _, d := range direct {
		require.False(t, d.Disposed, "降级档不得标记为已处置")
		require.Equal(t, dgUnmanagedUUID, d.InstanceUUID, "降级 finding 须能回溯到实例")
		require.Equal(t, f.pairDir, d.WorkDir)
		require.Contains(t, d.Detail, "auto_adopt=false", "finding 须写明降级原因")
	}

	// 留痕：审计 1 条「处置被拦截」并写明降级原因；绝不出现「已处置」「已收养」。
	require.Equal(t, 0, f.countAudit("orphan.scan_disposed"), "降级档不得落已处置审计")
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
	require.Equal(t, 1, f.countAudit("orphan.scan_detected"), "第 0 相的发现审计仍在")
	require.Equal(t, 1, f.countAudit("orphan.scan_dispose_blocked"),
		"整对只留一条降级审计（wrapper 与 Java 不各落一条）")
	blocked, ok := f.findAudit("orphan.scan_dispose_blocked")
	require.True(t, ok)
	assert.False(t, blocked.success, "未处置的拦截审计 success 必须为 false")
	assert.Equal(t, dgUnmanagedUUID, blocked.targetID)
	assert.Contains(t, blocked.detail, "auto_adopt_disabled", "审计须写明因 auto_adopt=false 降级")
	assert.Contains(t, blocked.detail, `"effectivePolicy":"warn"`, "审计须写明实际生效的策略")
	assert.Contains(t, blocked.detail, `"policy":"auto"`, "审计须保留配置的策略，便于对照")

	// 告警：WARN 明确说明降级、不做处置。
	out := logs()
	require.Contains(t, out, "降级为 warn", "降级必须产出 WARN 级告警")
	assert.Contains(t, out, dgUnmanagedUUID)
}

// TestOrphanScan_UnmanagedPairDowngradeNoRepeatNoise 降级留痕按变化去重：多轮只一条，PID 变化才算新事件。
func TestOrphanScan_UnmanagedPairDowngradeNoRepeatNoise(t *testing.T) {
	f := newUnmanagedDowngradeFixture(t, OrphanPolicyAuto, false)

	// 连续 3 轮：每轮都继续判决（不留缓存），但只留一条降级痕迹。
	for i := 0; i < 3; i++ {
		require.Len(t, directFindings(f.s.ScanOnce()), 2, "每轮都应继续识别该进程对")
	}
	require.Equal(t, 1, f.countAudit("orphan.scan_dispose_blocked"), "同一进程对多轮只落一条审计")
	require.Empty(t, f.kills)

	// PID 变化＝确实是新事件（旧进程已被换掉），应重新留痕一次。
	newWrapperPID, newJavaPID := dgWrapperPID+10, dgJavaPID+10
	f.m.recoverPIDAlive = func(pid int) bool { return pid == newWrapperPID || pid == newJavaPID }
	f.s.pidAlive = f.m.recoverPIDAlive
	require.NoError(t, daemon.NewPIDFile(filepath.Join(f.serversDir, dgUnmanagedUUID+".pid")).WriteRecord(daemon.PIDRecord{
		WrapperPID:   newWrapperPID,
		JavaPID:      newJavaPID,
		SocketAddr:   filepath.Join(f.serversDir, dgUnmanagedUUID+".sock"),
		InstanceUUID: dgUnmanagedUUID,
		WorkDir:      f.pairDir,
	}))
	f.s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: newWrapperPID, Cmdline: "/usr/local/bin/jmworker daemon", Cwd: f.pairDir},
			{PID: newJavaPID, Cmdline: "java -jar server.jar nogui", Cwd: f.pairDir},
		}, nil
	}

	require.Len(t, directFindings(f.s.ScanOnce()), 2)
	require.Equal(t, 2, f.countAudit("orphan.scan_dispose_blocked"), "PID 变化应重新留痕")
	require.Empty(t, f.kills)
}

// TestOrphanScan_UnmanagedPairDowngradeByPIDWhenCwdDiffers 降级判据含 PID 口径：
// 真实 wrapper 由 Worker 以 `exec.Command(exe, "daemon")` 拉起且**不设 cmd.Dir**，其 cwd 常不是
// 实例 workDir（见 auto-adopt-orphans spec §5）。此时若只按工作目录匹配，wrapper 仍会被误处置。
func TestOrphanScan_UnmanagedPairDowngradeByPIDWhenCwdDiffers(t *testing.T) {
	f := newUnmanagedDowngradeFixture(t, OrphanPolicyAuto, false)
	f.s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			// cwd 在受管根之外，但 cmdline 引用了受管根 → 仍被判为 direct 孤儿候选（既有判据），
			// 靠 PID 口径命中保护快照而免于处置。
			{PID: dgWrapperPID, Cmdline: "/usr/local/bin/jmworker daemon " + f.pairDir, Cwd: os.TempDir()},
		}, nil
	}

	findings := f.s.ScanOnce()

	require.Empty(t, f.kills, "wrapper 的 cwd 与实例目录不一致时，PID 口径仍须保护它")
	direct := directFindings(findings)
	require.Len(t, direct, 1)
	require.False(t, direct[0].Disposed)
	require.Equal(t, dgUnmanagedUUID, direct[0].InstanceUUID)
	require.Equal(t, 1, f.countAudit("orphan.scan_dispose_blocked"))
}

// TestOrphanScan_UnmanagedPairAdoptedWhenAutoAdoptEnabled 验收②：`auto_adopt=true` 时行为完全不变——
// 该收养的收养（进程对进受管集合，不被判为 direct 孤儿），该处置的照旧处置（真 direct 孤儿仍被强杀）。
func TestOrphanScan_UnmanagedPairAdoptedWhenAutoAdoptEnabled(t *testing.T) {
	f := newUnmanagedDowngradeFixture(t, OrphanPolicyAuto, true)
	strayDir := filepath.Join(f.serversDir, "survival-stray")
	require.NoError(t, os.MkdirAll(strayDir, 0o755))
	base := f.s.listProcesses
	f.s.listProcesses = func() ([]ScannedProcess, error) {
		procs, err := base()
		return append(procs, ScannedProcess{PID: dgStrayOrphanPID, Cmdline: "java -jar stray.jar", Cwd: strayDir}), err
	}

	findings := f.s.ScanOnce()

	// 该收养的收养：进程对进入受管集合、拨号一次、PID 不变。
	inst, ok := f.m.GetInstance(dgUnmanagedUUID)
	require.True(t, ok, "auto_adopt=true 时应照旧自动收养")
	require.Equal(t, StateRunning, inst.State)
	require.Equal(t, []string{filepath.Join(f.serversDir, dgUnmanagedUUID+".sock")}, f.dials)
	require.NotContains(t, f.kills, dgWrapperPID, "已收养的 wrapper 不得被杀")
	require.NotContains(t, f.kills, dgJavaPID, "已收养的 Java 不得被杀")
	// 该处置的照旧处置：无关真孤儿仍按 dispose_policy=auto 强杀。
	require.Equal(t, []int{dgStrayOrphanPID}, f.kills, "auto_adopt=true 时处置策略完全不变")
	require.Equal(t, 1, f.countAudit("orphan.scan_disposed"))
	// 开关开启时不产生任何降级留痕（保护快照为空）。
	require.Equal(t, 0, f.countAudit("orphan.scan_dispose_blocked"))
	_, guarded := f.s.unmanagedLiveGuardHit(dgWrapperPID, f.pairDir)
	require.False(t, guarded, "开关开启时保护快照必须为空（不得残留）")
	// 已收养的进程对不得再被判为 direct 孤儿（真孤儿仍会被判，故按实例 UUID 过滤）。
	for _, d := range directFindings(findings) {
		require.NotEqual(t, dgUnmanagedUUID, d.InstanceUUID, "已收养的进程对不得再被判为 direct 孤儿")
	}
}

// TestOrphanScan_ManagedInstanceStillDisposedUnderAuto 验收③：已纳管实例不受本改动影响。
//
// 两个方向都覆盖：受管 RUNNING 实例的进程照旧跳过；已纳管但脱管（STOPPED、无策略）实例目录下的
// 残留 Java 仍按 dispose_policy 处置——本改动只保护「未纳管 + 活」的进程对，不保护别人的孤儿。
func TestOrphanScan_ManagedInstanceStillDisposedUnderAuto(t *testing.T) {
	f := newUnmanagedDowngradeFixture(t, OrphanPolicyAuto, false)
	runningDir := filepath.Join(f.serversDir, "managed-running")
	stoppedDir := filepath.Join(f.serversDir, "managed-stopped")
	require.NoError(t, os.MkdirAll(runningDir, 0o755))
	require.NoError(t, os.MkdirAll(stoppedDir, 0o755))

	// 已纳管 RUNNING：根 PID=7101，其进程属受管。
	require.NoError(t, f.m.Create("managed-running-uuid", "r", "java -jar s.jar", "stop",
		runningDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	f.m.mu.Lock()
	running := f.m.instances["managed-running-uuid"]
	running.State = StateRunning
	running.strategy = &fakePIDStrategy{pid: dgManagedRunningPID}
	f.m.mu.Unlock()

	// 已纳管但脱管（Worker 硬崩后 CP 按 STOPPED 重登记）：WorkDir 在，strategy 为空。
	require.NoError(t, f.m.Create("managed-stopped-uuid", "s", "java -jar s.jar", "stop",
		stoppedDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	base := f.s.listProcesses
	f.s.listProcesses = func() ([]ScannedProcess, error) {
		procs, err := base()
		return append(procs,
			ScannedProcess{PID: dgManagedRunningPID, Cmdline: "java -jar s.jar", Cwd: runningDir},
			ScannedProcess{PID: dgManagedStoppedResidualPID, Cmdline: "java -jar s.jar nogui", Cwd: stoppedDir},
		), err
	}

	f.s.ScanOnce()

	// 受管 RUNNING 的进程不被杀；脱管实例的残留仍按 auto 处置；未纳管进程对仍受降级保护。
	require.Equal(t, []int{dgManagedStoppedResidualPID}, f.kills,
		"已纳管实例的处置路径不受本改动影响")
	require.Equal(t, 1, f.countAudit("orphan.scan_disposed"))
	require.Equal(t, 1, f.countAudit("orphan.scan_dispose_blocked"))
	blocked, ok := f.findAudit("orphan.scan_dispose_blocked")
	require.True(t, ok)
	require.Equal(t, dgUnmanagedUUID, blocked.targetID, "降级痕迹只针对未纳管进程对")
	require.Contains(t, blocked.detail, "auto_adopt_disabled")
}

// TestOrphanScan_WarnPolicyUnchanged 验收④：`dispose_policy=warn` 行为完全不变。
//
// warn 档本就只告警不处置，故不得新增「降级」留痕——否则审计形态会变，且会让运维误以为
// auto_adopt 开关在 warn 档生效。
func TestOrphanScan_WarnPolicyUnchanged(t *testing.T) {
	f := newUnmanagedDowngradeFixture(t, OrphanPolicyWarn, false)

	findings := f.s.ScanOnce()

	require.Empty(t, f.kills, "warn 档不得处置")
	require.Equal(t, 0, f.countAudit("orphan.scan_dispose_blocked"), "warn 档不产生降级留痕")
	require.Equal(t, 0, f.countAudit("orphan.scan_disposed"))
	// 既有形态保留：第 0 相 1 条 + direct 相每进程 1 条 scan_detected。
	require.Equal(t, 3, f.countAudit("orphan.scan_detected"))
	direct := directFindings(findings)
	require.Len(t, direct, 2)
	for _, d := range direct {
		require.False(t, d.Disposed)
	}
	// warn 档的 finding 不带降级文案（保持既有形态）。
	require.NotContains(t, direct[0].Detail, "auto_adopt=false")
}
