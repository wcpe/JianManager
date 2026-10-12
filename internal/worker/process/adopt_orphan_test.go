package process

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-497③ 回归：未纳管活进程的全自动收养。
//
// 判据与 ADR-093 / docs/specs/restart-resilience/spec.md §2.1 一致：只有「确属本实例」的活进程对
// （wrapper 分支须 /proc cmdline+environ 确证、Java 分支按 cmdline/cwd 匹配）才被收养；复核不过
// 一律只告警。收养是**非破坏**动作——只重连、不重启、不杀，PID 不变。
const (
	adoptWrapperPID = 5150
	adoptJavaPID    = 5151
	adoptUUID       = "11111111-2222-3333-4444-555555555555"
)

// adoptFixture 未纳管活进程收养夹具：PID 目录里一条「wrapper 与 Java 均活」的记录，内存表为空。
type adoptFixture struct {
	m      *Manager
	s      *OrphanScanner
	dir    string
	socket string
	// dials 每次拨号的 socket 地址（收养的唯一外部动作）。
	dials []string
	// kills 任何 killTree/signalTree 调用（收养必须**零**调用）。
	kills  []int
	audits []scanAudit
}

func newAdoptFixture(t *testing.T) *adoptFixture {
	t.Helper()
	dir := t.TempDir()
	f := &adoptFixture{dir: dir, socket: filepath.Join(dir, adoptUUID+".sock")}

	m := NewManager(dir)
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		f.audits = append(f.audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
	// 存活：只有记录里的 wrapper/java PID 认为是活的。
	m.recoverPIDAlive = func(pid int) bool { return pid == adoptWrapperPID || pid == adoptJavaPID }
	m.recoverDial = func(_ *daemonStrategy, addr string) error {
		f.dials = append(f.dials, addr)
		return nil
	}
	// 任何杀/终止动作都要被看见（收养路径必须一次都不调用）。
	m.recoverKillTree = func(pid int) error { f.kills = append(f.kills, pid); return nil }
	m.recoverTermTree = func(pid int) error { f.kills = append(f.kills, pid); return nil }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	m.recoverSleep = func(time.Duration) {}

	require.NoError(t, daemon.NewPIDFile(filepath.Join(dir, adoptUUID+".pid")).WriteRecord(daemon.PIDRecord{
		WrapperPID:   adoptWrapperPID,
		JavaPID:      adoptJavaPID,
		SocketAddr:   f.socket,
		InstanceUUID: adoptUUID,
		WorkDir:      filepath.Join(dir, "server"),
		ProbePort:    25566,
	}))

	s := NewOrphanScanner(m, time.Minute, OrphanPolicyWarn)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, nil }
	s.removeContainer = func(context.Context, string) error { return nil }

	f.m, f.s = m, s
	return f
}

// countAudit 统计某 action 的审计条数。
func (f *adoptFixture) countAudit(action string) int {
	n := 0
	for _, a := range f.audits {
		if a.action == action {
			n++
		}
	}
	return n
}

