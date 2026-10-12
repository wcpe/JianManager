package query

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestPR61MergedFanoutCountsOriginalRanges(t *testing.T) {
	for _, maxFanout := range []uint32{0, 1, 2} {
		t.Run(fmt.Sprint(maxFanout), func(t *testing.T) {
			client := &fakeMergedClient{}
			svc := NewService(NewPlanner(multiTargetCatalog(t, []string{"src-a", "src-b"}, "2026-09-22"), nil), client, "test")
			resp := svc.Search(context.Background(), QueryRequest{Budget: Budget{MaxFanout: maxFanout}})
			if maxFanout == 1 {
				require.Zero(t, atomic.LoadInt32(&client.mergedCalls), "一次union也要算两个原range，组预算不足不可偷偷全查")
				require.False(t, resp.Coverage.Complete)
				require.Contains(t, resp.Coverage.PartialReasons, ReasonBudgetExceeded)
				require.Equal(t, StatsPartial, resp.Quality.StatsQuality)
			} else {
				require.Nil(t, resp.Err)
				require.Equal(t, int32(1), atomic.LoadInt32(&client.mergedCalls))
				require.Equal(t, int32(2), atomic.LoadInt32(&client.lastMergedN))
				require.True(t, resp.Coverage.Complete)
			}
		})
	}
}

// 热层未完整12点组不得借冷层11点行推进；其它owner的14点安全行仍参与top-N。
func TestPR61OwnerMergeUsesNewestIncompleteFence(t *testing.T) {
	cat := catalog.New(nil)
	for _, owner := range []catalog.Owner{catalog.OwnerHot, catalog.OwnerCold} {
		key := catalog.PartitionKey{StorageNamespace: string(owner), UTCDay: "2026-09-22"}
		rec := catalog.NewStableRecord(key, owner, 1, "dir-"+string(owner))
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "pv-1", CoverageComplete: true, QueryLocationDirID: "dir-" + string(owner), QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
		require.NoError(t, cat.Put(rec))
	}
	var calls atomic.Int32
	client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, q RangeQuery) (RangeResult, error) {
		calls.Add(1)
		require.Positive(t, q.Budget.MaxBytes, "入站零预算只在请求级默认")
		if rng.Owner == catalog.OwnerHot {
			return RangeResult{Items: []logtypes.Event{buildEvent("hot", "g1", 1, 2, "2026-09-22T13:00:00Z", "safe")}, SafeBeforeUTC: "2026-09-22T12:00:00Z", CoverageReasons: []string{ReasonBudgetExceeded}, Bytes: 1}, nil
		}
		return RangeResult{Items: []logtypes.Event{buildEvent("cold", "g1", 1, 2, "2026-09-22T11:00:00Z", "unsafe"), buildEvent("cold", "g1", 3, 4, "2026-09-22T14:00:00Z", "safe")}, Exhausted: true, Bytes: 1}, nil
	}}
	resp := NewService(NewPlanner(cat, nil), client, "test").Search(context.Background(), QueryRequest{})
	require.Equal(t, int32(2), calls.Load(), "正常默认路径其它owner必须参与")
	require.Nil(t, resp.Err)
	require.Len(t, resp.Items, 2)
	require.Equal(t, "2026-09-22T14:00:00Z", resp.Items[0].EventTimeUTC)
	require.Equal(t, "2026-09-22T13:00:00Z", resp.Items[1].EventTimeUTC)
	require.Empty(t, resp.NextCursor)
	require.False(t, resp.Exhausted)
	require.False(t, resp.Coverage.Complete)
	require.Equal(t, StatsPartial, resp.Quality.StatsQuality)
}
