package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 可用性缺陷回归：**启动接管重试阻塞串行，导致扫描器延迟启用**。
//
// 缺陷形态：旧实现 `RecoverDaemonInstances` 对每条 PID 记录做「初拨 + 就地有界重试」
// （默认 1s→2s→4s→8s→16s→32s→64s ≈127s）。这些重试**串行阻塞**启动主流程，于是一条不可达记录
// 就把运行期孤儿扫描器（FR-456）的启用推迟到 Worker 启动后约 127 秒（N 条僵死记录线性累加），
// 该窗口内完全没有运行期扫描能力（真机证据：T7 的 D 臂扫描器 127s 后才启用）。
//
// 修复后：`RecoverDaemonInstances` 只同步做**快速首拨**，首拨失败的记录转**后台**继续重试，
// 故扫描器在首拨完成后立即可用。以下用例把「扫描器启用」定义为可观测事实——首轮 ScanOnce 真的
// 跑完了（进程枚举被调用）且真的产出了处置动作，而不是只断言某个变量被置位。

// lockedAuditSink 是并发安全的审计收集器：启动接管的后台协程与扫描协程都会写审计，
// 用例必须能安全地在两个协程之间读。
type lockedAuditSink struct {
	mu    sync.Mutex
	items []scanAudit
}

func (l *lockedAuditSink) record(action, targetID, detail string, success bool, errMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
}

// actions 返回审计动作名快照。
func (l *lockedAuditSink) actions() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.items))
	for _, it := range l.items {
		out = append(out, it.action)
	}
	return out
}

// hasDetail 报告是否存在「动作为 action 且 detail 含 needle」的审计。
func (l *lockedAuditSink) hasDetail(action, needle string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, it := range l.items {
		if it.action == action && strings.Contains(it.detail, needle) {
			return true
		}
	}
	return false
}

// newScannerOver 在**指定** Manager 上构造扫描器并注入全部替身桩。
//
// 必须复用同一个 Manager：本缺陷的并发隔离（takeoverPending / takeoverProtects）是 Manager 级状态，
// 换一个 Manager 就测不到保护是否生效。
func newScannerOver(m *Manager, policy OrphanDisposePolicy, sink *lockedAuditSink) *OrphanScanner {
	m.onOrphanAudit = sink.record
	s := NewOrphanScanner(m, time.Minute, policy)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, nil }
	s.removeContainer = func(context.Context, string) error { return nil }
	return s
}

// blockingSleepFactory 构造「阻塞式重试等待」：后台接管重试一旦进入 sleep 就停在这里，
// 直到调用 release。这样「接管仍在重试」是可确定控制的，用例不依赖真实时钟。
func blockingSleepFactory() (sleep func(time.Duration), release func(), entered <-chan struct{}) {
	releaseCh := make(chan struct{})
	enteredCh := make(chan struct{}, 1)
	var (
		once    sync.Once
		relOnce sync.Once
	)
	return func(time.Duration) {
			once.Do(func() { close(enteredCh) }) // 首次进入等待即宣告「接管进入后台重试阶段」
			<-releaseCh
		}, func() {
			relOnce.Do(func() { close(releaseCh) })
		}, enteredCh
}

