package process

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// ③ 回归：未纳管活进程的全自动收养。
//
// 判据与 docs/specs/restart-resilience/spec.md §2.1 一致：只有「确属本实例」的活进程对
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
	// 收养专用的归属复核桩（收紧后收养走 verifyAdoptOwnership，不再复用强杀路径的宽口径桩）。
	m.adoptVerifyOwner = func(int, string, string, bool) (bool, string) { return true, "test_stub" }
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
			f.m.adoptVerifyOwner = func(pid int, _ string, _ string, _ bool) (bool, string) {
				if pid == deny {
					return false, "test_denied"
				}
				return true, "test_stub"
			}

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

// TestOrphanScan_AutoAdoptDisabled ④：关闭开关后不自动收养，但**发现与告警必须保留**。
//
// 规格 §2.3/§4 的口径是「`false` 只关收养」「不收养（仍只告警）」：开关是**收养动作**的逃生口，
// 不是**观测能力**的逃生口。此前实现开关关闭后直接 return nil——不拨号、不登记、也不告警，
// 使「磁盘在跑、平台记账 STOPPED、控制面完全沉默」的缺陷形态原样复活（实测 7 轮扫描 0 条日志）。
func TestOrphanScan_AutoAdoptDisabled(t *testing.T) {
	f := newAdoptFixture(t)
	f.s.SetAutoAdopt(false)
	logs := captureScanLogs(t)

	findings := f.s.ScanOnce()

	// 发现保留：仍然报出「有个未纳管活进程在这儿」，只是 Adopted=false。
	require.Len(t, findings, 1, "开关关闭只关收养，发现能力必须保留")
	require.Equal(t, OrphanKindUnmanagedLiveRuntime, findings[0].Kind)
	require.Equal(t, adoptUUID, findings[0].InstanceUUID)
	require.Equal(t, []int{adoptWrapperPID, adoptJavaPID}, findings[0].PIDs)
	require.False(t, findings[0].Adopted, "开关关闭不得收养")

	// 收养动作一概不执行：不登记、不拨号（重连属收养动作）、更不杀进程（护栏五.1）。
	require.False(t, f.m.instanceKnown(adoptUUID), "开关关闭不得登记进内存表")
	require.Empty(t, f.dials, "开关关闭不得拨号")
	require.Empty(t, f.kills, "开关关闭更不得杀进程（护栏五.1）")
	require.True(t, pidFileExists(filepath.Join(f.dir, adoptUUID+".pid")), "保留 PID 文件")

	// 告警 + 审计：运维必须能看到这条线索。
	out := logs()
	require.Contains(t, out, "自动收养已关闭", "开关关闭时应产出 WARN 级告警")
	require.Contains(t, out, adoptUUID)
	require.Equal(t, 1, f.countAudit("orphan.scan_detected"), "应落一条「发现但未处置」审计")
	require.Equal(t, 0, f.countAudit("orphan.auto_adopted"))
	require.Equal(t, 0, f.countAudit("orphan.adopt_blocked"))
	for _, a := range f.audits {
		if a.action != "orphan.scan_detected" {
			continue
		}
		require.Contains(t, a.detail, "unmanaged_live_runtime")
		require.Contains(t, a.detail, "auto_adopt_disabled", "审计须写明未收养的原因")
		require.True(t, a.success)
	}

	// 默认口径：构造即开（配置层默认 true 的单一真源见 DefaultOrphanAutoAdopt）。
	require.True(t, DefaultOrphanAutoAdopt)
	require.True(t, NewOrphanScanner(f.m, time.Minute, OrphanPolicyWarn).autoAdopt)
}