// TestOrphanScan_AutoAdoptUnmanagedLiveRuntime ①：未纳管活进程被自动收养为 RUNNING，且进程未被重启。
func TestOrphanScan_AutoAdoptUnmanagedLiveRuntime(t *testing.T) {
	f := newAdoptFixture(t)

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1)
	require.Equal(t, OrphanKindUnmanagedLiveRuntime, findings[0].Kind)
	require.True(t, findings[0].Adopted)
	require.Equal(t, adoptUUID, findings[0].InstanceUUID)

	inst, ok := f.m.GetInstance(adoptUUID)
	require.True(t, ok, "未纳管活进程应被收养进内存表")
	require.Equal(t, StateRunning, inst.State)
	require.Equal(t, ProcessTypeDaemon, inst.processType)
	require.Equal(t, filepath.Join(f.dir, "server"), inst.WorkDir)
	require.Equal(t, 25566, inst.ProbePort)
	require.NotNil(t, inst.strategy)

	// PID 不变：收养只重连，不重启也不杀——策略报告的仍是 PID 记录里的那个 wrapper PID。
	require.Equal(t, adoptWrapperPID, inst.strategy.GetPID())
	require.Empty(t, f.kills, "收养路径不得调用任何杀/终止动作")
	require.Equal(t, []string{f.socket}, f.dials, "应且仅应拨号一次，目标是该实例的 socket")

	// 收养等价于既有纳管：心跳快照可见且为 RUNNING（CP 收敛与健康巡检的入口）。
	snap := f.m.GetAllInstanceStates()
	require.Len(t, snap, 1)
	require.Equal(t, string(StateRunning), snap[0].State)
	require.Equal(t, adoptWrapperPID, snap[0].PID)

	// 审计：一条 auto_adopted（success=true）；PID 文件保留（进程仍在跑，记录不能清）。
	require.Equal(t, 1, f.countAudit("orphan.auto_adopted"))
	for _, a := range f.audits {
		if a.action == "orphan.auto_adopted" {
			require.True(t, a.success)
		}
	}
	require.True(t, pidFileExists(filepath.Join(f.dir, adoptUUID+".pid")))
}

// TestOrphanScan_AutoAdoptOwnershipBlocked ②：归属复核不过 → 只告警、不纳管、不杀、保留 PID 文件。
func TestOrphanScan_AutoAdoptOwnershipBlocked(t *testing.T) {
	cases := []struct {
		name     string
		denyPID  int
		wantRole string
	}{
		{name: "wrapper 分支复核不过", denyPID: adoptWrapperPID, wantRole: "wrapper"},
		{name: "Java 分支复核不过", denyPID: adoptJavaPID, wantRole: "java"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdoptFixture(t)
			deny := tc.denyPID
			f.m.recoverVerifyOwner = func(pid int, _ string, _ string, _ bool) bool { return pid != deny }

			findings := f.s.ScanOnce()

			require.Len(t, findings, 1)
			require.False(t, findings[0].Adopted)
			require.False(t, f.m.instanceKnown(adoptUUID), "复核不过不得纳管")
			require.Empty(t, f.dials, "复核不过不得拨号")
			require.Empty(t, f.kills, "复核不过绝不动进程")
			require.Equal(t, 1, f.countAudit("orphan.adopt_blocked"))
			require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
			require.Contains(t, f.audits[0].detail, tc.wantRole)
			require.True(t, pidFileExists(filepath.Join(f.dir, adoptUUID+".pid")), "保留 PID 文件等下一轮/人工介入")
		})
	}
}

// TestOrphanScan_AutoAdoptIdempotent ③：重复扫描幂等——不重复拨号、不重复登记、不重复审计。
func TestOrphanScan_AutoAdoptIdempotent(t *testing.T) {
	f := newAdoptFixture(t)

	require.Len(t, f.s.ScanOnce(), 1)
	first, ok := f.m.GetInstance(adoptUUID)
	require.True(t, ok)
	firstStrategy := first.strategy

	f.dials = nil
	f.audits = nil
	second := f.s.ScanOnce()

	require.Empty(t, second, "已纳管实例不再出现在收养 finding 中")
	got, _ := f.m.GetInstance(adoptUUID)
	require.Same(t, firstStrategy, got.strategy, "不得用新策略替换已纳管的策略")
	require.Equal(t, StateRunning, got.State)
	require.Equal(t, adoptWrapperPID, got.strategy.GetPID(), "PID 保持不变")
	require.Empty(t, f.dials, "已纳管不得重复拨号")
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
	require.Empty(t, f.kills)
}

