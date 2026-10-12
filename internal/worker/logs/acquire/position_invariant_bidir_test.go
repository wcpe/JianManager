package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestPositionInvariantBidirectional（① 双向判别，按"零猜测配方"落地 ✓✓）：
// **暂停源的 `reclaim > read` 良性豁免** ✓ / **未暂停源的越界必须报出** ✓（真损坏 ✓）。
//
// 配方依据（全部行号级核实过 ✓）：`CanReclaim` 对 `RecoveryReleased` 放行（types.go:151-162 ✓）；
// `Positions.Delivery` 只反映**连续前缀**（ledger.go:508-509 ✓）⇒ 批次须从 0 连续覆盖到 `CoversTo` ✓；
// `target := ref.CoversTo`（ledger.go:1086 ✓）⇒ 回收只能由该写入点产生 ✓；`Restore` 会**归一**越界
// （A 案 ✓）⇒ 夹具必须先"合法"再让回收把 `reclaim` 推过 `read` ✓。
//
// 转红方式（实测两条方向，各自独立红 ✓）：
//   - 变异①：去掉暂停豁免 ⇒ 良性越界被报成违规 ⇒ 红；
//   - 变异②：豁免一切（未暂停也不报）⇒ 真损坏被掩盖 ⇒ 红。
func TestPositionInvariantBidirectional(t *testing.T) {
	const coversTo = 104_276_272

	build := func(t *testing.T, paused bool) *ledger.Ledger {
		t.Helper()
		key := testKey("inst:196/run", "g1")
		l := ledger.New()
		l.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
		snap := l.Get(key)
		snap.Positions.Durable = coversTo
		snap.Positions.Read = coversTo / 2 // 读落后于覆盖 ✓
		// 现场 CLI 的 ACKED = 内部 DeliveryRequestDone ✓（展示层标签 ≠ 常量名 ✓）。
		snap.DeliveryBatches = []ledger.DeliveryBatch{{Start: 0, End: coversTo, State: logtypes.DeliveryRequestDone}}
		require.NoError(t, l.Restore([]ledger.Entry{*snap}))
		require.Equal(t, uint64(coversTo), l.Get(key).Positions.Delivery, "Restore 必须把 Delivery 算到位 ✓（连续前缀 ✓）")
		require.NoError(t, l.RegisterRecovery(key, ledger.RecoveryRef{
			SegmentID: "seg1", Path: "/x", CoversFrom: 0, CoversTo: coversTo,
			State: logtypes.RecoveryReleased, ResponsibilityReceiver: "worker", // CanReclaim 放行态 ✓（无 hold ✓）
		}))
		wal := NewWAL(l, key)
		reclaimed, err := wal.TryReclaim()
		got := l.Get(key)
		t.Logf("tryReclaim reclaimed=%d err=%v delivery=%d reclaim=%d read=%d", reclaimed, err, got.Positions.Delivery, got.Positions.Reclaim, got.Positions.Read)
		require.NoError(t, err)
		require.Greater(t, got.Positions.Reclaim, got.Positions.Read, "夹具前提：回收必须已越过读 ✓")
		if paused {
			require.NoError(t, l.PauseAcquire(key, "wal backlog limit exceeded (entries=5/4)"))
		}
		return l
	}

	// ① 未暂停 ⇒ **必须报出**（真损坏 ✓）。
	running := build(t, false)
	require.False(t, running.Get(testKey("inst:196/run", "g1")).AcquirePaused)
	require.Len(t, running.PositionInvariantViolations(), 1,
		"未暂停源的 reclaim > read 必须被报出（读游标本该随之推进 ⇒ 不推进即真损坏 ✓）")

	// ② 暂停 ⇒ **良性豁免**（只投递不读取 ⇒ Read 冻结 ✓，回收仍在分段覆盖上推进 ✓；夹取会自造永久暂停 ✗✗）。
	pausedL := build(t, true)
	require.True(t, pausedL.Get(testKey("inst:196/run", "g1")).AcquirePaused)
	require.Empty(t, pausedL.PositionInvariantViolations(),
		"暂停源的越界是设计的必然、良性 ⇒ 不得报违规（否则诱导「夹取自锁」✗✗）")
}
