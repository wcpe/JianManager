package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestBacklogResumeBreaksAppendRejectedSelfLock（2026-10-04 现场终章 ①：60 源"零积压仍无一动"）
//
// 环死（代码即证据）：`ResumeAcquire`（ledger.go:876-887）对**任何**未消解缺口一律拒绝 ✗，而
// `APPEND_REJECTED` 的定义是"该批从未进入 WAL（采集被暂停 / 容量门禁拒了）"（gap_bounds.go:48-51）
// ⇒ 它只能靠**回读**愈合 ⇐ 回读在暂停期间被 tailer 拒绝 ✗ ⇒ 自锁 ✓✓。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 去掉本包的分类消解（恢复原状：任何缺口都拦截）⇒ 在「必须恢复」处红（环死 ✗）；
//   - 把放行扩成"任何原因都消解"⇒ 在「投递失败必须继续拦截」处红（缺口语义红线 ✗）。
func TestBacklogResumeBreaksAppendRejectedSelfLock(t *testing.T) {
	key := testKey("inst:selflock", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetFsync(func() error { return nil })
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})

	// 前置：积压类暂停 + 一条"暂停造成、从未进 WAL"的缺口（现场同型 ✓）+ 积压为零 ✓。
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	require.NoError(t, led.RecordGap(key, 0, 4242, ledger.GapReasonAppendRejected, "append rejected while paused"))
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(led.Get(key)))

	// **前提必须显式钉住**：这个缺口真的会挡住恢复（否则用例什么都证明不了 ✗）。
	directErr := led.ResumeAcquire(key)
	require.Error(t, directErr, "夹具前提：缺口必须真的挡住 ResumeAcquire（实测未挡 ⇒ 造法不对 ✗）")
	require.Contains(t, directErr.Error(), "unresolved gaps")
	require.True(t, led.Get(key).AcquirePaused, "前提：被拒后仍在暂停")

	// 关键：**不做任何 Append** ✗，只按回收/兜底入口评估一次 ⇒ 必须恢复 ✓（回读会重放该批 ✓）。
	require.True(t, wal.EvaluateResume(),
		"暂停造成的 APPEND_REJECTED 缺口不得把源永久锁死在暂停（现场 60 源无一动 ✗）")
	ent := led.Get(key)
	require.False(t, ent.AcquirePaused)
	// 判据用**阻塞语义**而不是总计数：`UnresolvedGapCountOf` 会把与恢复无关的洞一并计入 ✗，
	// 而恢复闸只看 `Gaps` 里的未消解项 ✓（见 ledger.ResumeAcquire 的实现 ✓）。
	blocking := 0
	for _, g := range ent.Gaps {
		if !g.Resolved {
			blocking++
		}
	}
	require.Zero(t, blocking, "暂停造成的 APPEND_REJECTED 缺口应按「恢复后回读重放」分类消解 ✓")

	// 负向对照（缺口语义红线 ✗）：与读游标无关的缺口（投递失败）**必须继续拦截** ✓。
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	require.NoError(t, led.RecordGap(key, 5000, 6000, ledger.GapReasonDeliverError, "transport down"))
	require.False(t, wal.EvaluateResume(), "投递失败类缺口不得被恢复放行（回读治不了它 ✗）")
	require.True(t, led.Get(key).AcquirePaused)
	blocking = 0
	for _, g := range led.Get(key).Gaps {
		if !g.Resolved {
			blocking++
		}
	}
	require.Equal(t, 1, blocking, "投递失败类缺口必须保持未消解（拦截状态 ✓）")
}
