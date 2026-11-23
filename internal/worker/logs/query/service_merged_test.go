package query

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// fakeMergedClient 同时实现 RangeClient 与 MergedRangeClient，用于验证分组执行路径。
type fakeMergedClient struct {
	searchCalls int32
	mergedCalls int32
	lastMergedN int32
	mergedErr   error
	mergedItems []logtypes.Event
}

func (f *fakeMergedClient) Search(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
	atomic.AddInt32(&f.searchCalls, 1)
	return RangeResult{Exhausted: true}, nil
}

func (f *fakeMergedClient) Stats(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
	return StatsResult{}, nil
}

func (f *fakeMergedClient) Tail(context.Context, AuthoritativeRange, RangeQuery, TailMode) (RangeResult, error) {
	return RangeResult{}, nil
}

func (f *fakeMergedClient) SearchMerged(_ context.Context, rngs []AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
	atomic.AddInt32(&f.mergedCalls, 1)
	atomic.StoreInt32(&f.lastMergedN, int32(len(rngs)))
	if f.mergedErr != nil {
		return RangeResult{}, f.mergedErr
	}
	return RangeResult{Items: f.mergedItems, Exhausted: true}, nil
}

// multiTargetCatalog 构造 namespaces × day 的 HOT 分区（每个都是可查询的权威记录）。
func multiTargetCatalog(t *testing.T, namespaces []string, day string) *catalog.Catalog {
	t.Helper()
	c := catalog.New(nil)
	for _, ns := range namespaces {
		key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: day}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion:    "pv-1",
			CoverageComplete:   true,
			QueryLocationDirID: "hot-g1",
			QueryGeneration:    1,
			ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
		}
		require.NoError(t, c.Put(rec))
	}
	return c
}

func mergedSearchRequest(day string) QueryRequest {
	from := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	if day != "" {
		parsed, err := time.Parse("2006-01-02", day)
		if err == nil {
			from = parsed
		}
	}
	return QueryRequest{
		RequestID: "merged-req",
		TimeRange: TimeRange{
			FromUTC: from.Format(time.RFC3339),
			ToUTC:   from.AddDate(0, 0, 1).Format(time.RFC3339),
		},
		Budget: Budget{Limit: 100},
	}
}

// 同天多目标 → 一次合并调用；不再逐 range。
func TestSearchMergesSameDayTargetsIntoOneCall(t *testing.T) {
	c := multiTargetCatalog(t, []string{"ns/a", "ns/b", "ns/c"}, "2026-09-10")
	fake := &fakeMergedClient{mergedItems: []logtypes.Event{
		buildEvent("ns/a", "g1", 1, 2, "2026-09-10T01:00:00Z", "merged-line"),
	}}
	svc := NewService(NewPlanner(c, nil), fake, "test-build")

	resp := svc.Search(context.Background(), mergedSearchRequest("2026-09-10"))

	require.Nil(t, resp.Err)
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.mergedCalls), "同日 3 目标应合并为一次调用")
	require.Equal(t, int32(3), atomic.LoadInt32(&fake.lastMergedN), "合并应携带全部 3 个 range")
	require.Equal(t, int32(0), atomic.LoadInt32(&fake.searchCalls), "不得再逐 range 调用")
	require.Len(t, resp.Items, 1)
	require.Equal(t, "merged-line", resp.Items[0].Message)
}

// 跨天合并：同 Owner 的多天 rng 合并为一次查询（不再按天拆分——VL 对"行数不足"
// 的窗口必须全扫，分天会把一次查询放大成每天一次全扫，实测 10× 差距）。
func TestSearchMergesSingleTargetAcrossDaysIntoSingleCall(t *testing.T) {
	c := catalog.New(nil)
	for i := 0; i < 3; i++ {
		from := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		key := catalog.PartitionKey{StorageNamespace: "ns/only", UTCDay: from.Format("2006-01-02")}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion:    "pv-1",
			CoverageComplete:   true,
			QueryLocationDirID: "hot-g1",
			QueryGeneration:    1,
			ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
		}
		require.NoError(t, c.Put(rec))
	}
	fake := &fakeMergedClient{}
	svc := NewService(NewPlanner(c, nil), fake, "test-build")

	resp := svc.Search(context.Background(), QueryRequest{
		RequestID: "days-req",
		TimeRange: TimeRange{
			FromUTC: "2026-09-10T00:00:00Z",
			ToUTC:   "2026-09-13T00:00:00Z",
		},
		Budget: Budget{Limit: 100},
	})

	require.Nil(t, resp.Err)
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.mergedCalls),
		"同 Owner 的 3 天应合并为一次跨天查询")
	require.Equal(t, int32(3), atomic.LoadInt32(&fake.lastMergedN))
	require.Equal(t, int32(0), atomic.LoadInt32(&fake.searchCalls), "不得逐 range/逐天执行")
}