// TestStartupTakeoverRetriesDoNotBlockOrphanScanner 本缺陷的核心验收（可转红）。
//
// 场景：PID 目录里有一条「永不成功」的接管记录（wrapper 与 Java 都活、socket 永远拨不通），
// 后台重试被阻塞在退避等待上。断言：
//  1. `RecoverDaemonInstances` 本身在阈值内返回（旧实现此处要等满整个重试窗口 ≈127s）；
//  2. 扫描器在阈值内启用 —— 首轮 ScanOnce 跑完、进程枚举被调用，并真的处置了一个**无关**的无主进程；
//  3. 该处置不得碰到在飞接管记录的进程（wrapper/java 都不得被强杀）——并发隔离正确；
//  4. 放行后台重试后接管仍完整收尾：落 alive_unreachable 审计、保留 PID 文件、不登记、不补杀。
//
// 转红方式（改回旧行为）：让 `RecoverDaemonInstances` 就地跑完 `reconnectWithRetry` 的重试
// （或在启动路径上等待 `takeoverWG`），第 1、2 条即在阈值处超时；去掉 pending 保护，
// 第 3 条会看到 wrapper/java 被 killTree。
func TestStartupTakeoverRetriesDoNotBlockOrphanScanner(t *testing.T) {
	// 阈值：首轮扫描必须在 5 秒内完成。修复前该值为「整个重试窗口」——默认序列 ≈127s。
	const enableWithin = 5 * time.Second

	serversDir := t.TempDir()
	pendingDir := filepath.Join(serversDir, "daemon-pending")
	require.NoError(t, os.MkdirAll(pendingDir, 0o755))
	orphanDir := filepath.Join(serversDir, "direct-orphan")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))

	const pendingUUID = "takeover-pending"
	pidPath := writeOrphanPIDRecordAt(t, serversDir, pendingUUID, pendingDir)

	m := NewManager(serversDir)
	sink := &lockedAuditSink{}
	var killedMu sync.Mutex
	var killed []int
	recordKill := func(pid int) error {
		killedMu.Lock()
		killed = append(killed, pid)
		killedMu.Unlock()
		return nil
	}
	killedSnapshot := func() []int {
		killedMu.Lock()
		defer killedMu.Unlock()
		return append([]int(nil), killed...)
	}

	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true } // 夹具 PID 非真实进程
	m.recoverKillTree = recordKill
	// wrapper 与 Java 恒存活（本用例只关心「扫描器是否被拖住」与并发隔离，不涉及处置分支）。
	m.recoverPIDAlive = func(pid int) bool { return pid == testWrapperPID || pid == testJavaPID }
	m.recoverDial = func(*daemonStrategy, string) error { return errors.New("socket 永远拨不通") }
	sleep, release, entered := blockingSleepFactory()
	m.recoverSleep = sleep
	defer release()

	// —— 阶段 1：启动接管。旧实现会在这里阻塞 ≈127s ——
	started := time.Now()
	recovered, err := m.RecoverDaemonInstances()
	elapsed := time.Since(started)
	require.NoError(t, err)
	assert.Equal(t, 0, recovered, "拨不通不得当场登记")
	assert.Less(t, elapsed, 2*time.Second,
		"启动接管只应同步花「首拨」的时间；旧实现此处要等满整个重试窗口（默认 ≈127s），实测 %v", elapsed)
	t.Logf("启动接管同步耗时 = %v（旧实现默认序列下 ≈127s，N 条僵死记录线性累加）", elapsed)
	assert.True(t, m.takeoverPendingHas(pendingUUID), "首拨失败应声明为「启动接管在飞」")
	assert.FileExists(t, pidPath, "重试期间必须保留 PID 文件")

	// 后台重试确实已进入退避等待（而非已经收敛）：否则下面的「扫描器启用」测不出并发。
	select {
	case <-entered:
	case <-time.After(enableWithin):
		t.Fatal("后台接管重试未在阈值内进入退避等待")
	}

	// —— 阶段 2：扫描器启用。这是缺陷的可观测判据 ——
	s := newScannerOver(m, OrphanPolicyAuto, sink)
	s.pidAlive = m.recoverPIDAlive
	var listCalls atomic.Int64
	s.listProcesses = func() ([]ScannedProcess, error) {
		listCalls.Add(1)
		return []ScannedProcess{
			// 在飞接管记录的 wrapper 与 Java：不得被当作无主进程处置。
			{PID: testWrapperPID, Cmdline: "worker daemon " + pendingDir, Cwd: pendingDir},
			{PID: testJavaPID, Cmdline: "java -jar server.jar nogui", Cwd: pendingDir},
			// 无关的真无主进程：扫描器必须照常处置它，否则「启用」只是空转。
			{PID: 9101, Cmdline: "java -jar orphan.jar nogui", Cwd: orphanDir},
		}, nil
	}

	scanCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(scanCtx)

	// 轮询等待首轮扫描跑完（Start 立即跑一轮）。
	deadline := time.Now().Add(enableWithin)
	for time.Now().Before(deadline) && listCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	enabledAfter := time.Since(started)
	require.Greater(t, listCalls.Load(), int64(0), "扫描器应在 %v 内启用（首轮已枚举进程）", enableWithin)
	assert.Less(t, enabledAfter, enableWithin,
		"扫描器必须在 %v 内启用；旧实现要等接管重试全部跑完（默认 ≈127s），实测 %v", enableWithin, enabledAfter)
	t.Logf("扫描器启用延迟（自启动接管开始计）= %v，阈值 %v", enabledAfter, enableWithin)

	// 等到无关无主进程被处置，证明扫描器不是空转。
	deadline = time.Now().Add(enableWithin)
	for time.Now().Before(deadline) && len(killedSnapshot()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, []int{9101}, killedSnapshot(),
		"扫描器应处置无关无主进程，且绝不强杀在飞接管记录的 wrapper/java")
	assert.Contains(t, sink.actions(), "orphan.scan_disposed")

	// —— 阶段 3：接管仍须完成（语义不丢）：放行后台重试，等它收敛 ——
	release()
	require.True(t, m.WaitStartupTakeover(30*time.Second), "后台接管重试应收敛")

	assert.Contains(t, sink.actions(), "orphan.dispose_blocked", "重试耗尽须落审计（语义不变）")
	assert.True(t, sink.hasDetail("orphan.dispose_blocked", "alive_unreachable"),
		"审计 detail 应标明 alive_unreachable")
	assert.FileExists(t, pidPath, "只告警不杀：PID 文件必须保留")
	assert.False(t, m.takeoverPendingHas(pendingUUID), "接管收敛后应撤销在飞声明")
	_, stErr := m.GetState(pendingUUID)
	assert.Error(t, stErr, "拨不通不应登记实例")
	assert.Equal(t, []int{9101}, killedSnapshot(), "接管收尾不得引发任何补杀")
}

