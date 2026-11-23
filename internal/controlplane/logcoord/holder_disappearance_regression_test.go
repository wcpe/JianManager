package logcoord

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// holderCoverageWorker 对各查询面上报同一真实视图的完整覆盖，用于隔离 Resolver 遗漏。
type holderCoverageWorker struct{ cursorWorker }

func (w *holderCoverageWorker) Stats(_ context.Context, req WorkerSearchRequest, _ []string, _ string) (*WorkerStatsResponse, error) {
	return &WorkerStatsResponse{ViewID: req.ViewID, Targets: []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}},
		Coverage: &Coverage{Complete: true, EnumerationState: EnumExhausted}, Quality: exactQuality}, nil
}

func (w *holderCoverageWorker) Facets(_ context.Context, req WorkerSearchRequest, _ []string, _ uint32) (*WorkerFacetsResponse, error) {
	return &WorkerFacetsResponse{ViewID: req.ViewID, Targets: []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}},
		Coverage: &Coverage{Complete: true, EnumerationState: EnumExhausted}}, nil
}

func TestSixModuleOnlineOnlyExcludedHolderNeverJoinsExistingView(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "holder:2", AuthorizationID: "inst:1", WorkerTargetID: "inst:1", WorkerID: "w2", Readiness: ReadyOffline, HistoricalHolder: true},
	}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = &holderCoverageWorker{cursorWorker{id: "w1", target: "inst:1"}}
	dialer.clients["w2"] = &holderCoverageWorker{cursorWorker{id: "w2", target: "inst:1"}}
	coord := New(resolver, dialer)
	query := review05Query()
	query.Fast, query.OnlineOnly = true, true
	first, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.False(t, first.Coverage.Complete)
	require.Len(t, first.View.CoverageTargets, 2)
	resolver.targets[1].Readiness = ReadyOnline
	query.ViewID = first.View.ViewID
	second, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.False(t, second.Coverage.Complete)
	require.Len(t, second.Coverage.Targets, 2)
	require.Equal(t, first.View.TargetIDs, second.View.TargetIDs)
	require.Zero(t, dialer.callCount("w2"), "首屏已排除身份上线后也不能加入既有 Worker 视图")
}

func TestSixModuleMissingHistoricalHolderRemainsInCoverage(t *testing.T) {
	for _, onlineOnly := range []bool{false, true} {
		for _, operation := range []string{"search", "fields", "stats", "facets", "page_export"} {
			t.Run(fmt.Sprintf("%s/仅在线%t", operation, onlineOnly), func(t *testing.T) {
				resolver := &fakeTargetResolver{targets: []TargetInfo{
					{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline},
					{ID: "holder:2", AuthorizationID: "inst:1", WorkerTargetID: "inst:1", WorkerID: "w2", Readiness: ReadyOnline, HistoricalHolder: true},
				}}
				if onlineOnly {
					resolver.targets[1].Readiness = ReadyOffline
				}
				dialer := newFakeDialer()
				dialer.clients["w1"] = &holderCoverageWorker{cursorWorker{id: "w1", target: "inst:1", items: []Event{
					mkEvent("first", "current", "g", 20, "2026-09-22T12:00:00Z", "INFO", "首屏"),
					mkEvent("old", "current", "g", 10, "2026-09-22T10:00:00Z", "INFO", "后页"),
					mkEvent("older", "current", "g", 0, "2026-09-22T08:00:00Z", "INFO", "仍有后页"),
				}}}
				dialer.clients["w2"] = &holderCoverageWorker{cursorWorker{id: "w2", target: "inst:1", items: []Event{
					mkEvent("historical", "old-holder", "g", 10, "2026-09-22T11:00:00Z", "INFO", "历史持有者"),
				}}}
				coord := New(resolver, dialer)
				query := review05Query()
				query.Fast = true
				query.OnlineOnly = onlineOnly
				first, err := coord.Search(context.Background(), query)
				require.NoError(t, err)
				if onlineOnly {
					require.False(t, first.Coverage.Complete)
					require.Len(t, first.View.TargetIDs, 1)
				} else {
					require.True(t, first.Coverage.Complete)
					require.NotEmpty(t, first.NextCursor)
					require.Len(t, first.View.TargetIDs, 2)
				}

				// 生产 holder 枚举失败会省略该成员；旧视图的成员资格不能因此缩小。
				resolver.targets = resolver.targets[:1]
				query.ViewID, query.Cursor, query.Fast, query.BypassCache = first.View.ViewID, first.NextCursor, false, true
				var coverage Coverage
				switch operation {
				case "search":
					out, err := coord.Search(context.Background(), query)
					require.NoError(t, err)
					coverage = out.Coverage
					require.Empty(t, out.NextCursor, "不能越过消失持有者未知范围签发下一页游标")
					require.False(t, out.Exhausted)
				case "fields":
					out, err := coord.Fields(context.Background(), query)
					require.NoError(t, err)
					coverage = out.Coverage
				case "stats":
					out, err := coord.Stats(context.Background(), StatsQuery{Query: query})
					require.NoError(t, err)
					coverage = out.Coverage
				case "facets":
					out, err := coord.Facets(context.Background(), FacetsQuery{Query: query, Dimensions: []string{"level"}})
					require.NoError(t, err)
					coverage = out.Coverage
				case "page_export":
					out, err := coord.ExportPage(context.Background(), query)
					require.NoError(t, err)
					coverage = out.Coverage
					require.True(t, out.ExportIncomplete)
					require.Nil(t, out.Artifact)
				}
				require.False(t, coverage.Complete, "固定视图中的历史持有者不能从覆盖全集消失")
				var missing *TargetCoverage
				for index := range coverage.Targets {
					if coverage.Targets[index].TargetID == "holder:2" {
						missing = &coverage.Targets[index]
					}
				}
				require.NotNil(t, missing)
				require.Equal(t, CoverageNotReady, missing.State)
				require.True(t, missing.HistoricalHolder)
				require.Len(t, coverage.Targets, 2)
			})
		}
	}
}
