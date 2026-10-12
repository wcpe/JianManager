package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestBacklogPausedSourceWithPublishedEvidenceGapRevivesByFallback（补 MUT1 支路的独立红证）：
// **积压为零（entries=0 ⇒ 无存量可投递 ✗）时，缺口只能靠「已发布投影运行」的证据消解** ⇒ 兜底
// 必须接上这条支路，否则现场那种"积压清零仍不复活"的源永远没有第二条路 ✗✗。
//
// 现场形态对照：44 源暂停里 27 源 `source_wal entries=0` —— 没有可投递存量 ⇒ 投递证据支路空转 ✗，
// 此时唯一能解闸的是"缺口末端 ≤ 已发布投影封闭水位 + 逐字段校验覆盖"（缺口语义红线允许的两种
// 证据之一 ✓）。
//
// 转红方式（实测）：去掉兜底环里的 `autoResolveGapsFromPublished` ⇒ 本用例其余断言全绿、仅
// 「缺口被消解 / 源已复活」变红 ✓（这正是 MUT1 在本夹具下曾误绿的那条支路 ✗）。
func TestBacklogPausedSourceWithPublishedEvidenceGapRevivesByFallback(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:pubgap/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:pubgap", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 前置一：先完成一次采集 ⇒ 已发布投影证据成立 ✓（与既有 published 用例同型）。
	m.pollOnce()
	closed, complete := m.publishedClosedForSource(source, m.state.Sources[key])
	require.True(t, complete, "夹具必须先有已发布投影")
	require.Greater(t, closed, uint64(0))

	// 前置二：一条**允许名单内**且被已发布水位覆盖的缺口（投递错误 ⇒ 允许按投影证据消解 ✓）。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, closed,
		ledger.GapReasonDeliverError, "transport down"))
	// 前置三：积压类暂停（现场同型 ✓）+ **积压为零**（无存量可投递 ⇒ 排空支路无法补证据 ✓）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))
	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, acquire.IsBacklogPauseReason(entry.PauseReason))
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry), "前置：必须存在未消解缺口")

	// 只走容量兜底（不走采集轮 ✓）：必须靠**已发布投影证据**解闸并复活 ✓。
	m.maybeResumePausedSources()
	got := pipe.Ledger().Get(pipe.Key())
	require.Zero(t, ledger.UnresolvedGapCountOf(got),
		"零积压时缺口只能靠已发布投影证据消解：兜底必须接上这条支路 ✗")
	require.False(t, got.AcquirePaused,
		"缺口消解 + 滞回满足后必须复活（现场 entries=0 仍 paused 的停死形态 ✗✗）")
}
