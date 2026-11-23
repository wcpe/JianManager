package query

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 首屏部分故障消失后，正常后续页不应跳过故障范围里尚未读到的更新行。
func TestReview05RangeFailureCursorSkipsRecoveredRows(t *testing.T) {
	cat := catalog.New(nil)
	for _, name := range []string{"ns/hot", "ns/cold"} {
		owner := catalog.OwnerHot
		if name == "ns/cold" {
			owner = catalog.OwnerCold
		}
		key := catalog.PartitionKey{StorageNamespace: name, UTCDay: "2026-09-22"}
		rec := catalog.NewStableRecord(key, owner, 1, name+"-1")
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "m1", CoverageComplete: true, QueryLocationDirID: name + "-1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
		require.NoError(t, cat.Put(rec))
	}
	failing := true
	client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
		if rng.Owner == catalog.OwnerHot && failing {
			return RangeResult{}, fmt.Errorf("隔离测试模拟暂时不可用")
		}
		res := RangeResult{Exhausted: true, DuplicateQuality: DupExact, StatsQuality: StatsExact}
		if rng.Owner == catalog.OwnerHot {
			res.Items = []logtypes.Event{{EventID: "recovered-new", EventTimeUTC: "2026-09-22T12:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "hot", SourceGeneration: "g1"}}}
		} else {
			res.Items = []logtypes.Event{{EventID: "old", EventTimeUTC: "2026-09-22T10:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "cold", SourceGeneration: "g1"}}, {EventID: "older", EventTimeUTC: "2026-09-22T09:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "cold", SourceGeneration: "g1"}}}
		}
		return res, nil
	}}
	svc := NewService(NewPlanner(cat, nil), client, "review05")
	req := QueryRequest{AuthorizedTargets: []string{"ns/hot", "ns/cold"}, Budget: Budget{Limit: 1}}
	first := svc.Search(context.Background(), req)
	require.Nil(t, first.Err)
	require.False(t, first.Coverage.Complete)
	require.Len(t, first.Items, 1)
	require.Empty(t, first.NextCursor, "未知range不能提交新游标")
	require.False(t, first.Exhausted)
	require.Equal(t, EnumOpen, first.Coverage.EnumerationState)
	t.Logf("首屏partial=%v item=%s cursor=%s", first.Coverage.PartialReasons, first.Items[0].EventID, first.NextCursor)
	failing = false
	req.View = &ViewRef{ViewID: first.View.ViewID, Cursor: first.NextCursor, OrderVersion: SortVersion}
	second := svc.Search(context.Background(), req)
	require.Nil(t, second.Err)
	ids := []string{}
	for _, ev := range second.Items {
		ids = append(ids, ev.EventID)
	}
	t.Logf("恢复后次页items=%v complete=%v exhausted=%v", ids, second.Coverage.Complete, second.Exhausted)
	require.Contains(t, ids, "recovered-new", "partial首屏发出的游标跨过了未读范围，恢复后更早结束仍漏行")
}

func TestReview05PlanDoesNotAcquireSnapshotLease(t *testing.T) {
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
	rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "m1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 100}}
	require.NoError(t, cat.Put(rec))
	plan := NewPlanner(cat, nil).Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, plan.Err)
	rec, _ = cat.Get(key)
	t.Logf("view=%s expires_zero=%v active_query_leases=%d", plan.View.ViewID, plan.View.ExpiresAt.IsZero(), rec.ActiveLeaseCount(plan.View.CreatedAt))
	require.Positive(t, rec.ActiveLeaseCount(plan.View.CreatedAt), "创建固定视图后Catalog必须有可核验查询租约")
}
