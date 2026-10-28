package stateindex

// 本文件原是一批「①水位化差异删除」（source_wal 谓词范围删除）的回归。
//
// 2026-10-29：WAL 改为纯内存队列后 `source_wal` 表整体删除，那一整套（WALPrune /
// planWALPrunes / applyWALPrune / walMirrorKeyAtOrBelow）也一并下线；本文件里只保留
// 与**表无关**的存储层不变量：周期预算有界可续跑、WAL 归并的未完成出口、受控截断的
// 三道门、readersInFlight 配对、cycleRows 覆盖后复原。
//
// 删掉的是：TestWALPruneRangeDeleteKeepsNonDurableRowsBelowWatermark、
// TestWALPruneGuardFallsBackWhenDesiredStillHoldsMatchingRow、
// TestWALPruneStatementsDoNotGrowWithPrunedRows、TestWALPruneDeleteIsBatchedUnderLargeRanges、
// 以及夹具 walPruneState / walRowsOf —— 它们断言的都是「source_wal 行在库里怎么被删」，
// 现在既没有那张表也没有那条路径，保留只能靠放弃断言换取绿灯。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// pruneFixtureState 构造「一个源 + 若干 gap/batch 行」的夹具，供「周期预算有界可续跑」
// 与「WAL 归并」两类用例把库灌到足够大。
func pruneFixtureState(key string, gaps int) State {
	var st State
	st.Sources = append(st.Sources, SourceRow{Key: key, LogSourceID: "node:prune", SourceGeneration: "g1", StorageNamespace: "ns:prune"})
	st.Positions = append(st.Positions, PositionRow{Key: key, ReadPos: 200, DurablePos: 150, ReclaimPos: 100})
	st.Projections = append(st.Projections, ProjectionRow{Key: key, Generation: "projection-1"})
	st.Aux = append(st.Aux, AuxRow{Key: key, Payload: []byte(`{}`)})
	for i := 0; i < gaps; i++ {
		st.Gaps = append(st.Gaps, GapRow{
			Key: key, ID: int64(i), StartPos: uint64(i * 10), EndPos: uint64(i*10 + 9),
			Reason: "PAUSED", Detail: fmt.Sprintf("gap-%d", i),
		})
	}
	for i := 0; i < gaps/2; i++ {
		st.Batches = append(st.Batches, BatchRow{
			Key: key, Ordinal: int64(i), StartPos: uint64(i * 10), EndPos: uint64(i*10 + 9), State: "REQUEST_DONE",
		})
	}
	return st
}

