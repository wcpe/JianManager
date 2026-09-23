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
			wantDials:      1 + len(recoverRetryBackoff),
			wantPIDFile:    true,
			wantRegistered: false,
			wantBlocked:    true,
		},
		{
			name:                   "重试期间 wrapper 已死→只观测不强杀（FR-471 非破坏）",
			succeedOnDial:          0,
			wrapperDiesDuringRetry: true,
			wantRecovered:          0,
			wantDials:              1 + len(recoverRetryBackoff),
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

			// 重试间隔递增：失败几次就应等待 recoverRetryBackoff 的对应前缀
			retrySleeps := len(recoverRetryBackoff)
			if tt.succeedOnDial > 0 {
				retrySleeps = tt.succeedOnDial - 1
			}
			require.GreaterOrEqual(t, len(sleeps), retrySleeps)
			assert.Equal(t, recoverRetryBackoff[:retrySleeps], sleeps[:retrySleeps], "重试间隔应按递增序列")

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