// TestStartupTakeoverPendingAdoptionStillWorksAfterRetries 修复不得让「自动收养」永久失能：
// 接管重试耗尽后，扫描器下一轮仍应收养该实例（FR-497③ 的既有出口）。
func TestStartupTakeoverPendingAdoptionStillWorksAfterRetries(t *testing.T) {
	serversDir := t.TempDir()
	const uuid = "pending-then-adopt"
	pidPath := writeOrphanPIDRecordAt(t, serversDir, uuid, serversDir)

	m := NewManager(serversDir)
	sink := &lockedAuditSink{}
	m.recoverPIDAlive = func(pid int) bool { return pid == testWrapperPID || pid == testJavaPID }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	// 收养走的是收紧后的强特征复核（verifyAdoptOwnership），夹具 PID 非真实进程故注入桩。
	m.adoptVerifyOwner = func(int, string, string, bool) (bool, string) { return true, "test_stub" }
	m.recoverMigrate = func(int, string) (int, error) { return 0, nil } // 假 cgroup 迁移
	// 接管窗口内 socket 不可达；收敛后恢复可达（模拟「瞬时不可达」）。
	var gate atomic.Bool
	m.recoverDial = func(*daemonStrategy, string) error {
		if gate.Load() {
			return nil
		}
		return errors.New("socket 暂时拨不通")
	}
	m.SetRecoverRetryBackoff([]time.Duration{time.Millisecond})
	m.recoverSleep = func(time.Duration) {}

	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 0, recovered)
	require.True(t, m.WaitStartupTakeover(10*time.Second), "后台接管重试应收敛")
	assert.FileExists(t, pidPath, "重试耗尽后保留 PID 文件")
	assert.False(t, m.takeoverPendingHas(uuid), "收敛后不再让路")

	// 接管已放弃，但 socket 现已可达 → 扫描器第 0 相把它收养为 RUNNING（语义出口不丢）。
	gate.Store(true)
	s := newScannerOver(m, OrphanPolicyWarn, sink)
	s.pidAlive = m.recoverPIDAlive
	s.SetAutoAdopt(true)
	findings := s.ScanOnce()
	require.NotEmpty(t, findings)
	assert.True(t, findings[0].Adopted, "接管放弃后应由自动收养补上")
	assert.Contains(t, sink.actions(), "orphan.auto_adopted")
	state, stErr := m.GetState(uuid)
	require.NoError(t, stErr)
	assert.Equal(t, StateRunning, state)
}

// TestStartupTakeoverPendingAutoAdoptDisabledRespected `auto_adopt: false` 时，即使接管放弃
// 也不得收养（开关语义不变）。
func TestStartupTakeoverPendingAutoAdoptDisabledRespected(t *testing.T) {
	serversDir := t.TempDir()
	const uuid = "pending-no-adopt"
	pidPath := writeOrphanPIDRecordAt(t, serversDir, uuid, serversDir)

	m := NewManager(serversDir)
	sink := &lockedAuditSink{}
	m.recoverPIDAlive = func(pid int) bool { return pid == testWrapperPID || pid == testJavaPID }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	var gate atomic.Bool
	m.recoverDial = func(*daemonStrategy, string) error {
		if gate.Load() {
			return nil // socket 已恢复：若收养逻辑没被开关关掉，这一轮就会收养
		}
		return errors.New("socket 暂时拨不通")
	}
	m.SetRecoverRetryBackoff([]time.Duration{time.Millisecond})
	m.recoverSleep = func(time.Duration) {}

	_, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	require.True(t, m.WaitStartupTakeover(10*time.Second))
	gate.Store(true)

	s := newScannerOver(m, OrphanPolicyWarn, sink)
	s.pidAlive = m.recoverPIDAlive
	s.SetAutoAdopt(false) // 应急逃生口：只告警不收养
	for i := 0; i < 3; i++ {
		s.ScanOnce()
	}

	assert.NotContains(t, sink.actions(), "orphan.auto_adopted", "关闭收养时不得收养")
	_, stErr := m.GetState(uuid)
	assert.Error(t, stErr, "关闭收养时不应登记实例")
	assert.FileExists(t, pidPath, "收养关掉后 PID 文件保留，等人工/接管流程")
}

