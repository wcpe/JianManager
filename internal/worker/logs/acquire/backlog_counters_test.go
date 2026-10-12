package acquire

// #66-② 的红/绿：`Backlog()/BacklogTotal()/ReplayInFlight()` 的**读取成本必须 O(1)**，
// 且三个读数与"全表重算"逐位一致。
//
// 现场（#65 归因第 4 项）：`backlogLocked` 由 `backlogTotalLocked`（Σ 全表字节）+ `replayInFlightLocked`
// （全表扫 Replay 条目）组成 ⇒ **两次 O(条数) 扫描**，而它被每次 `Append`（enforce 判闸）与每次
// 诊断读调用 ⇒ 10 万+ 条 WAL 上就是采集路径的固定税 ✗✗。
//
// 修后：三个计数在 Append/Restore/RestoreMixed/pruneReclaimed 处**增量维护** ⇒ 读数 O(1) ✓。
// 等价性 oracle：从 `Snapshot()` 全表重算同一口径的值（与旧实现逐字一致 ✓）。

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// backlogOracle 从全量快照重算三个读数（= 旧实现的算法 ✓ 等价性 oracle）。
func backlogOracle(w *WAL) (totalEntries, totalBytes, replayEntries, replayBytes int64) {
	for _, e := range w.Snapshot() {
		b := walEntryBytes(e)
		totalEntries++
		totalBytes += b
		if e.Replay {
			replayEntries++
			replayBytes += b
		}
	}
	return
}

func requireBacklogConsistent(t *testing.T, w *WAL, ctx string) {
	t.Helper()
	te, tb, re, rb := backlogOracle(w)
	gotTE, gotTB := w.BacklogTotal()
	gotRE, gotRB := w.ReplayInFlight()
	require.Equal(t, te, gotTE, "%s：总量条数必须与全表重算一致", ctx)
	require.Equal(t, tb, gotTB, "%s：总量字节必须与全表重算一致", ctx)
	require.Equal(t, re, gotRE, "%s：回放条数必须与全表重算一致", ctx)
	require.Equal(t, rb, gotRB, "%s：回放字节必须与全表重算一致", ctx)
}

func TestBacklogCountersIncrementalEquivalence(t *testing.T) {
	key := testKey("src-count", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(1<<30, 1<<40)
	identity := logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"}
	ev := func(msg string) logtypes.Event {
		return logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 10},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", msg)
	}
	requireBacklogConsistent(t, wal, "空 WAL")

	// Append（常规）+ AppendReplay（回放归属）逐条记账 ✓。
	require.NoError(t, wal.Append(ev("regular-1")))
	requireBacklogConsistent(t, wal, "Append 之后")
	require.NoError(t, wal.AppendReplay(ev("replay-1")))
	requireBacklogConsistent(t, wal, "AppendReplay 之后")
	require.NoError(t, wal.Append(ev("regular-2")))
	requireBacklogConsistent(t, wal, "再 Append 之后")
	re, _ := wal.ReplayInFlight()
	require.Equal(t, int64(1), re, "回放归属条数应为 1（方案① 口径 ✓）")
	require.NoError(t, wal.Commit())
	requireBacklogConsistent(t, wal, "Commit 之后")

	// prune（前缀删除）后记账必须跟着减 ✓；乱序条目由完整清扫收掉时同样记账 ✓。
	pos := uint64(10) // 覆盖前两条（End=10 ✓）
	wal.pruneReclaimed(pos)
	requireBacklogConsistent(t, wal, "前缀 prune 之后")

	// Restore（整体替换）必须重算 ✓。
	require.NoError(t, wal.Restore([]WALEntry{{Seq: 1, Durable: true, Replay: true, Event: ev("restored")}}))
	requireBacklogConsistent(t, wal, "Restore 之后")
	require.Equal(t, int64(1), func() int64 { n, _ := wal.ReplayInFlight(); return n }())
}

// TestBacklogReadIsO1WithLargeWAL：读数成本与条数无关（旧实现两次全表扫描 ⇒ 大 WAL 上 ms 级 ✗）。
func TestBacklogReadIsO1WithLargeWAL(t *testing.T) {
	const total = 200_000
	wal, _, _ := pruneFixture(t, total, 0) // 全部保留（不可回收）⇒ 只考读数成本 ✓

	requireBacklogConsistent(t, wal, "大 WAL")
	start := time.Now()
	for i := 0; i < 1000; i++ {
		_, _ = wal.Backlog()
		_, _ = wal.BacklogTotal()
		_, _ = wal.ReplayInFlight()
	}
	elapsed := time.Since(start)
	per := elapsed / 1000
	t.Logf("#66-② 观测：WAL %d 条 ⇒ 每次三读数合计 %s（旧实现 ≈ 2 次全表扫描/次 ✗）", total, per)
	require.Less(t, per, 50*time.Microsecond,
		"三个积压读数必须 O(1)（旧实现是 O(条数) ✗）；实测每次 %s", per)
	_ = fmt.Sprint()
}
