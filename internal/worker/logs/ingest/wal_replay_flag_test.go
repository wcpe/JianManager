package ingest

// 回归（ACQ-1，2026-10-29 重写）：`Replay` 回放标记必须在**内存队列**里保持住，
// 因为闸视图（积压 = 总量 − 回放）与诊断判据（BacklogTotalEntries > BacklogEntries）都读它。
//
// ## 与原用例的关系
//
// 原 `index_store_replay_roundtrip_test.go` 钉的是「Replay 随 source_wal 列落库再读回」——
// 那条通路（walRowsOf / indexStateToState 的 WAL 行映射、WALRef）已随表一起删除。
// 但 ACQ-1 真正要防的形态（“回放条目在某个环节退化成常规积压，把源重新推过闸”）
// 在内存队列里仍然成立，所以把断言搬到**队列自身**上：
//   - AppendReplay 追加的条目其 Replay 必须为 true；
//   - 常规 Append 不得被误标；
//   - ReplayInFlight 必须只统计回放条目（这就是闸视图的真实数据源）。
//
// 转红方式：把 appendLocked 里 `replay := origin == originReplay` 改成恒 false（或把 Replay
// 字段从 WALEntry 上摘掉）⇒ 首断言与 ReplayInFlight 断言同时红。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestWALReplayFlagStaysOnQueuedEntries(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "inst:acq1/file", SourceGeneration: "g1"}
	led := ledger.New()
	wal := newWAL(led, key)
	require.NoError(t, led.AdvanceRead(key, 0))

	identity := logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"}
	normal := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 10},
		"2026-10-12T00:00:00Z", "2026-10-12T00:00:01Z", "INFO", "stdout", "normal")
	replayed := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20},
		"2026-10-12T00:00:02Z", "2026-10-12T00:00:03Z", "INFO", "stdout", "replayed")

	require.NoError(t, wal.Append(normal))
	require.NoError(t, wal.Commit())
	require.NoError(t, wal.AppendReplay(replayed))
	require.NoError(t, wal.Commit())

	got := map[string]bool{}
	for _, entry := range wal.Snapshot() {
		got[entry.Event.EventID] = entry.Replay
	}
	require.True(t, got[replayed.EventID],
		"回放条目必须带 Replay 标记（否则闸视图把它计入常规积压，回放自锁）")
	require.False(t, got[normal.EventID], "对照：常规条目不得被误标为回放")

	entries, _ := wal.ReplayInFlight()
	require.EqualValues(t, 1, entries, "闸视图的回放计量只应包含回放条目")
}
