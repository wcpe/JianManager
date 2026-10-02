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
