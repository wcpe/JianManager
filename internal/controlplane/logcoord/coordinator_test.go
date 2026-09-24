package logcoord

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

type cursorWorker struct {
	id     string
	target string
	items  []Event
}

func (w *cursorWorker) OpenView(context.Context, WorkerSearchRequest) (*WorkerSearchResponse, error) {
	return &WorkerSearchResponse{ViewID: "wv-" + w.id, Targets: []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}}}, nil
}

func (w *cursorWorker) Search(_ context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	items := append([]Event(nil), w.items...)
	SortEventsInPlace(items)
	if req.Cursor != "" {
		cursor, err := workerquery.DecodeCursor(req.Cursor)
		if err != nil {
			return nil, err
		}
		boundary := SortKey{EventTimeUTC: MustParseRFC3339(cursor.SortKey.EventTimeUTC), LogSourceID: cursor.SortKey.LogSourceID,
			SourceGeneration: cursor.SortKey.SourceGeneration, RecordStart: cursor.SortKey.RecordStart,
			RecordEnd: cursor.SortKey.RecordEnd, EventID: cursor.SortKey.EventID}
		filtered := items[:0]
		for _, item := range items {
			if item.SortKey().Compare(boundary) > 0 {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	limit := req.Budget.Limit
	exhausted := true
	if limit > 0 && len(items) > limit {
		items = items[:limit]
		exhausted = false
	}
	return &WorkerSearchResponse{ViewID: req.ViewID, Items: items, Exhausted: exhausted,
		NextCursor: "worker-next", Targets: []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}}}, nil
}

func (w *cursorWorker) Tail(ctx context.Context, req WorkerSearchRequest, _ string) (*WorkerSearchResponse, error) {
	return w.Search(ctx, req)
}

func (w *cursorWorker) Fields(_ context.Context, req WorkerSearchRequest) (*WorkerFieldsResponse, error) {
	return &WorkerFieldsResponse{ViewID: req.ViewID, Fields: []string{"level", "stream"},
		Targets: []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}}}, nil
}

func (w *cursorWorker) Stats(context.Context, WorkerSearchRequest, []string, string) (*WorkerStatsResponse, error) {
	return nil, nil
}

func (w *cursorWorker) Facets(context.Context, WorkerSearchRequest, []string, uint32) (*WorkerFacetsResponse, error) {
	return nil, nil
}

func TestCoverageFromReadiness_ExecutionFailureIsNotSuccess(t *testing.T) {
	target := TargetInfo{ID: "node:1", WorkerID: "worker-1", Readiness: ReadyOnline}
	for _, reason := range []string{
		"worker_error", "search_failed", "stats_failed", "facets_failed",
		"empty_worker_response", "target_missing_in_worker_response", "fanout_budget_exceeded",
	} {
		got := CoverageFromReadiness(target, []string{reason})
		assert.Equal(t, CoveragePartial, got.State, reason)
	}
}

func TestSortKeyCompareOrder(t *testing.T) {
	newer := SortKey{EventTimeUTC: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), LogSourceID: "a", EventID: "1"}
	older := SortKey{EventTimeUTC: time.Date(2026, 9, 20, 11, 0, 0, 0, time.UTC), LogSourceID: "z", EventID: "9"}
	// event_time DESC → newer first
	assert.Negative(t, newer.Compare(older))
	assert.Positive(t, older.Compare(newer))

	// same time: log_source_id ASC
	a := SortKey{EventTimeUTC: newer.EventTimeUTC, LogSourceID: "a", EventID: "2"}
	b := SortKey{EventTimeUTC: newer.EventTimeUTC, LogSourceID: "b", EventID: "1"}
	assert.Negative(t, a.Compare(b))

	// same source: record_start DESC
	s1 := SortKey{EventTimeUTC: newer.EventTimeUTC, LogSourceID: "a", RecordStart: 20, EventID: "x"}
	s2 := SortKey{EventTimeUTC: newer.EventTimeUTC, LogSourceID: "a", RecordStart: 10, EventID: "y"}
	assert.Negative(t, s1.Compare(s2))
}

func TestCoordinatorBindsWorkerLocalViewBeforeSearch(t *testing.T) {
	worker := &fakeWorker{workerID: "w1", searchTarget: []WorkerTargetResult{{TargetID: "inst:1", State: CoverageSuccess}}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = worker
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)

	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"inst:1"},
		PrincipalKey:        "user:1|role:test|targets:inst:1",
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 1, worker.openViewCalls)
	assert.Equal(t, "worker-view-w1", worker.lastSearchView)
	assert.NotEqual(t, resp.View.ViewID, worker.lastSearchView)
}

