package logcoord

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReserveWorkerBudgetsZeroShareSkipsTarget 钉住 FR-480 验收项 9 的后半句：
// 「`max_bytes` 整请求预算、**零份额不拨号**」。
//
// ## 为什么这条必须单独立一个用例
//
// `reserveWorkerBudgets` 在 `share == 0` 时 `continue`（不把该目标放进返回表）。
// 这个分支一旦被删（改成「零也塞进去」），现场表现是：那一路 Worker 收到
// `max_bytes=0`，而 Worker 侧把 0 解释成「未声明额度 ⇒ 用默认 32MiB」——
// 于是**整请求预算被悄悄放大**，联邦查询在目标数远大于预算份额时超额读盘。
// 这不是性能问题，是预算门禁失效。
//
// 判据分两层：
//  1. 零份额的目标**不出现在**返回表里（调用方据此不拨号）；
//  2. 所有被拨号的目标份额之和**不超过**原始总额。
func TestReserveWorkerBudgetsZeroShareSkipsTarget(t *testing.T) {
	// 3 个目标、总额 2 字节 ⇒ 每份算下来是 1/1/0（ceil 分配），最后一个必然为 0。
	order := []string{"w1", "w2", "w3"}
	assigned := reserveWorkerBudgets(order, QueryBudget{MaxBytes: 2})

	require.NotContains(t, assigned, "w3", "零份额的目标必须被跳过（否则 Worker 会用默认额度放大预算）")
	require.Contains(t, assigned, "w1")
	require.Contains(t, assigned, "w2")

	var sum uint64
	for _, v := range assigned {
		require.Positive(t, v.MaxBytes, "进入返回表的份额必须为正")
		sum += v.MaxBytes
	}
	require.LessOrEqual(t, sum, uint64(2), "拨号份额之和不得超过请求总额")

	// 极端：总额小到一份都分不出来时，一个目标都不该被拨号。
	tiny := reserveWorkerBudgets([]string{"a", "b", "c"}, QueryBudget{MaxBytes: 1})
	var tinySum uint64
	for _, v := range tiny {
		require.Positive(t, v.MaxBytes)
		tinySum += v.MaxBytes
	}
	require.LessOrEqual(t, tinySum, uint64(1), "总额 1 字节时不得放大成多份")
	require.Len(t, tiny, 1, "总额 1 字节且 3 个目标时，只应有 1 个目标拿到份额")
}

// TestReserveWorkerBudgetsMaxFanoutBoundsShares 钉住 MaxFanout 对份额数的影响：
// 超出的目标不参与分配，也不拨号。
func TestReserveWorkerBudgetsMaxFanoutBoundsShares(t *testing.T) {
	order := []string{"w1", "w2", "w3", "w4"}
	assigned := reserveWorkerBudgets(order, QueryBudget{MaxBytes: 100, MaxFanout: 2})
	require.Len(t, assigned, 2, "MaxFanout=2 时只应有 2 个目标被拨号")
	require.NotContains(t, assigned, "w3")
	require.NotContains(t, assigned, "w4")

	var sum uint64
	for _, v := range assigned {
		sum += v.MaxBytes
	}
	require.Equal(t, uint64(100), sum, "被拨号目标平分总额，不浪费也不超额")
}
