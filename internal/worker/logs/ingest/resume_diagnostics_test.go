package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestResumeDiagnosticsExposePerSourceGate（2026-10-04 现场：`gap_open` 在降而 `paused` 平 ✗）：
// 全局计数不能回答"清零却仍暂停"⇒ 诊断面必须把**单源**判据的每个输入摊开 ✓。
//
// 判据（与 ResumeAcquire 一致 ✓）：单源解除暂停要求**它自己**的未消解缺口全部清零 ✓，
// 且暂停原因必须是本包积压/容量类 ✓（否则兜底环按红线跳过 ✓）。
func TestResumeDiagnosticsExposePerSourceGate(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:diag/file", SourceGeneration: "g1",
		Path: filepath.Join(root, "latest.log"), Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "inst:diag", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)
	require.NoError(t, os.WriteFile(source.Path, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))

	// 现场形态：积压类暂停 + 一条可分类消解（DELIVER_ERROR ✓）+ 一条不可（STDIO ✓）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "wal backlog limit exceeded (entries=5/4)"))
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 100, 200, ledger.GapReasonDeliverError, "connection refused"))
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 300, 400, ledger.GapReasonStdioRawWriteFailed, "raw write failed"))

	diags := m.ResumeDiagnostics()
	require.Len(t, diags, 1)
	d := diags[0]
	require.Equal(t, "inst:diag/file", d.LogSourceID)
	require.True(t, d.AcquirePaused)
	require.True(t, d.BacklogPause, "原因属积压类 ⇒ 走滞回自愈 ✓")
	require.Equal(t, 2, d.Unresolved, "未消解缺口总数 ✓")
	require.Equal(t, 1, d.UnresolvedByReason[ledger.GapReasonDeliverError])
	require.Equal(t, 1, d.UnresolvedByReason[ledger.GapReasonStdioRawWriteFailed])
	require.Equal(t, 2, len(d.UnresolvedGaps), "每条未消解缺口都要摊开（靶②要对水位 ✓）")
	for _, g := range d.UnresolvedGaps {
		if g.Reason == ledger.GapReasonDeliverError {
			require.True(t, g.Classiable, "DELIVER_ERROR 属可分类白名单 ✓")
			require.True(t, g.AboveDelivery,
				"本例投递水位为 0 ⇒ 缺口整体在其之上（= 靶②形态：水位覆盖判据消解不了，需恢复链重投 ✓）")
		}
	}
	require.Equal(t, 1, d.Blocking,
		"仍会挡住 ResumeAcquire 的只有不可分类那条（STDIO ✓）——这正是「清零却仍暂停」的判别输入 ✓")
}
