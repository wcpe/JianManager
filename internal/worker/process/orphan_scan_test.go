package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scanAudit struct {
	action   string
	targetID string
	detail   string
	success  bool
}

// newTestScanner 构造带注入桩的扫描器：pidAlive/进程枚举/容器枚举/杀树/复核全部可控。
func newTestScanner(t *testing.T, dir string, policy OrphanDisposePolicy) (*OrphanScanner, *Manager, *[]scanAudit) {
	t.Helper()
	m := NewManager(dir)
	audits := &[]scanAudit{}
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		*audits = append(*audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
	s := NewOrphanScanner(m, time.Minute, policy)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, nil }
	s.removeContainer = func(context.Context, string) error { return nil }
	return s, m, audits
}

// TestOrphanScan_WrapperGoneJavaAlive 覆盖三态之①：wrapper 死/Java 活。
func TestOrphanScan_WrapperGoneJavaAlive(t *testing.T) {
	t.Run("warn 档只告警不处置", func(t *testing.T) {
		dir := t.TempDir()
		uuid := "scan-wrapper-gone"
		pidPath := writeOrphanPIDRecord(t, dir, uuid)

		s, _, audits := newTestScanner(t, dir, OrphanPolicyWarn)
		// wrapper 死，Java 活。
		s.pidAlive = func(pid int) bool { return pid == testJavaPID }

		findings := s.ScanOnce()
		require.Len(t, findings, 1)
		assert.Equal(t, OrphanKindWrapperGoneJavaAlive, findings[0].Kind)
		assert.Equal(t, uuid, findings[0].InstanceUUID)
		assert.False(t, findings[0].Disposed, "warn 档不得处置")
		assert.FileExists(t, pidPath, "warn 档应保留 PID 文件")
		require.Len(t, *audits, 1)
		assert.Equal(t, "orphan.scan_detected", (*audits)[0].action)
	})

	t.Run("auto 档强杀 Java 并清理", func(t *testing.T) {
		dir := t.TempDir()
		uuid := "scan-wrapper-gone-auto"
		pidPath := writeOrphanPIDRecord(t, dir, uuid)

		s, m, audits := newTestScanner(t, dir, OrphanPolicyAuto)
		dead := map[int]bool{testWrapperPID: true}
		var killed []int
		s.pidAlive = func(pid int) bool { return !dead[pid] }
		m.recoverPIDAlive = s.pidAlive
		m.recoverSleep = func(time.Duration) {}
		m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
		m.recoverKillTree = func(pid int) error { killed = append(killed, pid); dead[pid] = true; return nil }

		findings := s.ScanOnce()
		require.Len(t, findings, 1)
		assert.Equal(t, []int{testJavaPID}, killed, "只杀 Java 孤儿")
		assert.True(t, findings[0].Disposed)
		assert.NoFileExists(t, pidPath, "死透后应清理 PID 文件")
		// 一轮里 reapOrphanWrapper 自身会落 dispose_reaped 审计。
		require.NotEmpty(t, *audits)
	})
}

// TestOrphanScan_DirectOrphan 覆盖三态之②：direct 孤儿（内存表无该实例）。
func TestOrphanScan_DirectOrphan(t *testing.T) {
	serversDir := t.TempDir()
	orphanDir := filepath.Join(serversDir, "survival-abc123")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyAuto)
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 9001, Cmdline: "java -jar server.jar nogui", Cwd: orphanDir},
			{PID: 9002, Cmdline: "ps aux", Cwd: "/tmp"}, // 无关进程
		}, nil
	}
	var killed []int
	// FR-456 F1：direct 孤儿处置前新增归属复核；夹具 PID 非真实进程，注入复核桩恒通过。
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }

	findings := s.ScanOnce()
	require.Len(t, findings, 1, "仅受管服务器根下的无主进程被识别")
	assert.Equal(t, OrphanKindDirect, findings[0].Kind)
	assert.Equal(t, orphanDir, findings[0].WorkDir)
	assert.Equal(t, []int{9001}, findings[0].PIDs)
	assert.Equal(t, []int{9001}, killed)
	assert.True(t, findings[0].Disposed)
	require.Len(t, *audits, 1)
	assert.Equal(t, "orphan.scan_disposed", (*audits)[0].action)
}

