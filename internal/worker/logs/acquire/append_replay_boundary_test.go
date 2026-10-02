package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestRegularIngestNeverMarkedAsReplay（用户指令的红证）：**常规采集绝不允许带 replay 标记**。
//
// 为什么这是红线（2026-10-04）：方案① 只把"回放可归因"的字节从闸计量里剔除 ✓；若常规入口也被
// 打上标记，等于给真正的洪流开后门 ✗ ⇒ 闸的绝对上界作废 ✗（`source_wal` 无界风险 ✓）。
//
// 转红方式（实测）：给常规入口（`Append` / `Pipeline.Ingest`）打 replay 标记 ⇒ 本用例在
// 「常规数据的 replay 在飞必须为 0」处红 ✓。
func TestRegularIngestNeverMarkedAsReplay(t *testing.T) {
	key := testKey("inst:boundary", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetFsync(func() error { return nil })
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
	wal.SetLimits(4, 0)
	pipe := NewPipeline(led, key, wal)

	// 常规采集 3 条（未越闸 ✓）：必须**不带**标记，且计入闸视图 ✓。
	require.NoError(t, pipe.Ingest(buildWALEvents("inst:boundary", "g1", 0, 3, "live")))
	require.NoError(t, wal.Commit())
	entries, bytes := wal.ReplayInFlight()
	require.Zero(t, entries, "常规采集绝不允许带 replay 标记 ✗（否则闸形同虚设）")
	require.Zero(t, bytes)
	gateEntries, _ := wal.BacklogAndLimits()
	require.Equal(t, int64(3), gateEntries, "常规数据必须计入闸视图 ✓")

	// 对照：回放入口才带标记，且被闸视图剔除 ✓。
	freshKey := testKey("inst:boundary-replay", "g1")
	led2 := ledger.New()
	wal2 := NewWAL(led2, freshKey)
	wal2.SetFsync(func() error { return nil })
	led2.Ensure(freshKey, logtypes.SourceIdentity{LogSourceID: freshKey.LogSourceID, SourceGeneration: freshKey.SourceGeneration, ParserVersion: "v1"})
	wal2.SetLimits(4, 0)
	pipe2 := NewPipeline(led2, freshKey, wal2)
	require.NoError(t, pipe2.IngestReplay(buildWALEvents("inst:boundary-replay", "g1", 0, 6, "replay")))
	require.NoError(t, wal2.Commit())
	replayEntries, _ := wal2.ReplayInFlight()
	require.Equal(t, int64(6), replayEntries, "回放入口必须带标记 ✓")
	gate2, _ := wal2.BacklogAndLimits()
	require.Zero(t, gate2, "回放字节必须被闸视图剔除 ✓")
	require.False(t, led2.Get(freshKey).AcquirePaused, "回放不得致暂停 ✓")
}