// TestOrphanScan_AutoAdoptDisabled ④：关闭开关后不自动收养。
func TestOrphanScan_AutoAdoptDisabled(t *testing.T) {
	f := newAdoptFixture(t)
	f.s.SetAutoAdopt(false)

	findings := f.s.ScanOnce()

	require.Empty(t, findings, "关闭收养后不应产生收养 finding")
	require.False(t, f.m.instanceKnown(adoptUUID))
	require.Empty(t, f.dials)
	require.Empty(t, f.kills)
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
	require.Equal(t, 0, f.countAudit("orphan.adopt_blocked"))
	require.True(t, pidFileExists(filepath.Join(f.dir, adoptUUID+".pid")))

	// 默认口径：构造即开（配置层默认 true 的单一真源见 DefaultOrphanAutoAdopt）。
	require.True(t, DefaultOrphanAutoAdopt)
	require.True(t, NewOrphanScanner(f.m, time.Minute, OrphanPolicyWarn).autoAdopt)
}

// TestOrphanScan_AutoAdoptRegisteredStoppedKeepsSpec ⑦：实例已在内存表但脱管（STOPPED、无策略，
// 典型：Worker 硬崩后 CP ResyncInstances 按 STOPPED 重登记而磁盘进程仍在跑）时就地补齐运行态，
// 并保留 CP 已下发的启动规格——整条覆盖会让后续 Restart 拿着空启动命令去拉起新 wrapper。
func TestOrphanScan_AutoAdoptRegisteredStoppedKeepsSpec(t *testing.T) {
	f := newAdoptFixture(t)
	workDir := filepath.Join(f.dir, "server")
	require.NoError(t, f.m.Create(adoptUUID, "农场-1", "java -jar server.jar nogui", "stop", workDir,
		map[string]string{"FOO": "bar"}, true, ProcessTypeDaemon, "/jdk", "/jdk/bin/java", 25566, 30))

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1)
	require.True(t, findings[0].Adopted)
	inst, ok := f.m.GetInstance(adoptUUID)
	require.True(t, ok)
	require.Equal(t, StateRunning, inst.State)
	require.Equal(t, "java -jar server.jar nogui", inst.StartCommand, "CP 下发的启动规格必须保留")
	require.Equal(t, map[string]string{"FOO": "bar"}, inst.EnvVars)
	require.Equal(t, "农场-1", inst.Name)
	require.NotNil(t, inst.strategy)
	require.Equal(t, adoptWrapperPID, inst.strategy.GetPID())
	require.Empty(t, f.kills)
	require.Equal(t, 1, f.countAudit("orphan.auto_adopted"))
}

// TestOrphanScan_AutoAdoptDialFailureKeepsPIDFile ⑤：复核通过但拨号失败 → 不破坏、保留 PID 文件待下轮。
func TestOrphanScan_AutoAdoptDialFailureKeepsPIDFile(t *testing.T) {
	f := newAdoptFixture(t)
	f.m.recoverDial = func(_ *daemonStrategy, addr string) error {
		f.dials = append(f.dials, addr)
		return errors.New("dial refused")
	}

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1)
	require.False(t, findings[0].Adopted)
	require.False(t, f.m.instanceKnown(adoptUUID))
	require.Equal(t, 1, f.countAudit("orphan.adopt_failed"))
	require.True(t, pidFileExists(filepath.Join(f.dir, adoptUUID+".pid")))
	require.Empty(t, f.kills)
}

// TestOrphanScannerStart_AutoAdoptsImmediately ⑥：Start 立即跑一轮（“Worker 启动”窗口由同一扫描器覆盖）。
func TestOrphanScannerStart_AutoAdoptsImmediately(t *testing.T) {
	f := newAdoptFixture(t)
	s := NewOrphanScanner(f.m, time.Hour, OrphanPolicyWarn)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, nil }
	s.removeContainer = func(context.Context, string) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	require.Eventually(t, func() bool { return f.m.instanceKnown(adoptUUID) }, 3*time.Second, 5*time.Millisecond,
		"Start 后应立即完成一轮收养，无需等待第一个周期")
	inst, ok := f.m.GetInstance(adoptUUID)
	require.True(t, ok)
	require.NotNil(t, inst.strategy)
	require.Equal(t, adoptWrapperPID, inst.strategy.GetPID(), "PID 不变")
	require.Empty(t, f.kills)
}
