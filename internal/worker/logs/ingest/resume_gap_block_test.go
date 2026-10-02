package ingest

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestBacklogPausedSourceWithResolvableGapRevivesByFallback（现场终章 2026-10-03，直指"44 源/27 清零不复活"）：
// **积压已清零、但被"未消解缺口"挡在暂停上的源，必须由容量兜底循环救活**。
//
// 现场形态：`source_wal entries=0` + `acquire_paused=1` + `gap_open` 66→85 ✗✗ ⇒ 恢复前置里的
// "账本无未解决 gap"（internal/worker/logs/ledger/ledger.go:876-885 `unresolved gaps still block
// acquisition`）直接拒绝 ResumeAcquire ⇒ 兜底环只评估积压**永远不会复活** ✗。
// 修法：兜底环先「尽力排空存量（补投递证据）+ 从已发布投影消解可消解缺口」，再评估恢复 ✓。
//
// 转红方式（实测两种，二者都必须红）：
//   - 去掉兜底环里的 `autoResolveGapsFromPublished`（只看积压）⇒ 源停在 paused ✗；
//   - 去掉兜底环里的 `DeliverPending`（无投递证据）⇒ 同样停在 paused ✗。
func TestBacklogPausedSourceWithResolvableGapRevivesByFallback(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:gapblock/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gapblock", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 造出"现场形态"：投递失败 ⇒ 缺口 + 积压越界 ⇒ 暂停。
	fixture.failWrites.Store(true)
	pipe.WAL().SetLimits(1, 0)
	m.pollOnce()
	appendLogLine(t, logPath, "[12:00:03] [Server thread/INFO]: third\n")
	m.pollOnce()
	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused, "前置：越界必须暂停")
	require.Positive(t, ledger.UnresolvedGapCountOf(entry), "前置：暂停期间必须存在未消解缺口（现场同型）")
	require.True(t, acquire.IsBacklogPauseReason(entry.PauseReason), "前置：暂停原因必须是积压类（%q）", entry.PauseReason)

	// 现场的另一半：积压清零（这里把可剪条目剪掉，模拟 entries=0 ✓）。
	pipe.WAL().SetLimits(1_000_000, 0)
	// 关键：**不再走采集轮**（现场 pollOnce 被回放重活撑满 ✗）——只允许容量兜底循环来救 ✓。
	fixture.failWrites.Store(false)
	m.maybeResumePausedSources()

	got := pipe.Ledger().Get(pipe.Key())
	require.Zero(t, ledger.UnresolvedGapCountOf(got),
		"兜底必须消解可消解的缺口（有已完成投递/已发布投影证据时才允许 ✓）")
	require.False(t, got.AcquirePaused,
		"积压清零 + 缺口消解后必须自动复活（现场 27 源 entries=0 仍 paused 的停死形态 ✗✗）")
	require.Empty(t, got.PauseReason)
}
