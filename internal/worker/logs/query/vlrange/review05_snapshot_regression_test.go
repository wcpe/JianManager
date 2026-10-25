package vlrange

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 审查反例进入正式回归，使用真实 Planner 与归因谓词。
func review05ScopedCatalog(t *testing.T) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion: "m1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1,
		ClosedVisibleSeq: map[string]uint64{key.String(): 1000, "default": 1000, "slow/g1": 100, "fast/g1": 1000},
		SourceProjections: []catalog.SourceProjection{
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "slow", SourceGeneration: "g1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 100},
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "fast", SourceGeneration: "g1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 1000},
		},
	}
	require.NoError(t, cat.Put(rec))
	return cat, key
}
func review05ScopedEvent(end uint64) logtypes.Event {
	return logtypes.Event{EventID: "slow-event", EventTimeUTC: "2026-09-22T12:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "slow", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: end - 1, End: end}, Fields: map[string]string{projectionGenerationField: "p1"}}
}

func TestReview05ProjectionDriftChangesPerSourceSnapshot(t *testing.T) {
	for _, change := range []string{"advance", "remove", "zero", "add", "rebuild"} {
		t.Run(change, func(t *testing.T) {
			cat, key := review05ScopedCatalog(t)
			planner := query.NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			first := planner.Plan(query.PlanRequest{TargetIDs: []string{key.StorageNamespace}})
			require.Nil(t, first.Err)
			require.Len(t, first.Ranges, 1)
			rec, _ := cat.Get(key)
			rec.PublishedProjection.ManifestVersion = "m2"
			var ev logtypes.Event
			wantMatch := false
			switch change {
			case "advance":
				ev = review05ScopedEvent(150)
				require.False(t, rangeMatchesEvent(first.Ranges[0], ev))
				rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 200
				rec.PublishedProjection.ClosedVisibleSeq["slow/g1"] = 200
			case "remove":
				ev = review05ScopedEvent(90)
				require.True(t, rangeMatchesEvent(first.Ranges[0], ev))
				rec.PublishedProjection.SourceProjections = rec.PublishedProjection.SourceProjections[1:]
				delete(rec.PublishedProjection.ClosedVisibleSeq, "slow/g1")
				wantMatch = true
			case "zero":
				ev = review05ScopedEvent(90)
				require.True(t, rangeMatchesEvent(first.Ranges[0], ev))
				rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 0
				rec.PublishedProjection.ClosedVisibleSeq["slow/g1"] = 0
				wantMatch = true
			case "add":
				rec.PublishedProjection.SourceProjections = append(rec.PublishedProjection.SourceProjections,
					catalog.SourceProjection{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "new-source", SourceGeneration: "g1"},
						ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 100})
			case "rebuild":
				rec.PublishedProjection.SourceProjections[0].ProjectionGenerations = []string{"replacement"}
			}
			require.NoError(t, cat.Put(rec))
			next := planner.Plan(query.PlanRequest{TargetIDs: []string{key.StorageNamespace}, ViewRef: &query.ViewRef{ViewID: first.View.ViewID}, AllowProjectionDrift: true})
			t.Logf("变化=%s；旧partition水位=%d，reuse错误=%v complete=%v", change, first.Ranges[0].ClosedVisibleSeq, next.Err, next.Coverage.Complete)
			if change != "advance" {
				require.NotNil(t, next.Err, "破坏原集合必须明确失效")
				require.Equal(t, query.ErrCodeViewStale, next.Err.Code)
				return
			}
			require.Nil(t, next.Err, "正常追加必须继续使用旧集合，不能令活源导出恒失败")
			require.Len(t, next.Ranges, 1)
			gotMatch := rangeMatchesEvent(next.Ranges[0], ev)
			t.Logf("旧view归因发生变化：record_end=%d，复用匹配=%v，冻结集合应匹配=%v", ev.Record.End, gotMatch, wantMatch)
			require.Equal(t, wantMatch, gotMatch, "同一view要么保持逐源集合、要么明确VIEW_STALE，不能静默改变集合")
		})
	}
}

func TestReview05LegacyZeroClosedPrefixIsUnlimited(t *testing.T) {
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "slow", UTCDay: "2026-09-22"}, Owner: catalog.OwnerHot, ClosedVisibleSeq: 0, Projection: &catalog.PublishedProjection{ProjectionGeneration: "p1"}}
	ev := review05ScopedEvent(10)
	t.Logf("legacy ClosedVisibleSeq=0；selector=%s，内存归因匹配=%v", selectorQuery(rng, query.RangeQuery{ClosedVisibleSeq: 0}), rangeMatchesEvent(rng, ev))
	require.False(t, rangeMatchesEvent(rng, ev), "零封闭前缀不能代表无限读取")
}
