package ingest

// 「可分类判据与白名单单一真源」的红绿用例（2026-10-10 生产缺陷）。
//
// 现场：87 个源被「不可分类缺口」永久阻塞（积压均已归零 0/20000），其中 86 源是
// `ROTATED_SEGMENT_NOT_READY`（时序观察，区间恒为 [0,0]）+ 3 源 `WAL_BODY_MISSING`
// （真实正文缺失）。区别在于：前者"恢复后回读可愈合"⇒ 应当自动放行 ✓，
// 后者回读造不出已丢字节 ⇒ 必须人工裁定 ✓。
//
// 此前判据在 ingest（本包 `gapReasonClassifiableOnResume`）与 acquire（分类裁定两段循环）
// **各写一份原因清单**，`ROTATED_SEGMENT_NOT_READY` 两处都不在 ⇒ 永远无出路且无任何报错 ✗。
// 修法：白名单收敛为 `ledger.GapReasonsHealableOnReResume` 单一真源，两侧只经由它判定。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// newBlockedFixtureSource 起一个带单源、已建管道的 Manager，并返回账本访问句柄。
func newBlockedFixtureSource(t *testing.T, logSourceID string) (*Manager, *ledger.Ledger, ledger.SourceKey) {
	t.Helper()
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	require.NotNil(t, pipe, "夹具应已建管道")
	return m, pipe.Ledger(), pipe.Key()
}

// TestGapReasonClassifiableOnResumeUsesLedgerSourceOfTruth —— 判据必须与账本真源逐项一致。
//
// 这条断言把"两侧名单必须手工保持一致"变成**结构性**保证：本包不再持有任何原因清单，
// 判定结果只能等于 `ledger.IsGapReasonHealableOnReResume`。
//
// 转红：把 `gapReasonClassifiableOnResume` 改回任何手写 switch（少了 ROTATED_SEGMENT_NOT_READY
// 即红）⇒ 必红 ✓。
func TestGapReasonClassifiableOnResumeUsesLedgerSourceOfTruth(t *testing.T) {
	for _, reason := range ledger.GapReasonsHealableOnReResume() {
		require.True(t, gapReasonClassifiableOnResume(reason),
			"白名单原因 %q 在 ingest 侧必须同样可分类（两侧名单不得漂移 ✗）", reason)
		require.True(t, ledger.IsGapReasonHealableOnReResume(reason))
	}
	// 名单外的一切（含真实数据缺失与未知原因）必须默认拒绝。
	for _, reason := range []string{
		ledger.GapReasonWALBodyMissing,
		ledger.GapReasonStdioRawWriteFailed,
		ledger.GapReasonWALCommitFailed,
		ledger.GapReasonArchiveGzipCorrupt,
		ledger.GapReasonFileRewritten,
		"FUTURE_UNKNOWN",
		"",
	} {
		require.False(t, gapReasonClassifiableOnResume(reason),
			"原因 %q 不得被判为可分类（放行即静默掩盖丢数据 ✗）", reason)
	}
}

// TestRotatedSegmentNotReadyNoLongerCountsAsBlocking —— 86 源现场形态现在**不再**判永久阻塞。
//
// 现场等价形态：源持有唯一一条 `ROTATED_SEGMENT_NOT_READY` 未决缺口（[0,0]）⇒ 修复前
// `Blocking == Unresolved == 1` ⇒ 被列入「永久阻塞」（而它其实有自动出路 ✗）；
// 修复后该原因可分类 ⇒ `Blocking == 0` ⇒ 不再列入 ✓。
//
// 转红：把 GapReasonRotatedSegmentNotReady 从 ledger 白名单真源移除 ⇒ 必红 ✓
// （本用例与 acquire 侧的 TestResumeHealsRotatedSegmentNotReadyGap 同时转红 ✓）。
func TestRotatedSegmentNotReadyNoLongerCountsAsBlocking(t *testing.T) {
	m, led, ledKey := newBlockedFixtureSource(t, "inst:rotated-marker")
	require.NoError(t, led.RecordGap(ledKey, 0, 0, ledger.GapReasonRotatedSegmentNotReady, "轮转段未就绪"))

	require.Empty(t, m.UnresolvedGapBlockedSources(),
		"可愈合的轮转时序缺口不得再判为永久阻塞（现场 86 源 ✗）")

	diags := m.ResumeDiagnostics()
	require.Len(t, diags, 1)
	require.Equal(t, 1, diags[0].Unresolved, "缺口仍应可见（只是不再是阻塞项 ✓）")
	require.Zero(t, diags[0].Blocking, "轮转时序缺口不得计入阻塞数 ✓")
	var view *UnresolvedGapView
	for i := range diags[0].UnresolvedGaps {
		if diags[0].UnresolvedGaps[i].Reason == ledger.GapReasonRotatedSegmentNotReady {
			view = &diags[0].UnresolvedGaps[i]
		}
	}
	require.NotNil(t, view, "诊断面必须仍列出该缺口 ✓")
	require.True(t, view.Classiable, "诊断面必须标它为可分类 ✓")
}

// TestWALBodyMissingStillCountsAsBlocking —— 负例：真实正文缺失仍然判永久阻塞。
//
// 语义：`WAL_BODY_MISSING` 是确有日志字节没被恢复 ⇒ 无自动出路 ⇒ 必须在诊断面点名，
// 让运维能把它与"还在等滞回"的源区分开（这正是本标记存在的理由 ✓）。
//
// 转红：把 GapReasonWALBodyMissing 加入 ledger 白名单真源 ⇒ 必红 ✓
// （与 acquire 侧的 TestResumeStillBlocksWALBodyMissingGap 同时转红 ✓）。
func TestWALBodyMissingStillCountsAsBlocking(t *testing.T) {
	m, led, ledKey := newBlockedFixtureSource(t, "inst:bodymissing-marker")
	require.NoError(t, led.RecordGap(ledKey, 300, 400, ledger.GapReasonWALBodyMissing, "正文缺失"))

	blocked := m.UnresolvedGapBlockedSources()
	require.Len(t, blocked, 1, "真实正文缺失必须仍判永久阻塞（人工裁定是唯一出路 ✓）")
	require.Equal(t, 1, blocked[0].Blocking)
	require.Equal(t, 1, blocked[0].Unresolved)

	lines := m.GapBlockedDiagnosticLines()
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], ledger.GapReasonWALBodyMissing,
		"打点须带出阻塞原因（运维据此判断该走人工裁定还是重置）✓")
}
