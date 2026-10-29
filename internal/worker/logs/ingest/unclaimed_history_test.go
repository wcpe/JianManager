package ingest

// 历史未认领段丢弃的回归（2026-10-30）。
//
// ## 被钉住的三件事
//
//	① 未认领段（既无 verified_runs 也无 request_done）会被丢弃，并留下可查缺口；
//	② 丢弃后 `delivery` 水位**能推进**（否则死锁原封不动）；
//	③ 但 `RequestDoneRanges` **不得**多出那段（不谎报已投递）。
//
// 第 ③ 条是红线：那段字节确实没投递成功过，只是被确定性地放弃了。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// unclaimedHistoryFixture 造一个「事件段有数据、但账本完全未认领」的源。
//
// 与生产 inst:318 同构：段里有正文，而 verified_runs 与 request_done 都是空的。
func unclaimedHistoryFixture(t *testing.T) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	source := SourceConfig{
		LogSourceID: "inst:unclaimed/file", SourceGeneration: "g1",
		Path: filepath.Join(root, "latest.log"), Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "inst:unclaimed", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(catalog.NewMemJournal()),
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	require.NotNil(t, pipe, "夹具应已建管道")

	// 向事件段写入正文（模拟历史遗留：段里有、但账本未认领）。
	ev := logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 100, End: 199},
		"2026-10-29T00:00:00Z", "2026-10-29T00:00:01Z", "INFO", "stdout", "未认领的历史行",
	)
	require.NoError(t, m.events.Append(key, []logtypes.Event{ev}))
	// 模拟真实形态：正文已读入（read 到段尾），但账本从未认领它。
	// 这正是 inst:318 的形态：read=449万、delivery=313900。
	require.NoError(t, pipe.Ledger().AdvanceRead(pipe.Key(), 199))
	return m, source, pipe
}

// RequestDoneRangesOfForTest 取该源当前已确认投递的区间（测试辅助）。
func RequestDoneRangesOfForTest(p *pipeline.Pipeline) []ledger.PositionRange {
	if p == nil {
		return nil
	}
	entry := p.Ledger().Get(p.Key())
	if entry == nil {
		return nil
	}
	return ledger.RequestDoneRangesOf(entry)
}



// TestDropUnclaimedHistoryAdvancesDeliveryWithoutFakingRequestDone 是主正例。
//
// 转红方式：把 `RecordDelivery(..., DeliveryDropped)` 那行删掉
// ⇒ 断言 ② 红（delivery 不推进，死锁依旧）。
func TestDropUnclaimedHistoryAdvancesDeliveryWithoutFakingRequestDone(t *testing.T) {
	mgr, source, pipe := unclaimedHistoryFixture(t)


	// 前置：账本完全没有认领证据（与 inst:318 同构）。
	before := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, before)
	require.Empty(t, RequestDoneRangesOfForTest(pipe), "前置：无任何投递证据")

	dropped := mgr.dropUnclaimedHistory(context.Background(), source, pipe)
	require.Positive(t, dropped, "事件段里有数据但无认领 ⇒ 必须丢掉那段")

	after := pipe.Ledger().Get(pipe.Key())

	// ① 缺口可查
	foundGap := false
	for _, g := range after.Gaps {
		if g.Reason == "UNCLAIMED_HISTORY_DROPPED" && !g.Resolved {
			foundGap = true
			require.Positive(t, g.EndPos-g.StartPos, "缺口区间必须非空")
		}
	}
	require.True(t, foundGap, "丢弃必须留可查缺口（不得静默丢）")

	// ② 水位推进
	require.Greater(t, after.Positions.Delivery, before.Positions.Delivery,
		"丢弃必须能推进 delivery（否则 resolveOneSourceGaps 的 delivery>=read 入口永远不成立）")

	// ③ 红线：不得谎报已投递
	for _, r := range RequestDoneRangesOfForTest(pipe) {
		require.False(t, r.From < after.Positions.Delivery && r.To >= after.Positions.Delivery,
			"RequestDoneRanges 不得因丢弃而变宽（那是谎报）")
	}
	require.Empty(t, RequestDoneRangesOfForTest(pipe),
		"本次从未真正投递过，RequestDoneRanges 必须仍然为空")

	// 幂等：再跑一次不应重复登记
	dropped2 := mgr.dropUnclaimedHistory(context.Background(), source, pipe)
	require.Zero(t, dropped2, "已丢弃的区间不得重复丢弃")
}
