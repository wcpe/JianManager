package acquire

// B1a 的「快照按条切分为引用/内联」与「引用恢复（RestoreMixed）」在 2026-10-29 随
// `source_wal` 表一起下线：WAL 改纯内存队列后，队列内容根本不落盘，既不需要引用形式
// （WALRef）也不需要由引用+内联合成队列的恢复路径，整个 wal_refs_test.go 因此删除。
//
// 仍然存在、值得守住的是**队列自身的顺序语义**：Snapshot 必须按插入/Seq 顺序返回，
// 供投递按序取用；这是内存队列的核心前提（本文件即钉这一条）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

func TestWALSnapshotPreservesDeliveryOrder(t *testing.T) {
	key := testKey("snapshot-order", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)

	require.NoError(t, pipe.Ingest(buildWALEvents("snapshot-order", "g1", 0, 4, "line")))
	require.NoError(t, wal.Commit())

	snapshot := wal.Snapshot()
	require.Len(t, snapshot, 4)
	for i := 1; i < len(snapshot); i++ {
		require.Less(t, snapshot[i-1].Seq, snapshot[i].Seq, "队列必须按 Seq 升序返回，保持投递顺序")
		require.LessOrEqual(t, snapshot[i-1].Event.Record.End, snapshot[i].Event.Record.Start,
			"记录区间不得重叠或倒序")
	}
	// 返回的是副本：改写它不得反写活队列。
	snapshot[0].Event.Message = "调用方改写"
	require.NotEqual(t, "调用方改写", wal.Snapshot()[0].Event.Message)
}
