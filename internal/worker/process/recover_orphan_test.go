package process

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-325 兜底路径单测：wrapper 存活但 reconnect 拨号失败时，接管扫描应
// 有界重试（期间保留 PID 文件）→ 重试耗尽按 PID 记录强杀孤儿进程树 → 死透才清理；
// 杀不死则保留 PID 文件待下次扫描。拨号/杀树/存活探测/等待均经 Manager 注入桩替身。

const (
	testWrapperPID = 4242
	testJavaPID    = 4243
)

// writeOrphanPIDRecord 写一份「wrapper 存活但拨不通」的 PID 记录夹具，返回 PID 文件路径。
func writeOrphanPIDRecord(t *testing.T, dir, uuid string) string {
	t.Helper()
	pidPath := filepath.Join(dir, uuid+".pid")
	require.NoError(t, daemon.NewPIDFile(pidPath).WriteRecord(daemon.PIDRecord{
		WrapperPID:   testWrapperPID,
		JavaPID:      testJavaPID,
		SocketAddr:   filepath.Join(dir, uuid+".sock"),
		InstanceUUID: uuid,
		WorkDir:      dir,
	}))
	return pidPath
}

// FR-325/FR-455 启动期接管扫描的行为，经 FR-471 修订为**非破坏**：
//
//   - wrapper 存活但 reconnect 拨不通 → 有界重试（期间保留 PID 文件）；耗尽后按 ADR-093 只告警不杀；
//   - wrapper 已死、Java 仍活 → **不再强杀**（真机事故 2026-09-23：一条陈旧 PID 记录一次打断 54 台在跑农场）；
//     改为只观测 + 保留 PID 文件，落审计 `orphan.startup_detected_not_reaped`，
//     交由周期孤儿扫描（默认 warn）与 FR-471 漂移/接管流程处置。
//
// 本用例集据此只断言「重试次数、PID 文件保留、是否登记、审计」——**断言不再出现杀树**。
func TestRecoverDaemonInstances_ReconnectFailureFallback(t *testing.T) {
	tests := []struct {
		name string
		// succeedOnDial 第 N 次拨号成功；0 = 永不成功（触发兜底）。
		succeedOnDial int
		// wrapperDiesDuringRetry 模拟「进入 reconnect 时 wrapper 存活、重试期间才死亡」。
		wrapperDiesDuringRetry bool
		wantRecovered          int
		wantDials              int
		wantPIDFile            bool
		wantRegistered         bool
		wantBlocked            bool
		wantStartupObserve     bool
	}{
		{
			name:           "reconnect 失败→重试→成功恢复",
			succeedOnDial:  3,
			wantRecovered:  1,
			wantDials:      3,
			wantPIDFile:    true,
			wantRegistered: true,
		},
		{
			name:           "重试耗尽但 wrapper 仍存活→按 ADR-093 只告警不杀",
			succeedOnDial:  0,
			wantRecovered:  0,
			wantDials:      1 + len(DefaultRecoverRetryBackoff),
			wantPIDFile:    true,
			wantRegistered: false,
			wantBlocked:    true,
		},
		{
			name:                   "重试期间 wrapper 已死→只观测不强杀（FR-471 非破坏）",
			succeedOnDial:          0,
			wrapperDiesDuringRetry: true,
			wantRecovered:          0,
			wantDials:              1 + len(DefaultRecoverRetryBackoff),
			wantPIDFile:            true,
			wantRegistered:         false,
			wantStartupObserve:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			uuid := "orphan-fallback"
			pidPath := writeOrphanPIDRecord(t, dir, uuid)

			m := NewManager(dir)
			dead := map[int]bool{}
			var killed []int
			var sleeps []time.Duration
			var audits []string
			dials := 0

			// FR-455①：夹具 PID 非真实进程，注入复核桩恒通过（本用例关注重试/处置流程）。
			m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
			// wrapper：入口存活；若 wrapperDiesDuringRetry，则在开始重试（dials>0）后视为死亡。
			m.recoverPIDAlive = func(pid int) bool {
				if pid == testWrapperPID {
					if tt.wrapperDiesDuringRetry {
						return dials == 0
					}
					return true
				}
				return !dead[pid]
			}
			m.recoverSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
			m.recoverKillTree = func(pid int) error {
				killed = append(killed, pid)
				dead[pid] = true
				return nil
			}
			m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
				audits = append(audits, action)
			}
			m.recoverDial = func(_ *daemonStrategy, _ string) error {
				dials++
				// 重试期间 PID 文件必须保留（下轮扫描仍可发现该实例）
				_, statErr := os.Stat(pidPath)
				assert.NoError(t, statErr, "重试期间 PID 文件不得被删除")
				if tt.succeedOnDial > 0 && dials >= tt.succeedOnDial {
					return nil
				}
				return errors.New("dial refused")
			}

			recovered, err := m.RecoverDaemonInstances()
			require.NoError(t, err)
			assert.Equal(t, tt.wantRecovered, recovered)
			assert.Equal(t, tt.wantDials, dials, "拨号次数 = 初拨 + 有界重试")
			assert.Empty(t, killed, "FR-471：启动恢复路径任何分支都不得强杀")
			if tt.wantBlocked {
				assert.Contains(t, audits, "orphan.dispose_blocked",
					"wrapper 仍存活（瞬时不可达）时应落 dispose_blocked 审计")
			}
			if tt.wantStartupObserve {
				assert.Contains(t, audits, "orphan.startup_detected_not_reaped",
					"wrapper 已死时只观测并落 non-reap 审计")
			}

			// 重试间隔递增：失败几次就应等待 DefaultRecoverRetryBackoff 的对应前缀
			retrySleeps := len(DefaultRecoverRetryBackoff)
			if tt.succeedOnDial > 0 {
				retrySleeps = tt.succeedOnDial - 1
			}
			require.GreaterOrEqual(t, len(sleeps), retrySleeps)
			assert.Equal(t, DefaultRecoverRetryBackoff[:retrySleeps], sleeps[:retrySleeps], "重试间隔应按递增序列")

			// FR-471：启动路径不删 PID 文件（交由周期扫描/接管），故恒保留。
			assert.FileExists(t, pidPath, "启动恢复路径应保留 PID 文件")

			st, stErr := m.GetState(uuid)
			if tt.wantRegistered {
				require.NoError(t, stErr)
				assert.Equal(t, StateRunning, st, "重试成功应登记为 RUNNING")
			} else {
				assert.Error(t, stErr, "兜底处置后不应登记实例")
			}
		})
	}
}

