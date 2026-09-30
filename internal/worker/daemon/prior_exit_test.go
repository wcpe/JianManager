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
//   - 无 PID 文件：立即返回 nil（上一代已自清理）
//   - PID 文件中进程已死：立即返回 nil
//   - PID 文件中进程仍存活：等满超时后 **报错拒绝启动**
//
// 这是 FR-035 代理重启竞态（旧进程占端口致新进程 exit status 1）的回归测试。
//
// 第三种情形是本缺陷（实例 153 / beacon-main 实证）的回归：旧实现超时后"仍继续启动"，
// 而 wrapper 的优雅停止强杀兜底（默认 30s，平台设置可放大）长于旧等待上限 15s，于是
// 旧进程仍在关服时就 spawn 了第二个 wrapper 与第二个 Java——新进程抢端口秒崩
// （日志 4 次 exitCode=1 durationMs≈195）、旧进程继续用旧配置跑 30s 才被强杀，
// 而 Manager.Restart 早已返回成功（静默假成功）。修复后超时必须显式失败：
// 宁可让本次启动失败并保留可观测的旧进程，也不允许新旧进程并存。
func TestWaitForPriorExit(t *testing.T) {
	t.Run("无 PID 文件立即返回", func(t *testing.T) {
		start := time.Now()
		require.NoError(t, waitForPriorExit(t.TempDir(), "absent", 5*time.Second),
			"无 PID 文件说明上一代已退出，不得报错")
		assert.Less(t, time.Since(start), time.Second, "无 PID 文件不应等待")
	})

	t.Run("进程已死立即返回", func(t *testing.T) {
		pidDir := t.TempDir()
		dead := reapedPID(t)
		writePIDRecord(t, pidDir, "dead", PIDRecord{WrapperPID: dead, JavaPID: dead, InstanceUUID: "dead"})
		start := time.Now()
		require.NoError(t, waitForPriorExit(pidDir, "dead", 5*time.Second),
			"上一代进程已死，不得报错")
		assert.Less(t, time.Since(start), 3*time.Second, "进程已死不应等满超时")
	})

	t.Run("进程存活超时报错拒绝启动", func(t *testing.T) {
		pidDir := t.TempDir()
		// 用测试进程自身 PID 模拟仍存活的上一代 wrapper。
		writePIDRecord(t, pidDir, "alive", PIDRecord{WrapperPID: os.Getpid(), InstanceUUID: "alive"})
		const timeout = 300 * time.Millisecond
		start := time.Now()
		err := waitForPriorExit(pidDir, "alive", timeout)
		elapsed := time.Since(start)
		require.ErrorIs(t, err, ErrPriorExitTimeout,
			"上一代进程仍存活时必须报错拒绝启动，不得静默继续（否则新旧进程并存）")
		assert.GreaterOrEqual(t, elapsed, timeout, "存活进程应等待至超时")
		assert.Less(t, elapsed, timeout+2*time.Second, "超时后应尽快返回，不应无限阻塞")
	})
}

// TestPriorExitBudget 验证等待预算的解析优先级与下限。
//
// 预算必须**覆盖 wrapper 的优雅停止强杀兜底**：若预算短于该兜底，旧进程正在正常关服
// 就会被误判超时并拒绝启动（把静默假成功换成假失败）。环境变量优先，供测试/集成缩短。
func TestPriorExitBudget(t *testing.T) {
	t.Run("环境变量优先", func(t *testing.T) {
		t.Setenv(envStartWaitTimeout, "7s")
		assert.Equal(t, 7*time.Second, PriorExitBudget(120),
			"环境变量应覆盖预算计算（供测试/集成缩短）")
	})

	t.Run("未配置优雅停止时按 wrapper 默认值加收尾余量", func(t *testing.T) {
		t.Setenv(envStartWaitTimeout, "")
		t.Setenv(envGracefulStopTimeout, "")
		assert.Equal(t, gracefulStopTimeout+priorExitMargin, PriorExitBudget(0),
			"未下发优雅停止超时时，预算须覆盖 wrapper 的默认强杀兜底（30s）加收尾余量")
	})

	t.Run("下发优雅停止超时时按其放大", func(t *testing.T) {
		t.Setenv(envStartWaitTimeout, "")
		t.Setenv(envGracefulStopTimeout, "")
		assert.Equal(t, 90*time.Second+priorExitMargin, PriorExitBudget(90),
			"预算须随平台设置下发的优雅停止超时放大，否则长关服会被误判超时")
	})

	t.Run("不短于下限", func(t *testing.T) {
		t.Setenv(envStartWaitTimeout, "")
		t.Setenv(envGracefulStopTimeout, "1s")
		assert.Equal(t, startWaitTimeout, PriorExitBudget(0),
			"极短优雅停止下预算仍不得低于下限")
	})
}
