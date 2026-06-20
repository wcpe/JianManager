package daemon

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePIDRecord 在 pidDir 写入指定实例的 PID 文件（测试辅助）。
func writePIDRecord(t *testing.T, pidDir, uuid string, rec PIDRecord) {
	t.Helper()
	require.NoError(t, NewPIDFile(PIDFileName(pidDir, uuid)).WriteRecord(rec))
}

// reapedPID 启动一个立即退出的进程并回收，返回其（已死）PID。
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := buildJavaCmd(WrapperConfig{StartCommand: "exit 0"})
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

// TestWaitForPriorExit 验证「重启前等待上一代进程退出」的三种情形：
//   - 无 PID 文件：立即返回（上一代已自清理）
//   - PID 文件中进程已死：立即返回
//   - PID 文件中进程仍存活：等满超时后尽力而为返回（不无限期阻塞、也不提前返回）
// 这是 FR-035 代理重启竞态（旧进程占端口致新进程 exit status 1）的回归测试。
func TestWaitForPriorExit(t *testing.T) {
	t.Run("无 PID 文件立即返回", func(t *testing.T) {
		start := time.Now()
		waitForPriorExit(t.TempDir(), "absent", 5*time.Second)
		assert.Less(t, time.Since(start), time.Second, "无 PID 文件不应等待")
	})

	t.Run("进程已死立即返回", func(t *testing.T) {
		pidDir := t.TempDir()
		dead := reapedPID(t)
		writePIDRecord(t, pidDir, "dead", PIDRecord{WrapperPID: dead, JavaPID: dead, InstanceUUID: "dead"})
		start := time.Now()
		waitForPriorExit(pidDir, "dead", 5*time.Second)
		assert.Less(t, time.Since(start), 3*time.Second, "进程已死不应等满超时")
	})

	t.Run("进程存活等满超时", func(t *testing.T) {
		pidDir := t.TempDir()
		// 用测试进程自身 PID 模拟仍存活的上一代 wrapper。
		writePIDRecord(t, pidDir, "alive", PIDRecord{WrapperPID: os.Getpid(), InstanceUUID: "alive"})
		const timeout = 300 * time.Millisecond
		start := time.Now()
		waitForPriorExit(pidDir, "alive", timeout)
		elapsed := time.Since(start)
		assert.GreaterOrEqual(t, elapsed, timeout, "存活进程应等待至超时")
		assert.Less(t, elapsed, timeout+2*time.Second, "超时后应尽力而为返回，不应无限阻塞")
	})
}
