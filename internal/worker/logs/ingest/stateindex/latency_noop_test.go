package stateindex

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件钉住一条读数口径：**空转周期不得冒充提交单元**。
//
// 为什么这条需要单独一个文件（FR-498 2026-10-27 真机定位）：生产 p50=2ms 与 p95=107ms
// 相差两个量级，看起来像「提交偶发极度变慢」。实测发现 p95 的主体根本不是提交——
// 空转周期（零差异、不开事务、一条 SQL 都不发）也会记一条 Sample，而它的 Duration
// 是**整个 ApplyScoped 调用**的墙钟，含 scopeState + Validate + planState 的全部规划成本。
// 生产每周期规划均值一百多毫秒（nsPlan 累计 600s 窗口内占 85%），于是这些「一行没写」
// 的样本把 p95 抬到了验收线（≤50ms）之外。
//
// 验收线约束的对象是**提交单元**（原子单元 = 持写锁单元），所以分位只能拿真实提交算。

// TestLatencyExcludesNoopSamplesFromCommitPercentiles 验证空转样本不进 P50/P95/Max/Count。
func TestLatencyExcludesNoopSamplesFromCommitPercentiles(t *testing.T) {
	store := newTestStore(t)

	// Apply(State{}) 在空库上是幂等空转（无差异、无写入）——正好是空转样本。
	if _, err := store.Apply(State{}); err != nil {
		t.Fatalf("空库 Apply 失败: %v", err)
	}

	lat := store.Latency()
	require.Positive(t, lat.NoopCount, "空库 Apply 应记为空转样本")
	require.Zero(t, lat.Count, "空转不得计入提交单元个数")
	require.Zero(t, lat.P50, "没有提交单元时 P50 必须为 0，不能拿空转填")
	require.Zero(t, lat.P95, "没有提交单元时 P95 必须为 0，不能拿空转填")
	require.Zero(t, lat.Max, "没有提交单元时 Max 必须为 0，不能拿空转填")
	require.Positive(t, lat.NoopP95, "空转耗时仍需可观测（单列字段）")
}

// TestLatencyNoopP95IsSeparateFromCommitP95 用**人工注入的极端值**锁住分离语义：
// 一个 900ms 的空转样本绝不能把提交 P95 推到 900ms 量级。
//
// 用注入而不是真跑 900ms：要让真实规划跑出 900ms 得造百万行，那会让本用例变成负载敏感型
// （在本仓库的共享宿主上既慢又飘）。要验的是「分类逻辑」，不是「规划有多慢」。
func TestLatencyNoopP95IsSeparateFromCommitP95(t *testing.T) {
	store := newTestStore(t)

	store.mu.Lock()
	store.cycle++
	store.recordSample(Sample{
		StartedAt: time.Now(), Duration: 900 * time.Millisecond,
		CycleID: store.cycle, Chunks: 1, Noop: true,
	})
	store.cycle++
	store.recordSample(Sample{
		StartedAt: time.Now(), Duration: 3 * time.Millisecond,
		CycleID: store.cycle, Chunks: 1, RowsWritten: 5,
	})
	store.mu.Unlock()

	lat := store.Latency()
	require.Equal(t, 1, lat.NoopCount, "一条空转样本")
	require.Equal(t, 1, lat.Count, "一条提交单元样本")
	require.Equal(t, 3*time.Millisecond, lat.P50, "P50 只能来自提交单元")
	require.Equal(t, 3*time.Millisecond, lat.P95, "P95 只能来自提交单元")
	require.Equal(t, 3*time.Millisecond, lat.Max, "Max 只能来自提交单元")
	require.Equal(t, 900*time.Millisecond, lat.NoopP95, "空转耗时单列")
}

// TestLatencyNoopSamplesStillCountCycles 锁住「周期数」不受分类影响：
// Cycles 表达的是「采样环覆盖了多少个持久化周期」，空转周期也是周期，不能因不计分位而被吞掉。
func TestLatencyNoopSamplesStillCountCycles(t *testing.T) {
	store := newTestStore(t)

	store.mu.Lock()
	store.cycle++
	store.recordSample(Sample{CycleID: store.cycle, Chunks: 1, Noop: true})
	store.cycle++
	store.recordSample(Sample{CycleID: store.cycle, Chunks: 1, RowsWritten: 1})
	store.mu.Unlock()

	lat := store.Latency()
	require.Equal(t, 2, lat.Cycles, "两个周期都算在内")
	require.Equal(t, 1, lat.Count, "但提交单元只有一个")
	require.Equal(t, 1, lat.NoopCount)
}