func TestCoordinatorFansOutWorkersConcurrently(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "node:1", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "node:2", WorkerID: "w2", Readiness: ReadyOnline},
	}}
	dialer := newFakeDialer()
	for i, workerID := range []string{"w1", "w2"} {
		targetID := fmt.Sprintf("node:%d", i+1)
		dialer.clients[workerID] = &fakeWorker{workerID: workerID, searchDelay: 150 * time.Millisecond,
			searchTarget: []WorkerTargetResult{{TargetID: targetID, State: CoverageSuccess}}}
	}
	coord := New(resolver, dialer)
	started := time.Now()
	resp, err := coord.Search(context.Background(), Query{AuthorizedTargetIDs: []string{"node:1", "node:2"},
		PrincipalKey: "principal", Budget: QueryBudget{Limit: 10}})
	require.NoError(t, err)
	require.True(t, resp.Coverage.Complete)
	require.Less(t, time.Since(started), 275*time.Millisecond, "two 150ms workers must not be queried serially")
}

func TestCoordinatorPreservesArchiveMissingCoverageOnWorkerError(t *testing.T) {
	worker := &fakeWorker{workerID: "w1", searchErr: "no authoritative ranges; coverage partial",
		searchTarget: []WorkerTargetResult{{TargetID: "node:1", State: CoverageArchiveMissing, Reasons: []string{"ARCHIVE_NOT_RESTORED", "REHYDRATE_FAILED"}}}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = worker
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "node:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	resp, err := coord.Search(context.Background(), Query{AuthorizedTargetIDs: []string{"node:1"}, Budget: QueryBudget{Limit: 10}})
	require.NoError(t, err)
	require.False(t, resp.Coverage.Complete)
	require.Len(t, resp.Coverage.Targets, 1)
	require.Equal(t, CoverageArchiveMissing, resp.Coverage.Targets[0].State)
	require.Contains(t, resp.Coverage.Targets[0].Reasons, "ARCHIVE_NOT_RESTORED")
	require.Contains(t, resp.Coverage.PartialReasons, "REHYDRATE_FAILED")
	require.Contains(t, resp.Coverage.PartialReasons, "ARCHIVE_MISSING")
}

func TestCoordinatorRejectsWorkerGenerationDriftWithinView(t *testing.T) {
	worker := &fakeWorker{workerID: "w1", searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "INFO", "x")}, searchTarget: []WorkerTargetResult{
		{TargetID: "inst:1", State: CoverageSuccess, CatalogGeneration: "1"},
	}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = worker
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	first, err := coord.Search(context.Background(), Query{AuthorizedTargetIDs: []string{"inst:1"}, PrincipalKey: "principal",
		Budget: QueryBudget{Limit: 10}})
	require.NoError(t, err)
	require.Equal(t, "1", first.View.WorkerCatalogGenerations["inst:1"])
	worker.mu.Lock()
	worker.searchTarget[0].CatalogGeneration = "2"
	worker.mu.Unlock()
	_, err = coord.Search(context.Background(), Query{AuthorizedTargetIDs: []string{"inst:1"}, PrincipalKey: "principal", ViewID: first.View.ViewID,
		Budget: QueryBudget{Limit: 10}})
	require.ErrorIs(t, err, ErrViewReuseMismatch)
}

func TestHistoricalHolderUsesPhysicalWorkerTargetAndDistinctCoverageID(t *testing.T) {
	current := &fakeWorker{workerID: "current", searchTarget: []WorkerTargetResult{{TargetID: "inst:1", State: CoverageSuccess}}}
	historical := &fakeWorker{workerID: "old", searchItems: []Event{mkEvent("old-e", "inst:1", "old-g", 1, "2026-09-20T12:00:00Z", "INFO", "old")},
		searchTarget: []WorkerTargetResult{{TargetID: "inst:1", State: CoverageSuccess}}}
	dialer := newFakeDialer()
	dialer.clients["current"] = current
	dialer.clients["old"] = historical
	coord := New(&fakeTargetResolver{targets: []TargetInfo{
		{ID: "inst:1", AuthorizationID: "inst:1", WorkerID: "current", Readiness: ReadyOnline},
		{ID: "holder:7", AuthorizationID: "inst:1", WorkerTargetID: "inst:1", WorkerID: "old", Readiness: ReadyOnline, HistoricalHolder: true},
	}}, dialer)
	resp, err := coord.Search(context.Background(), Query{AuthorizedTargetIDs: []string{"inst:1"}, PrincipalKey: "principal", Budget: QueryBudget{Limit: 10}})
	require.NoError(t, err)
	require.Equal(t, []string{"inst:1"}, historical.lastOpenTargets)
	require.Equal(t, []string{"inst:1"}, historical.lastSearchTargets)
	require.Len(t, resp.Items, 1)
	byID := map[string]TargetCoverage{}
	for _, target := range resp.Coverage.Targets {
		byID[target.TargetID] = target
	}
	require.Contains(t, byID, "inst:1")
	require.Contains(t, byID, "holder:7")
	require.True(t, byID["holder:7"].HistoricalHolder)
}