// TestOrphanScan_AutoAdoptDisabledNoRepeatNoise ④-a：开关关闭时的告警按变化去重，不每轮刷屏。
//
// 扫描默认 60s 一拍，若每轮都 WARN + 落审计，一个长期驻留的未纳管进程会以 1440 条/天的量刷屏并
// 撑大审计表。故同一 (UUID, wrapper/java PID) 只在首次出现或 PID 变化时告警一次。
func TestOrphanScan_AutoAdoptDisabledNoRepeatNoise(t *testing.T) {
	f := newAdoptFixture(t)
	f.s.SetAutoAdopt(false)

	// 连续 5 轮：每轮都继续发现（观测不缓存），但只告警 + 落审计一次。
	for i := 0; i < 5; i++ {
		require.Len(t, f.s.ScanOnce(), 1, "每轮都应继续发现")
	}
	require.Equal(t, 1, f.countAudit("orphan.scan_detected"), "同一进程对多轮只落一条审计")
	require.Empty(t, f.dials)
	require.Empty(t, f.kills)

	// PID 变化＝确实是新事件（旧进程已被换掉），应重新告警一次。
	newWrapperPID, newJavaPID := adoptWrapperPID+10, adoptJavaPID+10
	f.m.recoverPIDAlive = func(pid int) bool { return pid == newWrapperPID || pid == newJavaPID }
	require.NoError(t, daemon.NewPIDFile(filepath.Join(f.dir, adoptUUID+".pid")).WriteRecord(daemon.PIDRecord{
		WrapperPID:   newWrapperPID,
		JavaPID:      newJavaPID,
		SocketAddr:   f.socket,
		InstanceUUID: adoptUUID,
		WorkDir:      filepath.Join(f.dir, "server"),
		ProbePort:    25566,
	}))
	require.Len(t, f.s.ScanOnce(), 1)
	require.Equal(t, 2, f.countAudit("orphan.scan_detected"), "PID 变化应重新告警")
	require.Empty(t, f.kills)
}

// TestOrphanScan_AutoAdoptDisabledNoCandidateNoNoise ④-b：开关关闭且无未纳管进程时不产生任何噪音。
func TestOrphanScan_AutoAdoptDisabledNoCandidateNoNoise(t *testing.T) {
	f := newAdoptFixture(t)
	f.s.SetAutoAdopt(false)
	// PID 记录仍在，但两个进程都已死：不构成「未纳管活进程」。
	f.m.recoverPIDAlive = func(int) bool { return false }
	logs := captureScanLogs(t)

	require.Empty(t, f.s.ScanOnce(), "无未纳管活进程不应产出 finding")

	out := logs()
	require.NotContains(t, out, "未纳管的活进程", "无候选时不得产出噪音日志")
	require.NotContains(t, out, "自动收养已关闭")
	require.Empty(t, f.audits, "无候选时不得落审计")
	require.Empty(t, f.dials)
	require.Empty(t, f.kills)
}

// TestOrphanScan_AutoAdoptDisabledIgnoresOwnershipGate ④-c：归属复核不是「观测闸」。
//
// 归属复核是**收养动作**的安全闸；开关关闭时根本不存在收养动作，故复核不通过也不该让发现静默——
// 否则 Windows（无 /proc，wrapper 分支恒 fail-closed）会连告警都发不出，退回原始缺陷。
func TestOrphanScan_AutoAdoptDisabledIgnoresOwnershipGate(t *testing.T) {
	f := newAdoptFixture(t)
	f.s.SetAutoAdopt(false)
	f.m.recoverVerifyOwner = func(int, string, string, bool) bool { return false }

	findings := f.s.ScanOnce()

	require.Len(t, findings, 1, "复核不过不应让发现静默")
	require.False(t, findings[0].Adopted)
	require.Equal(t, 1, f.countAudit("orphan.scan_detected"))
	require.Equal(t, 0, f.countAudit("orphan.adopt_blocked"), "开关关闭时不存在收养动作")
	require.Empty(t, f.dials)
	require.Empty(t, f.kills)
}

// captureScanLogs 把默认 logger 临时重定向到内存缓冲，返回读取函数（还原由 t.Cleanup 负责）。
// 本包测试不使用 t.Parallel()，故替换默认 logger 不会与其它用例互相干扰。
func captureScanLogs(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
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
