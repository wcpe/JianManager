package ingest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
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

	// 造出**现场形态**：积压类暂停（积压本身已为零 ⇒ 恢复判定必须绑定"活着的读数"而非 Append ✗）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))
	require.True(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused)

	var mu sync.Mutex
	probed := 0
	m.resumeProbe = func(string) { mu.Lock(); probed++; mu.Unlock() }
	m.resumeInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunCapacityResumeLoop(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		n := probed
		mu.Unlock()
		return n > 0
	}, 3*time.Second, 10*time.Millisecond, "兜底循环必须扫到积压类暂停的源（不得只在 Append 处重估 ✗）")

	require.Eventually(t, func() bool {
		got := pipe.Ledger().Get(pipe.Key())
		return got != nil && !got.AcquirePaused
	}, 3*time.Second, 10*time.Millisecond, "零积压的源必须被兜底恢复到活 ✓")

	// 红线：非积压/容量类暂停不得被清除 ✓。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "disk usage 95.0% >= 90.0%"))
	// 非积压/容量类暂停会被**跳过**（不进处理分支）⇒ 多轮之后仍必须停在暂停上 ✓。
	time.Sleep(120 * time.Millisecond) // 至少 5 个兜底周期
	require.True(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused, "非本包原因不得被容量兜底清除 ✗")
	require.Equal(t, "disk usage 95.0% >= 90.0%", pipe.Ledger().Get(pipe.Key()).PauseReason)
	require.True(t, acquire.IsBacklogPauseReason("wal backlog limit exceeded (entries=5/4)"))
	require.False(t, acquire.IsBacklogPauseReason("disk usage 95.0% >= 90.0%"))
}