func TestSearchTwoWorkersMergeStableOrder(t *testing.T) {
	t1, t2 := "inst-a", "inst-b"
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: t1, WorkerID: "w1", Readiness: ReadyOnline},
		{ID: t2, WorkerID: "w2", Readiness: ReadyOnline},
	}}

	// 交错时间线：必须按 event_time DESC 全局合并。
	w1 := &fakeWorker{
		workerID: "w1",
		searchItems: []Event{
			mkEvent("e1", "src-a", "g1", 100, "2026-09-20T12:00:00Z", "INFO", "w1-mid"),
			mkEvent("e0", "src-a", "g1", 50, "2026-09-20T11:00:00Z", "INFO", "w1-old"),
		},
		searchTarget: []WorkerTargetResult{successTarget(t1, "w1", "src-a/g1:100", "7")},
	}
	w2 := &fakeWorker{
		workerID: "w2",
		searchItems: []Event{
			mkEvent("e3", "src-b", "g1", 200, "2026-09-20T13:00:00Z", "ERROR", "w2-new"),
			mkEvent("e2", "src-b", "g1", 90, "2026-09-20T12:30:00Z", "WARN", "w2-mid"),
		},
		searchTarget: []WorkerTargetResult{successTarget(t2, "w2", "src-b/g1:200", "3")},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.clients["w2"] = w2
	coord := New(resolver, dialer)

	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{t1, t2},
		PrincipalKey:        "user:1|role:10|targets:test",
		FromUTC:             "2026-09-20T00:00:00Z",
		ToUTC:               "2026-09-21T00:00:00Z",
		Budget:              QueryBudget{Limit: 100},
	})
	require.NoError(t, err)
	require.Len(t, resp.Items, 4)

	gotIDs := make([]string, 0, 4)
	for _, ev := range resp.Items {
		gotIDs = append(gotIDs, ev.EventID)
	}
	assert.Equal(t, []string{"e3", "e2", "e1", "e0"}, gotIDs, "k-way merge must follow composite sort key")

	assert.True(t, resp.Coverage.Complete)
	assert.Empty(t, resp.Coverage.PartialReasons)
	require.Len(t, resp.Coverage.Targets, 2)
	assert.Equal(t, OrderVersion, resp.View.OrderVersion)
	assert.ElementsMatch(t, []string{t1, t2}, resp.View.TargetIDs)
}

func TestSearchBudgetCapsTotalRows(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "a", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "b", WorkerID: "w2", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "1")},
		searchTarget: []WorkerTargetResult{
			successTarget("a", "w1", "1", "1"),
		},
	}
	w2 := &fakeWorker{
		workerID: "w2",
		searchItems: []Event{
			mkEvent("e3", "s2", "g", 3, "2026-09-20T14:00:00Z", "I", "3"),
			mkEvent("e2", "s2", "g", 2, "2026-09-20T13:00:00Z", "I", "2"),
		},
		searchTarget: []WorkerTargetResult{
			successTarget("b", "w2", "2", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.clients["w2"] = w2
	coord := New(resolver, dialer)

	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"a", "b"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 2},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Items, 2)
	assert.False(t, resp.BudgetCut, "page size is not a resource-budget cut")
	assert.False(t, resp.Exhausted)
	assert.NotEmpty(t, resp.NextCursor)
	// 页大小只限本页行数，不改变覆盖判定：目标都成功时 coverage 仍 complete。
	assert.True(t, resp.Coverage.Complete)
}

