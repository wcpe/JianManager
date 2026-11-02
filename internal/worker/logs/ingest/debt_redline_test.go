package ingest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 本文件是「补登记」修复的**红线验收**：
// 补登记只能为「真的能在 VL 里查到」的记录登记证据；
// **真的不在 VL 里的记录必须继续算欠账**，一条都不许被静默吞掉。
//
// 为什么这条最要紧（ADR-101 的语义）：
// 「自动放弃 = 静默丢日志」。补登记的诱惑是"把欠账都标成已验证，closed 就通了"——
// 那等于把没落库的数据谎报成已发布。本文件用两条互补的用例把这条路堵死。

// TestBlockingRecordStillDebtsWhenTrulyMissing 钉住：
// 记录**真的不在 VL 里**时，补登记不得把它变成"已验证"。
//
// 转红方式（实测）：把 debt 分支的补登记改成"无条件 recordVerifiedRuns(blocking)"
// （不问可见性）⇒ 本用例必红——那正是静默抹平的形态。
func TestBlockingRecordStillDebtsWhenTrulyMissing(t *testing.T) {
	source, root := gapVisibility_TestSource(t)
	day := source.UTCDay

	// 一条**永远不会被写进 VL** 的旧记录。
	oldEvents := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}}, "missing")
	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	m, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe, ok := m.pipeFor(key)
	require.True(t, ok)

	for _, ev := range oldEvents {
		pipe.WAL().Append(ev)
	}
	pipe.WAL().Commit()

	// 夹具里**故意不写入任何行** ⇒ VL 恒查不到。
	// 直接验「补登记」所依赖的那个判断：landedVisibleInVL 必须返回空。
	landed := m.landedVisibleInVL(source, oldEvents)
	require.Empty(t, landed,
		"VL 里根本没有的记录，可见性核对必须返回空——这是补登记唯一的准入判据；"+
			"一旦它返回非空，debt 分支就会把没落库的数据标成已验证（静默抹平欠账）")

	// 推论：debt 判据仍然成立（记录仍然算欠账）。
	tail := gapVisibilityEvents(source, day, [][2]uint64{{1000, 1100}}, "tail")
	debt, blocking, err := m.hasKnownUnpublishedDayDebt(context.Background(), source, tail, nil, true)
	require.NoError(t, err)
	require.True(t, debt, "真的没落库的记录必须继续算欠账")
	require.NotNil(t, blocking)

	fixture.mu.Lock()
	rows := len(fixture.rows)
	fixture.mu.Unlock()
	require.Zero(t, rows, "夹具前提：VL 里一行都没有")
}

// TestBlockingRecordVerifiedOnlyWhenVisible 钉住正向：
// 记录**确实在 VL 里**时，补登记才允许发生。
//
// 与上一条合起来构成完整判据：可见才登记，不可见不登记。
//
// 转红方式：把补登记的判据从 `len(landed)==1` 改成无条件执行 ⇒ 上一条红；
// 改成永不执行 ⇒ 本用例红。
func TestBlockingRecordVerifiedOnlyWhenVisible(t *testing.T) {
	source, _ := gapVisibility_TestSource(t)
	day := source.UTCDay

	oldEvents := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}}, "visible")
	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	publishGapVisibilityDay(t, cat, source, oldEvents, day)

	// 把这条记录真的写进 VL。
	fixture.mu.Lock()
	for _, ev := range oldEvents {
		fixture.rows = append(fixture.rows, reconcileVLRow{
			ProjectionGeneration: "projection-1",
			EventID:              ev.EventID,
			EventTimeUTC:         ev.EventTimeUTC,
			LogSourceID:          source.LogSourceID,
			SourceGeneration:     source.SourceGeneration,
			RecordEnd:            ev.Record.End,
			Message:              ev.Message,
			Level:                ev.Level,
			Stream:               ev.Stream,
			CanonicalHash:        ev.CanonicalHash,
		})
	}
	fixture.mu.Unlock()

	m, err := New(Options{
		Root: t.TempDir(), VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	landed := m.landedVisibleInVL(source, oldEvents)
	require.Len(t, landed, 1,
		"VL 里查得到的记录必须被认定为可见——补登记正依赖这个结论把它从欠账里摘出去")
}