// TestRecoverDaemonInstances_StartupNeverKills FR-471 核心不变量：**启动恢复路径任何分支都不强杀**。
//
// 真机事故（2026-09-23）：换二进制重启 Worker 时，一条陈旧 PID 记录触发对在跑农场的强杀（一次 54 台）。
// 因此本用例穷举启动路径的各种「wrapper 已死」形态，断言杀树桩**永不被调用**、PID 文件恒保留。
//
// 强杀能力仍存在于**显式 auto 策略**的周期孤儿扫描（见 orphan_scan_test 的「auto 档强杀 Java 并清理」），
// 由运维显式开启；启动路径不再有任何静默破坏。
func TestRecoverDaemonInstances_StartupNeverKills(t *testing.T) {
	t.Run("wrapper 已死 / Java 活 / 复核通过", func(t *testing.T) {
		dir := t.TempDir()
		uuid := "startup-nokill-1"
		pidPath := writeOrphanPIDRecord(t, dir, uuid)

		m := NewManager(dir)
		var killed []int
		m.recoverSleep = func(time.Duration) {}
		m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
		m.recoverPIDAlive = func(pid int) bool { return pid != testWrapperPID }
		m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }
		m.recoverDial = func(_ *daemonStrategy, _ string) error { return errors.New("dial refused") }

		recovered, err := m.RecoverDaemonInstances()
		require.NoError(t, err)
		assert.Equal(t, 0, recovered)
		assert.Empty(t, killed, "启动路径不得强杀（FR-471）")
		assert.FileExists(t, pidPath)
	})

	t.Run("wrapper 已死 / Java 活 / 复核不通过（PID 疑被复用）", func(t *testing.T) {
		dir := t.TempDir()
		uuid := "startup-nokill-2"
		pidPath := writeOrphanPIDRecord(t, dir, uuid)

		m := NewManager(dir)
		var killed []int
		var audits []string
		m.recoverSleep = func(time.Duration) {}
		m.recoverVerifyOwner = func(int, string, string, bool) bool { return false }
		m.recoverPIDAlive = func(pid int) bool { return pid != testWrapperPID }
		m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }
		m.recoverDial = func(_ *daemonStrategy, _ string) error { return errors.New("dial refused") }
		m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
			audits = append(audits, action)
		}

		recovered, err := m.RecoverDaemonInstances()
		require.NoError(t, err)
		assert.Equal(t, 0, recovered)
		assert.Empty(t, killed, "启动路径不得强杀（FR-471）")
		assert.FileExists(t, pidPath, "PID 文件应保留供周期扫描/接管处置")
		// 复核不通过这一态在新语义下不再单独分支（启动一律观测），审计只要求「已发现未处置」不静默。
		assert.Contains(t, audits, "orphan.startup_detected_not_reaped")
	})
}

