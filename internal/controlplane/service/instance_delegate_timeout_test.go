package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestDelegateRPCTimeout 验证实例动作委托给 Worker 的 RPC 超时预算。
//
// start 与 restart 在 Worker 侧都要等「上一代进程退出」（见 worker daemon.PriorExitBudget：
// 生效优雅停止超时 + 收尾余量），预算耗尽即**拒绝启动**（宁可保留仍在服务的旧进程，也不新旧
// 并存）。若 start 仍与其它动作共用 30s，慢关服实例点「启动」会先被 CP 判超时——实例被误标
// CRASHED 且原因写成 RPC 超时（假失败）——随后 Worker 的等待到点又返回「上一代未在预算内退出」
// （第二次失败）：两个错误都不是真因（2026-10-02 复审 P1-4/P1-5）。
func TestDelegateRPCTimeout(t *testing.T) {
	t.Run("不需要等上一代退出的动作沿用默认 30s", func(t *testing.T) {
		for _, action := range []string{"stop", "kill"} {
			assert.Equal(t, delegateRPCTimeoutDefault, delegateRPCTimeout(action, 120),
				"动作 %s 不等待进程退出，不应被等待预算放大", action)
		}
	})

	t.Run("start/restart 按生效优雅停止超时同口径放大", func(t *testing.T) {
		for _, action := range []string{"start", "restart"} {
			assert.Equal(t, 30*time.Second+lifecycleWaitRPCTimeoutMargin, delegateRPCTimeout(action, 30),
				"%s：平台设置为默认 30s 时，预算应覆盖 Worker 侧 30s 强杀兜底加余量", action)
			assert.Equal(t, 120*time.Second+lifecycleWaitRPCTimeoutMargin, delegateRPCTimeout(action, 120),
				"%s：平台设置放大时，预算必须同步放大，否则长关服实例被误判超时", action)
		}
	})

	t.Run("未取到生效值时按基线默认兜底", func(t *testing.T) {
		want := time.Duration(gracefulStopTimeoutDefaultSeconds)*time.Second + lifecycleWaitRPCTimeoutMargin
		for _, action := range []string{"start", "restart"} {
			assert.Equal(t, want, delegateRPCTimeout(action, 0),
				"%s：未装配 settings（取不到生效值）时须按平台基线默认兜底", action)
			assert.Greater(t, want, delegateRPCTimeoutDefault,
				"%s：等待预算必须大于其它动作的默认超时，否则会长期假失败", action)
		}
	})

	// 本用例是「双重假失败」的**决定性判据**：CP 的委托超时必须严格覆盖 Worker 的等待预算，
	// 否则 CP 一定先超时、Worker 的结论永远送不回来（运维看到的是 CP 的 RPC 超时 + Worker 的
	// 拒绝启动两个错误）。
	t.Run("委托超时严格覆盖 Worker 的等待预算", func(t *testing.T) {
		// 环境变量优先，避免宿主/CI 环境把它设成异常值而掩盖真实关系。
		t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "")
		t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "")
		for _, graceful := range []int32{0, 30, 90, 120} {
			budget := daemon.PriorExitBudget(int(graceful))
			for _, action := range []string{"start", "restart"} {
				got := delegateRPCTimeout(action, graceful)
				assert.Greater(t, got, budget,
					"gracefulStop=%ds 时 %s 的委托超时（%s）必须覆盖 Worker 等待预算（%s），"+
						"否则 CP 会先判超时并给出假失败", graceful, action, got, budget)
			}
		}
	})
}