func TestFederationCursorPaginatesAcrossWorkersWithoutCoverageDowngrade(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "node:1", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "node:2", WorkerID: "w2", Readiness: ReadyOnline},
	}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = &cursorWorker{id: "w1", target: "node:1", items: []Event{
		mkEvent("e4", "node:1", "g1", 4, "2026-09-20T14:00:00Z", "I", "4"),
		mkEvent("e2", "node:1", "g1", 2, "2026-09-20T12:00:00Z", "I", "2"),
	}}
	dialer.clients["w2"] = &cursorWorker{id: "w2", target: "node:2", items: []Event{
		mkEvent("e3", "node:2", "g1", 3, "2026-09-20T13:00:00Z", "I", "3"),
		mkEvent("e1", "node:2", "g1", 1, "2026-09-20T11:00:00Z", "I", "1"),
	}}
	coord := New(resolver, dialer)
	base := Query{AuthorizedTargetIDs: []string{"node:1", "node:2"}, PrincipalKey: "principal", Budget: QueryBudget{Limit: 2}}
	first, err := coord.Search(context.Background(), base)
	require.NoError(t, err)
	require.Equal(t, []string{"e4", "e3"}, []string{first.Items[0].EventID, first.Items[1].EventID})
	require.True(t, first.Coverage.Complete)
	require.False(t, first.BudgetCut)
	require.NotEmpty(t, first.NextCursor)

	base.Cursor = first.NextCursor
	second, err := coord.Search(context.Background(), base)
	require.NoError(t, err)
	require.Equal(t, []string{"e2", "e1"}, []string{second.Items[0].EventID, second.Items[1].EventID})
	require.True(t, second.Coverage.Complete)
	require.True(t, second.Exhausted)
	require.Empty(t, second.NextCursor)
}

func TestSearchOneOfflineYieldsPartialCoverage(t *testing.T) {
	online, offline := "inst-on", "inst-off"
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: online, WorkerID: "w-on", Readiness: ReadyOnline},
		{ID: offline, WorkerID: "w-off", Readiness: ReadyOffline, HistoricalHolder: true},
	}}
	wOn := &fakeWorker{
		workerID:    "w-on",
		searchItems: []Event{mkEvent("e1", "s-on", "g", 1, "2026-09-20T12:00:00Z", "I", "ok")},
		searchTarget: []WorkerTargetResult{
			successTarget(online, "w-on", "s-on/g:1", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w-on"] = wOn
	dialer.fail["w-off"] = errors.New("tunnel closed")

	coord := New(resolver, dialer)
	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{online, offline},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 50},
	})
	require.NoError(t, err)

	assert.False(t, resp.Coverage.Complete, "offline target must not yield false complete")
	require.Len(t, resp.Coverage.Targets, 2)

	byID := map[string]TargetCoverage{}
	for _, tc := range resp.Coverage.Targets {
		byID[tc.TargetID] = tc
	}
	assert.Equal(t, CoverageSuccess, byID[online].State)
	assert.Equal(t, CoverageOffline, byID[offline].State)
	assert.NotEmpty(t, resp.Coverage.PartialReasons)
	assert.True(t, byID[offline].HistoricalHolder)
	// 在线目标的事件仍返回；离线目标不得被静默丢弃成 complete。
	require.Len(t, resp.Items, 1)
	assert.Equal(t, online, resp.Items[0].TargetID)
}

func TestSearchOfflineWithoutDialStillRecorded(t *testing.T) {
	// 离线目标仍尝试 dial（可能刚恢复）；失败时 coverage 记 offline。
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t-off", WorkerID: "w-off", Readiness: ReadyOffline},
	}}
	dialer := newFakeDialer()
	dialer.fail["w-off"] = errors.New("node unreachable")
	coord := New(resolver, dialer)

	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t-off"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)
	assert.False(t, resp.Coverage.Complete)
	require.Len(t, resp.Coverage.Targets, 1)
	assert.Equal(t, CoverageOffline, resp.Coverage.Targets[0].State)
	assert.Empty(t, resp.Items)
}

func TestSearchExplicitOnlineOnlyShrinksSet(t *testing.T) {
	on, off := "t-on", "t-off"
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: on, WorkerID: "w-on", Readiness: ReadyOnline},
		{ID: off, WorkerID: "w-off", Readiness: ReadyOffline},
	}}
	wOn := &fakeWorker{
		workerID:    "w-on",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "x")},
		searchTarget: []WorkerTargetResult{
			successTarget(on, "w-on", "1", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w-on"] = wOn
	// w-off 不注册：online_only 下不应被 dial
	coord := New(resolver, dialer)

	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{on, off},
		PrincipalKey:        "user:1|role:10|targets:test",
		OnlineOnly:          true,
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)

	// 扇出收缩：只 dial 在线 worker
	assert.Equal(t, 2, dialer.callCount("w-on"), "one call creates the Worker view and one executes Search")
	assert.Equal(t, 0, dialer.callCount("w-off"))

	// view 目标集合收缩
	assert.Equal(t, []string{on}, resp.View.TargetIDs)
	assert.True(t, resp.View.OnlineOnly)

	// coverage 仍记录被排除的离线目标，complete 不得为 true（禁止假完整）
	require.Len(t, resp.Coverage.Targets, 2)
	byID := map[string]TargetCoverage{}
	for _, tc := range resp.Coverage.Targets {
		byID[tc.TargetID] = tc
	}
	assert.Equal(t, CoverageSuccess, byID[on].State)
	assert.Equal(t, CoverageOffline, byID[off].State)
	assert.False(t, resp.Coverage.Complete)
	found := false
	for _, r := range byID[off].Reasons {
		if r == "online_only_excluded" {
			found = true
		}
	}
	assert.True(t, found, "excluded target must carry online_only_excluded reason")
}

