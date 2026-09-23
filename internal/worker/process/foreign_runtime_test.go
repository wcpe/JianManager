package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustListenLocal 在回环地址上占用一个由内核分配的端口（端口占用正例夹具）。
func mustListenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

// listenerPort 取监听器实际绑定的端口号。
func listenerPort(t *testing.T, ln net.Listener) int {
	t.Helper()
	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}

// portText 端口号文本（供断言失败原因含端口号）。
func portText(port int) string { return strconv.Itoa(port) }

// TestOrphanScan_ForeignRuntimeDriftOutsideServersDir FR-471 正例：实例已注册（记账 STOPPED）、
// 其工作目录**在 serversDir 之外**（真机农场目录 /home/<user>/server），目录下仍有活进程
// （cwd 落在该目录）→ 必须被第 4 相识别为漂移，写观测缓存并落审计，且**不杀进程**。
func TestOrphanScan_ForeignRuntimeDriftOutsideServersDir(t *testing.T) {
	serversDir := t.TempDir()
	// 外部启动的服务器目录：刻意放在 serversDir 之外。
	farmDir := filepath.Join(t.TempDir(), "survival")
	require.NoError(t, os.MkdirAll(farmDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyAuto)
	uuid := "drift-outside-uuid"
	require.NoError(t, m.Create(uuid, "农场服", "java -jar server.jar nogui", "stop", farmDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	var killed []int
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 5011, Cmdline: "java -Xmx4G -jar server.jar nogui", Cwd: farmDir}, // 外来活进程
			{PID: 5012, Cmdline: "vim notes.txt", Cwd: "/tmp"},                      // 无关进程
		}, nil
	}

	findings := s.ScanOnce()
	require.Len(t, findings, 1, "serversDir 之外的实例目录同样须命中漂移")
	assert.Equal(t, OrphanKindForeignRuntime, findings[0].Kind)
	assert.Equal(t, uuid, findings[0].InstanceUUID)
	assert.Equal(t, farmDir, findings[0].WorkDir)
	assert.Equal(t, []int{5011}, findings[0].PIDs)
	assert.False(t, findings[0].Disposed, "只观测不杀")
	assert.Empty(t, killed, "第 4 相不得处置任何进程")

	require.Len(t, *audits, 1)
	assert.Equal(t, "orphan.foreign_runtime_detected", (*audits)[0].action)
	assert.Equal(t, uuid, (*audits)[0].targetID)
	assert.Contains(t, (*audits)[0].detail, `"policy":"observe"`)
	assert.Contains(t, (*audits)[0].detail, `"pid":5011`)

	// 观测缓存经心跳快照对外可见。
	states := m.GetAllInstanceStates()
	require.Len(t, states, 1)
	assert.Equal(t, 5011, states[0].ForeignPID)
	assert.Contains(t, states[0].ForeignCmdline, "server.jar")
}

// TestOrphanScan_ForeignRuntimeNoFalsePositive FR-471 反例①：目录下无活进程时不误报，
// 且缓存为空；反例②：`go test` 自身进程等无关进程（cwd 不在实例目录）不得命中。
func TestOrphanScan_ForeignRuntimeNoFalsePositive(t *testing.T) {
	serversDir := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "quiet")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyWarn)
	uuid := "no-drift-uuid"
	require.NoError(t, m.Create(uuid, "安静实例", "java -jar server.jar nogui", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 6001, Cmdline: "java -jar other.jar nogui", Cwd: t.TempDir()}, // 别的目录
			{PID: 6002, Cmdline: "nginx: worker process", Cwd: "/"},             // 无关
		}, nil
	}

	findings := s.ScanOnce()
	assert.Empty(t, findings, "目录下无活进程不得误报漂移")
	assert.Empty(t, *audits, "无漂移不得落审计")

	states := m.GetAllInstanceStates()
	require.Len(t, states, 1)
	assert.Zero(t, states[0].ForeignPID)
	assert.Empty(t, states[0].ForeignCmdline)
}

