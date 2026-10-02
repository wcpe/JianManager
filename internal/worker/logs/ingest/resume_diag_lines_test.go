package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestResumeDiagnosticLinesIncludePausedSourcesReachability（用户点名的**可达性红证** ✗✓）：
// **有暂停源时，采样输出必须含该源** ✓ —— 这条钉住的正是"实现存在但不可达"的形态 ✗✗
// （`ResumeDiagnostics` 一度只有定义与测试引用 ⇒ 现场只能靠 SQL 反推 ✗）。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：采样折行时把 paused 源过滤掉（"只打非零"被误实现成"只打缺口非零" ✗）⇒ 红；
//   - 变异②：ResumeDiagnosticLines 直接返回空（= 接线形同没有 ✗）⇒ 红。
func filepathJoinForDiag(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(p, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
	return p
}

func TestResumeDiagnosticLinesIncludePausedSourcesReachability(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	cfgs := []SourceConfig{
		{LogSourceID: "inst:diaglg-paused/file", SourceGeneration: "g1", Path: filepathJoinForDiag(t, root, "paused.log"), Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:diaglg", UTCDay: runtimeTestUTCDay()},
		{LogSourceID: "inst:diaglg-ok/file", SourceGeneration: "g1", Path: filepathJoinForDiag(t, root, "ok.log"), Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:diaglg2", UTCDay: runtimeTestUTCDay()},
		// **现场形态**：暂停 + **零未消解缺口**（"清零却仍暂停"✗✓）—— 它只能靠 paused 分支进采样，
		// 正是"过滤掉 paused 源"这类变异的判别点 ✓。
		{LogSourceID: "inst:diaglg-cleared/file", SourceGeneration: "g1", Path: filepathJoinForDiag(t, root, "cleared.log"), Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:diaglg3", UTCDay: runtimeTestUTCDay()},
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: cfgs})
	require.NoError(t, err)
	paused := m.pipes["inst:diaglg-paused/file/g1"]
	require.NotNil(t, paused)
	require.NoError(t, paused.Ledger().PauseAcquire(paused.Key(), "wal backlog limit exceeded (entries=5/4)"))
	require.NoError(t, paused.Ledger().RecordGap(paused.Key(), 10, 20, ledger.GapReasonDeliverError, "connection refused"))
	cleared := m.pipes["inst:diaglg-cleared/file/g1"]
	require.NotNil(t, cleared)
	require.NoError(t, cleared.Ledger().PauseAcquire(cleared.Key(), "wal backlog limit exceeded (entries=5/4)"))
	require.Zero(t, ledger.UnresolvedGapCountOf(cleared.Ledger().Get(cleared.Key())), "夹具前提：清零 ✓")

	lines := m.ResumeDiagnosticLines()
	require.NotEmpty(t, lines, "有暂停源时采样输出不得为空（= 新工具不可达 ✗✓）")
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "inst:diaglg-paused/file", "暂停源必须出现在采样输出里 ✓")
	require.Contains(t, joined, "paused=true")
	require.Contains(t, joined, "unresolved=1")
	require.Contains(t, joined, "inst:diaglg-cleared/file",
		"**零缺口但暂停**的源也必须出现在采样输出里（现场 6 家的形态 ✗✓）")
	require.NotContains(t, joined, "inst:diaglg-ok/file", "正常源不得刷屏 ✓")
}
