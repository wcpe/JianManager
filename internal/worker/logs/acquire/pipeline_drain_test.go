package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 暂停期间仍必须**排空已 durable 的待投递条目**（2026-09-30 生产实证）。
//
// 缺了它：投递挂在摄取上 → 暂停即断流 → 已读未投的积压永不外发 → 投递位置不动 →
// 恢复分段无从覆盖 → 回收停滞 → 滞回（≤上限一半）永不满足 → 源永久停在 paused。
// 实测：某源残留 7998 条卡死数小时，积压数字一个字节不动。
//
// 变异验证：删掉 APPEND_REJECTED 分支里的 `p.deliverPendingBestEffort()`，本测试立即转红。
func TestPausedIngestStillDrainsDurablePending(t *testing.T) {
	key := testKey("src-drain", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(1, 0) // 条目上限 1 → 首批即越界，进入暂停态
	pipe := NewPipeline(led, key, wal)

	deliveries := 0
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		deliveries++
		return 204, false, nil
	})

	// 首批发 2 条：上限 1 → 越界暂停；该批已 durable，正常路径下会走一次投递。
	require.NoError(t, pipe.Ingest(buildWALEvents("src-drain", "g1", 0, 2, "line")))
	require.True(t, led.Get(key).AcquirePaused, "夹具应处于暂停态")
	require.Equal(t, 1, deliveries, "正常批次的投递仍应发生")

	// 暂停期间的追加被拒 —— 但**存量必须被排空**（本用例守的就是这条）。
	err := pipe.Ingest(buildWALEvents("src-drain", "g1", 2, 1, "line"))
	require.Error(t, err, "暂停期间不得再追加")
	require.Contains(t, err.Error(), "paused")
	require.GreaterOrEqual(t, deliveries, 2,
		"暂停期间仍须投递已 durable 的存量：否则积压永不外发、回收永不推进、源永久停在 paused")
}