// TestOrphanScan_ForeignRuntimeClearedWhenResolved FR-471：漂移消解后（进程退出）下一轮扫描
// 必须把观测缓存清空，使心跳上报的漂移归零——否则面板会永远标红一个已不存在的进程。
func TestOrphanScan_ForeignRuntimeClearedWhenResolved(t *testing.T) {
	serversDir := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "resolve")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	s, m, _ := newTestScanner(t, serversDir, OrphanPolicyWarn)
	uuid := "resolve-uuid"
	require.NoError(t, m.Create(uuid, "实例", "java -jar server.jar nogui", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 7001, Cmdline: "java -jar server.jar nogui", Cwd: workDir}}, nil
	}
	require.Len(t, s.ScanOnce(), 1)
	require.Equal(t, 7001, m.GetAllInstanceStates()[0].ForeignPID)

	// 外来进程退出。
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	assert.Empty(t, s.ScanOnce())
	assert.Zero(t, m.GetAllInstanceStates()[0].ForeignPID, "漂移消解后缓存须清零")
}

// TestOrphanScan_ForeignRuntimeSkipsManagedRunning FR-471：实例记账为 RUNNING 时，
// 其目录下的进程属受管运行态，不得判为漂移（否则接管动作会去杀一个正常运行的自己）。
func TestOrphanScan_ForeignRuntimeSkipsManagedRunning(t *testing.T) {
	serversDir := t.TempDir()
	workDir := filepath.Join(serversDir, "running-xyz")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyWarn)
	uuid := "running-uuid"
	require.NoError(t, m.Create(uuid, "实例", "java -jar server.jar nogui", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	m.mu.Lock()
	m.instances[uuid].State = StateRunning
	m.instances[uuid].strategy = &fakePIDStrategy{pid: 8001}
	m.mu.Unlock()

	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 8001, Cmdline: "java -jar server.jar nogui", Cwd: workDir}, // 受管根进程
			{PID: 8002, Cmdline: "java -jar extra.jar", Cwd: workDir},        // 同目录其它进程
		}, nil
	}

	findings := s.ScanOnce()
	assert.Empty(t, findings, "RUNNING 实例不属漂移（其进程由生命周期掌管）")
	assert.Empty(t, *audits)
	assert.Zero(t, m.GetAllInstanceStates()[0].ForeignPID)
}

// TestOrphanScan_ForeignRuntimeReusesProcessSnapshot FR-471：第 2/4 相复用同一轮进程枚举快照，
// 每轮只枚举一次（避免每拍两次全机枚举的成本）。
func TestOrphanScan_ForeignRuntimeReusesProcessSnapshot(t *testing.T) {
	s, _, _ := newTestScanner(t, t.TempDir(), OrphanPolicyWarn)
	calls := 0
	s.listProcesses = func() ([]ScannedProcess, error) {
		calls++
		return nil, nil
	}
	s.ScanOnce()
	assert.Equal(t, 1, calls, "单轮扫描只应枚举一次本机进程")
}

// TestOrphanScan_ForeignRuntimeNoDuplicateAudit FR-471：同一漂移连续两轮只落一条审计
// （扫描每 60s 一轮，否则会持续刷审计）。
func TestOrphanScan_ForeignRuntimeNoDuplicateAudit(t *testing.T) {
	s, m, audits := newTestScanner(t, t.TempDir(), OrphanPolicyWarn)
	workDir := t.TempDir()
	uuid := "dup-audit-uuid"
	require.NoError(t, m.Create(uuid, "实例", "java -jar server.jar nogui", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 9001, Cmdline: "java -jar server.jar nogui", Cwd: workDir}}, nil
	}

	require.Len(t, s.ScanOnce(), 1)
	require.Len(t, s.ScanOnce(), 1)
	assert.Len(t, *audits, 1, "同一漂移未变化时不重复落审计")

	// PID 变化（换了一个外来进程）→ 视为新漂移，重新落审计。
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 9002, Cmdline: "java -jar server.jar nogui", Cwd: workDir}}, nil
	}
	require.Len(t, s.ScanOnce(), 1)
	assert.Len(t, *audits, 2)
}