// TestRecoverDaemonInstances_WrapperGoneJavaAlive 覆盖「wrapper 已死、Java 仍活」的场景。
//
// 旧行为（FR-325/FR-455）：按 PID 记录强杀 Java 树，死透才清理 PID 文件。
// **FR-471 修订为非破坏**：真机事故（2026-09-23）证明该强杀会直接打断正在服务的服务器
// （换二进制重启 Worker 时，一条陈旧 PID 记录一次打断 54 台在跑农场）。而它原本要解决的
// 「面板 STOPPED 却占着端口、实例再也起不来」已由 FR-471 的启动冲突预检（防双开）与
// 漂移观测 + 接管入口非破坏地覆盖。
//
// 新行为：**不杀任何进程、保留 PID 文件**、落 `orphan.startup_detected_not_reaped` 审计，
// 交由周期孤儿扫描（按 policy，默认 warn）与接管流程处置。
func TestRecoverDaemonInstances_WrapperGoneJavaAlive(t *testing.T) {
	dir := t.TempDir()
	uuid := "orphan-wrapper-gone"
	pidPath := writeOrphanPIDRecord(t, dir, uuid)

	m := NewManager(dir)
	dead := map[int]bool{testWrapperPID: true} // wrapper 已死，Java 仍活
	var killed []int
	var audits []string
	dials := 0

	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
	m.recoverSleep = func(time.Duration) {}
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); dead[pid] = true; return nil }
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		audits = append(audits, action)
	}
	m.recoverDial = func(_ *daemonStrategy, _ string) error { dials++; return nil }

	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 0, recovered)
	assert.Equal(t, 0, dials, "wrapper 已死时不应尝试 reconnect")
	assert.Empty(t, killed, "FR-471：启动期发现 wrapper 已死 / Java 仍活时不得强杀")
	assert.FileExists(t, pidPath, "FR-471：应保留 PID 文件供周期扫描与接管流程使用")
	assert.Contains(t, audits, "orphan.startup_detected_not_reaped",
		"应落「已发现但未处置」审计，不静默")

	_, stErr := m.GetState(uuid)
	assert.Error(t, stErr, "只观测不处置，不应登记实例")
}

// TestRecoverDaemonInstances_WrapperAndJavaBothGone 回归保护：wrapper 与 Java 都已死时，
// 仍走原有的轻量清理路径（不触发杀树），避免遗留 PID 文件。
func TestRecoverDaemonInstances_WrapperAndJavaBothGone(t *testing.T) {
	dir := t.TempDir()
	uuid := "orphan-both-gone"
	pidPath := writeOrphanPIDRecord(t, dir, uuid)

	m := NewManager(dir)
	dead := map[int]bool{testWrapperPID: true, testJavaPID: true}
	var killed []int
	m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
	m.recoverSleep = func(time.Duration) {}
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); return nil }

	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 0, recovered)
	assert.Empty(t, killed, "两者都已死时无需杀树")
	assert.NoFileExists(t, pidPath, "两者都已死时应清理 PID 文件")
}

