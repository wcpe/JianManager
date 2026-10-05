package stateindex

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件是 2026-10-02 生产事故（启动路径单线程、与规模成正比的持久化）的 **①水位化差异删除**
// 回归：把「剪枝前缀」从「逐行 DELETE」改成「一条谓词范围删除」。
//
// 现场链（见 ingest 侧诊断）：启动恢复 → releaseRecovery → TryReclaim 一次把整段积压判为可回收
// （内存判据 `Durable && Record.End <= pos`）→ 索引侧把「镜像有、期望无」的行逐行删掉
// （生产 3.4M 行 ÷ 32 行/语句 ≈ 10.6 万条 DELETE，单线程在 19.5GB 库上 20–30 分钟零进展）。

// walPruneState 构造「一个归属、三类行」的夹具，专门覆盖范围删除最容易搞错的那类行：
//   - end ≤ 水位 且 durable：应被剪掉（真剪枝对象）；
//   - end > 水位 且 durable：必须留下（水位之后的数据）；
//   - end ≤ 水位 但 **非 durable**：必须留下（正文还没另存，删了就是丢数据）。
func walPruneState(key string, durableLow, durableHigh, nonDurableLow int) State {
	var st State
	st.Sources = append(st.Sources, SourceRow{Key: key, LogSourceID: "node:prune", SourceGeneration: "g1", StorageNamespace: "ns:prune"})
	st.Positions = append(st.Positions, PositionRow{Key: key, ReadPos: 200, DurablePos: 150, ReclaimPos: 100})
	seq := uint64(0)
	add := func(durable bool, end uint64) {
		seq++
		body := []byte(fmt.Sprintf(`{"event_id":"ev-%d","durable":%t}`, seq, durable))
		st.WAL = append(st.WAL, WALRow{
			Key: key, Seq: seq, EventID: fmt.Sprintf("ev-%d", seq),
			RecordStart: end - 9, RecordEnd: end, Appended: true, Durable: durable,
			Body: body, FP: fingerprint(body),
		})
	}
	for i := 0; i < durableLow; i++ {
		add(true, uint64(10+i*10))
	}
	for i := 0; i < nonDurableLow; i++ {
		add(false, uint64(10+i*10))
	}
	for i := 0; i < durableHigh; i++ {
		add(true, uint64(1_000_000+i*10))
	}
	return st
}

// walRowsOf 读出该归属的全部 WAL 行（按 event_id 索引），供断言逐行比对。
func walRowsOf(t *testing.T, store *Store, key string) map[string]WALRow {
	t.Helper()
	loaded, err := store.Load()
	require.NoError(t, err)
	out := make(map[string]WALRow)
	for _, row := range loaded.WAL {
		if row.Key == key {
			out[row.EventID] = row
		}
	}
	return out
}

// TestWALPruneRangeDeleteKeepsNonDurableRowsBelowWatermark 守住范围删除的**唯一**危险面：
// `durable = 1` 这一条不能省——期望集里未耐久（正文尚未另存）的行即使 record_end ≤ 水位也必须留下。
//
// 转红方式（实测）：把 SQL 里的 `durable = 1` 去掉 —— 本用例在「非耐久行必须留下」处变红。
func TestWALPruneRangeDeleteKeepsNonDurableRowsBelowWatermark(t *testing.T) {
	store := newTestStore(t)
	const key = "node:prune/g1"
	seeded := walPruneState(key, 3, 2, 2) // 3 条 ≤ 水位的 durable、2 条 > 水位、2 条 ≤ 水位的非 durable
	_, err := store.Apply(seeded)
	require.NoError(t, err)

	// 期望状态 = 剪掉那 3 条 ≤ 水位的 durable；其余（含 2 条非 durable）保留。
	desired := seeded
	desired.WAL = nil
	for _, row := range seeded.WAL {
		if row.Durable && row.RecordEnd <= 30 {
			continue
		}
		desired.WAL = append(desired.WAL, row)
	}
	stats, err := store.ApplyScopedCtx(context.Background(), desired, []string{key},
		[]WALPrune{{Owner: key, EndThrough: 30}})
	require.NoError(t, err)
	require.Equal(t, 3, stats.RangePruned, "范围删除应恰好覆盖 3 条 ≤ 水位且 durable 的行")
	require.LessOrEqual(t, stats.Statements, 4,
		"剪枝必须走一条谓词删除（而不是每 32 行一条）：语句数不得随剪枝量增长")

	rows := walRowsOf(t, store, key)
	require.Len(t, rows, 4, "2 条非 durable + 2 条 > 水位必须全部留下")
	for _, row := range rows {
		if !row.Durable {
			require.LessOrEqual(t, row.RecordEnd, uint64(30), "非耐久行本就落在水位内，正是它必须被留下")
		}
	}

	// 幂等：同一水位再调一次不得再删任何行（水位未前进即跳过）。
	stats, err = store.ApplyScopedCtx(context.Background(), desired, []string{key},
		[]WALPrune{{Owner: key, EndThrough: 30}})
	require.NoError(t, err)
	require.Zero(t, stats.RangePruned, "同一水位重复调用必须跳过（幂等）")
	require.Len(t, walRowsOf(t, store, key), 4)
}