// TestOrphanScan_DirectOrphan_OwnershipBlocked FR-456 F1：auto 档归属复核不通过 → 只告警不杀。
func TestOrphanScan_DirectOrphan_OwnershipBlocked(t *testing.T) {
	serversDir := t.TempDir()
	orphanDir := filepath.Join(serversDir, "survival-abc123")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyAuto)
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 9001, Cmdline: "java -jar server.jar nogui", Cwd: orphanDir},
		}, nil
	}
	var killed []int
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return false } // 复核不通过
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }

	findings := s.ScanOnce()
	require.Len(t, findings, 1)
	assert.Empty(t, killed, "归属复核不通过时不得强杀")
	assert.False(t, findings[0].Disposed, "未处置时 Disposed 应为 false")
	require.Len(t, *audits, 1)
	assert.Equal(t, "orphan.scan_dispose_blocked", (*audits)[0].action)
	assert.False(t, (*audits)[0].success, "被拦截的处置审计 success 应为 false")
}

// TestOrphanScan_DirectOrphan_StoppingInstanceSkipped FR-456 F2：优雅停止期（STOPPING）实例
// 的 Java 不得被判为 direct 孤儿（否则 auto 档强杀会绕过优雅关服）。
func TestOrphanScan_DirectOrphan_StoppingInstanceSkipped(t *testing.T) {
	serversDir := t.TempDir()
	managedDir := filepath.Join(serversDir, "stopping-xyz")
	require.NoError(t, os.MkdirAll(managedDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyAuto)
	require.NoError(t, m.Create("stopping-uuid", "s", "java -jar s.jar", "stop", managedDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	m.mu.Lock()
	inst := m.instances["stopping-uuid"]
	inst.State = StateStopping // 优雅停止中
	inst.strategy = &fakePIDStrategy{pid: 8001}
	m.mu.Unlock()

	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 8001, Cmdline: "java -jar s.jar", Cwd: managedDir}}, nil
	}
	var killed []int
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }

	findings := s.ScanOnce()
	assert.Empty(t, findings, "STOPPING 实例的进程属受管，不得判为 direct 孤儿")
	assert.Empty(t, killed)
	assert.Empty(t, *audits)
}

// TestOrphanScan_DirectOrphan_StoppedInstanceDirStillScanned FR-456 N1 回归负例：
// STOPPED 实例的工作目录**不**受保护——Worker 硬崩重启后 CP 会把 direct 实例按 STOPPED + WorkDir
// 重登记，此时目录下残留的 Java（真实孤儿，占 Paper session.lock）仍必须被识别为 direct 孤儿，
// 否则兜底扫描对这个目标场景完全失效。
func TestOrphanScan_DirectOrphan_StoppedInstanceDirStillScanned(t *testing.T) {
	serversDir := t.TempDir()
	managedDir := filepath.Join(serversDir, "stopped-xyz")
	require.NoError(t, os.MkdirAll(managedDir, 0o755))

	s, m, audits := newTestScanner(t, serversDir, OrphanPolicyAuto)
	// 重登记后的 STOPPED direct 实例：有 WorkDir、无 strategy（无活进程）。
	require.NoError(t, m.Create("stopped-uuid", "s", "java -jar s.jar", "stop", managedDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	m.mu.Lock()
	require.Equal(t, StateStopped, m.instances["stopped-uuid"].State)
	m.mu.Unlock()

	// 该目录下仍有残留 Java（cwd 即该实例的 WorkDir）——必须仍被识别。
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 9101, Cmdline: "java -jar s.jar nogui", Cwd: managedDir}}, nil
	}
	var killed []int
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }

	findings := s.ScanOnce()
	// FR-471：该目录同时属「已注册 STOPPED 实例目录下存在活进程」，第 4 相会另落一条
	// foreign_runtime 观测 finding（只观测不杀）。此处只校验既有 FR-456 direct 判决未回归。
	var direct *OrphanFinding
	for i := range findings {
		if findings[i].Kind == OrphanKindDirect {
			direct = &findings[i]
		}
	}
	require.NotNil(t, direct, "STOPPED 实例目录下的残留进程仍应被识别为 direct 孤儿")
	assert.Equal(t, managedDir, direct.WorkDir)
	assert.Equal(t, []int{9101}, killed)
	require.NotEmpty(t, *audits)
	// direct 相先于 FR-471 相执行，故处置审计仍是第一条。
	assert.Equal(t, "orphan.scan_disposed", (*audits)[0].action)
}

// TestOrphanScan_DirectOrphan_SkipsManaged 已受管实例的进程不得被判为孤儿。
func TestOrphanScan_DirectOrphan_SkipsManaged(t *testing.T) {
	serversDir := t.TempDir()
	managedDir := filepath.Join(serversDir, "managed-xyz")
	require.NoError(t, os.MkdirAll(managedDir, 0o755))

	s, m, _ := newTestScanner(t, serversDir, OrphanPolicyWarn)
	// 登记一个 RUNNING 实例，根 PID=7001，工作目录=managedDir。
	require.NoError(t, m.Create("managed-uuid", "m", "java -jar s.jar", "stop", managedDir, nil, false, ProcessTypeDirect, "", "", 0, 0))
	m.mu.Lock()
	inst := m.instances["managed-uuid"]
	inst.State = StateRunning
	inst.strategy = &fakePIDStrategy{pid: 7001}
	m.mu.Unlock()

	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 7001, Cmdline: "java -jar s.jar", Cwd: managedDir}, // 受管 PID
			{PID: 7002, Cmdline: "java -jar s.jar", Cwd: managedDir}, // 同目录但非受管 PID → 视为孤儿? (同工作目录有在跑实例 → 跳过)
		}, nil
	}
	findings := s.ScanOnce()
	assert.Empty(t, findings, "受管 PID 与受管工作目录下的进程都不应判为孤儿")
}

