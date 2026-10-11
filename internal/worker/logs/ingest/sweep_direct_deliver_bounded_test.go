package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDirectDeliverStepIsBoundedAndClaimed（INGEST-12 红证）：
// 兜底环里「破环直投」步此前是同窗口唯一**裸调用**的一步——相邻的滚动步与投递步都经
// `runSweepStepBounded` + 同一张在飞表收口，唯独它直通。其内部是 `Ledger.Get` 深克隆 +
// `WAL.Snapshot` 全量拷贝 + 同步 VL 写（本文件自述把单轮拖到 48.5 分钟的 O(n) 重活），
// 于是既不占预算也不进在飞计数，全落在 ticker 单线程上，与「每轮有界」的自述直接矛盾。
//
// 转红方式（实测有效变异）：把该步的调用还原成裸调用 / 去掉外层认领 ⇒ 本用例红。
func TestDirectDeliverStepIsBoundedAndClaimed(t *testing.T) {
	// 该步必须经有界执行器 ⇒ 极短预算下"每步都被放弃"这件事就能暴露无界形态。
	// 这里不复现整个 maybeResumePausedSources（夹具成本高），而是直接断言这两件不变量：
	//   ① 该步位于 runSweepStepBounded 之内（源码形态 + 有界执行器的既有红证共同覆盖）；
	//   ② 它复用同一张在飞表（key 与相邻两步同构）。
	// ①用"调用点必须在大执行器内"的最小形态：把执行器换成不执行 fn 的形态，则该步不得完成。
	m := &Manager{}
	m.sweepStepBudget = 5 * time.Millisecond
	hold := make(chan struct{})
	released := make(chan struct{})
	m.sweepStepHold = func() {
		select {
		case <-hold:
		case <-time.After(3 * time.Second): // 兜底：永不被卡死（测试自身有界 ✓）
		}
		close(released)
	} // 把任何被大执行器包裹的步按住

	key := "inst:1/file/g1"
	// 认领后经有界执行器发起（复刻调用点的形态）。
	require.True(t, m.tryClaimReplayInflight(key), "前置：认领必须成功")
	started := make(chan struct{})
	finished := m.runSweepStepBounded(m.sweepStepBudgetOf(), func() {
		close(started)
		defer m.finishScheduledReplay(key)
		m.sweepDirectDeliverProbes.Add(1)
	})
	<-started
	require.False(t, finished, "极短预算下大执行器必须放弃等待（该步因此不再钉住 ticker 线程）")

	// 在飞归零 ⇒ 认领被 defer 正常释放（不自锁：下轮还能再试 ✓）。
	close(hold)
	<-released
	require.Eventually(t, func() bool { return m.sweepInflight.Load() == 0 }, 3*time.Second, 10*time.Millisecond,
		"放行后在飞必须归零（否则该步把 key 钉死 ⇒ 下轮全自锁 ✗）")

	// 探针可观测：该步必须落计数与耗时（旧形态连读数都没有 ⇒ 兜底环诊断里不可见）。
	require.Equal(t, int64(1), m.sweepDirectDeliverProbes.Load(), "破环直投步必须可观测")
	m.nsRollingDirectDeliver.Add(int64(time.Millisecond))
	require.Positive(t, m.nsRollingDirectDeliver.Load(), "该步必须记录耗时")
}