// 客户端未实现 MergedRangeClient → 自动退回逐 range（回归保障）。
func TestSearchFallsBackWhenMergedUnimplemented(t *testing.T) {
	c := multiTargetCatalog(t, []string{"ns/a", "ns/b", "ns/c"}, "2026-09-10")
	var calls int32
	fake := FuncRangeClient{
		SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
			atomic.AddInt32(&calls, 1)
			return RangeResult{Exhausted: true}, nil
		},
	}
	svc := NewService(NewPlanner(c, nil), fake, "test-build")

	resp := svc.Search(context.Background(), mergedSearchRequest("2026-09-10"))

	require.Nil(t, resp.Err)
	require.Equal(t, int32(3), atomic.LoadInt32(&calls), "未实现合并能力时必须逐 range")
}

// 合并失败 → 组内全部 target 标 NotReady + coverage 不完整。
func TestSearchMergeFailureMarksGroupTargetsNotReady(t *testing.T) {
	c := multiTargetCatalog(t, []string{"ns/a", "ns/b", "ns/c"}, "2026-09-10")
	fake := &fakeMergedClient{mergedErr: errors.New("vl unavailable")}
	svc := NewService(NewPlanner(c, nil), fake, "test-build")

	resp := svc.Search(context.Background(), mergedSearchRequest("2026-09-10"))

	require.Nil(t, resp.Err, "单组失败不应上升为请求级错误")
	require.False(t, resp.Coverage.Complete, "组失败必须使覆盖不完整")
	notReady := 0
	for _, tg := range resp.Coverage.Targets {
		if tg.State == CoverageNotReady {
			notReady++
		}
	}
	require.Equal(t, len(resp.Coverage.Targets), notReady, "组内全部 target 应标 NotReady")
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.mergedCalls))
}

// twoDayCatalog 构造两天、每天两个 namespace 的分区（每天一个可合并组）。
func twoDayCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c := multiTargetCatalog(t, []string{"ns/a", "ns/b"}, "2026-09-09")
	for _, ns := range []string{"ns/c", "ns/d"} {
		key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: "2026-09-10"}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion:    "pv-1",
			CoverageComplete:   true,
			QueryLocationDirID: "hot-g1",
			QueryGeneration:    1,
			ClosedVisibleSeq:   map[string]uint64{key.String(): 10},
		}
		require.NoError(t, c.Put(rec))
	}
	return c
}

func twoDayRequest() QueryRequest {
	return QueryRequest{
		RequestID: "early-stop",
		TimeRange: TimeRange{
			FromUTC: "2026-09-09T00:00:00Z",
			ToUTC:   "2026-09-11T00:00:00Z",
		},
		Budget: Budget{Limit: 100},
	}
}

// 跨天合并：最新天 + 旧天合并为一次查询；不足页时同一次查询自然覆盖全部天
// （不再按天续查——分天会让 VL 每天独立全扫）。
func TestSearchMergesAcrossDaysWhenPageNotFilled(t *testing.T) {
	c := twoDayCatalog(t)
	fake := &fakeMergedClient{mergedItems: make([]logtypes.Event, 3)}
	svc := NewService(NewPlanner(c, nil), fake, "test-build")

	resp := svc.Search(context.Background(), twoDayRequest())

	require.Nil(t, resp.Err)
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.mergedCalls),
		"两天应合并为一次跨天查询（不足页也由同一窗口覆盖）")
	require.Len(t, resp.Items, 3, "合并查询返回的行原样透出")
}

// 天批次早停（跨 Owner 批次间）：最新批（Owner 组）够页后，更旧批不再查询。
func TestSearchMergesAcrossDaysAndFillsPageFromNewest(t *testing.T) {
	c := twoDayCatalog(t)
	fake := &fakeMergedClient{mergedItems: make([]logtypes.Event, 101)}
	svc := NewService(NewPlanner(c, nil), fake, "test-build")

	resp := svc.Search(context.Background(), twoDayRequest())

	require.Nil(t, resp.Err)
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.mergedCalls),
		"同 Owner 的多天合并为一次查询")
	require.Equal(t, int32(0), atomic.LoadInt32(&fake.searchCalls))
	require.Len(t, resp.Items, 100, "响应仍截断到页大小")
	require.NotEmpty(t, resp.NextCursor, "有更多数据时必须给下一页游标")
}

