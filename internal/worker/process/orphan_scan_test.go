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
	success  bool
}

// newTestScanner 构造带注入桩的扫描器：pidAlive/进程枚举/容器枚举/杀树/复核全部可控。
func newTestScanner(t *testing.T, dir string, policy OrphanDisposePolicy) (*OrphanScanner, *Manager, *[]scanAudit) {
	t.Helper()
	m := NewManager(dir)
	audits := &[]scanAudit{}
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		*audits = append(*audits, scanAudit{action: action, targetID: targetID, success: success})
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
