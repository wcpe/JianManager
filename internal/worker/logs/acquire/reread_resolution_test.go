package acquire

// #179-① 小测：待重读消解词的**真源**必须可导出、谓词判定必须只认这两条（行为中性 ✓）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 末端水位只能提示候选扫描，未知、归档及原始写失败不得被顺带消解。
func TestReReadResumeClassificationAllowsOnlyKnownReasons(t *testing.T) {
	for _, blockedReason := range []string{"ARCHIVE_GZIP_CORRUPT", "FUTURE_UNKNOWN", ledger.GapReasonStdioRawWriteFailed, ledger.GapReasonWALCommitFailed} {
		t.Run(blockedReason, func(t *testing.T) {
			key := testKey("reread-reasons", "g1")
			led := ledger.New()
			wal := NewWAL(led, key)
			led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
			require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason))
			require.NoError(t, led.RecordGap(key, 0, 0, blockedReason, "没有可重读证据"))
			require.NoError(t, led.RecordGap(key, 10, 20, ledger.GapReasonDeliverError, "投递失败"))
			require.NoError(t, led.RecordGap(key, 30, 40, ledger.GapReasonDeliverErrorWorkerSource, "自源投递失败"))
			require.NoError(t, led.RecordGap(key, 50, 60, ledger.GapReasonAppendRejected, "暂停拒收"))
			resumed := wal.EvaluateResume()
			gaps := led.Get(key).Gaps
			t.Logf("resumed=%v gaps=%+v", resumed, gaps)
			require.False(t, gaps[0].Resolved, "未知和不可修复原因必须继续阻塞")
			require.False(t, resumed)
			require.True(t, led.Get(key).AcquirePaused)
			for _, gap := range gaps[1:] {
				require.True(t, gap.Resolved)
				want := ResolutionReReadOnResumeDelivery
				if gap.Reason == ledger.GapReasonAppendRejected {
					want = ResolutionReReadOnResume
				}
				require.Equal(t, want, gap.Resolution, "APPEND 与 DELIVERY 必须分账")
				require.Empty(t, gap.ReasonCode, "分类不得伪装永久放弃")
			}
			require.Empty(t, led.Get(key).Abandonments)
		})
	}
}

// 源位置零是合法缺口，不能借最大水位零值把正常自愈出口关闭。
func TestReReadResumeClassificationHandlesZeroPosition(t *testing.T) {
	for _, reason := range []string{ledger.GapReasonAppendRejected, ledger.GapReasonDeliverError, ledger.GapReasonDeliverErrorWorkerSource} {
		t.Run(reason, func(t *testing.T) {
			key := testKey("reread-zero", "g1")
			led := ledger.New()
			wal := NewWAL(led, key)
			led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
			require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason))
			require.NoError(t, led.RecordGap(key, 0, 0, reason, "零位置"))
			require.True(t, wal.EvaluateResume(), "已知原因必须保持自愈出口")
			gap := led.Get(key).Gaps[0]
			require.True(t, gap.Resolved)
			require.True(t, IsReReadHealableResolution(gap.Resolution))
		})
	}
}

func TestIsReReadHealableResolutionMatchesExportedSource(t *testing.T) {
	require.Equal(t, ResolutionReReadOnResume, gapResolutionReReadOnResume, "别名必须与真源同值 ✓")
	require.Equal(t, ResolutionReReadOnResumeDelivery, gapResolutionReReadOnResumeDelivery, "别名必须与真源同值 ✓")
	require.True(t, IsReReadHealableResolution(ResolutionReReadOnResume), "复投类待重读词必须命中 ✓")
	require.True(t, IsReReadHealableResolution(ResolutionReReadOnResumeDelivery), "投递错类待重读词必须命中 ✓")
	require.False(t, IsReReadHealableResolution(""), "空词不得命中 ✓")
	require.False(t, IsReReadHealableResolution("operator-abandon"), "非待重读词不得命中（不放宽判据 ✓）")
}