// bulkInsertGaps 绕过镜像直接灌 gap 行，用索引 WAL 把库顶到归并阈值以上。
func bulkInsertGaps(t *testing.T, store *Store, key string, start, count int) {
	t.Helper()
	tx, err := store.db.Begin()
	require.NoError(t, err)
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO gap" +
		"(key, id, start_pos, end_pos, reason, detail, resolved, fold_span_merges, reason_code, resolved_at_utc, resolved_by, fold_spans_unknown, first_seen_unix)" +
		" VALUES(?,?,?,?,?,?,0,0,'','','',0,0)")
	require.NoError(t, err)
	for i := 0; i < count; i++ {
		_, err = stmt.Exec(key, int64(start+i), uint64(start+i)*10, uint64(start+i)*10+9, "PAUSED", fmt.Sprintf("bulk-%d", start+i))
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
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

	// 把周期预算刻意压到 8 行：删掉 4 个源共数十行差异，必然需要多次调用才做得完。
	store.budget.CycleMaxRows = 8

	trimmed := fixtureState(2)
	cycles := 0
	var total int
	for {
		stats, err := store.ApplyScopedCtx(context.Background(), trimmed, []string{
			"node:002/g1", "node:003/g1", "node:004/g1", "node:005/g1",
		})
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
	for _, row := range loaded.Gaps {
		require.NotContains(t, []string{"node:002/g1", "node:003/g1", "node:004/g1", "node:005/g1"}, row.Key,
			"被删源的 gap 行必须全部删净（续跑不得遗漏）")
	}
	require.Positive(t, total)
}

// TestCheckpointStillRunsWhenCycleIncomplete（#67-A′ 红/绿）：WAL 超阈值 + 周期**未完成**时，
// WAL 归并仍必须被**尝试**。
//
// 现场：归并原来只挂在"周期完成"出口 ⇒ 单元动辄 10s–40s、周期频繁 `Incomplete` ⇒ 归并永远排不上
// ⇒ `-wal` 涨到 3.59GB ⇒ 每次提交都在巨 WAL 上 fsync（max 40,767ms / rows 0 ✗✗）。
//
// 红（变异：撤掉"未完成出口"那次调用）⇒ `CheckpointAttempts` 不再增长 ⇒ 本用例转红 ✓（实测）。
func TestCheckpointStillRunsWhenCycleIncomplete(t *testing.T) {
	root := t.TempDir()
	budget := DefaultCommitBudget()
	budget.CycleMaxRows = 1 // 强制周期**未完成**（一次调用只推进 1 行差异 ✓）
	store, err := OpenWithBudget(root+"/ingest.index.db", budget)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const key = "node:prune/g1"
	// 先把源/水位行落地（外键父行 ✓，同时也是"期望状态"的底座 ✓）。
	small := pruneFixtureState(key, 2)
	_, err = store.Apply(small)
	require.NoError(t, err)
	bulkInsertGaps(t, store, key, 1000, 120_000)
	require.Greater(t, store.WALBytes(), int64(walCheckpointPages)*sqlitePageSize,
		"前置：WAL 必须超过归并阈值（4 MiB）——否则归并本就会在阈值处提前返回 ✓")

	// 只改一行（差异 ≥1 ⇒ 至少一个提交单元 ⇒ 因 CycleMaxRows=1 而**未完成** ✓）。
	desired := small
	desired.Gaps = append([]GapRow(nil), small.Gaps...)
	desired.Gaps[0].Detail = "changed-detail-for-incomplete-cycle"
	before := store.Latency()
	stats, err := store.ApplyScopedCtx(context.Background(), desired, []string{key})
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

// TestControlledTruncateOffByDefaultAndGated（#67-B 分件红/绿）：
//
//	(c) 默认关：门全满足也**不得**截断（变异：去掉开关判断 ⇒ 第一处断言转红 ✓）；
//	(a) 残余门：只有 WAL 已 ≤ 64 MiB 才允许"短截断"（3.6GB 级页拷贝绝不在此做 ✗）；
//	(b) 锁外执行：持锁时 `TryLock` 必败 ⇒ **让路**（绝不等待 ✗）——这正是 74min 停摆的对症防呆 ✓。
func TestControlledTruncateOffByDefaultAndGated(t *testing.T) {
	root := t.TempDir()
	store, err := OpenWithBudget(root+"/ingest.index.db", DefaultCommitBudget())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const key = "node:prune/g1"
	_, err = store.Apply(pruneFixtureState(key, 2)) // 外键父行 ✓
	require.NoError(t, err)
	bulkInsertGaps(t, store, key, 2000, 120_000)
	require.Greater(t, store.WALBytes(), int64(walCheckpointPages)*sqlitePageSize, "前置：WAL 超归并阈值 ✓")
	require.Less(t, store.WALBytes(), int64(walTruncateResidualBytes), "前置：WAL 在残余门内（允许短截断的窗口 ✓）")

	// (c) 默认关 ⇒ 即使门全满足也必须拒绝 ✓（变异：删掉 `!s.controlledTruncate` 判断 ⇒ 此处转红 ✓）。
	require.False(t, store.TryControlledTruncate(context.Background()), "默认关必须拒绝 ✓")
	require.Zero(t, store.Latency().CheckpointTruncate, "默认关时截断计数必须为 0 ✓")

	// (a)+(b) 开启后：TryLock 成功 + 门满足 ⇒ 允许短截断，WAL 截回极小 ✓。
	store.SetControlledTruncate(true)
	require.True(t, store.TryControlledTruncate(context.Background()), "开启且门满足时必须截断 ✓")
	require.Equal(t, int64(1), store.Latency().CheckpointTruncate, "截断计数必须 +1 ✓")
	require.Less(t, store.WALBytes(), int64(walTruncateResidualBytes), "截断后 WAL 应回到极小 ✓")

	// (b) 持锁（= 有在飞 apply/读）⇒ TryLock 必败 ⇒ 让路、不等待 ✓。
	store.mu.Lock()
	require.False(t, store.TryControlledTruncate(context.Background()), "持锁时必须让路（绝不等待 ✗）✓")
	store.mu.Unlock()
}

// 回归（M1，2026-10-12 修复）：`TryControlledTruncate` 的「无在飞只读句柄」护栏必须
// 能看到**另一个 Store 实例**（OpenReadOnly）持有的只读句柄。
//
// 转红方式（实测）：把 `readersInFlight` 改回实例字段（两处构造点各自 `new(atomic.Int64)`）
// ⇒ 断言②（写实例必须看到只读实例的计数）立即红。
func TestControlledTruncateSeesReadOnlyHandlesFromOtherInstances(t *testing.T) {
	root := t.TempDir()
	path := root + "/ingest.index.db"
	writer, err := OpenWithBudget(path, DefaultCommitBudget())
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })

	// 前置：写实例当前看不到任何在飞读者。
	require.Equal(t, int64(0), writer.readersInFlight.Load(), "前置：无读者时应为 0")

	// 另一个实例持只读句柄（模拟导出 JSON / 事件状态查询）。
	reader, err := OpenReadOnly(path)
	require.NoError(t, err)

	// ① 护栏判据在**写实例**上必须看到它 —— 这正是修复点。
	require.Equal(t, int64(1), writer.readersInFlight.Load(),
		"写实例必须看到只读实例持有的句柄（否则 TryControlledTruncate 的护栏失效）")

	// ② 截断必须因此让路（护栏的实际作用面）。
	writer.mu.Lock()
	writer.controlledTruncate = true // 绕过开关，直接测护栏本身
	writer.mu.Unlock()
	require.False(t, writer.TryControlledTruncate(context.Background()),
		"有在飞只读句柄时不得执行受控截断（会与读者争抢，重现 2026-10-05 停摆机制）")

	// ③ 读者关闭后护栏解除。
	require.NoError(t, reader.Close())
	require.Equal(t, int64(0), writer.readersInFlight.Load(), "只读句柄关闭后计数必须归还")
}