// 冷热 Owner 批次必须合并后再做全局 top-N，不能因先完成一批而漏掉更新的批次。
func TestReviewOwnerBatchesGlobalTopN(t *testing.T) {
	for _, limit := range []uint32{1, 2} {
		t.Run(fmt.Sprintf("limit%d", limit), func(t *testing.T) {
			c := multiTargetCatalog(t, []string{"ns/hot"}, "2026-09-22")
			key := catalog.PartitionKey{StorageNamespace: "ns/cold", UTCDay: "2026-09-21"}
			rec := catalog.NewStableRecord(key, catalog.OwnerCold, 1, "cold-g1")
			rec.PublishedProjection = &catalog.PublishedProjection{
				ManifestVersion: "pv-1", CoverageComplete: true,
				QueryLocationDirID: "cold-g1", QueryGeneration: 1,
				ClosedVisibleSeq: map[string]uint64{key.String(): 10},
			}
			require.NoError(t, c.Put(rec))
			cold := []logtypes.Event{
				buildEvent("ns/cold", "g1", 5, 6, "2026-09-21T23:58:00Z", "cold-old-1"),
				buildEvent("ns/cold", "g1", 3, 4, "2026-09-21T23:57:00Z", "cold-old-2"),
				buildEvent("ns/cold", "g1", 1, 2, "2026-09-21T23:56:00Z", "cold-old-3"),
			}
			hot := []logtypes.Event{buildEvent("ns/hot", "g1", 1, 2, "2026-09-22T12:00:00Z", "hot-newest")}
			var coldCalls, hotCalls int32
			client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
				rows := hot
				if rng.Owner == catalog.OwnerCold {
					atomic.AddInt32(&coldCalls, 1)
					rows = cold
				} else {
					atomic.AddInt32(&hotCalls, 1)
				}
				n := int(q.Budget.EffectiveLimit()) + 1
				exhausted := len(rows) <= n
				if len(rows) > n {
					rows = rows[:n]
				}
				return RangeResult{Items: append([]logtypes.Event(nil), rows...), Exhausted: exhausted}, nil
			}}
			svc := NewService(NewPlanner(c, nil), client, "review-build")
			resp := svc.Search(context.Background(), QueryRequest{
				RequestID: "review-global-top",
				TimeRange: TimeRange{FromUTC: "2026-09-21T00:00:00Z", ToUTC: "2026-09-23T00:00:00Z"},
				Budget:    Budget{Limit: limit},
			})
			require.Nil(t, resp.Err)
			require.Len(t, resp.Items, int(limit))
			require.Equal(t, hot[0].EventID, resp.Items[0].EventID)
			require.Equal(t, int32(1), atomic.LoadInt32(&hotCalls), "更新的 HOT 批次必须参与查询")
		})
	}
}

// range 未枚举完时，服务层必须保留可继续分页的语义。
func TestReviewExhaustedPropagation(t *testing.T) {
	c := multiTargetCatalog(t, []string{"a"}, "2026-09-22")
	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		return RangeResult{Items: []logtypes.Event{buildEvent("a", "g1", 1, 2, "2026-09-22T01:00:00Z", "m")}, Exhausted: false}, nil
	}}
	svc := NewService(NewPlanner(c, nil), client, "review-build")
	resp := svc.Search(context.Background(), mergedSearchRequest("2026-09-22"))
	require.Nil(t, resp.Err)
	require.False(t, resp.Exhausted)
	require.NotEmpty(t, resp.NextCursor)
}

// 扇出预算漏查目标时，覆盖必须保持不完整。
func TestReviewFanoutCoverage(t *testing.T) {
	c := multiTargetCatalog(t, []string{"a", "b"}, "2026-09-22")
	var calls int32
	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		atomic.AddInt32(&calls, 1)
		return RangeResult{Exhausted: true}, nil
	}}
	svc := NewService(NewPlanner(c, nil), client, "review-build")
	req := mergedSearchRequest("2026-09-22")
	req.Budget = Budget{Limit: 10, MaxFanout: 1}
	resp := svc.Search(context.Background(), req)
	require.False(t, resp.Coverage.Complete)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// RFC3339 纳秒事件排序必须按时间值而非字符串字典序比较。
func TestReviewFractionalSort(t *testing.T) {
	require.True(t, LessSortKey(
		SortKey{EventTimeUTC: "2026-09-22T01:00:00.1Z"},
		SortKey{EventTimeUTC: "2026-09-22T01:00:00Z"},
	))
}
