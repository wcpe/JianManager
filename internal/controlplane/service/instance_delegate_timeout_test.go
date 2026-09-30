package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDelegateRPCTimeout 验证实例动作委托给 Worker 的 RPC 超时预算。
//
// restart 在 Worker 侧是「优雅停止 → 等待上一代进程退出 → 启动」的同步串行链：等待预算随生效的
// 优雅停止超时放大（见 worker daemon.PriorExitBudget，默认 30s 强杀兜底 + 收尾余量）。若 restart
// 仍与其它动作共用 30s，长关服实例会在链路中途被 CP 判超时并把实例误标 CRASHED，而 Worker 实际
// 仍在正常重启——「假失败」，与静默假成功同源的状态不一致。
func TestDelegateRPCTimeout(t *testing.T) {
	t.Run("非重启动作沿用默认 30s", func(t *testing.T) {
		for _, action := range []string{"start", "stop", "kill"} {
			assert.Equal(t, delegateRPCTimeoutDefault, delegateRPCTimeout(action, 120),
				"动作 %s 不应被重启预算放大", action)
		}
	})

	t.Run("重启按生效优雅停止超时放大", func(t *testing.T) {
		assert.Equal(t, 30*time.Second+restartRPCTimeoutMargin, delegateRPCTimeout("restart", 30),
			"平台设置为默认 30s 时，预算应覆盖 Worker 侧 30s 强杀兜底加余量")
		assert.Equal(t, 120*time.Second+restartRPCTimeoutMargin, delegateRPCTimeout("restart", 120),
			"平台设置放大时，预算必须同步放大，否则长关服实例被误判超时")
	})

	t.Run("未取到生效值时按基线默认兜底", func(t *testing.T) {
		want := time.Duration(gracefulStopTimeoutDefaultSeconds)*time.Second + restartRPCTimeoutMargin
		assert.Equal(t, want, delegateRPCTimeout("restart", 0),
			"未装配 settings（取不到生效值）时须按平台基线默认兜底")
		assert.Greater(t, want, delegateRPCTimeoutDefault,
			"重启预算必须大于其它动作的默认超时，否则重启会长期假失败")
	})
}