// TestRecoverDaemonInstances_OwnershipVerifyBlocksKill FR-455① 的误杀拦截在 FR-471 后收敛为
// 「启动路径一律不杀」——本用例断言即便归属复核**通过**（最危险的情形：目录被外部进程占用），
// 启动期也不得强杀，只观测 + 保留 PID 文件 + 落审计（不静默）。
func TestRecoverDaemonInstances_OwnershipVerifyBlocksKill(t *testing.T) {
	dir := t.TempDir()
	uuid := "orphan-verify-block"
	pidPath := writeOrphanPIDRecord(t, dir, uuid)

	m := NewManager(dir)
	dead := map[int]bool{testWrapperPID: true} // wrapper 已死：只剩 Java
	var killed []int
	type auditRec struct {
		action   string
		targetID string
		success  bool
	}
	var audits []auditRec

	m.recoverSleep = func(time.Duration) {}
	m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
	m.recoverKillTree = func(pid int) error { killed = append(killed, pid); dead[pid] = true; return nil }
	m.recoverDial = func(_ *daemonStrategy, _ string) error { return errors.New("dial refused") }
	// 复核**通过**：这正是旧实现在 in-place 导入实例上误杀在跑农场的情形（目录归属相符）。
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		audits = append(audits, auditRec{action: action, targetID: targetID, success: success})
	}

	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 0, recovered)
	assert.Empty(t, killed, "FR-471：启动路径即便复核通过也不得强杀")
	assert.FileExists(t, pidPath, "应保留 PID 文件待周期扫描/接管处置")

	require.NotEmpty(t, audits, "应落审计，不静默")
	seen := false
	for _, a := range audits {
		if a.action == "orphan.startup_detected_not_reaped" {
			seen = true
			assert.Equal(t, uuid, a.targetID)
		}
	}
	assert.True(t, seen, "应落「已发现未处置」审计")
}

// auditActions 抽出审计动作名序列，供 Contains/NotContains 断言。
func auditActions(audits []scanAudit) []string {
	out := make([]string, 0, len(audits))
	for _, a := range audits {
		out = append(out, a.action)
	}
	return out
}

// newOrphanReapFixture 构造「接管兜底处置」用例夹具：PID 记录 + 注入桩（存活读数/杀树/审计）。
// 返回的 alive 可被用例改写为模拟 wrapper 死亡。
func newOrphanReapFixture(t *testing.T, uuid string) (*Manager, string, *daemon.PIDRecord, *[]int, *[]scanAudit) {
	t.Helper()
	dir := t.TempDir()
	pidPath := writeOrphanPIDRecord(t, dir, uuid)
	rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
	require.NoError(t, err)
	require.NotNil(t, rec)

	m := NewManager(dir)
	m.recoverSleep = func(time.Duration) {} // 复核/等待不真睡
	killed := &[]int{}
	audits := &[]scanAudit{}
	m.recoverKillTree = func(pid int) error {
		*killed = append(*killed, pid)
		return nil
	}
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		*audits = append(*audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
	return m, pidPath, rec, killed, audits
}

// TestReapOrphanWrapper_WrapperAliveOnlyWarns FR-455① 验收 2b 回归（可转红）。
//
// 直接打处置原语 reapOrphanWrapper——它是「接管兜底」与「周期扫描 auto 档」共用的**唯一**强杀入口。
// 传入「接管重试耗尽」而非 errOrphanedWrapperGone、且 wrapper 仍存活：即旧行为最危险的形态
// ——wrapper 与 Java 都健康、只是 socket 一时拨不通的运行中服务器。ADR-093 决策 1 判其为
// 「确属本实例、仅瞬时不可达，不是孤儿」→ 只告警 + 落审计、保留 PID 文件、**一个进程都不杀**。
//
// 转红方式（改回旧行为）：删掉函数开头那道「wrapper 仍存活即返回」的判定，让重试耗尽直接落到
// 按 PID 记录强杀 wrapper + Java 两棵树——killed 会含 4242/4243、PID 文件被删、审计变成
// orphan.dispose_reaped，本用例的 killed/PID 文件/dispose_blocked 三条断言立即失败。
func TestReapOrphanWrapper_WrapperAliveOnlyWarns(t *testing.T) {
	uuid := "reap-wrapper-alive"
	m, pidPath, rec, killed, audits := newOrphanReapFixture(t, uuid)

	// wrapper 与 Java 均存活（接管只不过拨不通 socket）。
	m.recoverPIDAlive = func(pid int) bool { return pid == testWrapperPID || pid == testJavaPID }
	// 即便归属复核**通过**（确属本实例）也不得杀：存活判据优先于「socket 是否拨通」。
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }

	m.reapOrphanWrapper(uuid, pidPath, rec, errors.New("dial refused"))

	assert.Empty(t, *killed, "wrapper 仍存活时不得强杀任何进程（强杀会误杀运行中的服务器）")
	assert.FileExists(t, pidPath, "应保留 PID 文件等下一轮扫描或人工介入")
	assert.Contains(t, auditActions(*audits), "orphan.dispose_blocked", "应落「不处置」审计，保证不静默")
	assert.NotContains(t, auditActions(*audits), "orphan.dispose_reaped", "不得出现「已处置」审计")
	for _, a := range *audits {
		if a.action == "orphan.dispose_blocked" {
			assert.False(t, a.success, "被拦截的处置审计 success 应为 false")
			assert.Contains(t, a.detail, "alive_unreachable", "审计 detail 应标明拦截原因")
		}
	}
}

