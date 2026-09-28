package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// B1a：WAL 快照必须**按条**切分为「可引用」与「必须内联」两部分，且引用恢复要按 Seq 合并、
// 正文缺失不得静默丢弃。
func TestWALSnapshotSplitAndRestoreMixed(t *testing.T) {
	key := testKey("src-split", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	pipe := NewPipeline(led, key, wal)

	// 4 条事件，record 区间 [0,99] [100,199] [200,299] [300,399]
	require.NoError(t, pipe.Ingest(buildWALEvents("src-split", "g1", 0, 4, "line")))
	require.NoError(t, wal.Commit())
	require.Len(t, wal.Snapshot(), 4)

	// 只把 record_end <= 200 的条目视为「正文已在段存储」。
	refs, inline := wal.SnapshotSplit(func(ev logtypes.Event) bool { return ev.Record.End <= 200 })
	require.Len(t, refs, 2, "前两条应只写引用")
	require.Len(t, inline, 2, "其余必须内联（正文未入库，不能丢）")
	require.Equal(t, uint64(0), refs[0].RecordStart)
	require.Equal(t, uint64(199), refs[1].RecordEnd)

	// 引用恢复：正文经 hydrate 取回，且与内联合并后按 Seq 保持投递顺序。
	bodies := map[string]logtypes.Event{}
	for _, e := range wal.Snapshot() {
		bodies[e.Event.EventID] = e.Event
	}
	fresh := NewWAL(led, key)
	missing := fresh.RestoreMixed(inline, refs, func(id string) (logtypes.Event, bool) {
		ev, ok := bodies[id]
		return ev, ok
	})
	require.Empty(t, missing)
	restored := fresh.Snapshot()
	require.Len(t, restored, 4, "内联 + 引用应合成为完整 WAL")
	for i := 1; i < len(restored); i++ {
		require.Less(t, restored[i-1].Seq, restored[i].Seq, "必须按 Seq 升序，保持投递顺序")
	}
	require.Equal(t, wal.Snapshot()[0].Event.EventID, restored[0].Event.EventID)

	// 正文缺失：以 missing 原样返回（调用方据此记可见缺口），不得静默丢弃。
	fresh2 := NewWAL(led, key)
	missing = fresh2.RestoreMixed(inline, refs, func(string) (logtypes.Event, bool) { return logtypes.Event{}, false })
	require.Len(t, missing, 2, "取不到正文的引用必须原样返回")
	require.Len(t, fresh2.Snapshot(), 2, "内联部分仍须恢复")

	// covered=nil 时全部内联（等价旧行为，供段存储不可用时使用）。
	refs2, inline2 := wal.SnapshotSplit(nil)
	require.Empty(t, refs2)
	require.Len(t, inline2, 4)
}
