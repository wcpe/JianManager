package query

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestQueryViewReuseDoesNotFreezeTransientRangeFailure(t *testing.T) {
	cat, key := queryLeaseCatalog(t, nil)
	planner := NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	var first atomic.Bool
	first.Store(true)
	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		if first.Swap(false) {
			return RangeResult{Exhausted: false, StatsQuality: StatsUnavailable, CoverageReasons: []string{ReasonRangeUnavailable}}, nil
		}
		return RangeResult{Exhausted: true, DuplicateQuality: DupExact, StatsQuality: StatsExact}, nil
	}, StatsFn: func(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
		return StatsResult{DuplicateQuality: DupExact, StatsQuality: StatsExact}, nil
	}}
	svc := NewService(planner, client, "transient-quality")
	req := QueryRequest{AuthorizedTargets: []string{key.String()}}
	failed := svc.Search(context.Background(), req)
	require.Nil(t, failed.Err)
	require.Equal(t, StatsUnavailable, failed.Quality.StatsQuality)
	require.False(t, failed.Coverage.Complete)
	req.View = failed.View
	recovered := svc.Search(context.Background(), req)
	require.Nil(t, recovered.Err)
	require.True(t, recovered.Coverage.Complete)
	require.Equal(t, DupExact, recovered.Quality.DuplicateQuality)
	require.Equal(t, StatsExact, recovered.Quality.StatsQuality)
	stats := svc.Stats(context.Background(), req, nil)
	require.Nil(t, stats.Err)
	require.Equal(t, StatsExact, stats.Quality.StatsQuality)
}

func TestQueryViewReuseKeepsFrozenWorstQualityAcrossAppendAndMigration(t *testing.T) {
	for _, entry := range []string{"open-view", "search"} {
		t.Run(entry, func(t *testing.T) {
			cat, key := queryLeaseCatalog(t, nil)
			expected, _ := cat.Get(key)
			conflicted := expected.Clone()
			conflicted.PublishedProjection.ConflictCount = 1
			require.NoError(t, cat.PublishProjection(expected, conflicted))
			planner := NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
				return RangeResult{Items: []logtypes.Event{{EventID: "logical", EventTimeUTC: "2026-09-22T12:00:00Z"}}, Exhausted: true, DuplicateQuality: DupExact, StatsQuality: StatsExact}, nil
			}, StatsFn: func(context.Context, AuthoritativeRange, RangeQuery) (StatsResult, error) {
				return StatsResult{Points: []StatsPoint{{Count: 1}}, DuplicateQuality: DupExact, StatsQuality: StatsExact}, nil
			}}
			svc := NewService(planner, client, "quality-view")
			req := QueryRequest{AuthorizedTargets: []string{key.String()}}
			var first SearchResponse
			if entry == "open-view" {
				first = svc.OpenView(req)
			} else {
				first = svc.Search(context.Background(), req)
			}
			require.Nil(t, first.Err)
			require.True(t, first.Coverage.Complete, "冲突质量与覆盖完整性是独立维度")
			require.Equal(t, DupConflict, first.Quality.DuplicateQuality)
			require.Equal(t, StatsPartial, first.Quality.StatsQuality)
			req.View = first.View
			check := func() {
				response := svc.Search(context.Background(), req)
				require.Nil(t, response.Err)
				require.True(t, response.Coverage.Complete)
				require.Equal(t, DupConflict, response.Quality.DuplicateQuality)
				require.Equal(t, StatsPartial, response.Quality.StatsQuality)
				stats := svc.Stats(context.Background(), req, nil)
				require.Nil(t, stats.Err)
				require.Equal(t, DupConflict, stats.Quality.DuplicateQuality)
				require.Equal(t, StatsPartial, stats.Quality.StatsQuality)
			}
			check()
			expected, _ = cat.Get(key)
			next := expected.Clone()
			next.PublishedProjection.ManifestVersion = "appended"
			next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 200
			next.PublishedProjection.ConflictCount = 0
			require.NoError(t, cat.PublishProjection(expected, next))
			check()
			_, err := cat.BeginMigration(key, catalog.OwnerCold, "cold-2")
			require.NoError(t, err)
			for _, state := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify, catalog.StateAttachedStaging} {
				_, err = cat.Advance(key, state)
				require.NoError(t, err)
			}
			require.NoError(t, cat.RecordVerification(key, "unchanged-logical-content"))
			current, _ := cat.Get(key)
			movedProjection := current.Clone().PublishedProjection
			movedProjection.QueryLocationDirID = "cold-2"
			movedProjection.QueryGeneration = 2
			_, err = cat.SwitchOwner(key, catalog.OwnerCold, "cold-2", 2, movedProjection)
			require.NoError(t, err)
			check()
			// 日已切到 COLD 但责任未验证 ⇒ 仍不可退休（门禁本身的结论）。
			if ok, reason := cat.CanRetirePhysicalDay(key.UTCDay); ok {
				t.Fatalf("未验证责任转移时物理日不得可退休：%s", reason)
			}
			check()
		})
	}
}
