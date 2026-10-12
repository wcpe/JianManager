package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestReplayBytesExcludedFromGateButNewDataStillBounded（2026-10-04 现场，用户定调方案① ✓）：
// **回放/补账的字节不计入闸视图（阈值不动 ✓），而新数据仍被同一把尺挡住 ✓**。
//
// 现场依据：恢复期回放/补账是设计行为 ⇒ 用"抬高阈值"容纳它（方案②，我此前实现 ✗）会把闸的
// **绝对上界一起抬高** ✗ ⇒ 现场 WAL +15,964 / +463k 的"复活→re-append→再越闸"震荡 ✗✗。
// 方案①：阈值不动，只把回放可归因的字节**从计量里剔除** ✓ —— 既不锁，也不放大在飞量 ✓。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：把回放计入闸（= 恢复原实现/平铺抬高阈值的形态 ✗）⇒ 第一条断言（回放不得致暂停）红；
//   - 变异②：闸视图把**所有**字节都排除（闸变瞎 ✗）⇒ 第二条断言（新数据必须仍被挡住）红。
func TestReplayBytesExcludedFromGateButNewDataStillBounded(t *testing.T) {
	key := testKey("inst:replayquota", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetFsync(func() error { return nil })
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
	wal.SetLimits(4, 0) // 常规闸：4 条（**不抬高** ✓）

	// 恢复期回放：8 条（> 常规 4）⇒ 不得致暂停 ✓，并可在"回放在飞"里看到 ✓。
	require.NoError(t, wal.AppendReplay(buildWALEvents("inst:replayquota", "g1", 0, 8, "replay")...))
	require.NoError(t, wal.Commit())
	require.False(t, led.Get(key).AcquirePaused,
		"回放/补账不得把源推成暂停（阈值未动 ✓ 只因计量剔除 ✓）")
	totalEntries, _ := wal.BacklogTotal()
	replayEntries, _ := wal.ReplayInFlight()
	require.Equal(t, int64(8), totalEntries, "总量视图含回放 ✓")
	require.Equal(t, int64(8), replayEntries, "回放在飞必须可观测 ✓")

	// 新数据（常规采集）：5 条 > 常规闸 4 ⇒ **必须照常暂停** ✓（红线：闸语义不削弱 ✗）。
	require.NoError(t, wal.Append(buildWALEvents("inst:replayquota", "g1", 100, 5, "live")...))
	require.NoError(t, wal.Commit())
	require.True(t, led.Get(key).AcquirePaused,
		"新数据必须仍被同一把尺挡住（回放剔除不得变成闸变瞎 ✗）")
	require.True(t, IsBacklogPauseReason(led.Get(key).PauseReason))
}
