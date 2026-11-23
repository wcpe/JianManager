package query_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 两个范围同属一个 Worker 和同一日志命名空间，排除 CP 跨 Worker 预算先行截断的反证。
func pr61FanoutPlanner(t *testing.T) *query.Planner {
	t.Helper()
	cat := catalog.New(nil)
	for i, day := range []string{"2026-09-21", "2026-09-22"} {
		key := catalog.PartitionKey{StorageNamespace: "inst:7/stdout", UTCDay: day}
		dir := fmt.Sprintf("hot-%d", i+1)
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, dir)
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion: "pv-1", CoverageComplete: true, QueryLocationDirID: dir, QueryGeneration: 1,
			ClosedVisibleSeq: map[string]uint64{key.String(): 100},
		}
		if err := cat.Put(rec); err != nil {
			t.Fatal(err)
		}
	}
	return query.NewPlanner(cat, nil)
}

func pr61BudgetReason(c query.Coverage) bool {
	for _, reason := range c.PartialReasons {
		if reason == query.ReasonBudgetExceeded {
			return true
		}
	}
	return false
}

// 预算小于范围数必须报告跳过；零预算和正好足够预算是独立反证控制。
func TestPR61ReproFanoutCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		maxFanout   uint32
		wantCalls   uint32
		wantPartial bool
	}{
		{name: "不限制", maxFanout: 0, wantCalls: 2},
		{name: "只允许一个范围", maxFanout: 1, wantCalls: 1, wantPartial: true},
		{name: "正好两个范围", maxFanout: 2, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := pr61FanoutPlanner(t)
			req := query.QueryRequest{RequestID: "pr61-fanout", Budget: query.Budget{Limit: 10, MaxFanout: tc.maxFanout}}
			plan := planner.Plan(query.PlanRequest{RequestID: req.RequestID, Budget: req.Budget})
			if plan.Err != nil || len(plan.Ranges) != 2 || !plan.Coverage.Complete || len(plan.Coverage.PartialReasons) != 0 {
				t.Fatalf("反证前提失败：planner 应交付两个完整权威范围，实际=%+v", plan)
			}
			t.Logf("planner 范围=%d complete=%v reasons=%v MaxFanout=%d", len(plan.Ranges), plan.Coverage.Complete, plan.Coverage.PartialReasons, tc.maxFanout)
			var searchCalls, statsCalls atomic.Uint32
			client := query.FuncRangeClient{
				SearchFn: func(_ context.Context, rng query.AuthoritativeRange, _ query.RangeQuery) (query.RangeResult, error) {
					searchCalls.Add(1)
					return query.RangeResult{
						Items: []logtypes.Event{{EventID: rng.Key.String(), EventTimeUTC: rng.Key.UTCDay + "T10:00:00Z",
							Source: logtypes.SourceIdentity{LogSourceID: rng.Key.StorageNamespace, SourceGeneration: "g1"},
							Record: logtypes.RecordRange{Start: 1, End: 2}}},
						Exhausted: true, DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact,
					}, nil
				},
				StatsFn: func(_ context.Context, _ query.AuthoritativeRange, _ query.RangeQuery) (query.StatsResult, error) {
					statsCalls.Add(1)
					return query.StatsResult{Points: []query.StatsPoint{{Count: 1, Sum: 1, Min: 1, Max: 1}},
						DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}, nil
				},
			}
			svc := query.NewService(planner, client, "pr61-isolation")
			search := svc.Search(context.Background(), req)
			if search.Err != nil {
				t.Fatalf("Search 意外错误: %v", search.Err)
			}
			t.Logf("Search 调用=%d 行数=%d complete=%v reasons=%v quality=%s exhausted=%v targets=%+v", searchCalls.Load(), len(search.Items), search.Coverage.Complete, search.Coverage.PartialReasons, search.Quality.StatsQuality, search.Exhausted, search.Coverage.Targets)
			if searchCalls.Load() != tc.wantCalls || len(search.Items) != int(tc.wantCalls) {
				t.Errorf("Search 应调用并返回 %d 个范围，实际调用=%d 行数=%d", tc.wantCalls, searchCalls.Load(), len(search.Items))
			}
			if search.Coverage.Complete == tc.wantPartial || pr61BudgetReason(search.Coverage) != tc.wantPartial {
				t.Errorf("Search 跳过范围必须报告不完整与 BUDGET_EXCEEDED：应 partial=%v，实际=%+v", tc.wantPartial, search.Coverage)
			}
			stats := svc.Stats(context.Background(), req, nil)
			if stats.Err != nil {
				t.Fatalf("Stats 意外错误: %v", stats.Err)
			}
			var count uint64
			for _, point := range stats.Points {
				count += point.Count
			}
			t.Logf("Stats 调用=%d count=%d complete=%v reasons=%v quality=%s targets=%+v", statsCalls.Load(), count, stats.Coverage.Complete, stats.Coverage.PartialReasons, stats.Quality.StatsQuality, stats.Coverage.Targets)
			if statsCalls.Load() != tc.wantCalls || count != uint64(tc.wantCalls) {
				t.Errorf("Stats 应调用并汇总 %d 个范围，实际调用=%d count=%d", tc.wantCalls, statsCalls.Load(), count)
			}
			wantQuality := query.StatsExact
			if tc.wantPartial {
				wantQuality = query.StatsPartial
			}
			if stats.Coverage.Complete == tc.wantPartial || pr61BudgetReason(stats.Coverage) != tc.wantPartial || stats.Quality.StatsQuality != wantQuality {
				t.Errorf("Stats 跳过范围必须报告不完整与部分质量：应 partial=%v quality=%s，实际 coverage=%+v quality=%s", tc.wantPartial, wantQuality, stats.Coverage, stats.Quality.StatsQuality)
			}
		})
	}
}