// TestReapOrphanWrapper_OwnershipUnverifiedBlocksKill FR-455① 验收 2 回归（可转红）。
//
// 真孤儿形态（wrapper 已死、Java 仍活）但**归属复核不通过**——PID 可能已被 OS 复用给无关进程。
// 此时代码必须只告警 + 落审计、不杀、保留 PID 文件，等下一轮扫描或人工介入。
//
// 转红方式（改回旧行为）：移除 killTree 之前的 m.verifyProcessOwnership 门，真孤儿会被直接强杀
// ——killed 会含 Java PID（4243），本用例的 killed/PID 文件/dispose_blocked 断言立即失败。
func TestReapOrphanWrapper_OwnershipUnverifiedBlocksKill(t *testing.T) {
	uuid := "reap-verify-blocked"
	m, pidPath, rec, killed, audits := newOrphanReapFixture(t, uuid)

	// wrapper 已死、Java 仍活 → 进入真孤儿处置分支。
	m.recoverPIDAlive = func(pid int) bool { return pid == testJavaPID }
	var verifyWorkDir string
	var verifyExpectWrapper bool
	verifyCalled := 0
	m.recoverVerifyOwner = func(pid int, instanceUUID, workDir string, expectWrapper bool) bool {
		verifyCalled++
		verifyWorkDir = workDir
		verifyExpectWrapper = expectWrapper
		return false // 复核不通过：PID 可能已被 OS 复用给无关进程
	}

	m.reapOrphanWrapper(uuid, pidPath, rec, errOrphanedWrapperGone)

	assert.Equal(t, 1, verifyCalled, "处置前必须做归属复核")
	assert.Equal(t, rec.WorkDir, verifyWorkDir, "复核须以该实例工作目录为匹配主键")
	assert.False(t, verifyExpectWrapper, "真孤儿分支复核的是 Java（wrapper 已死）")
	assert.Empty(t, *killed, "归属复核不通过时不得强杀（PID 可能已被复用给无关进程）")
	assert.FileExists(t, pidPath, "复核不通过应保留 PID 文件等下一轮或人工介入")
	assert.Contains(t, auditActions(*audits), "orphan.dispose_blocked")
	assert.NotContains(t, auditActions(*audits), "orphan.dispose_reaped")
	for _, a := range *audits {
		if a.action == "orphan.dispose_blocked" {
			assert.False(t, a.success)
			assert.Contains(t, a.detail, "ownership_unverified")
		}
	}
}

// TestRecoverDaemonInstances_ShortRetryWindowNeverKillsLiveServer FR-455① 验收 1/2b 端到端回归（可转红）。
//
// 场景即缺陷原文：接管 reconnect 在**很短的窗口内**（旧序列 {1s,2s,4s}≈7s，此处用注入的等价小值
// 替身以便瞬时跑完）重试耗尽，而 wrapper 与 Java 都仍存活。新行为：只告警 + 落审计、保留 PID 文件、
// 不杀任何进程；同时本用例证明注入的重试序列真实生效（初拨 + 3 次重试 = 4 次拨号）。
//
// 转红方式（改回旧行为）：
//   - 让重试耗尽后走 reapOrphanWrapper 强杀 wrapper + Java 两棵树 → killed 非空、PID 文件被删、
//     审计出现 orphan.dispose_reaped；
//   - 让重试序列忽略注入（恒用默认 7 步）→ dials 由 4 变 8。
//
// 两种改法都会让本用例失败。
func TestRecoverDaemonInstances_ShortRetryWindowNeverKillsLiveServer(t *testing.T) {
	uuid := "recover-short-window"
	m, pidPath, _, killed, audits := newOrphanReapFixture(t, uuid)

	// 旧序列 {1s,2s,4s} 的等价小值：窗口短（旧口径 ≈7s）但测试瞬时完成。
	injected := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	m.SetRecoverRetryBackoff(injected)
	m.recoverPIDAlive = func(pid int) bool { return pid == testWrapperPID || pid == testJavaPID }
	m.recoverVerifyOwner = func(int, string, string, bool) bool { return true }
	var sleeps []time.Duration
	m.recoverSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	dials := 0
	m.recoverDial = func(_ *daemonStrategy, _ string) error {
		dials++
		return errors.New("dial refused")
	}

	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 0, recovered, "拨不通不应登记实例")
	assert.Equal(t, 1+3, dials, "重试次数应等于注入序列长度 + 初拨")
	assert.Equal(t, injected, sleeps, "重试间隔应逐项取注入序列")
	assert.Empty(t, *killed, "重试耗尽但 wrapper 仍存活时不得强杀任何进程")
	assert.FileExists(t, pidPath, "应保留 PID 文件等下一轮/人工介入")
	assert.Contains(t, auditActions(*audits), "orphan.dispose_blocked")
	assert.NotContains(t, auditActions(*audits), "orphan.dispose_reaped")
}