// 回归（2026-10-12 复审）：`readersInFlight` 的增减必须**严格配对**——两个方向都不能破。
//
// 转红方式（实测）：去掉 `Close` 的 `readerReleased` CAS 保护 ⇒ ② 红（重复关闭把计数打成 -1）。
func TestReadersInFlightIncrementDecrementArePaired(t *testing.T) {
	root := t.TempDir()
	path := root + "/ingest.index.db"
	writer, err := OpenWithBudget(path, DefaultCommitBudget())
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	require.Equal(t, int64(0), writer.readersInFlight.Load(), "前置：无读者时应为 0")

	// ① 正常配对：开 → 1，关 → 0，且可重复多轮（不得漂移）。
	for round := 0; round < 3; round++ {
		reader, openErr := OpenReadOnly(path)
		require.NoError(t, openErr)
		require.Equal(t, int64(1), writer.readersInFlight.Load(),
			"第 %d 轮：只读句柄存活期间计数必须为 1", round)
		require.NoError(t, reader.Close())
		require.Equal(t, int64(0), writer.readersInFlight.Load(),
			"第 %d 轮：关闭后必须精确归还到 0（漂移会让护栏永久失效）", round)
	}

	// ② 重复 Close 不得二次归还（否则计数为负，护栏同样永久关闭）。
	reader, err := OpenReadOnly(path)
	require.NoError(t, err)
	require.Equal(t, int64(1), writer.readersInFlight.Load())
	require.NoError(t, reader.Close())
	require.NoError(t, reader.Close(), "重复 Close 必须幂等（不得报错也不得二次归还）")
	require.Equal(t, int64(0), writer.readersInFlight.Load(),
		"重复 Close 后计数仍须为 0：负值与正值一样会让 TryControlledTruncate 永久让路")
}

// 回归（M2，2026-10-12 修复）：`ApplyScopedCtxRows` 的 `cycleRows` 覆盖必须在调用返回后
// **恢复**原预算，不得永久钉死该 Store 实例的周期行数上界。
//
// 转红方式（实测）：把恢复逻辑改回 `defer func(original int){...}(s.budget.CycleMaxRows)`
// ⇒ 断言②（调用后预算必须复原）立即红。
func TestApplyScopedCtxRowsRestoresCycleBudget(t *testing.T) {
	root := t.TempDir()
	store, err := OpenWithBudget(root+"/ingest.index.db", DefaultCommitBudget())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	before := store.budget.CycleMaxRows
	require.Positive(t, before, "前置：默认周期预算应为正")

	const small = 16
	require.NotEqual(t, before, small, "前置：覆盖值必须与默认值不同，否则本用例无判别力")

	// 一次最小调用（空期望集）只为触发覆盖与恢复路径。
	_, err = store.ApplyScopedCtxRows(context.Background(), State{}, nil, small)
	require.NoError(t, err)

	// 核心断言：调用返回后必须复原，不得永久钉死。
	require.Equal(t, before, store.budget.CycleMaxRows,
		"cycleRows 覆盖必须在调用返回后复原（否则该实例的周期预算被永久钉死 ⇒ 后续落库步长退化）")
}