// TestScanForeign_TakesSmallestPID FR-471：同一目录多个外来进程时取最小 PID 作为稳定代表。
func TestScanForeign_TakesSmallestPID(t *testing.T) {
	s, m, _ := newTestScanner(t, t.TempDir(), OrphanPolicyWarn)
	workDir := t.TempDir()
	uuid := "smallest-pid-uuid"
	require.NoError(t, m.Create(uuid, "实例", "java -jar server.jar nogui", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 7020, Cmdline: "java -jar server.jar nogui", Cwd: workDir},
			{PID: 7010, Cmdline: "java -jar server.jar nogui", Cwd: workDir},
		}, nil
	}
	findings := s.ScanOnce()
	require.Len(t, findings, 1)
	assert.Equal(t, []int{7010}, findings[0].PIDs)
}

// TestManager_AdoptForeignRuntime_WithDrift FR-471 接管主路径：有漂移 → 先 SIGTERM 进程树等待退出，
// 再以受管方式 Start；返回被停止的 PID，实例转为 RUNNING，漂移缓存清空。
func TestManager_AdoptForeignRuntime_WithDrift(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemGuard(MemGuardConfig{Disabled: true})
	workDir := t.TempDir()
	uuid := "adopt-with-drift"
	require.NoError(t, m.Create(uuid, "农场服", "sleep 60", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	m.SetForeignRuntimes(map[string]ForeignRuntime{uuid: {PID: 9601, Cmdline: "java -jar server.jar"}})

	dead := map[int]bool{}
	var termSent []int
	m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
	m.recoverSleep = func(time.Duration) {}
	m.recoverKillTree = func(pid int) error { dead[pid] = true; return nil }
	m.recoverTermTree = func(pid int) error { termSent = append(termSent, pid); dead[pid] = true; return nil }
	// 漂移 PID 是夹具值（非真实进程），注入归属复核桩恒通过（真实现读 cmdline/cwd 会判 false）。
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }

	stopped, err := m.AdoptForeignRuntime(uuid)
	require.NoError(t, err)
	require.Equal(t, 9601, stopped, "应返回被接管停止的 PID")
	assert.Equal(t, []int{9601}, termSent, "接管第一步必须对漂移进程树发 SIGTERM")

	state, err := m.GetState(uuid)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, state, "接管后实例应转为受管 RUNNING")

	states := m.GetAllInstanceStates()
	require.Len(t, states, 1)
	assert.Zero(t, states[0].ForeignPID, "接管后漂移缓存须清空")
	assert.Greater(t, states[0].PID, 0, "受管进程应有 PID")

	// 清理真实子进程。
	require.NoError(t, m.Kill(uuid))
}

// TestManager_AdoptForeignRuntime_NoDriftStartsIdempotently FR-471：无漂移时直接走正常启动
// （返回 0），不发送任何信号。
func TestManager_AdoptForeignRuntime_NoDriftStartsIdempotently(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemGuard(MemGuardConfig{Disabled: true})
	workDir := t.TempDir()
	uuid := "adopt-no-drift"
	require.NoError(t, m.Create(uuid, "实例", "sleep 60", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))

	signalCalled := false
	m.recoverTermTree = func(int) error { signalCalled = true; return nil }

	stopped, err := m.AdoptForeignRuntime(uuid)
	require.NoError(t, err)
	assert.Zero(t, stopped, "无漂移应返回 0")
	assert.False(t, signalCalled, "无漂移不得发送 SIGTERM")

	state, err := m.GetState(uuid)
	require.NoError(t, err)
	assert.Equal(t, StateRunning, state)
	require.NoError(t, m.Kill(uuid))
}