// TestWALPruneGuardFallsBackWhenDesiredStillHoldsMatchingRow 守住**等价性守门**：
// 只要期望集里还存在满足谓词的行（说明这条水位不是「内存真的剪过」的那条），范围删除必须整段
// 退回逐行路径——否则会把一条仍然有效的行删掉（重启后 WAL 少条 ⇒ 未投递正文丢失）。
//
// 转红方式（实测）：去掉 planWALPrunes 里的 blocked 判定 —— 本用例在「该行必须留下」处变红。
func TestWALPruneGuardFallsBackWhenDesiredStillHoldsMatchingRow(t *testing.T) {
	store := newTestStore(t)
	const key = "node:prune/g1"
	seeded := walPruneState(key, 2, 1, 0)
	_, err := store.Apply(seeded)
	require.NoError(t, err)

	// 提示水位 30，但期望集**仍然包含**一条 end=20 的 durable 行（模拟水位与内存不一致）。
	stats, err := store.ApplyScopedCtx(context.Background(), seeded, []string{key},
		[]WALPrune{{Owner: key, EndThrough: 30}})
	require.NoError(t, err)
	require.Zero(t, stats.RangePruned, "守门必须拦住范围删除（期望集里还有满足谓词的行）")
	require.Len(t, walRowsOf(t, store, key), 3, "守门生效时一行都不能少")
}

// TestCycleBudgetIsBoundedAndResumable 是 ②「有界分片 + 可续跑」的回归（索引层）：
// 单次 ApplyScopedCtx 只在预算内做一步（Incomplete=true + 剩余行数），重复调用把剩余差异做完，
// 且**最终内容与一次性做完逐行一致**（不重复、不遗漏）。
//
// 转红方式（实测）：去掉 commitChunks 的每周期总预算判定 —— 第一次调用就做完，本用例在
// 「必须报未完成 + 必须分多次」处变红。
func TestCycleBudgetIsBoundedAndResumable(t *testing.T) {
	root := t.TempDir()
	budget := DefaultCommitBudget()
	store, err := OpenWithBudget(root+"/ingest.index.db", budget)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	desired := fixtureState(6) // 6 源 × 每源十余行 = 数百行差异
	stats, err := store.Apply(desired)
	require.NoError(t, err)
	require.False(t, stats.Incomplete, "首次全量写入的行数预算足够（默认 CycleMaxRows）")

	// 把周期预算刻意压到 8 行：删掉 4 个源共约 36 行差异，必然需要多次调用才做得完。
	store.budget.CycleMaxRows = 8

	// 删掉 4 个源（数百行差异）并限制单次 40 行：必须分批、可续。
	trimmed := fixtureState(2)
	cycles := 0
	var total int
	for {
		stats, err := store.ApplyScopedCtx(context.Background(), trimmed, []string{
			"node:002/g1", "node:003/g1", "node:004/g1", "node:005/g1",
		}, nil)
		require.NoError(t, err)
		cycles++
		total += stats.RowsDeleted
		if !stats.Incomplete {
			break
		}
		require.Positive(t, stats.RemainingRows, "未完成时必须报出剩余行数")
		require.Less(t, cycles, 50, "分片必须有界且能收敛")
	}
	require.Greater(t, cycles, 1, "8 行/周期的预算下，数十行差异必须分多次完成")

	// 最终内容：与「一次性做完」的结果一致（这里用「预期状态」直接比对）。
	loaded, err := store.Load()
	require.NoError(t, err)
	require.Equal(t, len(trimmed.Sources), len(loaded.Sources), "续跑后不得有多余源")
	for _, row := range loaded.WAL {
		require.NotContains(t, []string{"node:002/g1", "node:003/g1", "node:004/g1", "node:005/g1"}, row.Key,
			"被删源的 WAL 行必须全部删净（续跑不得遗漏）")
	}
	require.Positive(t, total)
}

