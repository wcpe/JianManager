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

// TestBacklogPausedSourceWithResolvableGapRevivesByFallback（现场终章 2026-10-03，直指"44 源/27 清零不复活"）：
// **积压已清零、但被"未消解缺口"挡在暂停上的源，必须由容量兜底循环救活**。
//
// 现场形态：`source_wal entries=0` + `acquire_paused=1` + `gap_open` 66→85 ✗✗ ⇒ 恢复前置里的
// "账本无未解决 gap"（internal/worker/logs/ledger/ledger.go:876-885 `unresolved gaps still block
// acquisition`）直接拒绝 ResumeAcquire ⇒ 兜底环只评估积压**永远不会复活** ✗。
// 修法：兜底环先「尽力排空存量（补投递证据）+ 从已发布投影消解可消解缺口」，再评估恢复 ✓。
//
// 2026-10-29 语义变更：积压越界不再暂停采集（改丢最旧），已造不出“越界暂停”；
// 而本用例要守的是「**暂停源被未消解缺口挡死，兜底环必须把它救活**」这条链——
// 暂停从哪来无关紧要，但必须是兜底环**认的**那一类（`IsBacklogPauseReason`，否则候选门直接 skip）。
// 所以沿用旧二进制留下的**存量积压暂停**形态（legacyBacklogPauseReason），
// 缺口仍由**真实投递失败**产生（不是手写 RecordGap：那样就绕过了缺口登记的真实条件）。
//
// 转红方式（实测）：去掉兜底环里的 `DeliverPending`（无投递证据）⇒ 源停在 paused ✗。
func TestBacklogPausedSourceWithResolvableGapRevivesByFallback(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	// 先建空文件：FILE_PRIMARY 源登记一律 StartAtEOF（裁定 D），内容必须在登记之后才产生。
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
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

	// 造出"现场形态"：投递失败 ⇒ 缺口 + **存量积压暂停**。
	fixture.failWrites.Store(true)
	pipe.WAL().SetLimits(1, 0)
	appendPollTestLines(t, logPath, "[12:00:01] [Server thread/INFO]: first\n"+
		"[12:00:02] [Server thread/INFO]: second\n")
	m.pollOnce()
	appendLogLine(t, logPath, "[12:00:03] [Server thread/INFO]: third\n")
	m.pollOnce()
	extra := pipe.Ledger().Get(pipe.Key())
	require.False(t, extra.AcquirePaused,
		"越界不得暂停采集（2026-10-29 丢弃语义）；若这里已经暂停说明实现回退了")
	if dropped, _ := pipe.WAL().DropStats(); dropped <= 0 {
		t.Fatalf("越界必须丢最旧（不背压），实测丢弃 %d 条", dropped)
	}
	// 暂停必须由兜底环**认的**原因造成（否则候选门直接 skip，测不到“能否救活”）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), legacyBacklogPauseReason))

	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused, "前置：源必须处于存量积压暂停（兜底环的入口）")
	require.Positive(t, ledger.UnresolvedGapCountOf(entry), "前置：暂停期间必须存在未消解缺口（现场同型）")
	require.True(t, acquire.IsBacklogPauseReason(entry.PauseReason), "前置：暂停原因必须是积压类（%q）", entry.PauseReason)

	// 现场的另一半：积压清零（这里把可剪条目剪掉，模拟 entries=0 ✓）。
	pipe.WAL().SetLimits(1_000_000, 0)
	// 关键：**不再走采集轮**（现场 pollOnce 被回放重活撑满 ✗）——只允许容量兜底循环来救 ✓。
	fixture.failWrites.Store(false)
	m.maybeResumePausedSources()

	got := pipe.Ledger().Get(pipe.Key())
	// 按原因数：可愈合类（投递失败）必须被兜底消解到 0；
	// 丢弃类缺口（丢弃语义新引入的那一条）**必须仍然可见**、且不得阻塞复活
	// （它已在 ledger.gapReasonsNonBlocking 里）。两者混成一个总数就分不清
	// “兜底没生效”和“多了一个本质不同的原因”。
	require.Zero(t, unresolvedGapsOfReason(got, ledger.GapReasonDeliverError),
		"兜底必须消解可消解的缺口（有已完成投递/已发布投影证据时才允许 ✓）")
	require.Positive(t, unresolvedGapsOfReason(got, ledger.GapReasonWALBacklogDropped),
		"丢弃类缺口必须仍然可见（真丢不回来的字节不得被自动消解抹掉）")
	// 强度不降：剩下的**全部**未解决缺口必须就是那些丢弃类缺口——不得还留着别的原因。
	require.Equal(t, unresolvedGapsOfReason(got, ledger.GapReasonWALBacklogDropped),
		ledger.UnresolvedGapCountOf(got),
		"除丢弃类外不得残留任何未解决缺口（否则就是“兜底没消完”被漏过）")
	require.False(t, got.AcquirePaused,
		"积压清零 + 缺口不再阻塞后必须自动复活（现场 27 源 entries=0 仍 paused 的停死形态 ✗✗）")
	require.Empty(t, got.PauseReason)
}
