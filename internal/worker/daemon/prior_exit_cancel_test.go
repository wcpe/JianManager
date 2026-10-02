package daemon

// 复审 P1-5 回归：启动前的等待必须**可取消**，且取消是「可重试」而非「旧进程仍在」。
//
// 现场形态：等待预算按生效优雅停止超时放大（平台设 120s 时 =130s），而调用方（CP 的委托 RPC）
// 可能提前取消/超时。旧实现只有固定 time.Sleep 轮询、完全不看 ctx：请求早已取消，Worker 仍会
// 把预算耗尽后再拒绝启动——运维最终看到「CP 超时」与「Worker 拒绝」两个与真因无关的错误
// （双重假失败）。
//
// 本文件的用例：
//  1. TestWaitForPriorExitContextCanceledIsRetryable —— 取消立即生效，返回可重试错误而非超时拒绝；
//  2. TestSocketServedContextCanceledIsNotAFalseNegative —— 取消时不得把「状态未知」当成
//     「无人监听」（那正好会退化成双开）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWaitForPriorExitContextCanceledIsRetryable 守住「取消 ≠ 超时拒绝」：
// ctx 取消必须**立即**收手，并返回 ErrStartWaitCanceled（可重试）而不是 ErrPriorExitTimeout。
//
// 转红：把轮询退回固定 time.Sleep（不看 ctx）即红——本用例会等满预算（3s）后拿到
// ErrPriorExitTimeout，两个断言同时失败。
func TestWaitForPriorExitContextCanceledIsRetryable(t *testing.T) {
	pidDir := t.TempDir()
	// 用测试进程自身 PID 模拟仍存活的上一代 wrapper。
	writePIDRecord(t, pidDir, "alive", PIDRecord{WrapperPID: os.Getpid(), InstanceUUID: "alive"})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := WaitForPriorExitContext(ctx, pidDir, "alive", 3*time.Second)
	elapsed := time.Since(start)

	require.Error(t, err, "取消后不得再报告「可以启动」")
	assert.Less(t, elapsed, time.Second, "取消必须立即收手，不得继续等满预算（实测 %s）", elapsed)
	assert.ErrorIs(t, err, ErrStartWaitCanceled, "取消是可重试状态，不是确定性结论")
	assert.NotErrorIs(t, err, ErrPriorExitTimeout, "取消不得被当作「上一代未退出」")
}

// TestWaitForPriorExitContextDeadlineIsRetryable 同上，但用带 deadline 的 ctx（CP 的委托 RPC
// 超时就是这条路径）。
func TestWaitForPriorExitContextDeadlineIsRetryable(t *testing.T) {
	pidDir := t.TempDir()
	writePIDRecord(t, pidDir, "alive", PIDRecord{WrapperPID: os.Getpid(), InstanceUUID: "alive"})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := WaitForPriorExitContext(ctx, pidDir, "alive", 3*time.Second)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, time.Second, "deadline 到期必须立即收手（实测 %s）", elapsed)
	assert.ErrorIs(t, err, ErrStartWaitCanceled)
	assert.NotErrorIs(t, err, ErrPriorExitTimeout)
}

// TestSocketServedContextCanceledIsNotAFalseNegative 守住「取消 ⇒ 状态未知」：
// 取消时不得返回 (false, nil)——那会被调用方解读成「无人监听」并直接 spawn 新 wrapper（双开）。
//
// 转红：把第二次探测之间的等待退回固定 time.Sleep（不看 ctx）即红——本用例会拿到
// (true, nil)（有人在监听）而不是取消错误。
func TestSocketServedContextCanceledIsNotAFalseNegative(t *testing.T) {
	pidDir := t.TempDir()
	uuid := "sock-cancel"
	listener, err := Listen(SocketAddr(pidDir, uuid))
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	// 未取消时：连续两次都可拨通 ⇒ 判真（既有语义不变）。
	served, probeErr := SocketServedContext(context.Background(), pidDir, uuid)
	require.NoError(t, probeErr)
	require.True(t, served, "有人在监听时必须判真（新 wrapper 必须让位）")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	served, err = SocketServedContext(ctx, pidDir, uuid)
	elapsed := time.Since(start)
	require.Error(t, err, "取消时必须报错：服务状态未知，绝不能当成「无人监听」放行")
	assert.ErrorIs(t, err, ErrStartWaitCanceled)
	assert.Less(t, elapsed, time.Second, "取消必须立即生效（实测 %s）", elapsed)
	assert.False(t, served)
}

// TestSocketServedContextUnservedIsImmediate 守住「无人监听」的快路径：不等待、不报错。
func TestSocketServedContextUnservedIsImmediate(t *testing.T) {
	start := time.Now()
	served, err := SocketServedContext(context.Background(), t.TempDir(), "absent")
	require.NoError(t, err)
	assert.False(t, served)
	assert.Less(t, time.Since(start), 100*time.Millisecond, "无人监听时应立即返回")
}