// TestManager_AdoptForeignRuntime_Unregistered 未注册实例直接报错，不做任何事。
func TestManager_AdoptForeignRuntime_Unregistered(t *testing.T) {
	m := NewManager(t.TempDir())
	_, err := m.AdoptForeignRuntime("not-registered")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "不存在")
}

// TestManager_AdoptForeignRuntime_TerminateTimeoutEscalates FR-471：SIGTERM 后进程不退出 →
// 超时升级强杀；仍未退净则报错且**不启动**（避免双开）。
func TestManager_AdoptForeignRuntime_TerminateTimeoutEscalates(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemGuard(MemGuardConfig{Disabled: true})
	workDir := t.TempDir()
	uuid := "adopt-stubborn"
	require.NoError(t, m.Create(uuid, "顽固实例", "sleep 60", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	m.SetForeignRuntimes(map[string]ForeignRuntime{uuid: {PID: 9701, Cmdline: "java -jar server.jar"}})

	killTreeCalled := false
	m.recoverTermTree = func(int) error { return nil } // SIGTERM 无效
	m.recoverPIDAlive = func(int) bool { return true } // 始终活着
	m.recoverSleep = func(time.Duration) {}            // 免真等待
	m.recoverKillTree = func(int) error { killTreeCalled = true; return nil }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }

	stopped, err := m.AdoptForeignRuntime(uuid)
	require.Error(t, err, "进程未退净应报错")
	assert.Equal(t, 9701, stopped)
	assert.True(t, killTreeCalled, "超时后必须升级强杀进程树")

	state, err := m.GetState(uuid)
	require.NoError(t, err)
	assert.NotEqual(t, StateRunning, state, "外来进程未退净时不得启动（避免双开）")
}

// TestManager_AdoptForeignRuntime_OwnershipBlocked FR-455① 纪律：漂移观测与人工点击之间若该 PID
// 已被 OS 回收给无关进程（归属复核不通过），必须拒绝处置——不发信号、不启动、落审计。
func TestManager_AdoptForeignRuntime_OwnershipBlocked(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemGuard(MemGuardConfig{Disabled: true})
	workDir := t.TempDir()
	uuid := "adopt-ownership-blocked"
	require.NoError(t, m.Create(uuid, "实例", "sleep 60", "stop", workDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	m.SetForeignRuntimes(map[string]ForeignRuntime{uuid: {PID: 9801, Cmdline: "java -jar server.jar"}})

	var audits []string
	m.onOrphanAudit = func(action, _, _ string, _ bool, _ string) { audits = append(audits, action) }

	signalCalled := false
	m.recoverPIDAlive = func(int) bool { return true }
	m.recoverTermTree = func(int) error { signalCalled = true; return nil }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return false } // 复核不通过

	stopped, err := m.AdoptForeignRuntime(uuid)
	require.Error(t, err)
	assert.Equal(t, 9801, stopped)
	assert.False(t, signalCalled, "归属复核不通过时不得发信号")

	state, err := m.GetState(uuid)
	require.NoError(t, err)
	assert.NotEqual(t, StateRunning, state)
	assert.Contains(t, audits, "orphan.foreign_runtime_adopt_blocked")
}

// TestPreflight_WorkDirBusy FR-471 预检正反各一例：工作目录下有活进程 → 失败并带 PID 与指引；
// 目录下无活进程 → 通过。
func TestPreflight_WorkDirBusy(t *testing.T) {
	orig := preflightListProcesses
	defer func() { preflightListProcesses = orig }()

	workDir := t.TempDir()
	otherDir := t.TempDir()

	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 1234, Cmdline: "java -jar server.jar nogui", Cwd: workDir},
		}, nil
	}
	err := checkWorkDirBusy(workDir)
	require.Error(t, err, "目录下有活进程应失败")
	assert.Contains(t, err.Error(), "1234")
	assert.Contains(t, err.Error(), "可能为未纳管进程或残留，请先接管或清理")

	assert.NoError(t, checkWorkDirBusy(otherDir), "其它目录不应被牵连")
	assert.NoError(t, checkWorkDirBusy(""), "空工作目录由 work_dir 项负责，本项放行")

	// 进程枚举失败时不拦启动（只告警降级）。
	preflightListProcesses = func() ([]ScannedProcess, error) { return nil, os.ErrPermission }
	assert.NoError(t, checkWorkDirBusy(workDir))
}

