package ingest

// 测试侧把一组事件装进内存 WAL 队列的辅助。
//
// ## 为什么需要它
//
// 2026-10-29：WAL 改为**纯内存队列**后 `acquire.WAL.Restore` / `RestoreMixed` 整组被删除
// （队列内容不再跨重启恢复，生产路径也不再从磁盘装条目）。但仍有一批回归需要把
// 「一批已知事件」直接装进队列作为前置状态（剪枝、pending cursor、资格门等仍是内存语义）。
//
// 这里用**仍然存在**的生产 API 实现：`Append`（写 read 水位）+ `Commit`（翻 durable）。
// 它与旧 Restore 的差异只有两点，且都在测试可接受范围内：
//   - Seq 由队列自增，从 1 开始（旧 Restore 允许任意 Seq，多数夹具本来就传 1..N）；
//   - 不做"持久化 ACK 证据重建"（那条路径已不存在，条目 acknowledged 恒 nil）。
//
// 对需要**非 durable** 条目的夹具，用 seedWALPending（只 Append 不 Commit）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// seedWAL 把事件按给定顺序追加并提交（Appended=true, Durable=true）。
func seedWAL(t *testing.T, wal *acquire.WAL, events ...logtypes.Event) {
	t.Helper()
	if len(events) == 0 {
		return
	}
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
}

// seedWALPending 只追加不提交：条目落在队列里但仍是「未耐久」。
func seedWALPending(t *testing.T, wal *acquire.WAL, events ...logtypes.Event) {
	t.Helper()
	if len(events) == 0 {
		return
	}
	require.NoError(t, wal.Append(events...))
}

// walEntriesToEvents 取队列快照里的事件序列（用于「重新装入等价队列」）。
func walEntriesToEvents(entries []acquire.WALEntry) []logtypes.Event {
	out := make([]logtypes.Event, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Event)
	}
	return out
}