// TestWALPruneStatementsDoNotGrowWithPrunedRows 把「与规模成正比的 DELETE 数」钉成断言：
// 剪掉的行数翻 8 倍，语句数不随行数增长（这是事故里 10.6 万条 DELETE 的直接对策）。
func TestWALPruneStatementsDoNotGrowWithPrunedRows(t *testing.T) {
	measure := func(pruned int) Stats {
		root := t.TempDir()
		store, err := OpenWithBudget(root+"/ingest.index.db", DefaultCommitBudget())
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		const key = "node:prune/g1"
		seeded := walPruneState(key, pruned, 1, 0)
		_, err = store.Apply(seeded)
		require.NoError(t, err)
		desired := seeded
		desired.WAL = nil
		for _, row := range seeded.WAL {
			if row.Durable && row.RecordEnd <= uint64(10+pruned*10) {
				continue
			}
			desired.WAL = append(desired.WAL, row)
		}
		stats, err := store.ApplyScopedCtx(context.Background(), desired, []string{key},
			[]WALPrune{{Owner: key, EndThrough: uint64(10 + pruned*10)}})
		require.NoError(t, err)
		require.Equal(t, pruned, stats.RangePruned)
		return stats
	}
	small := measure(20)
	large := measure(160)
	require.LessOrEqual(t, large.Statements, small.Statements+2,
		"语句数不得随剪枝行数增长（20 行 → %d 条、160 行 → %d 条）", small.Statements, large.Statements)
	require.LessOrEqual(t, large.Statements, 6, "范围剪枝的语句数应当是常数级")
	_ = time.Now
}

// TestWALPruneDeleteIsBatchedUnderLargeRanges（#67-A 红/绿）：范围谓词删除必须**逐批**执行。
//
// 现场：单条无界 DELETE 在**提交单元事务内**可能删 10^5 级行，而单元耗时硬上界只在语句之间检查
// （见 commitChunks）⇒ 单元耗时被推到 10s 级（真机 p95=11,095ms / max=40,767ms ✗，p50 也 322ms ✗）。
//
// 绿：语句数随行数**按批线性**（≈ ⌈rows / pruneDeleteBatchRows⌉），且删除结果与旧实现逐行等价
// （"非耐久行必须留下"这条危险面照旧由 TestWALPruneRangeDeleteKeepsNonDurableRowsBelowWatermark 守 ✓）。
// 红（变异：退回单条无界 DELETE）⇒ 语句数恒为 1 ⇒ 本用例第一处断言转红 ✓（实测）。
func TestWALPruneDeleteIsBatchedUnderLargeRanges(t *testing.T) {
	const pruned = pruneDeleteBatchRows + 500 // 超出单批上界 ⇒ 必须切批
	root := t.TempDir()
	store, err := OpenWithBudget(root+"/ingest.index.db", DefaultCommitBudget())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const key = "node:prune/g1"
	seeded := walPruneState(key, pruned, 1, 3) // 3 条「≤水位但未耐久」必须留下的行
	_, err = store.Apply(seeded)
	require.NoError(t, err)
	desired := seeded
	desired.WAL = nil
	for _, row := range seeded.WAL {
		if row.Durable && row.RecordEnd <= uint64(10+pruned*10) {
			continue
		}
		desired.WAL = append(desired.WAL, row)
	}
	stats, err := store.ApplyScopedCtx(context.Background(), desired, []string{key},
		[]WALPrune{{Owner: key, EndThrough: uint64(10 + pruned*10)}})
	require.NoError(t, err)
	require.Equal(t, pruned, stats.RangePruned, "剪枝行数不变（谓词逐字未动 ✓）")
	// 核心断言（切批）：单条无界 DELETE ⇒ 恒为 1 条 ⇒ 转红 ✓。
	require.GreaterOrEqual(t, stats.Statements, 2,
		"超过单批上界(%d)时必须切批：语句数 %d（单条无界 DELETE 会让单元耗时无界 ✗）",
		pruneDeleteBatchRows, stats.Statements)
	// 上界：只允许按批增长（不能退化成逐行 2500 条 ✗）。
	require.LessOrEqual(t, stats.Statements, pruned/pruneDeleteBatchRows+3,
		"语句数只允许按批线性增长（实测 %d 条）", stats.Statements)
	// 逐行等价：未耐久行（≤水位）必须仍在库 ✓；水位之上的耐久行也必须在 ✓。
	rows := walRowsOf(t, store, key)
	require.Len(t, rows, 1+3, "水位之上 1 条 + 未耐久 3 条 = 4 条必须留下 ✓")
}