// TestDefaultRecoverRetryBackoff_CoversMinuteScale FR-455① 回归（可转红）：默认序列必须是覆盖
// 分钟级的递增序列（spec §6：1s→2s→4s→8s→16s→32s→64s ≈127s）。
//
// 转红方式：改回旧 {1s,2s,4s}（≈7s）——长度、逐项值、总窗口三条断言全部失败。
func TestDefaultRecoverRetryBackoff_CoversMinuteScale(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second,
	}
	assert.Equal(t, want, DefaultRecoverRetryBackoff)

	total := time.Duration(0)
	for i, d := range DefaultRecoverRetryBackoff {
		assert.Greater(t, d, time.Duration(0), "序列项必须为正")
		if i > 0 {
			assert.Greater(t, d, DefaultRecoverRetryBackoff[i-1], "序列必须递增")
		}
		total += d
	}
	assert.Equal(t, 127*time.Second, total, "总重试窗口应约 127s（≈分钟级）")
	assert.GreaterOrEqual(t, int(total/time.Second), 120, "重试窗口须覆盖分钟级瞬时故障")
}

// TestRecoverRetryBackoff_Injection 注入语义：空/全非法回退默认序列，合法序列覆盖默认且被
// reconnectWithRetry 真实使用；入口做防御性拷贝（调用方后续改写不影响运行期节奏）。
func TestRecoverRetryBackoff_Injection(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	assert.Equal(t, DefaultRecoverRetryBackoff, m.recoverRetrySequence(), "未注入时用默认序列")

	m.SetRecoverRetryBackoff(nil)
	assert.Equal(t, DefaultRecoverRetryBackoff, m.recoverRetrySequence(), "nil 应回退默认")

	m.SetRecoverRetryBackoff([]time.Duration{0, -time.Second})
	assert.Equal(t, DefaultRecoverRetryBackoff, m.recoverRetrySequence(), "全非法项应回退默认")

	m.SetRecoverRetryBackoff([]time.Duration{5 * time.Millisecond, 0, 7 * time.Millisecond})
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 7 * time.Millisecond},
		m.recoverRetrySequence(), "应过滤非正项")

	injected := []time.Duration{3 * time.Millisecond}
	m.SetRecoverRetryBackoff(injected)
	injected[0] = time.Hour // 调用方后续改写不得影响已注入序列
	assert.Equal(t, []time.Duration{3 * time.Millisecond}, m.recoverRetrySequence(), "注入应做防御性拷贝")

	// 真实使用：重试次数跟随注入序列长度。
	s := newDaemonStrategy(m, CommandSpec{UUID: "inject-used", WorkDir: dir})
	m.recoverSleep = func(time.Duration) {}
	dials := 0
	m.recoverDial = func(_ *daemonStrategy, _ string) error {
		dials++
		return errors.New("dial refused")
	}
	addr := filepath.Join(dir, "x.sock")
	require.Error(t, m.reconnectWithRetry(s, addr, "inject-used"), "全部拨号失败应返回错误")
	assert.Equal(t, 1+1, dials, "单元素序列 = 初拨 + 1 次重试")

	m.SetRecoverRetryBackoff([]time.Duration{time.Millisecond, time.Millisecond})
	dials = 0
	require.Error(t, m.reconnectWithRetry(s, addr, "inject-used"), "全部拨号失败应返回错误")
	assert.Equal(t, 1+2, dials, "重试次数应等于注入序列长度 + 初拨")
}
