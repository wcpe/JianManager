package ingest

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSweepInflightCapBoundsAbandonedCalls（新账 2 的红证 ✓，用户 2026-10-05）：
// **在飞（含被放弃）的步骤数必须被上限钉住** —— 防"超时只放弃等待 ✗、被放弃调用仍带载荷等
// `m.mu`"在多轮里叠加到不可控（现场 RSS 8–14GB/轮爬升的形态 ✓，后证非单调 ⇒ 暂态可接受，
// 但极端负载下必须有界 ✓）。
//
// 转红方式（实测，**有效变异** ✓：先验编译）：去掉执行器里的上限检查 ⇒ 10 次全部发起 ⇒
// 在飞 > 上限 ⇒ 本用例在「在飞 ≤ 上限」处红 ✓✓。
func TestSweepInflightCapBoundsAbandonedCalls(t *testing.T) {
	m := &Manager{}
	m.sweepStepBudget = 5 * time.Millisecond // 极短预算 ⇒ 每次都超时、每次都被放弃 ✓
	m.sweepInflightCap = 3                   // 上限 3 ✓
	hold := make(chan struct{})
	m.sweepStepHold = func() { <-hold } // 每步都被按住 ⇒ 全部留在"在飞" ✓

	var started sync.WaitGroup
	for i := 0; i < 10; i++ {
		started.Add(1)
		go func() {
			defer started.Done()
			_ = m.runSweepStepBounded(m.sweepStepBudgetOf(), func() {})
		}()
	}
	started.Wait() // 10 次调用都返回（有界 ✓）

	require.LessOrEqual(t, m.sweepInflight.Load(), int64(3),
		"在飞（含被放弃）必须 ≤ 上限（实测 %d > 3 ⇒ 取消上限检查 ✗✗）", m.sweepInflight.Load())
	require.GreaterOrEqual(t, m.skipInflightCap.Load(), int64(1),
		"到上限后必须拒发新步并计数（实测 %d 次）", m.skipInflightCap.Load())

	close(hold) // 放行被按住的调用 ⇒ 在飞应归零 ✓（无泄漏 ✓）
	require.Eventually(t, func() bool { return m.sweepInflight.Load() == 0 }, 3*time.Second, 10*time.Millisecond,
		"放行后在飞必须归零（否则计数泄漏 ✗），实测 %d", m.sweepInflight.Load())

	// 兜底：第 5 个断言（上限生效后仍能正常执行未被拒的步）——用极长预算 + 不按住 ✓。
	m2 := &Manager{}
	m2.sweepStepBudget = 1 * time.Second
	ok := m2.runSweepStepBounded(m2.sweepStepBudgetOf(), func() {})
	require.True(t, ok, "未被拒的步必须正常完成（上限不得把功能一起关掉 ✗）")
	require.Zero(t, m2.sweepInflight.Load(), "正常完成后在飞归零 ✓")
}
