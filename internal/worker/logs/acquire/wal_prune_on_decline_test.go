package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 账本拒绝**推进**回收位置时，WAL 仍必须按**当前**水位剪枝（2026-09-30 生产实证）。
//
// 缺了它：wal.TryReclaim 一遇错便 return → pruneReclaimed 永不执行 → 连水位**之下的**
// 条目也永远保留 → 积压只增不减 → 滞回（≤上限一半）永不满足 → 源永久停在 paused
// （实测某源 7998 条卡死数小时，积压数字一个字节不动）。
//
// 形态刻意与生产一致：回收位置恰好等于分段边界（CoversTo == Reclaim），
// 于是 `CoversTo > Reclaim` 不成立 → 账本报错，而水位之下的一切其实早已判定安全。
//
// 变异验证：把 TryReclaim 的错误分支改回「直接 return」，本测试立即转红。
func TestTryReclaim_StillPrunesWhenLedgerDeclinesAdvance(t *testing.T) {
	key := testKey("src-prune", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	// 一条 durable 事件，Record.End = 99（落在一段 0..100 的覆盖之内）。
	evs := buildWALEvents("src-prune", "g1", 0, 1, "line")
	require.NoError(t, wal.Append(evs...))
	require.NoError(t, wal.Commit())
	require.Len(t, wal.Snapshot(), 1)

	// 铺好账本：段 0..100 走完责任转移并推进回收 → Reclaim=100。
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src-prune", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RegisterRecovery(key, ledger.RecoveryRef{
		SegmentID: "seg1", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 100,
	}))
	require.NoError(t, led.TransitionRecovery(key, "seg1", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg1", logtypes.RecoveryWALResponsibilityXfer, "", "receiver-A"))
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(100), pos, "首次回收应推进到分段末端")

	// 第二次：水位已等于分段边界 → CoversTo > Reclaim 不成立 → 账本必然报错。
	// 但水位之下的条目（End=99）必须被剪掉，否则积压永不回落。
	_, err = wal.TryReclaim()
	require.Error(t, err, "夹具应处于「账本拒绝继续推进」的状态")
	require.Empty(t, wal.Snapshot(),
		"账本拒绝推进时仍须剪掉水位之下的条目：否则积压永不回落、源永久停在 paused")
}