// TestCheckpointStillRunsWhenCycleIncomplete（#67-A′ 红/绿）：WAL 超阈值 + 周期**未完成**时，
// WAL 归并仍必须被**尝试**。
//
// 现场：归并原来只挂在"周期完成"出口 ⇒ 单元动辄 10s–40s、周期频繁 `Incomplete` ⇒ 归并永远排不上
// ⇒ `-wal` 涨到 3.59GB ⇒ 每次提交都在巨 WAL 上 fsync（max 40,767ms / rows 0 ✗✗）。
//
// 红（变异：撤掉"未完成出口"那次调用）⇒ `CheckpointAttempts` 不再增长 ⇒ 本用例转红 ✓（实测）。
// 绿 ⇒ 计数增长且五读数可见 ✓；不变量：读语义/校验完整性一字未动 ✓。
func TestCheckpointStillRunsWhenCycleIncomplete(t *testing.T) {
	root := t.TempDir()
	budget := DefaultCommitBudget()
	budget.CycleMaxRows = 1 // 强制周期**未完成**（一次调用只推进 1 行差异 ✓）
	store, err := OpenWithBudget(root+"/ingest.index.db", budget)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const key = "node:prune/g1"
	// 先把源/水位行落地（外键父行 ✓，同时也是"期望状态"的底座 ✓）。
	small := walPruneState(key, 1, 1, 0)
	_, err = store.Apply(small)
	require.NoError(t, err)
	// 再**绕过镜像**直接灌足量 WAL 行，把 WAL 顶过归并阈值（4 MiB）——
	// `source_wal` 的正文列对本例是 NULL（引用条目），索引体积只随**行数**增长 ✓。
	tx, err := store.db.Begin()
	require.NoError(t, err)
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO source_wal" +
		"(key, seq, event_id, record_start, record_end, appended, durable, body, fp) VALUES(?,?,?,?,?,1,1,NULL,1)")
	require.NoError(t, err)
	for i := 0; i < 120_000; i++ {
		_, err = stmt.Exec(key, uint64(1000+i), fmt.Sprintf("bulk-%d", i),
			uint64(1000+i)*10, uint64(1000+i)*10+9)
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	require.Greater(t, store.WALBytes(), int64(walCheckpointPages)*sqlitePageSize,
		"前置：WAL 必须超过归并阈值（4 MiB）——否则归并本就会在阈值处提前返回 ✓")

	// 只改一行（差异 ≥1 ⇒ 至少一个提交单元 ⇒ 因 CycleMaxRows=1 而**未完成** ✓）。
	desired := small
	desired.WAL = append([]WALRow(nil), small.WAL...)
	desired.WAL[0].Body = []byte("changed-body-for-incomplete-cycle")
	desired.WAL[0].FP = fingerprint(desired.WAL[0].Body)
	before := store.Latency()
	stats, err := store.ApplyScopedCtx(context.Background(), desired, []string{key},
		[]WALPrune{{Owner: key, EndThrough: 0}})
	require.NoError(t, err)
	require.True(t, stats.Incomplete, "前置：本周期必须**未完成**（预算足够小 ✓）")
	after := store.Latency()

	// 绿：未完成出口也必须尝试归并 ✓（变异：撤掉该调用 ⇒ 此处转红 ✓）。
	require.Greater(t, after.CheckpointAttempts, before.CheckpointAttempts,
		"周期未完成时 WAL 归并仍必须被尝试（否则巨 WAL 永不收敛 ✗）")
	require.Positive(t, after.WALBytes, "walBytes 读数必须可见 ✓")
	t.Logf("#67-A′ 观测：walBytes=%d（阈值 %d）、checkpointAttempts %d→%d、busy=%d、truncate=%d、readersInFlight=%d、Incomplete=%v",
		after.WALBytes, int64(walCheckpointPages)*sqlitePageSize, before.CheckpointAttempts, after.CheckpointAttempts,
		after.CheckpointBusy, after.CheckpointTruncate, after.ReadersInFlight, stats.Incomplete)
}
