package ingest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestCapacityResumeLoopHealsPausedSource（2026-10-03 现场「暂停不回落」的正主）：
// **兜底循环必须在没有任何新 Append、也不依赖采集轮的情况下，把积压类暂停的源恢复到活。**
//
// 现场形态：27 个源 acquire_paused=1 且 source_wal entries=0 —— 恢复的唯一检查点挂在 Append 上，
// 暂停后没人再 Append ⇒ 永远不再评估 ⇒ 成批源停死数小时 ✗✗。
//
// 转红方式（实测）：把 maybeResumePausedSources 改成空实现（= 只在 Append 处重估）——
// 本用例在「兜底已扫到该源」与「源已恢复」两处变红。
func TestCapacityResumeLoopHealsPausedSource(t *testing.T) {
	m, _ := newGateFixture(t, 50, 16)
	m.mu.Lock()
	var pipe *pipeline.Pipeline
	for _, p := range m.pipes { // 夹具只有一个源；键形态与实现内部一致（不硬编码键名）
		pipe = p
	}
	m.mu.Unlock()
	require.NotNil(t, pipe, "夹具应已建立源管道")

	// 造出**现场形态**：存量积压暂停（积压本身已为零 ⇒ 恢复判定必须绑定"活着的读数"而非 Append ✗）。
	//
	// 2026-10-29：积压闸已改为丢弃语义，本包不再产生新的积压暂停，但**存量库里仍有**旧二进制
	// 写下的这种暂停（生产 10 个源就是此形态），`maybeResumeBacklogLocked` 负责它们。
	// 这里就用那个存量原因造夹具——它才是这条收敛路径的真实输入。
	//
	// **必须先造出真实回收欠账（reclaim < delivery）**：兜底环在候选门**之前**先给该源一次回收步
	// （`if pos.Reclaim < pos.Delivery` 才进，见 runtime.go 的 #66-⑤），而回收步末尾的
	// `WAL.pruneReclaimed` → `maybeResumeBacklogLocked` 才是本用例要钉的那条收敛路径。
	// 没有欠账时环只会走候选分支（那是**另一条**路，probe 会被调用），本用例的"门之前"判别力
	// 就消失了。欠账用一条真实的 REQUEST_DONE 投递批次制造：它把 delivery 推上去，而
	// reclaim 被 read/durable 夹住 ⇒ 欠账长期存在（这正是现场"已确认投递但未回收"的形态）。
	pos, ok := pipe.Ledger().Positions(pipe.Key())
	require.True(t, ok)
	require.NoError(t, pipe.Ledger().RecordDelivery(pipe.Key(),
		pos.Delivery, pos.Delivery+100, logtypes.DeliveryRequestDone))
	debt := pipe.Ledger().Get(pipe.Key())
	require.Greater(t, debt.Positions.Delivery, debt.Positions.Reclaim,
		"夹具前提：必须存在真实回收欠账（reclaim < delivery），否则兜底环不会走「门之前的回收步」")
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), legacyBacklogPauseReason))
	require.True(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused)

	var mu sync.Mutex
	probed := 0
	m.resumeProbe = func(string) { mu.Lock(); probed++; mu.Unlock() }
	// 上限 12s（原 3s）：与后台全仓并发时 3s 偏紧 ⇒ 实测出现过一次负载敏感的偶发红 ✗（非实现缺陷 ✓）。
	m.resumeInterval = 20 * time.Millisecond

	// **无任何新 Append**，也不推进采集轮：只看向兜底环能不能自己解开它。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunCapacityResumeLoop(ctx)

	require.Eventually(t, func() bool {
		got := pipe.Ledger().Get(pipe.Key())
		return got != nil && !got.AcquirePaused
	}, 12*time.Second, 10*time.Millisecond,
		"无新 Append 的存量积压暂停必须被兜底环自行解除（不得只在 Append 处重估 ✗）")
	require.Empty(t, pipe.Ledger().Get(pipe.Key()).PauseReason, "解除后不得残留暂停原因")
	// 观测面：本路径必须真的走过兜底环（probe 是被测环的内部观测点）。
	// 注意：存量积压暂停是被环内**轮动/回收步**（`reclaimStep0RollingOnly` → `maybeResumeBacklogLocked`）
	// 解除的，而不是候选分支（它在候选门之前就跑了），所以 probe 这里本应为 0；用“环真的跑过”做观测面。
	mu.Lock()
	hits := probed
	mu.Unlock()
	require.Zero(t, hits, "存量积压暂停应在候选门之前就被环内的回收步解开")
	require.Positive(t, m.ResumeSweepStats().SweepTicks, "必须是兜底环真实跑过才解开的，不是其它路径的副作用")

	// 红线：非积压/容量类暂停不得被清除 ✓。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "disk usage 95.0% >= 90.0%"))
	// 非积压/容量类暂停会被**跳过**（不进处理分支）⇒ 多轮之后仍必须停在暂停上 ✓。
	time.Sleep(120 * time.Millisecond) // 至少 5 个兜底周期
	require.True(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused, "非本包原因不得被容量兜底清除 ✗")
	require.Equal(t, "disk usage 95.0% >= 90.0%", pipe.Ledger().Get(pipe.Key()).PauseReason)
	require.True(t, acquire.IsBacklogPauseReason(legacyBacklogPauseReason))
	require.False(t, acquire.IsBacklogPauseReason("disk usage 95.0% >= 90.0%"))
}
