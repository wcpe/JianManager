package acquire

// ROTATED_SEGMENT_NOT_READY 入白名单的回归用例（2026-10-10 生产缺陷）。
//
// 现场：87 个源的采集被「不可分类缺口」永久阻塞（积压早已归零 0/20000），其中 86 源是
// `ROTATED_SEGMENT_NOT_READY`。根因链：`ResumeAcquire` 对任何未消解缺口一律拒绝 ✗，
// 而恢复路径的「分类裁定」白名单里没有这个原因 ⇒ 它永远不被自动放行 ✗。
// 该原因由 ingest 的轮转分支登记，位置恒为 [0,0]（**不覆盖任何日志字节**，只是"轮转段还没就绪"
// 的时序观察）⇒ 轮转段就绪后回读会自然覆盖 ⇒「恢复后回读可愈合」成立 ✓。
//
// 同时钉住负例：`WAL_BODY_MISSING`（真实正文缺失，区间是真实 from/to）**不得**被放行 ✓。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// newPausedSourceForGap 造一个「积压类暂停 + 一条指定原因的未消解缺口」的源（现场同型 ✓）。
func newPausedSourceForGap(t *testing.T, logSourceID, reason string, start, end uint64) (*ledger.Ledger, *WAL, ledger.SourceKey) {
	t.Helper()
	key := testKey(logSourceID, "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	require.NoError(t, led.RecordGap(key, start, end, reason, "现场同型缺口"))
	// 前提必须显式钉住：这个缺口真的挡住 ResumeAcquire（否则用例什么都证明不了 ✗）。
	require.Error(t, led.ResumeAcquire(key), "夹具前提：缺口必须真的挡住 ResumeAcquire")
	require.True(t, led.Get(key).AcquirePaused, "夹具前提：被拒后仍在暂停")
	return led, wal, key
}

// TestResumeHealsRotatedSegmentNotReadyGap 验收：轮转时序缺口现在能被自动消解并恢复采集。
//
// 这是 86 个生产源的等价形态：零长度 [0,0] 缺口 + 积压类暂停 + 不做任何 Append ✗。
//
// 转红（实测）：把 GapReasonRotatedSegmentNotReady 从 ledger.GapReasonsHealableOnReResume
// 真源里删掉 ⇒ 本用例在「必须恢复」处红 ✓（消解集合少一项）。
func TestResumeHealsRotatedSegmentNotReadyGap(t *testing.T) {
	led, wal, key := newPausedSourceForGap(t, "inst:rotated", ledger.GapReasonRotatedSegmentNotReady, 0, 0)

	require.True(t, wal.EvaluateResume(),
		"轮转时序缺口（[0,0] 观察记录，不覆盖任何字节）不得把源永久锁死在暂停（现场 86 源 ✗）")

	ent := led.Get(key)
	require.False(t, ent.AcquirePaused)
	blocking := 0
	for _, g := range ent.Gaps {
		if !g.Resolved {
			blocking++
		}
	}
	require.Zero(t, blocking, "轮转时序缺口应按「恢复后回读重放」分类消解 ✓")

	// 消解词必须**分账**：用 append/delivery 的措辞记录一条轮转观察是事实错误（审计面会读到
	// 一个从未发生的失败类型 ✗）。
	gap := ent.Gaps[0]
	require.Equal(t, ResolutionReReadOnResumeRotation, gap.Resolution, "轮转类必须用自己的消解词 ✓")
	require.True(t, IsReReadHealableResolution(gap.Resolution), "必须属「待重读」类（非终局 ✓）")
	require.Empty(t, gap.ReasonCode, "分类不得伪装永久放弃 ✓")
	require.Empty(t, ent.Abandonments, "分类裁定不是放弃裁定 ✓")
}

// TestResumeHealsRotatedSegmentNotReadyGapAtRealPosition 验收同一原因在**非零位置**也成立。
//
// 为什么单列一条：登记点目前恒写 [0,0]，但"零长度"是登记点的性质而不是原因码的定义——
// 若将来登记点带上真实位置，白名单语义必须仍然正确（消解判据是「末端 ≤ 上界」✓）。
//
// 转红：同上下（删真源项）⇒ 在「必须恢复」处红 ✓。
func TestResumeHealsRotatedSegmentNotReadyGapAtRealPosition(t *testing.T) {
	led, wal, key := newPausedSourceForGap(t, "inst:rotated-pos", ledger.GapReasonRotatedSegmentNotReady, 100, 200)
	require.True(t, wal.EvaluateResume(), "带真实区间的轮转缺口同样属可愈合类 ✓")
	require.False(t, led.Get(key).AcquirePaused)
}

// TestResumeStillBlocksWALBodyMissingGap 是**负例**：真实正文缺失不得被自动放行。
//
// 语义（决定修复的正确性）：`WAL_BODY_MISSING` 由「WAL 引用在事件段存储中找不到正文」登记，
// 区间是**真实的 from/to** —— 确有日志字节没被恢复 ✗。回读造不出已经丢掉的正文 ⇒
// 只有人工裁定能处置；自动放行等于静默掩盖丢数据（缺口语义红线 ✗）。
//
// 转红（实测）：把 GapReasonWALBodyMissing 加入 ledger.GapReasonsHealableOnReResume
// 真源 ⇒ 本用例在「必须继续阻塞」处红 ✓。
func TestResumeStillBlocksWALBodyMissingGap(t *testing.T) {
	led, wal, key := newPausedSourceForGap(t, "inst:bodymissing", ledger.GapReasonWALBodyMissing, 300, 400)

	require.False(t, wal.EvaluateResume(), "真实正文缺失必须继续挡住恢复（人工裁定是唯一出路 ✓）")
	ent := led.Get(key)
	require.True(t, ent.AcquirePaused, "被挡住的形态：仍在暂停 ✓")

	unresolved := 0
	for _, g := range ent.Gaps {
		if !g.Resolved {
			unresolved++
		}
	}
	require.Equal(t, 1, unresolved, "WAL_BODY_MISSING 必须保持未消解 ✓")
	require.Empty(t, ent.Gaps[0].Resolution, "不得留下任何消解词（未消解 ⇒ 无裁定 ✓）")
}

// TestResumeHealsRotatedGapEvenWhenBodyMissingCoexists 验收两类共存时的**分账**行为。
//
// 形态即现场：同一源既有轮转时序缺口（可愈合 ⇒ 应被消解 ✓）又有正文缺失（不可愈合 ⇒ 应保留 ✓）。
// 源整体**不得恢复**（正文缺失仍挡住 ✓），但轮转缺口必须已被消解——
// 否则"轮转缺口被正文缺失连坐"会让 86 源的修复在混合形态下失效 ✗。
//
// 这同时是**判别力测试**：若把两类混进同一个消解组（例如共用一个 through），
// 较大的上界会把正文缺失一并消解 ⇒ 在「必须保持未消解」处红 ✓。
func TestResumeHealsRotatedGapEvenWhenBodyMissingCoexists(t *testing.T) {
	key := testKey("inst:mixed", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	require.NoError(t, led.RecordGap(key, 0, 0, ledger.GapReasonRotatedSegmentNotReady, "轮转段未就绪"))
	require.NoError(t, led.RecordGap(key, 300, 400, ledger.GapReasonWALBodyMissing, "正文缺失"))

	require.False(t, wal.EvaluateResume(), "正文缺失仍必须挡住恢复 ✓")
	ent := led.Get(key)
	require.True(t, ent.AcquirePaused)

	var rotated, bodyMissing *ledger.Gap
	for i := range ent.Gaps {
		switch ent.Gaps[i].Reason {
		case ledger.GapReasonRotatedSegmentNotReady:
			rotated = &ent.Gaps[i]
		case ledger.GapReasonWALBodyMissing:
			bodyMissing = &ent.Gaps[i]
		}
	}
	require.NotNil(t, rotated, "轮转缺口必须仍在账本中（未被裁掉 ✓）")
	require.NotNil(t, bodyMissing, "正文缺失缺口必须仍在账本中 ✓")
	require.True(t, rotated.Resolved, "可愈合的轮转缺口不得被不可愈合的邻居连坐（现场混合形态 ✓）")
	require.Equal(t, ResolutionReReadOnResumeRotation, rotated.Resolution)
	require.False(t, bodyMissing.Resolved, "正文缺失绝不能被同批消解 ✓")
	require.Empty(t, bodyMissing.Resolution, "正文缺失不得获得任何消解词 ✓")
}

// TestResumeHealsRotatedGapWithSiblingDeliverError 验收混组时的**上界不串扰**。
//
// 构造：轮转缺口 [0,0] + 投递错缺口 [5000,6000]（同属白名单但**不同消解词组**）。
// 期望：两组各自被消解、各用自己的词；轮转组**不得**因同批处理的投递错上界而被写错措辞 ✓。
//
// 转红：把消解词分配退回"全白名单共用一个词"（即让 reReadResolutionForReason 只剩一个分支）
// ⇒ 在「轮转必须用自己的消解词」处红 ✓。注意：本用例**不**因删白名单项而红
// （两因都在名单内时仍全绿 ✓），它盯的是分账而不是集合。
func TestResumeHealsRotatedGapWithSiblingDeliverError(t *testing.T) {
	key := testKey("inst:twogroups", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	require.NoError(t, led.PauseAcquire(key, walBacklogPauseReason+" (entries=5/4)"))
	require.NoError(t, led.RecordGap(key, 0, 0, ledger.GapReasonRotatedSegmentNotReady, "轮转段未就绪"))
	require.NoError(t, led.RecordGap(key, 5000, 6000, ledger.GapReasonDeliverError, "投递失败"))

	require.True(t, wal.EvaluateResume(), "两类都在白名单内 ⇒ 必须恢复 ✓")
	ent := led.Get(key)
	for _, g := range ent.Gaps {
		require.True(t, g.Resolved, "白名单内原因都应被消解：reason=%s ✓", g.Reason)
		switch g.Reason {
		case ledger.GapReasonRotatedSegmentNotReady:
			require.Equal(t, ResolutionReReadOnResumeRotation, g.Resolution,
				"轮转类必须用自己的消解词（不得被同批的投递错措辞覆盖 ✗）")
		case ledger.GapReasonDeliverError:
			require.Equal(t, ResolutionReReadOnResumeDelivery, g.Resolution,
				"投递错类必须用自己的消解词（与轮转明确分账 ✓）")
		}
	}
}

// TestResumeHealsZeroLengthHealableGaps 钉住「零长度缺口（EndPos=0）能被正确处理」。
//
// 机制：消解判据是「缺口末端 ≤ 上界」（`ledger.ResolveGapsThroughAllowed` 的 `gap.EndPos > position`
// 反判），而零长度缺口不会推高上界（`EndPos=0`）⇒ `through=0` 必须能消解 `[0,0]` ✓。
// **这正是现场 86 源的形态**（登记点恒写 [0,0]）。此前的实现靠一个独立的 `found` 标志来保证
// "候选存在"，一旦把它换成"上界 map 里有没有 key"，零长度缺口就会被整组静默丢掉 ✗
// （实测：本用例与既有 TestReReadResumeClassificationHandlesZeroPosition 同时转红 ✓）。
func TestResumeHealsZeroLengthHealableGaps(t *testing.T) {
	for _, reason := range []string{
		ledger.GapReasonRotatedSegmentNotReady,
		ledger.GapReasonAppendRejected,
		ledger.GapReasonDeliverError,
		ledger.GapReasonDeliverErrorWorkerSource,
		ledger.GapReasonWALCommitFailed, // 名单外：零长度也不得被消解（负例同批 ✓）
	} {
		t.Run(reason, func(t *testing.T) {
			led, wal, key := newPausedSourceForGap(t, "inst:zerolen", reason, 0, 0)
			resumed := wal.EvaluateResume()
			gap := led.Get(key).Gaps[0]
			if ledger.IsGapReasonHealableOnReResume(reason) {
				require.True(t, resumed, "零长度可愈合缺口必须能被消解（through=0 足够 ✓）")
				require.True(t, gap.Resolved)
				require.Equal(t, reReadResolutionForReason(reason), gap.Resolution)
				require.True(t, IsReReadHealableResolution(gap.Resolution))
				return
			}
			require.False(t, resumed, "白名单外的原因即使零长度也必须继续阻塞 ✓")
			require.False(t, gap.Resolved)
		})
	}
}

// TestReReadResolutionForReasonKeepsAuditSplit 钉住分账表：白名单内每个原因都有精确措辞。
//
// 为什么必须显式钉：兜底词（gapResolutionReReadFallback）的存在是为了让"新增白名单原因"不会
// 静默卡死；代价是"忘记登记措辞"不再有任何可见症状 ⇒ 由这条断言把它变成红灯 ✓。
func TestReReadResolutionForReasonKeepsAuditSplit(t *testing.T) {
	require.Equal(t, gapResolutionReReadOnResume,
		reReadResolutionForReason(ledger.GapReasonAppendRejected))
	require.Equal(t, gapResolutionReReadOnResumeDelivery,
		reReadResolutionForReason(ledger.GapReasonDeliverError))
	require.Equal(t, gapResolutionReReadOnResumeDelivery,
		reReadResolutionForReason(ledger.GapReasonDeliverErrorWorkerSource))
	require.Equal(t, ResolutionReReadOnResumeRotation,
		reReadResolutionForReason(ledger.GapReasonRotatedSegmentNotReady))

	// 白名单内**每一个**原因都必须拿到非兜底措辞（新增白名单项而忘记登记 ⇒ 在此转红 ✓）。
	for _, reason := range ledger.GapReasonsHealableOnReResume() {
		require.NotEqual(t, gapResolutionReReadFallback, reReadResolutionForReason(reason),
			"白名单原因 %q 必须有精确的消解措辞（不得落到兜底词 ✓）", reason)
	}
	// 兜底词本身也必须属"待重读"类：否则白名单项即使拿到兜底词也不会被破环直投步认作非终局 ✓。
	require.True(t, IsReReadHealableResolution(gapResolutionReReadFallback))
}
