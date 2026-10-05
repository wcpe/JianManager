package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestWALReplayDuplicateAdjudication 验收 #179 上游根修：回放/补账入口不得把
// 「同 id 同区间异哈希」再写第二遍 ✗；可裁定形态原位替换并追加更正版，其余维持追加。
func TestWALReplayDuplicateAdjudication(t *testing.T) {
	key := testKey("src-replaydedup", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(1<<30, 1<<40)
	identity := logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"}
	rec := logtypes.RecordRange{Start: 100, End: 110}
	at := func(eventTime, msg string) logtypes.Event {
		return logtypes.BuildEvent(identity, rec, eventTime, "2026-10-03T00:17:08.875Z", "WARN", "stdout", msg)
	}

	// ① 同哈希 ⇒ 回放不得再写第二遍 ✓。
	first := at("2026-10-03T05:08:50Z", "同一行")
	require.NoError(t, wal.Append(first))
	require.Len(t, wal.Snapshot(), 1)
	require.NoError(t, wal.AppendReplay(first))
	require.Len(t, wal.Snapshot(), 1, "同哈希回放必须被丢弃（不写第二遍 ✗）")

	// ② 形态 2（仅时间不同）⇒ 旧条目原位替换 + 更正版追加；两条哈希收敛一致 ✓。
	fixed := at("2026-10-02T05:08:50Z", "同一行")
	require.NotEqual(t, first.CanonicalHash, fixed.CanonicalHash)
	require.NoError(t, wal.AppendReplay(fixed))
	snap := wal.Snapshot()
	require.Len(t, snap, 2)
	require.Equal(t, fixed.CanonicalHash, snap[0].Event.CanonicalHash, "旧条目必须被更正版原位替换 ✓")
	require.Equal(t, fixed.CanonicalHash, snap[1].Event.CanonicalHash, "追加的更正版 ✓")
	require.True(t, snap[0].Replay, "替换后的条目归属回放 ✓")

	// ③ 不可裁定（同区间、message 不同且等长）⇒ 维持追加（由下游可见硬失败挡住 ✓）。
	a := at("2026-10-03T05:08:50Z", "AAAAAAAAA")
	b := at("2026-10-02T05:08:50Z", "BBBBBBBBB")
	before := len(wal.Snapshot())
	require.NoError(t, wal.AppendReplay(a))
	require.NoError(t, wal.AppendReplay(b))
	require.Equal(t, before+2, len(wal.Snapshot()), "不可裁定形态维持追加（可见硬失败在下游 ✓）")
}