// TestOrphanScan_DockerLeftover 覆盖三态之③：docker 残留。
func TestOrphanScan_DockerLeftover(t *testing.T) {
	s, _, audits := newTestScanner(t, t.TempDir(), OrphanPolicyWarn)
	s.listContainers = func(context.Context) ([]ManagedContainer, error) {
		return []ManagedContainer{
			{UUID: "leftover-uuid", Name: "jianmanager-leftover-uuid", Running: true},
			{Name: "unrelated", Running: true}, // 非本平台容器
		}, nil
	}
	findings := s.ScanOnce()
	require.Len(t, findings, 1)
	assert.Equal(t, OrphanKindDockerLeftover, findings[0].Kind)
	assert.Equal(t, "leftover-uuid", findings[0].InstanceUUID)
	assert.Equal(t, "jianmanager-leftover-uuid", findings[0].ContainerName)
	assert.False(t, findings[0].Disposed)
	require.Len(t, *audits, 1)
	assert.Equal(t, "orphan.scan_detected", (*audits)[0].action)
}

// TestOrphanScan_DockerLeftoverAuto FR-456 F1/F13：auto 档只删「已退出 + 内存表无该实例 + 平台容器」；
// running 容器与内存表在册实例的容器保守不删。
func TestOrphanScan_DockerLeftoverAuto(t *testing.T) {
	s, m, audits := newTestScanner(t, t.TempDir(), OrphanPolicyAuto)
	// 内存表登记一个实例（其容器即便已退出也不得被扫描删除）。
	require.NoError(t, m.Create("registered-uuid", "r", "java -jar s.jar", "stop", t.TempDir(), nil, false, ProcessTypeDaemon, "", "", 0, 0))

	s.listContainers = func(context.Context) ([]ManagedContainer, error) {
		return []ManagedContainer{
			{UUID: "exited-uuid", Name: "jianmanager-exited-uuid", Running: false},         // 应删
			{UUID: "running-uuid", Name: "jianmanager-running-uuid", Running: true},        // running → 不删
			{UUID: "registered-uuid", Name: "jianmanager-registered-uuid", Running: false}, // 在册 → 不删
		}, nil
	}
	var removed []string
	s.removeContainer = func(_ context.Context, name string) error {
		removed = append(removed, name)
		return nil
	}

	findings := s.ScanOnce()
	require.Len(t, findings, 3)
	assert.Equal(t, []string{"jianmanager-exited-uuid"}, removed, "仅删已退出且无主且平台归属的容器")

	var disposed, blocked int
	for _, a := range *audits {
		switch a.action {
		case "orphan.scan_disposed":
			disposed++
			assert.True(t, a.success, "已处置审计应为成功")
		case "orphan.scan_dispose_blocked":
			blocked++
			assert.False(t, a.success, "保守不删审计应为未处置")
		}
	}
	assert.Equal(t, 1, disposed, "1 个容器实际删除")
	assert.Equal(t, 2, blocked, "running 与在册实例的容器各落一条保守审计")
}

// TestOrphanScan_DockerLeftoverOwnership FR-456 N5：docker 归属复核不再是恒真的伪复核。
//   - 带受管标签且标签实例与容器名一致 → 强归属（managed_label），可删；
//   - 带受管标签但标签实例与容器名矛盾 → 归属不通过（label_mismatch），只告警不删；
//   - 无标签（本 FR 之前的存量容器）→ 退化为容器名格式校验（name_format_only），仍可删但证据如实入审计。
func TestOrphanScan_DockerLeftoverOwnership(t *testing.T) {
	s, _, audits := newTestScanner(t, t.TempDir(), OrphanPolicyAuto)
	s.listContainers = func(context.Context) ([]ManagedContainer, error) {
		return []ManagedContainer{
			{UUID: "labeled-ok", Name: "jianmanager-labeled-ok", Running: false, ManagedLabel: true, LabelInstanceUUID: "labeled-ok"},
			{UUID: "labeled-bad", Name: "jianmanager-labeled-bad", Running: false, ManagedLabel: true, LabelInstanceUUID: "someone-else"},
			{UUID: "legacy", Name: "jianmanager-legacy", Running: false},
		}, nil
	}
	var removed []string
	s.removeContainer = func(_ context.Context, name string) error {
		removed = append(removed, name)
		return nil
	}

	findings := s.ScanOnce()
	require.Len(t, findings, 3)
	assert.ElementsMatch(t, []string{"jianmanager-labeled-ok", "jianmanager-legacy"}, removed,
		"强归属与仅名字格式匹配者删除；标签与容器名矛盾者拒绝")

	joined := ""
	for _, a := range *audits {
		joined += a.detail + "\n"
	}
	assert.Contains(t, joined, "managed_label", "强归属档位应入审计")
	assert.Contains(t, joined, "name_format_only", "存量无标签容器的降级证据应如实入审计")
	assert.Contains(t, joined, "label_mismatch", "标签矛盾档位应入审计")
}