func TestStatsWeightedAvgMerge(t *testing.T) {
	a, b := "t-a", "t-b"
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: a, WorkerID: "w1", Readiness: ReadyOnline},
		{ID: b, WorkerID: "w2", Readiness: ReadyOnline},
	}}
	// w1: count=2 sum=10 avg=5 ; w2: count=4 sum=20 avg=5 → merged avg should be 30/6=5
	// 更能证明加权：w1 count=2 sum=2 (avg=1), w2 count=6 sum=30 (avg=5) → weighted avg=32/8=4
	// 简单平均会得到 (1+5)/2=3，错误。
	w1 := &fakeWorker{
		workerID: "w1",
		statsPoints: []WorkerStatsPoint{{
			Dimensions:    map[string]string{"level": "INFO"},
			TimeBucketUTC: "2026-09-20T12:00:00Z",
			Agg:           StatsAggregate{Count: 2, Sum: 2, Min: 0.5, Max: 1.5, HasMinMax: true},
		}},
		statsTargets: []WorkerTargetResult{successTarget(a, "w1", "1", "1")},
	}
	w2 := &fakeWorker{
		workerID: "w2",
		statsPoints: []WorkerStatsPoint{{
			Dimensions:    map[string]string{"level": "INFO"},
			TimeBucketUTC: "2026-09-20T12:00:00Z",
			Agg:           StatsAggregate{Count: 6, Sum: 30, Min: 2, Max: 9, HasMinMax: true},
		}},
		statsTargets: []WorkerTargetResult{successTarget(b, "w2", "1", "1")},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.clients["w2"] = w2
	coord := New(resolver, dialer)

	resp, err := coord.Stats(context.Background(), StatsQuery{
		Query: Query{
			AuthorizedTargetIDs: []string{a, b},
			PrincipalKey:        "user:1|role:10|targets:test",
			Budget:              QueryBudget{Limit: 100},
		},
		GroupBy:     []string{"level"},
		TimeBucket:  "1m",
		MetricField: "duration_ms",
	})
	require.NoError(t, err)
	require.Len(t, resp.Points, 1)
	p := resp.Points[0]
	assert.Equal(t, uint64(8), p.Agg.Count)
	assert.InDelta(t, 32.0, p.Agg.Sum, 1e-9)
	assert.InDelta(t, 4.0, p.Agg.Avg(), 1e-9, "avg must be weighted sum/count, not mean-of-avgs")
	assert.InDelta(t, 0.5, p.Agg.Min, 1e-9)
	assert.InDelta(t, 9.0, p.Agg.Max, 1e-9)
	assert.Equal(t, "2026-09-20T12:00:00Z", p.TimeBucketUTC)
	assert.True(t, resp.Coverage.Complete)
}

func TestStatsBucketAbsoluteUTC(t *testing.T) {
	start, err := BucketStartRFC3339("2026-09-20T12:03:47Z", "5m")
	require.NoError(t, err)
	assert.Equal(t, "2026-09-20T12:00:00Z", start)

	start, err = BucketStartRFC3339("2026-09-20T12:59:01Z", "1h")
	require.NoError(t, err)
	assert.Equal(t, "2026-09-20T12:00:00Z", start)

	// epoch-aligned 5m
	d, err := ParseBucketDuration("5m")
	require.NoError(t, err)
	got := BucketStartUTC(time.Date(2026, 9, 20, 12, 7, 0, 0, time.UTC), d)
	assert.Equal(t, time.Date(2026, 9, 20, 12, 5, 0, 0, time.UTC), got)
}

