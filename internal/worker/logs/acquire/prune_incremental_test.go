package acquire

// #66-① 的红/绿：`pruneReclaimed` 的单次成本必须与"**剩余条数**"无关。
//
// 现场（#65 归因）：单跳 ~1s 的主项之一是 `append([]WALEntry(nil), kept...)` —— 旧实现每次 prune
// 都**复制全部剩余条目**（WALEntry 含整个 Event ≈250B ⇒ 20 万条 ≈ 50MB 拷贝 ✗✗），而 prune 每次
// `TryReclaim` 都会跑（一次 hop 里 1–2 次 ✓）。
//
// 修后：**头偏移窗 + 已删前缀置零**（单次 = O(被删条数)，每条只置零一次 ⇒ 全生命周期摊销 O(1)/条 ✓），
// 并每 `pruneFullSweepEvery` 次做一次完整清扫兜"乱序条目"（归档补账会追加更早的 record 范围 ✓）。
//
// 等价性 oracle：**旧算法本身**（全表过滤）—— 清扫后的保留集合必须与之逐条一致 ✓。

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// referencePrune 是**旧实现**（全表过滤 + 复制剩余）：等价性 oracle ✓。
func referencePrune(entries []WALEntry, pos uint64) []WALEntry {
	out := make([]WALEntry, 0, len(entries))
	for _, e := range entries {
		if e.Durable && e.Event.Record.End <= pos {
			continue
		}
		out = append(out, e)
	}
	return out
}

// pruneFixture 造 n 条条目：前 droppable 条可回收（End ≤ pos），其余超水位（不可回收）✓。
func pruneFixture(t *testing.T, n, droppable int) (*WAL, []WALEntry, uint64) {
	t.Helper()
	key := testKey("src-prune", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(1<<30, 1<<40) // 本用例只考 prune 成本：闸给足，避免 Restore 触发暂停 ✓
	identity := logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"}
	entries := make([]WALEntry, 0, n)
	for i := 0; i < n; i++ {
		var end uint64
		if i < droppable {
			end = uint64(i*10 + 10) // 可回收：End ≤ pos
		} else {
			end = uint64(1_000_000 + i*10) // 超水位：必须保留
		}
		entries = append(entries, WALEntry{
			Seq: uint64(i + 1), Durable: true,
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", fmt.Sprintf("prune-%d", i)),
		})
	}
	require.NoError(t, wal.Restore(entries))
	pos := uint64(droppable*10 + 7) // 覆盖前 droppable 条的末端 ✓
	return wal, entries, pos
}

func TestPruneReclaimedCostIndependentOfRemainingEntries(t *testing.T) {
	const total = 200_000
	const droppable = 1_000
	wal, entries, pos := pruneFixture(t, total, droppable)

	start := time.Now()
	wal.pruneReclaimed(pos)
	elapsed := time.Since(start)
	t.Logf("#66-① 观测：剩余 %d 条、可回收 %d 条 ⇒ 单次 prune 用时 %s（旧实现复制剩余全部 ≈50MB ✗）",
		total-droppable, droppable, elapsed)
	require.Less(t, elapsed, 2*time.Millisecond,
		"单次 prune 必须与剩余条数无关（旧实现 O(剩余) ⇒ 现场单跳 ~1s ✗）；实测 %s", elapsed)

	// 等价性：保留集合必须与旧算法逐条一致（顺序 + Seq ✓）。
	want := referencePrune(entries, pos)
	got := wal.Snapshot()
	require.Len(t, got, len(want), "保留条数必须一致（旧算法 oracle ✓）")
	for i := range want {
		require.Equal(t, want[i].Seq, got[i].Seq, "第 %d 条保留序位必须一致", i)
		require.Equal(t, want[i].Event.EventID, got[i].Event.EventID)
	}
}

// TestPruneReclaimedCollectsOutOfOrderEntriesOnSweep：乱序条目（可回收但不在头部）由**周期性完整清扫**
// 收掉 ⇒ 与旧算法的差异只在"晚回收 ≤ N 次 prune"（纯内存节奏，语义无影响 ✓）。
func TestPruneReclaimedCollectsOutOfOrderEntriesOnSweep(t *testing.T) {
	key := testKey("src-prune-ooo", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(1<<30, 1<<40)
	identity := logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"}
	mk := func(seq uint64, start, end uint64, msg string) WALEntry {
		return WALEntry{Seq: seq, Durable: true,
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: end},
				"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", msg)}
	}
	// 头部不可回收（超水位）⇒ 前缀为 0；其后夹一条**可回收**条目（乱序：归档补账会追加更早 range ✓）。
	entries := []WALEntry{
		mk(1, 1_000_000, 1_000_010, "keep-head"),
		mk(2, 10, 20, "droppable-out-of-order"),
		mk(3, 2_000_000, 2_000_010, "keep-tail"),
	}
	require.NoError(t, wal.Restore(entries))
	pos := uint64(100) // 只覆盖第 2 条 ✓

	wal.pruneReclaimed(pos)
	require.Len(t, wal.Snapshot(), 3, "乱序条目在第一次 prune 时**允许**延后（有界 ✓）")

	for i := 0; i < pruneFullSweepEvery; i++ {
		wal.pruneReclaimed(pos)
	}
	got := wal.Snapshot()
	require.Len(t, got, 2, "周期性完整清扫必须收掉乱序条目（与旧算法有限步内等价 ✓）")
	require.Equal(t, uint64(1), got[0].Seq)
	require.Equal(t, uint64(3), got[1].Seq)
}
