package acquire

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// buildWALEvents 构造 n 条位置相邻的事件（from 为起始序号，保证多次调用位置单调递增）。
func buildWALEvents(id, gen string, from, n int, payload string) []logtypes.Event {
	evs := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		idx := from + i
		evs = append(evs, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: gen, ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: uint64(idx * 100), End: uint64(idx*100 + 99)},
			"t0", "t1", "INFO", "stdout", payload,
		))
	}
	return evs
}

// 单源积压超上限必须**暂停该源采集**，而不是无界增长；且**不得静默丢数据**（B1b）。
//
// 事故背景（2026-09-28 生产）：某源投递/校验长时间失败 → reclaim 永不推进 → WAL 条目无界累积
// （每条内联完整正文），单源把 ingest.state.json 撑到 1.2 GB；每次持久化全量重写 → 持续
// 117 MB/s、Worker CPU 138%、整机 iowait 90%，并拖慢同机 CP 的 SQLite。
func TestWALBacklogLimitPausesAcquisitionWithoutDroppingEntries(t *testing.T) {
	key := testKey("src-backlog", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0) // 条目上限 4（字节上限走默认）
	pipe := NewPipeline(led, key, wal)

	require.NoError(t, pipe.Ingest(buildWALEvents("src-backlog", "g1", 0, 3, "line")))
	require.False(t, led.Get(key).AcquirePaused, "未越界时不得暂停")

	// 追加到 5 条：越过上限 4 → 暂停，但已入的事件全部保留（不丢数据）。
	require.NoError(t, pipe.Ingest(buildWALEvents("src-backlog", "g1", 3, 2, "line")))
	ent := led.Get(key)
	require.True(t, ent.AcquirePaused, "积压越界必须暂停该源采集")
	require.True(t, strings.HasPrefix(ent.PauseReason, walBacklogPauseReason),
		"暂停原因须标明来源，便于区分容量门禁等其它暂停：%q", ent.PauseReason)
	require.Len(t, wal.Snapshot(), 5, "越界不得静默丢条目")

	// 暂停对后续追加生效：尾随追加被拒（调用方据此停止读取，源文件游标不前进）。
	err := pipe.Ingest(buildWALEvents("src-backlog", "g1", 5, 1, "line"))
	require.Error(t, err, "暂停期间不得再追加")
	require.Contains(t, err.Error(), "paused")
	require.Len(t, wal.Snapshot(), 5, "被拒的追加不得改变既有条目")
}

// 积压回落至低水位（上限的一半）才恢复，且仅恢复本包造成的暂停（滞回 + 不越权）。
func TestWALBacklogLimitResumeRequiresLowWatermarkAndOwnPause(t *testing.T) {
	key := testKey("src-backlog-resume", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0)
	pipe := NewPipeline(led, key, wal)

	require.NoError(t, pipe.Ingest(buildWALEvents("src-backlog-resume", "g1", 0, 5, "line")))
	require.NoError(t, wal.Commit())
	require.True(t, led.Get(key).AcquirePaused, "前置：应已暂停")

	// 回落到 3 条（> 上限一半 2）→ 仍暂停（滞回，避免抖动）。
	// 事件位置为 [0,99] [100,199] [200,299] [300,399] [400,499]，End<=200 丢弃前两条。
	wal.pruneReclaimed(200)
	require.Len(t, wal.Snapshot(), 3)
	require.True(t, led.Get(key).AcquirePaused, "未达低水位不得恢复")

	// 回到 2 条（<= 半）→ 自动恢复。
	wal.pruneReclaimed(300)
	require.Len(t, wal.Snapshot(), 2)
	require.False(t, led.Get(key).AcquirePaused, "回落到低水位应自动恢复采集")
	require.Empty(t, led.Get(key).PauseReason)

	// 越权反例：原因非本包所写时，即使低于低水位也不得清除。
	require.NoError(t, led.PauseAcquire(key, "capacity budget exhausted"))
	wal.pruneReclaimed(400)
	require.Len(t, wal.Snapshot(), 1)
	require.True(t, led.Get(key).AcquirePaused, "不得清除其它路径设置的暂停")
	require.Equal(t, "capacity budget exhausted", led.Get(key).PauseReason)
}
