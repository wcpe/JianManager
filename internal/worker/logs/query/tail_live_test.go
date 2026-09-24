package query

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

type liveRangeFixture struct{ FuncRangeClient }

func (f liveRangeFixture) FollowLive(ctx context.Context, r AuthoritativeRange, q RangeQuery) (RangeResult, error) {
	return f.Search(ctx, r, q)
}

func TestFollowLiveReplansNewProjectionAndNewUTCDay(t *testing.T) {
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")
	rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "p1", ProjectionGeneration: "p1", CoverageComplete: true,
		QueryLocationDirID: "hot", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{"default": 10}}
	require.NoError(t, cat.Put(rec))
	p := NewPlanner(cat, nil)
	calls := 0
	client := liveRangeFixture{FuncRangeClient{SearchFn: func(ctx context.Context, r AuthoritativeRange, q RangeQuery) (RangeResult, error) {
		calls++
		require.NotZero(t, q.ClosedVisibleSeq, "live must not bypass closed prefixes")
		if calls == 1 {
			next := catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-23"}, catalog.OwnerHot, 1, "next-day")
			next.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "p2", ProjectionGeneration: "p2", CoverageComplete: true,
				QueryLocationDirID: "next-day", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{"default": 20}}
			require.NoError(t, cat.Put(next))
		}
		id := "old"
		if r.Key.UTCDay == "2026-09-23" {
			id = "new"
		}
		return RangeResult{Items: []logtypes.Event{{EventID: id, CanonicalHash: id}}, Exhausted: true}, nil
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp := NewService(p, client, "test").Tail(ctx, QueryRequest{AuthorizedTargets: []string{"inst:1"}, Budget: Budget{Limit: 2}}, TailFollowLive)
	require.Nil(t, resp.Err)
	require.Len(t, resp.Items, 2)
	require.False(t, resp.Exhausted)
	require.Equal(t, EnumOpen, resp.Coverage.EnumerationState)
	require.LessOrEqual(t, len(p.Views()), 1, "live cycles must not leak new reusable views")
}

func TestFollowLiveReportsCallerCancellation(t *testing.T) {
	cat := newCatalogWithHot(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := liveRangeFixture{FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		cancel()
		return RangeResult{Exhausted: true}, nil
	}}}
	resp := NewService(NewPlanner(cat, nil), client, "test").Tail(ctx, QueryRequest{}, TailFollowLive)
	require.NotNil(t, resp.Err)
	require.Equal(t, ErrCodeCancelled, resp.Err.Code)
	require.False(t, resp.Coverage.Complete)
}
