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

func TestRecoverDaemonInstances_ReconnectFailureFallback(t *testing.T) {
	tests := []struct {
		name string
		// succeedOnDial 第 N 次拨号成功；0 = 永不成功（触发杀树兜底）。
		succeedOnDial int
		killErr       error
		// killMakesDead 杀树桩是否把目标 PID 置为已死（模拟杀树生效 / 权限不足杀不死）。
		killMakesDead  bool
		wantRecovered  int
		wantDials      int
		wantKilled     []int
		wantPIDFile    bool
		wantRegistered bool
	}{
		{
			name:           "reconnect 失败→重试→成功恢复",
			succeedOnDial:  3,
			wantRecovered:  1,
			wantDials:      3,
			wantKilled:     nil,
			wantPIDFile:    true,
			wantRegistered: true,
		},
		{
			name:           "重试耗尽→杀树→清理",
			succeedOnDial:  0,
			killMakesDead:  true,
			wantRecovered:  0,
			wantDials:      1 + len(recoverRetryBackoff),
			wantKilled:     []int{testWrapperPID, testJavaPID},
			wantPIDFile:    false,
			wantRegistered: false,
		},
		{
			name:           "杀树失败→记日志保留 PID 文件",
			succeedOnDial:  0,
			killErr:        errors.New("access denied"),
			killMakesDead:  false,
			wantRecovered:  0,
			wantDials:      1 + len(recoverRetryBackoff),
			wantKilled:     []int{testWrapperPID, testJavaPID},
			wantPIDFile:    true,
			wantRegistered: false,
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
			dials := 0

			m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
			m.recoverSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
			m.recoverKillTree = func(pid int) error {
				killed = append(killed, pid)
				if tt.killMakesDead {
					dead[pid] = true
				}
				return tt.killErr
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
			assert.Equal(t, tt.wantKilled, killed, "杀树目标应先 wrapper 树再补杀 Java 树")

			// 重试间隔递增：失败几次就应等待 recoverRetryBackoff 的对应前缀
			retrySleeps := len(recoverRetryBackoff)
			if tt.succeedOnDial > 0 {
				retrySleeps = tt.succeedOnDial - 1
			}
			require.GreaterOrEqual(t, len(sleeps), retrySleeps)
			assert.Equal(t, recoverRetryBackoff[:retrySleeps], sleeps[:retrySleeps], "重试间隔应按递增序列")

			if tt.wantPIDFile {
				assert.FileExists(t, pidPath, "PID 文件应保留")
			} else {
				assert.NoFileExists(t, pidPath, "死透后应清理 PID 文件")
			}

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

// TestRecoverDaemonInstances_KillVerifyWaitsAsyncExit 强杀返回成功但进程延迟退出
// （Windows taskkill /T /F 异步终止）时，存活复核应有界等待到退出后再清理。
func TestRecoverDaemonInstances_KillVerifyWaitsAsyncExit(t *testing.T) {
	dir := t.TempDir()
	uuid := "orphan-async-exit"
	pidPath := writeOrphanPIDRecord(t, dir, uuid)

	m := NewManager(dir)
	aliveChecksAfterKill := 0
	killIssued := false
	m.recoverSleep = func(time.Duration) {}
	m.recoverDial = func(_ *daemonStrategy, _ string) error { return errors.New("dial refused") }
	m.recoverKillTree = func(int) error { killIssued = true; return nil }
	m.recoverPIDAlive = func(pid int) bool {
		if !killIssued {
			return true
		}
		// 杀树后前两次复核仍报存活（模拟异步终止窗口），之后死透
		aliveChecksAfterKill++
		return aliveChecksAfterKill <= 2
	}

	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	assert.Equal(t, 0, recovered)
	assert.NoFileExists(t, pidPath, "异步退出窗口结束后应完成清理")
}

// TestRecoverDaemonInstances_WrapperGoneJavaAlive 覆盖「wrapper 已死、Java 仍活」的接管场景。
//
// 真机事故：Worker 重启后 wrapper 因故消失，而它托管的 Java 仍活着（父进程变为 init）。
// 旧逻辑只检查 wrapper，见到 wrapper 死就直接删 PID 文件——活着的 Java 从此不可发现，
// 继续占着服务端口与 Paper session.lock，面板却显示 STOPPED，实例再也起不来。
// 正确行为：按 PID 记录强杀 Java 树；死透才清理，杀不死则保留 PID 文件待下次兜底。
func TestRecoverDaemonInstances_WrapperGoneJavaAlive(t *testing.T) {
	tests := []struct {
		name           string
		killMakesDead  bool
		wantKilled     []int
		wantPIDFile    bool
		wantRegistered bool
	}{
		{
			name:          "Java 孤儿被杀→清理 PID 文件",
			killMakesDead: true,
			// 只杀 Java：wrapper 已不存在，对其杀树会刷无意义告警且 PGID 可能已被复用
			wantKilled:     []int{testJavaPID},
			wantPIDFile:    false,
			wantRegistered: false,
		},
		{
			name:           "Java 杀不死（权限不足）→保留 PID 文件待下次兜底",
			killMakesDead:  false,
			wantKilled:     []int{testJavaPID},
			wantPIDFile:    true,
			wantRegistered: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			uuid := "orphan-wrapper-gone"
			pidPath := writeOrphanPIDRecord(t, dir, uuid)

			m := NewManager(dir)
			dead := map[int]bool{testWrapperPID: true} // wrapper 已死，Java 仍活
			var killed []int
			dials := 0

			m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
			m.recoverSleep = func(time.Duration) {}
			m.recoverKillTree = func(pid int) error {
				killed = append(killed, pid)
				if tt.killMakesDead {
					dead[pid] = true
				}
				return nil
			}
			m.recoverDial = func(_ *daemonStrategy, _ string) error {
				dials++
				return nil
			}

			recovered, err := m.RecoverDaemonInstances()
			require.NoError(t, err)
			assert.Equal(t, 0, recovered)
			assert.Equal(t, 0, dials, "wrapper 已死时不应尝试 reconnect")
			assert.Equal(t, tt.wantKilled, killed, "只强杀 Java 孤儿，不碰已消失的 wrapper")

			if tt.wantPIDFile {
				assert.FileExists(t, pidPath, "Java 未死透时应保留 PID 文件供下次兜底")
			} else {
				assert.NoFileExists(t, pidPath, "Java 死透后应清理 PID 文件")
			}

			_, stErr := m.GetState(uuid)
			assert.Error(t, stErr, "兜底处置后不应登记实例")
		})
	}
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