func TestFacetsControlledMergeExactHighCardinalityTruncates(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "t2", WorkerID: "w2", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID: "w1",
		facets: []WorkerFacetDimension{
			{Dimension: "level", Values: []FacetValue{{Value: "INFO", Count: 3}, {Value: "ERROR", Count: 1}}},
			{Dimension: "request_id", Values: []FacetValue{{Value: "r1", Count: 1}}, Truncated: true, TruncatedCount: 50},
		},
		facetTargets: []WorkerTargetResult{successTarget("t1", "w1", "1", "1")},
	}
	w2 := &fakeWorker{
		workerID: "w2",
		facets: []WorkerFacetDimension{
			{Dimension: "level", Values: []FacetValue{{Value: "INFO", Count: 2}, {Value: "WARN", Count: 4}}},
			{Dimension: "request_id", Values: []FacetValue{{Value: "r2", Count: 2}}, Truncated: true, TruncatedCount: 30},
		},
		facetTargets: []WorkerTargetResult{successTarget("t2", "w2", "1", "1")},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.clients["w2"] = w2
	coord := New(resolver, dialer)

	resp, err := coord.Facets(context.Background(), FacetsQuery{
		Query: Query{
			AuthorizedTargetIDs: []string{"t1", "t2"},
			PrincipalKey:        "user:1|role:10|targets:test",
			Budget:              QueryBudget{Limit: 10},
		},
		Dimensions:     []string{"level", "request_id"},
		DimensionLimit: 10,
	})
	require.NoError(t, err)

	byDim := map[string]FacetDimension{}
	for _, d := range resp.Dimensions {
		byDim[d.Dimension] = d
	}
	lvl := byDim["level"]
	assert.True(t, lvl.Controlled)
	assert.False(t, lvl.Truncated)
	counts := map[string]uint64{}
	for _, v := range lvl.Values {
		counts[v.Value] += v.Count
	}
	assert.Equal(t, uint64(5), counts["INFO"])
	assert.Equal(t, uint64(4), counts["WARN"])
	assert.Equal(t, uint64(1), counts["ERROR"])

	req := byDim["request_id"]
	assert.False(t, req.Controlled)
	assert.True(t, req.Truncated)
	assert.Equal(t, uint64(80), req.TruncatedCount)
	assert.True(t, resp.Truncated)
}

func TestExportCompleteSuccessArtifact(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "hello")},
		searchTarget: []WorkerTargetResult{
			successTarget("t1", "w1", "s/g:1", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	coord := New(resolver, dialer)

	res, err := coord.Export(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 100},
	})
	require.NoError(t, err)
	assert.False(t, res.ExportIncomplete)
	require.NotNil(t, res.Artifact)
	assert.Contains(t, string(res.Artifact), `"e1"`)
	assert.False(t, res.BudgetExceeded)
	assert.True(t, res.Coverage.Complete)
}

func TestExportIncompleteWhenCoveragePartial(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "t2", WorkerID: "w2", Readiness: ReadyNotReady},
	}}
	w1 := &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "x")},
		searchTarget: []WorkerTargetResult{
			successTarget("t1", "w1", "1", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.fail["w2"] = errors.New("not ready")
	coord := New(resolver, dialer)

	res, err := coord.Export(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1", "t2"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 100},
	})
	require.NoError(t, err)
	assert.True(t, res.ExportIncomplete)
	assert.Nil(t, res.Artifact, "no success artifact when partial coverage")
	assert.Contains(t, res.IncompleteReasons, "coverage_incomplete")
}

func TestExportIncompleteWhenBudgetExceeded(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID: "w1",
		searchItems: []Event{
			mkEvent("e1", "s", "g", 2, "2026-09-20T12:00:00Z", "I", "a"),
			mkEvent("e2", "s", "g", 1, "2026-09-20T11:00:00Z", "I", "b"),
			mkEvent("e3", "s", "g", 0, "2026-09-20T10:00:00Z", "I", "c"),
		},
		searchTarget: []WorkerTargetResult{
			successTarget("t1", "w1", "3", "1"),
		},
		searchTrunc: true,
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	coord := New(resolver, dialer)

	res, err := coord.Export(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 1},
	})
	require.NoError(t, err)
	assert.True(t, res.ExportIncomplete)
	assert.Nil(t, res.Artifact)
	assert.True(t, res.BudgetExceeded || res.Cut)
}

func TestExportReusesSameViewAsSearch(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "x")},
		searchTarget: []WorkerTargetResult{
			successTarget("t1", "w1", "s/g:1", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	coord := New(resolver, dialer)

	q := Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 50},
	}
	searchResp, err := coord.Search(context.Background(), q)
	require.NoError(t, err)

	exportQ := q
	exportQ.ViewID = searchResp.View.ViewID
	res, err := coord.Export(context.Background(), exportQ)
	require.NoError(t, err)
	assert.Equal(t, searchResp.View.ViewID, res.View.ViewID)
	assert.Equal(t, searchResp.View.TargetIDs, res.View.TargetIDs)
	assert.False(t, res.ExportIncomplete)
}

