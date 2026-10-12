package ingest

// #179 第 1 步（D）：补段受阻的**可见性**回归 —— 四类静默 return 必须可观测（计数 + 障碍读数），
// 且"回收连续多轮无进展"必须**告警**（②有界判据；本步只告警、不做自动动作 ✓）。
//
// 变异（实测见报告）：
//   M1 撤掉任一处 noteReclaimBlocked ⇒ 对应计数断言转红 ✓；
//   M2 撤掉 3 轮判据（永不告警）⇒ StallWarns 断言转红 ✓。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestBackfillBlockedCategoriesAreVisible(t *testing.T) {
	f := newLaggingSourceFixture(t)
	pos, ok := f.p.Ledger().Positions(f.p.Key())
	require.True(t, ok)
	gen := f.src.SourceGeneration
	base := f.m.ReclaimRollingStats()

	// ① config-hold：配置层钉住 ⇒ 连补段都不做 ✓。
	f.m.recoveryHold = func(SourceConfig, string) (bool, string) { return true, "test-hold" }
	require.NoError(t, f.m.backfillDeliveryConfirmedCoverage(f.p, f.src, gen, pos))
	// ② no-material：to <= from（无"已确认但未覆盖"区间）✓。
	f.m.recoveryHold = func(SourceConfig, string) (bool, string) { return false, "" }
	require.NoError(t, f.m.backfillDeliveryConfirmedCoverage(f.p, f.src, gen,
		logtypes.Positions{Reclaim: pos.Reclaim, Delivery: pos.Reclaim}))
	// ③ watermark-moved：传入水位与账本当前值不一致（变则弃）✓。
	require.NoError(t, f.m.backfillDeliveryConfirmedCoverage(f.p, f.src, gen,
		logtypes.Positions{Reclaim: pos.Reclaim + 99, Delivery: pos.Reclaim + 100}))

	after := f.m.ReclaimRollingStats()
	require.Equal(t, base.BlockedConfigHold+1, after.BlockedConfigHold, "配置层 hold 必须计数 ✓")
	require.Equal(t, base.BlockedNoMaterial+1, after.BlockedNoMaterial, "无料（to<=from）必须计数 ✓")
	require.Equal(t, base.BlockedWatermarkMoved+1, after.BlockedWatermarkMoved, "水位复读失败必须计数 ✓")
}

func TestReclaimStallWarnsOnceThenThrottles(t *testing.T) {
	f := newLaggingSourceFixture(t)
	// 配置层 hold ⇒ 步 0 补段被拒 ⇒ reclaim 恒停在 0 < delivery(24) ⇒ 每轮都是"有欠账未前进" ✓。
	f.m.recoveryHold = func(SourceConfig, string) (bool, string) { return true, "test-hold" }
	require.Zero(t, f.m.ReclaimRollingStats().BlockedStallWarns, "前置：尚未告警 ✓")

	for i := 0; i < 3; i++ {
		_, _ = f.m.maybeResumePausedSources()
	}
	require.Equal(t, int64(1), f.m.ReclaimRollingStats().BlockedStallWarns, "第 3 轮必须恰好告警一次 ✓")

	_, _ = f.m.maybeResumePausedSources() // 第 4 轮：节流窗口内 ⇒ 不重复告警 ✓
	require.Equal(t, int64(1), f.m.ReclaimRollingStats().BlockedStallWarns, "节流：非 3 的倍数轮不得重复告警 ✓")

	pos, _ := f.p.Ledger().Positions(f.p.Key())
	require.Less(t, pos.Reclaim, pos.Delivery, "前置：本夹具下回收确有欠账（告警的前提 ✓）")
	_ = time.Now
}
