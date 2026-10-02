package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestBacklogResumeHealsDeliveryErrorGapsByReRead（2026-10-04 现场红线修正，用户批准）：
// **`DELIVER_ERROR` 类缺口必须允许积压恢复路径按"恢复后回读重投"分类消解**。
//
// 现场依据：67 条 `DELIVER_ERROR` 恒等不变 ✗（`inst:156` id0 两端各差 1 字节 ✗），而该类已消解的
// 46 条全部是"源尚未暂停时由**活投递**消解" ✓ ⇒ 暂停后活投递事件结构性不可达（无新批次 + 无待投
// 条目 ⇒ 永不产生"成功投递事件" ✗）⇒ 两种证据都拿不到 ⇒ 永久停死 ✓✓ = 鸡生蛋。
// 语义上也成立：`DELIVER_ERROR` = 投递时 VL 不可达 ⇒ 恢复后回读会让恢复链**重投那段
// `UNSENT basis=gap` 区间**（现场 119.78MB）⇒ 恰恰是可愈合的 ✓。
//
// 转红方式（实测）：把 `GapReasonDeliverError` 放回排除名单（= 我上批的实现 ✗）⇒ 本用例在
// 「必须恢复」处红 ✓；把排除名单清空 ⇒ `STDIO_RAW_WRITE_FAILED` 的负向对照处红 ✓。
func TestBacklogResumeHealsDeliveryErrorGapsByReRead(t *testing.T) {
	key := testKey("inst:delivererr", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetFsync(func() error { return nil })
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})

	// 现场形态：积压类暂停 + 积压为零 + 一条 DELIVER_ERROR 缺口（落在 unsent 区间 ✓）。
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=1,139,541/5000)"))
	require.NoError(t, led.RecordGap(key, 68_163_086, 68_528_091, ledger.GapReasonDeliverError, "connect: connection refused"))
	require.Error(t, led.ResumeAcquire(key), "夹具前提：该缺口必须真的挡住 ResumeAcquire ✓")

	require.True(t, wal.EvaluateResume(),
		"DELIVER_ERROR 必须允许按「恢复后回读重投」分类消解并恢复（现场 67 条恒等不变的形态 ✗✓）")
	ent := led.Get(key)
	require.False(t, ent.AcquirePaused)
	for _, g := range ent.Gaps {
		if g.Reason == ledger.GapReasonDeliverError {
			require.True(t, g.Resolved, "该类缺口应按分类裁定消解 ✓")
			require.Equal(t, gapResolutionReReadOnResumeDelivery, g.Resolution, "文本必须与 APPEND_REJECTED 分账 ✓")
		}
	}

	// 负向对照（红线 ✓）：STDIO_RAW_WRITE_FAILED = "原始字节从未落盘 ⇒ 重投无据"
	// （= "源文件对应字节已不再可得"的等价形态 ✓）⇒ **必须保持未消解并继续挡住恢复** ✓。
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	require.NoError(t, led.RecordGap(key, 1_000_000, 1_000_100, ledger.GapReasonStdioRawWriteFailed, "raw write failed"))
	require.False(t, wal.EvaluateResume(), "STDIO_RAW_WRITE_FAILED 必须继续挡住恢复（红线 ✗不许宣称重读即愈合 ✗）")
	blocking := 0
	for _, g := range led.Get(key).Gaps {
		if !g.Resolved && g.Reason == ledger.GapReasonStdioRawWriteFailed {
			blocking++
		}
	}
	require.Equal(t, 1, blocking, "该缺口必须保持未消解（语义断言 ✓）")
}
