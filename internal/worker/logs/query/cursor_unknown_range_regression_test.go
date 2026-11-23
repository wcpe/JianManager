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

func TestQueryUnknownRangesNeverCommitGlobalCursor(t *testing.T) {
	for _, unknown := range []string{"newer-error", "older-error", "top-level-coverage", "missing-boundary", "one-fence-one-error", "fanout-cut"} {
		t.Run(unknown, func(t *testing.T) {
			cat := catalog.New(nil)
			for _, entry := range []struct {
				namespace string
				owner     catalog.Owner
			}{{"hot", catalog.OwnerHot}, {"cold", catalog.OwnerCold}} {
				key := catalog.PartitionKey{StorageNamespace: entry.namespace, UTCDay: "2026-09-22"}
				require.NoError(t, cat.Put(queryableRecord(key, entry.owner, 1, entry.namespace+"-1")))
			}
			planner := NewPlanner(cat, nil)
			t.Cleanup(func() { require.NoError(t, planner.Close()) })
			var recovery atomic.Bool
			client := FuncRangeClient{SearchFn: func(_ context.Context, rng AuthoritativeRange, _ RangeQuery) (RangeResult, error) {
				if !recovery.Load() && rng.Owner == catalog.OwnerHot {
					switch unknown {
					case "newer-error", "older-error", "one-fence-one-error":
						return RangeResult{}, fmt.Errorf("未知范围")
					case "top-level-coverage":
						return RangeResult{Exhausted: true, CoverageReasons: []string{"UNKNOWN_SOURCE_PREFIX"}}, nil
					case "missing-boundary":
						return RangeResult{}, nil
					}
				}
				if unknown == "one-fence-one-error" && rng.Owner == catalog.OwnerCold {
					return RangeResult{SafeBeforeUTC: "2026-09-22T10:00:00Z"}, nil
				}
				time := "2026-09-22T12:00:00Z"
				if rng.Owner == catalog.OwnerCold {
					time = "2026-09-22T11:00:00Z"
				}
				if unknown == "older-error" && rng.Owner == catalog.OwnerHot {
					time = "2026-09-22T09:00:00Z"
				}
				return RangeResult{Exhausted: true, Items: []logtypes.Event{{EventID: string(rng.Owner), EventTimeUTC: time,
					Source: logtypes.SourceIdentity{LogSourceID: string(rng.Owner), SourceGeneration: "s1"}}}}, nil
			}}
			svc := NewService(planner, client, "cursor-unknown")
			req := QueryRequest{AuthorizedTargets: []string{"hot", "cold"}, Budget: Budget{Limit: 1}}
			if unknown == "fanout-cut" {
				req.Budget.MaxFanout = 1
			}
			first := svc.Search(context.Background(), req)
			require.False(t, first.Coverage.Complete)
			require.Empty(t, first.NextCursor)
			require.False(t, first.Exhausted)
			require.Equal(t, EnumOpen, first.Coverage.EnumerationState)
			if unknown == "one-fence-one-error" {
				return
			}
			recovery.Store(true)
			req.Budget.MaxFanout = 0
			req.View = &ViewRef{ViewID: first.View.ViewID, Cursor: first.NextCursor}
			retried := svc.Search(context.Background(), req)
			require.Nil(t, retried.Err)
			require.True(t, retried.Coverage.Complete)
			require.NotEmpty(t, retried.Items)
		})
	}
}

func TestQueryFailedContinuationCanRetrySameIncomingCursor(t *testing.T) {
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "one", UTCDay: "2026-09-22"}
	require.NoError(t, cat.Put(queryableRecord(key, catalog.OwnerHot, 1, "hot-1")))
	planner := NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	var failed atomic.Bool
	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		if failed.Load() {
			return RangeResult{}, fmt.Errorf("临时失败")
		}
		return RangeResult{Exhausted: true, Items: []logtypes.Event{{EventID: "first", EventTimeUTC: "2026-09-22T12:00:00Z"},
			{EventID: "second", EventTimeUTC: "2026-09-22T11:00:00Z"}, {EventID: "third", EventTimeUTC: "2026-09-22T10:00:00Z"}}}, nil
	}}
	svc := NewService(planner, client, "cursor-retry")
	req := QueryRequest{AuthorizedTargets: []string{key.String()}, Budget: Budget{Limit: 1}}
	first := svc.Search(context.Background(), req)
	require.NotEmpty(t, first.NextCursor)
	req.View = &ViewRef{ViewID: first.View.ViewID, Cursor: first.NextCursor}
	original := req.View.Cursor
	failed.Store(true)
	partial := svc.Search(context.Background(), req)
	require.Empty(t, partial.NextCursor)
	require.False(t, partial.Exhausted)
	require.Equal(t, original, req.View.Cursor, "失败不销毁或推进原入 cursor")
	failed.Store(false)
	retry := svc.Search(context.Background(), req)
	require.Nil(t, retry.Err)
	require.Equal(t, "second", retry.Items[0].EventID)
}

func TestQueryRangeFailureKeepsVerifiedPartialRowsButNoCursor(t *testing.T) {
	cat := newCatalogWithHot(t)
	planner := NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	client := FuncRangeClient{SearchFn: func(context.Context, AuthoritativeRange, RangeQuery) (RangeResult, error) {
		return RangeResult{Items: []logtypes.Event{{EventID: "verified-partial", EventTimeUTC: "2026-09-20T12:00:00Z"}}}, fmt.Errorf("后续块读取失败")
	}}
	response := NewService(planner, client, "partial-row").Search(context.Background(), QueryRequest{Budget: Budget{Limit: 1}})
	require.Len(t, response.Items, 1)
	require.Equal(t, "verified-partial", response.Items[0].EventID)
	require.False(t, response.Coverage.Complete)
	require.False(t, response.Exhausted)
	require.Empty(t, response.NextCursor)
	require.Equal(t, EnumOpen, response.Coverage.EnumerationState)
}
