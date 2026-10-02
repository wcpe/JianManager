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

	// **负向对照必须与 APPEND_REJECTED 共存**（否则分类路径根本不会被走到 ⇒ 变异不可达 ✗✗，
	// 我实测过：只有 DELIVER_ERROR 时"删排除项"的变异仍绿 ✗，那不算证据 ✓）。
	// 形态：同一源同时有 ①APPEND_REJECTED（触发分类 ✓）②落在 through 之内的 DELIVER_ERROR
	// （本该被排除 ✓）⇒ 断言 ② 必须保持未消解 ✓、而 ① 被分类消解 ✓。
	// **必须先重新暂停**：分类路径挂在 `maybeResumeBacklogLocked` 里，而它开头就判"未暂停则返回" ✗——
	// 上一轮我的共存构造漏了这一步 ⇒ 分类根本没被走到 ⇒ 未变异即红 ✗（夹具问题，不是实现 ✓）。
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	for _, g := range led.Get(key).Gaps {
		if !g.Resolved {
			t.Logf("共存前置：未消解 gap reason=%s [%d,%d]", g.Reason, g.StartPos, g.EndPos)
		}
	}
	require.NoError(t, led.RecordGap(key, 6100, 6200, ledger.GapReasonAppendRejected, "append rejected while paused"))
	// **关键：投递失败缺口必须落在 `through` 之内**（through = 未消解 APPEND_REJECTED 的最大 EndPos ✓）。
	// 上一版我把它放在 [6300,6400]（> 6200）⇒ 分类根本够不到它 ⇒ "删排除项"的变异仍绿 ✗✗，
	// 那样得到的只是"不可达变异"，不算证据 ✓（实测教训 ✓）。
	require.NoError(t, led.RecordGap(key, 5000, 5100, ledger.GapReasonDeliverError, "transport down"))
	// **正确期望**：共存时源**不得恢复** ✓——排除表里的 DELIVER_ERROR 必须继续挡住恢复 ✓
	// （这正是回归早期踩过的坑：我曾误写成"仍必须恢复"，那是与红线自相矛盾的期望 ✗）。
	// 判别力在于：若把 GapReasonDeliverError 从排除表删掉 ⇒ 它会被一并消解 ⇒ 本断言变红 ✓✓。
	require.False(t, wal.EvaluateResume(), "投递失败类缺口必须继续挡住恢复（红线 ✓）")
	require.True(t, led.Get(key).AcquirePaused, "被挡住的形态：仍在暂停 ✓")
	// **据实打印**（父指示）：分类调用后的真实 gap 列表（合并/trim 之后 ✓）。
	for i, g := range led.Get(key).Gaps {
		t.Logf("共存后 gap[%d] reason=%s [%d,%d] resolved=%v resolution=%q", i, g.Reason, g.StartPos, g.EndPos, g.Resolved, g.Resolution)
	}
	var deliverUnresolved, appendRejectedUnresolved int
	for _, g := range led.Get(key).Gaps {
		if g.Resolved {
			continue
		}
		switch g.Reason {
		case ledger.GapReasonDeliverError:
			deliverUnresolved++
		case ledger.GapReasonAppendRejected:
			appendRejectedUnresolved++
		}
	}
	require.Zero(t, appendRejectedUnresolved, "APPEND_REJECTED 应按「恢复后回读重放」分类消解 ✓")
	require.Equal(t, 1, deliverUnresolved,
		"**排除表必须挡住投递失败类**：放开一因（删 GapReasonDeliverError）即在此变红 ✓（前提：该缺口落在 through 之内 ✓）")

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
	require.GreaterOrEqual(t, blocking, 1,
		"投递失败类缺口必须保持未消解（拦截状态 ✓；共存阶段那条 6300-6400 仍在 ⇒ 至少 1 条 ✓）")
}