func TestUnsupportedWorkerNotSilentComplete(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "old", WorkerID: "w-legacy", Readiness: ReadyOnline},
	}}
	legacy := &fakeWorker{workerID: "w-legacy", searchUnsup: true}
	dialer := newFakeDialer()
	dialer.clients["w-legacy"] = legacy
	coord := New(resolver, dialer)

	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"old"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)
	assert.False(t, resp.Coverage.Complete)
	require.Len(t, resp.Coverage.Targets, 1)
	assert.Equal(t, CoverageNotReady, resp.Coverage.Targets[0].State)
	assert.Contains(t, resp.Coverage.Targets[0].Reasons, "log_rpc_unsupported")
}

func TestViewReuseDoesNotReselectTargets(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
		{ID: "t2", WorkerID: "w2", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "x")},
		searchTarget: []WorkerTargetResult{
			successTarget("t1", "w1", "1", "1"),
		},
	}
	w2 := &fakeWorker{
		workerID: "w2",
		searchItems: []Event{
			mkEvent("e2", "s2", "g", 2, "2026-09-20T13:00:00Z", "I", "y"),
		},
		searchTarget: []WorkerTargetResult{
			successTarget("t2", "w2", "2", "1"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.clients["w2"] = w2
	coord := New(resolver, dialer)

	view, err := coord.CreateView(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1", "t2"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)

	// 同 view 二次 Search：目标集合锁定
	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1", "t2"},
		PrincipalKey:        "user:1|role:10|targets:test",
		ViewID:              view.ViewID,
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, view.ViewID, resp.View.ViewID)
	assert.ElementsMatch(t, []string{"t1", "t2"}, resp.View.TargetIDs)
	assert.Len(t, resp.Items, 2)
}

func TestMergeStreamsDeterministicTieBreak(t *testing.T) {
	// 相同 event_time：按 log_source_id ASC
	ts := "2026-09-20T12:00:00Z"
	s1 := EventStream{WorkerID: "w1", Items: []Event{
		mkEvent("b2", "src-b", "g1", 10, ts, "I", "b2"),
	}}
	s2 := EventStream{WorkerID: "w2", Items: []Event{
		mkEvent("a1", "src-a", "g1", 10, ts, "I", "a1"),
	}}
	rows, pageMore, byteCut := MergeStreams([]EventStream{s1, s2}, 0, 0)
	assert.False(t, pageMore)
	assert.False(t, byteCut)
	require.Len(t, rows, 2)
	assert.Equal(t, "a1", rows[0].EventID)
	assert.Equal(t, "b2", rows[1].EventID)
}

func TestBuildExportAllIncompleteReasons(t *testing.T) {
	view := View{Budget: QueryBudget{Limit: 1}}
	cov := Coverage{Complete: false, PartialReasons: []string{"t2:offline"}}
	res := BuildExport(view, cov, Quality{}, []Event{
		mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "a"),
		mkEvent("e2", "s", "g", 0, "2026-09-20T11:00:00Z", "I", "b"),
	}, true, true)
	assert.True(t, res.ExportIncomplete)
	assert.Nil(t, res.Artifact)
	assert.True(t, res.BudgetExceeded)
	assert.True(t, res.Cut)
	assert.Contains(t, res.IncompleteReasons, "coverage_incomplete")
	assert.Contains(t, res.IncompleteReasons, "budget_exceeded")
}

// F-001：跨用户复用 viewId 必须拒绝。
func TestViewReuseRejectsForeignPrincipal(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline},
	}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "x")},
		searchTarget: []WorkerTargetResult{
			successTarget("inst:1", "w1", "1", "1"),
		},
	}
	coord := New(resolver, dialer)
	view, err := coord.CreateView(context.Background(), Query{
		AuthorizedTargetIDs: []string{"inst:1"},
		PrincipalKey:        "user:1|role:10|targets:a",
		Budget:              QueryBudget{Limit: 10},
	})
	require.NoError(t, err)

	// 其他用户猜到 viewId
	_, err = coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"inst:1"},
		PrincipalKey:        "user:2|role:1|targets:a",
		ViewID:              view.ViewID,
		Budget:              QueryBudget{Limit: 10},
	})
	require.ErrorIs(t, err, ErrViewAuthz)

	// 同主体但授权集合收窄到不含 view 目标
	_, err = coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"inst:9"},
		PrincipalKey:        "user:1|role:10|targets:a",
		ViewID:              view.ViewID,
		Budget:              QueryBudget{Limit: 10},
	})
	require.ErrorIs(t, err, ErrViewAuthz)
}