// TestPreflight_PortFree FR-471 预检正反各一例：真占一个端口 → 失败并带端口号；空闲端口 → 通过；
// port<=0（未配置）不检查。
func TestPreflight_PortFree(t *testing.T) {
	// 真占一个端口：拿到内核分配的端口号后，该端口应被判为占用。
	ln := mustListenLocal(t)
	defer ln.Close()
	busyPort := listenerPort(t, ln)

	err := checkPortFree(busyPort)
	require.Error(t, err, "已被监听的端口应判为占用")
	assert.Contains(t, err.Error(), portText(busyPort), "失败原因须含端口号")

	// 空闲端口：先占后放，拿到一个刚释放的端口号。
	freeLn := mustListenLocal(t)
	freePort := listenerPort(t, freeLn)
	require.NoError(t, freeLn.Close())
	assert.NoError(t, checkPortFree(freePort), "空闲端口应通过")

	assert.NoError(t, checkPortFree(0), "未配置端口不检查")
}

// TestManager_PreflightStart_IncludesFR471Checks FR-471：PreflightStart 在既有三项后返回
// work_dir_busy 与 port_free 两项（docker 仍整体放行）。
func TestManager_PreflightStart_IncludesFR471Checks(t *testing.T) {
	origList, origPort := preflightListProcesses, preflightPortFree
	defer func() { preflightListProcesses, preflightPortFree = origList, origPort }()
	withJavaProbe(t, func(string) (int, bool) { return 21, true })

	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.jar"), []byte("x"), 0o644))
	m := NewManager(t.TempDir())
	uuid := "preflight-fr471"
	require.NoError(t, m.Create(uuid, "实例", "java -jar server.jar nogui", "stop", workDir, nil, false, ProcessTypeDirect, "/opt/jdk21", "", 0, 0))
	m.SetServerPort(uuid, 25599)

	preflightListProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	preflightPortFree = func(port int) error { return nil }
	checks, err := m.PreflightStart(uuid)
	require.NoError(t, err)
	names := make([]string, 0, len(checks))
	for _, c := range checks {
		names = append(names, c.Name)
		assert.True(t, c.OK, "预检项 %s 应通过：%s", c.Name, c.Message)
	}
	assert.Equal(t, []string{"java_runtime", "work_dir", "launch_target", "work_dir_busy", "port_free"}, names)

	// 两项失败时如实上报（含 PID / 端口号）。
	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 4321, Cmdline: "java -jar server.jar", Cwd: workDir}}, nil
	}
	preflightPortFree = func(port int) error {
		if port == 25599 {
			return fmt.Errorf("端口 %d 已被监听", port)
		}
		return nil
	}
	checks, err = m.PreflightStart(uuid)
	require.NoError(t, err)
	failed := map[string]string{}
	for _, c := range checks {
		if !c.OK {
			failed[c.Name] = c.Message
		}
	}
	assert.Contains(t, failed["work_dir_busy"], "4321")
	assert.Contains(t, failed["port_free"], "25599")
}

// TestCheckPortFree_RealListener 直测真实端口探测实现（不经替换桩）。
func TestCheckPortFree_RealListener(t *testing.T) {
	ln := mustListenLocal(t)
	defer ln.Close()
	port := listenerPort(t, ln)
	assert.Error(t, checkPortFree(port))
}