// TestStartupTakeoverProtectionSparesOwnershipUnverified 安全语义不变：归属复核不通过时
// 扫描器仍不得强杀。此处复核桩恒 false（模拟 PID 已被复用给无关进程）。
func TestStartupTakeoverProtectionSparesOwnershipUnverified(t *testing.T) {
	serversDir := t.TempDir()
	orphanDir := filepath.Join(serversDir, "verify-blocked")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))

	m := NewManager(serversDir)
	sink := &lockedAuditSink{}
	var killed []int
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return false } // 复核不通过
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }

	s := newScannerOver(m, OrphanPolicyAuto, sink)
	s.listProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 9201, Cmdline: "java -jar x.jar", Cwd: orphanDir}}, nil
	}

	s.ScanOnce()
	assert.Empty(t, killed, "归属复核不通过时不得强杀（安全校验未放宽）")
	assert.Contains(t, sink.actions(), "orphan.scan_dispose_blocked")
}

// TestTakeoverPendingGuards 保护集的单元级行为：PID / 工作目录双口径命中，撤销后立即失效。
func TestTakeoverPendingGuards(t *testing.T) {
	serversDir := t.TempDir()
	m := NewManager(serversDir)
	rec := &daemon.PIDRecord{
		WrapperPID:   7001,
		JavaPID:      7002,
		WorkDir:      filepath.Join(serversDir, "inst"),
		InstanceUUID: "guard-uuid",
	}
	assert.False(t, m.takeoverPendingHas("guard-uuid"), "初始不在飞")
	assert.False(t, m.takeoverProtects(7001, ""), "初始不保护任何 PID")

	m.markTakeoverPending("guard-uuid", rec)
	assert.True(t, m.takeoverPendingHas("guard-uuid"))
	assert.True(t, m.takeoverProtects(7001, ""), "wrapper PID 应受保护")
	assert.True(t, m.takeoverProtects(7002, ""), "Java PID 应受保护")
	assert.True(t, m.takeoverProtects(0, rec.WorkDir), "工作目录应受保护")
	assert.True(t, m.takeoverProtects(0, rec.WorkDir+string(os.PathSeparator)), "工作目录清洁后同样命中")
	assert.False(t, m.takeoverProtects(9999, filepath.Join(serversDir, "other")), "无关 PID/目录不受保护")

	// direct 相的受管集合也必须把在飞记录算作受管。
	pids, workDirs := m.managedInstanceRuntime()
	_, hasWrapper := pids[7001]
	_, hasJava := pids[7002]
	_, hasDir := workDirs[filepath.Clean(rec.WorkDir)]
	assert.True(t, hasWrapper && hasJava && hasDir, "在飞接管记录应计入受管 PID 与工作目录")

	m.clearTakeoverPending("guard-uuid")
	assert.False(t, m.takeoverPendingHas("guard-uuid"), "撤销后不再在飞")
	assert.False(t, m.takeoverProtects(7001, rec.WorkDir), "撤销后不再保护")
}

// writeOrphanPIDRecordAt 在 pidDir 写一份 PID 记录，工作目录另指 workDir
// （模拟真实布局：PID 文件集中存放，实例工作目录在 serversDir 下的实例目录）。
func writeOrphanPIDRecordAt(t *testing.T, pidDir, uuid, workDir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(pidDir, 0o755))
	pidPath := filepath.Join(pidDir, uuid+".pid")
	require.NoError(t, daemon.NewPIDFile(pidPath).WriteRecord(daemon.PIDRecord{
		WrapperPID:   testWrapperPID,
		JavaPID:      testJavaPID,
		SocketAddr:   filepath.Join(pidDir, uuid+".sock"),
		InstanceUUID: uuid,
		WorkDir:      workDir,
	}))
	return pidPath
}