// F-006：复用 view 时固定查询条件变化必须拒绝。
func TestViewReuseRejectsChangedQueryDimensions(t *testing.T) {
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
	}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = &fakeWorker{
		workerID:    "w1",
		searchItems: []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "x")},
		searchTarget: []WorkerTargetResult{
			successTarget("t1", "w1", "1", "1"),
		},
	}
	coord := New(resolver, dialer)
	base := Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		FromUTC:             "2026-09-20T00:00:00Z",
		ToUTC:               "2026-09-21T00:00:00Z",
		Filter:              "error",
		OnlineOnly:          false,
		PermissionScope:     "instance",
		Budget:              QueryBudget{Limit: 10},
	}
	view, err := coord.CreateView(context.Background(), base)
	require.NoError(t, err)

	changedTime := base
	changedTime.ViewID = view.ViewID
	changedTime.FromUTC = "2026-09-19T00:00:00Z"
	_, err = coord.Search(context.Background(), changedTime)
	require.ErrorIs(t, err, ErrViewReuseMismatch)

	changedFilter := base
	changedFilter.ViewID = view.ViewID
	changedFilter.Filter = "warn"
	_, err = coord.Search(context.Background(), changedFilter)
	require.ErrorIs(t, err, ErrViewReuseMismatch)

	changedOnline := base
	changedOnline.ViewID = view.ViewID
	changedOnline.OnlineOnly = true
	_, err = coord.Search(context.Background(), changedOnline)
	require.ErrorIs(t, err, ErrViewReuseMismatch)

	changedScope := base
	changedScope.ViewID = view.ViewID
	changedScope.PermissionScope = "platform"
	_, err = coord.Search(context.Background(), changedScope)
	require.ErrorIs(t, err, ErrViewReuseMismatch)

	// 同条件复用成功
	ok := base
	ok.ViewID = view.ViewID
	_, err = coord.Search(context.Background(), ok)
	require.NoError(t, err)
}

// F-007：Router 将 ErrViewAuthz/ErrViewReuseMismatch 映射为 403/VIEW_STALE（非 500）。
func TestWriteFederationError_ViewAuthzMapping(t *testing.T) {
	// 纯错误映射契约：见 router/log_federation.go writeFederationError
	// 这里通过 errors.Is 语义锁定，HTTP 映射由 router 测试覆盖。
	require.ErrorIs(t, fmt.Errorf("%w: x", ErrViewAuthz), ErrViewAuthz)
	require.ErrorIs(t, fmt.Errorf("%w: y", ErrViewReuseMismatch), ErrViewReuseMismatch)
	require.NotErrorIs(t, ErrViewReuseMismatch, ErrViewAuthz)
}

func TestStatsAggregateMergeMath(t *testing.T) {
	a := StatsAggregate{Count: 2, Sum: 2, Min: 0.5, Max: 1.5, HasMinMax: true}
	b := StatsAggregate{Count: 6, Sum: 30, Min: 2, Max: 9, HasMinMax: true}
	a.Merge(b)
	assert.Equal(t, uint64(8), a.Count)
	assert.InDelta(t, 32.0, a.Sum, 1e-9)
	assert.InDelta(t, 0.5, a.Min, 1e-9)
	assert.InDelta(t, 9.0, a.Max, 1e-9)
	assert.InDelta(t, 4.0, a.Avg(), 1e-9)
	assert.False(t, math.IsNaN(a.Avg()))
}

// TestCoordinatorViewStoreIsBounded 锁定 CP 视图注册表有界（真机压测暴露无界增长）。
func TestCoordinatorViewStoreIsBounded(t *testing.T) {
	c := New(&fakeTargetResolver{}, nil)
	base := time.Now()
	for i := 0; i < maxRetainedViews+40; i++ {
		id := "cv" + strconv.Itoa(i)
		c.views[id] = View{ViewID: id, CreatedAt: base.Add(time.Duration(i) * time.Millisecond)}
	}
	c.mu.Lock()
	c.pruneViewsLocked()
	size := len(c.views)
	_, hasOldest := c.views["cv0"]
	_, hasNewest := c.views["cv"+strconv.Itoa(maxRetainedViews+39)]
	c.mu.Unlock()
	if size >= maxRetainedViews {
		t.Fatalf("CP view store must be below cap after prune: got %d", size)
	}
	if hasOldest {
		t.Fatal("oldest CP view should be evicted")
	}
	if !hasNewest {
		t.Fatal("newest CP view must be retained")
	}
}