// TestVerifyContainerOwnership 归属复核判据的直测（FR-456 N5）。
func TestVerifyContainerOwnership(t *testing.T) {
	// 名字格式不符：无论标签如何都拒绝。
	ev, ok := verifyContainerOwnership("some-other-container", true, "x")
	assert.False(t, ok)
	assert.Equal(t, "name_format_invalid", ev)

	// 带受管标签且标签实例与容器名一致 → 强归属。
	ev, ok = verifyContainerOwnership("jianmanager-abc", true, "abc")
	assert.True(t, ok)
	assert.Equal(t, "managed_label", ev)

	// 带受管标签但标签缺实例 UUID：无从矛盾，仍算强归属。
	ev, ok = verifyContainerOwnership("jianmanager-abc", true, "")
	assert.True(t, ok)
	assert.Equal(t, "managed_label", ev)

	// 标签实例与容器名矛盾 → 拒绝（真判据，可失败）。
	ev, ok = verifyContainerOwnership("jianmanager-abc", true, "xyz")
	assert.False(t, ok)
	assert.Equal(t, "label_mismatch", ev)

	// 无标签（存量容器）→ 仅名字格式校验，放行但证据标注降级。
	ev, ok = verifyContainerOwnership("jianmanager-abc", false, "")
	assert.True(t, ok)
	assert.Equal(t, "name_format_only", ev)

	// 容忍 docker 展示名的前导斜杠。
	ev, ok = verifyContainerOwnership("/jianmanager-abc", false, "")
	assert.True(t, ok)
	assert.Equal(t, "name_format_only", ev)
}

// TestOrphanScan_ListFailureDegrades 进程/容器枚举失败仅告警降级，不 panic、不误处置。
func TestOrphanScan_ListFailureDegrades(t *testing.T) {
	s, _, _ := newTestScanner(t, t.TempDir(), OrphanPolicyAuto)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, errors.New("permission denied") }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, errors.New("docker down") }
	findings := s.ScanOnce()
	assert.Empty(t, findings)
}

// TestContainerUUID 容器名前缀解析。
func TestContainerUUID(t *testing.T) {
	u, ok := containerUUID("jianmanager-abc")
	assert.True(t, ok)
	assert.Equal(t, "abc", u)
	_, ok = containerUUID("other-abc")
	assert.False(t, ok)
	_, ok = containerUUID("jianmanager-")
	assert.False(t, ok)
}

// TestNormalizeOrphanDisposePolicy 策略归一化：空/未知回退 warn。
func TestNormalizeOrphanDisposePolicy(t *testing.T) {
	assert.Equal(t, OrphanPolicyWarn, NormalizeOrphanDisposePolicy(""))
	assert.Equal(t, OrphanPolicyWarn, NormalizeOrphanDisposePolicy("WARN"))
	assert.Equal(t, OrphanPolicyWarn, NormalizeOrphanDisposePolicy("bogus"))
	assert.Equal(t, OrphanPolicyAuto, NormalizeOrphanDisposePolicy("AUTO"))
	assert.Equal(t, OrphanPolicyAuto, NormalizeOrphanDisposePolicy(" auto "))
}

// fakePIDStrategy 仅实现 IProcessCommand 的 GetPID（其余方法本测试不触达）。
type fakePIDStrategy struct{ pid int }

func (f *fakePIDStrategy) Start(context.Context) error { return nil }
func (f *fakePIDStrategy) Stop() error                 { return nil }
func (f *fakePIDStrategy) Kill() error                 { return nil }
func (f *fakePIDStrategy) SendCommand(string) error    { return nil }
func (f *fakePIDStrategy) State() InstanceState        { return StateRunning }
func (f *fakePIDStrategy) Close() error                { return nil }
func (f *fakePIDStrategy) GetPID() int                 { return f.pid }

var _ IProcessCommand = (*fakePIDStrategy)(nil)
