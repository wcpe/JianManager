package logcoord

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

type snapshotChainRows struct {
	mu   sync.RWMutex
	rows []logtypes.Event
}

func newSnapshotChain(t *testing.T) (*Coordinator, *review05PB, *snapshotChainRows, *catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	cat, key := review05Catalog(t)
	rows := &snapshotChainRows{rows: []logtypes.Event{
		{EventID: "first", EventTimeUTC: "2026-09-22T12:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: 0, End: 10}},
		{EventID: "old", EventTimeUTC: "2026-09-22T10:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: 10, End: 20}},
	}}
	client := workerquery.FuncRangeClient{SearchFn: func(_ context.Context, _ workerquery.AuthoritativeRange, q workerquery.RangeQuery) (workerquery.RangeResult, error) {
		rows.mu.RLock()
		defer rows.mu.RUnlock()
		out := workerquery.RangeResult{Exhausted: true, DuplicateQuality: workerquery.DupExact, StatsQuality: workerquery.StatsExact}
		for _, row := range rows.rows {
			if row.Record.End <= q.ClosedVisibleSeq {
				out.Items = append(out.Items, row)
			}
		}
		return out, nil
	}}
	coord, pb := review05Coordinator(t, cat, client)
	return coord, pb, rows, cat, key
}

func appendSnapshotChain(t *testing.T, rows *snapshotChainRows, cat *catalog.Catalog, key catalog.PartitionKey) {
	t.Helper()
	rows.mu.Lock()
	rows.rows = append(rows.rows, logtypes.Event{EventID: "late", EventTimeUTC: "2026-09-22T11:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: 20, End: 30}})
	rows.mu.Unlock()
	record, _ := cat.Get(key)
	record.PublishedProjection.ManifestVersion = "m2"
	record.PublishedProjection.ClosedVisibleSeq[key.String()] = 30
	record.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 30
	require.NoError(t, cat.Put(record))
}

func TestSixModuleCachedFastViewRetainsRealWorkerSnapshot(t *testing.T) {
	coord, pb, rows, cat, key := newSnapshotChain(t)
	query := review05Query()
	query.Fast = true
	first, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	appendSnapshotChain(t, rows, cat, key)
	query.PrincipalKey, query.PermissionScope = "other-authorized-user", "other-authorized-scope"
	cached, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.NotEqual(t, first.View.ViewID, cached.View.ViewID)
	require.Equal(t, first.View.WorkerViewIDs, cached.View.WorkerViewIDs)
	require.Equal(t, first.View.ClosedVisibleSeq, cached.View.ClosedVisibleSeq)
	require.Equal(t, int32(1), pb.searchCalls.Load(), "命中不得重读，更不能重采 Worker 快照")
	require.Zero(t, pb.openCalls.Load())
	query.ViewID, query.Cursor = cached.View.ViewID, cached.NextCursor
	page, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "old", page.Items[0].EventID)
	require.Equal(t, cached.View.WorkerViewIDs, page.View.WorkerViewIDs)
	stored, ok := coord.GetView(query.ViewID)
	require.True(t, ok)
	require.Equal(t, "20", stored.ClosedVisibleSeq["inst:1"])
}

func TestSixModuleConcurrentFastReuseUsesOneActualSnapshot(t *testing.T) {
	coord, pb, rows, cat, key := newSnapshotChain(t)
	query := review05Query()
	query.Fast = true
	first, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	appendSnapshotChain(t, rows, cat, key)
	query.ViewID, query.Cursor, query.BypassCache = first.View.ViewID, first.NextCursor, true
	const readers = 8
	outputs := make(chan *SearchResponse, readers)
	errors := make(chan error, readers)
	var group sync.WaitGroup
	for index := 0; index < readers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			out, err := coord.Search(context.Background(), query)
			outputs <- out
			errors <- err
		}()
	}
	group.Wait()
	for index := 0; index < readers; index++ {
		require.NoError(t, <-errors)
		out := <-outputs
		require.Len(t, out.Items, 1)
		require.Equal(t, "old", out.Items[0].EventID)
		require.Equal(t, first.View.WorkerViewIDs, out.View.WorkerViewIDs)
	}
	require.Zero(t, pb.openCalls.Load(), "并发复用不得 OpenView 换集合")
	pb.requestMu.Lock()
	defer pb.requestMu.Unlock()
	for _, viewID := range pb.searchViews[1:] {
		require.Equal(t, first.View.WorkerViewIDs["w1"], viewID)
	}
}

func TestSixModuleFailedInitialWorkerDoesNotInventResolverVector(t *testing.T) {
	coord, _, _, _, _ := newSnapshotChain(t)
	resolver := coord.resolver.(*fakeTargetResolver)
	resolver.targets = append(resolver.targets, TargetInfo{ID: "inst:2", WorkerID: "w2", Readiness: ReadyOnline, ClosedVisibleSeq: "99", CatalogGeneration: "later"})
	dialer := coord.dialer.(*fakeDialer)
	dialer.fail["w2"] = errors.New("首屏不可用")
	query := review05Query()
	query.AuthorizedTargetIDs = []string{"inst:1", "inst:2"}
	query.Fast = true
	first, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.False(t, first.Coverage.Complete)
	require.NotContains(t, first.View.ClosedVisibleSeq, "inst:2", "注册表提示不是实际 Worker 快照向量")
	require.NotContains(t, first.View.WorkerCatalogGenerations, "inst:2")
}

func TestSixModuleFailedInitialWorkerNeverRecapturesLatestSnapshot(t *testing.T) {
	coord, pb, rows, cat, key := newSnapshotChain(t)
	resolver := coord.resolver.(*fakeTargetResolver)
	resolver.targets = append(resolver.targets, TargetInfo{ID: "inst:2", WorkerID: "w2", Readiness: ReadyOnline})
	dialer := coord.dialer.(*fakeDialer)
	dialer.fail["w2"] = errors.New("首屏不可用")
	other := &fakeWorker{workerID: "w2", searchTarget: []WorkerTargetResult{successTarget("inst:2", "w2", "30", "1")}}
	dialer.clients["w2"] = other
	query := review05Query()
	query.AuthorizedTargetIDs = []string{"inst:1", "inst:2"}
	query.Fast = true
	first, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.False(t, first.Coverage.Complete)
	require.Empty(t, first.NextCursor)
	require.NotEmpty(t, first.View.WorkerViewIDs["w1"])
	require.NotEmpty(t, first.View.WorkerViewErrors["w2"])
	appendSnapshotChain(t, rows, cat, key)
	delete(dialer.fail, "w2")
	query.ViewID = first.View.ViewID
	retry, err := coord.Search(context.Background(), query)
	require.NoError(t, err)
	require.False(t, retry.Coverage.Complete)
	require.Empty(t, retry.NextCursor)
	require.False(t, retry.Exhausted)
	require.Equal(t, first.View.WorkerViewIDs, retry.View.WorkerViewIDs)
	require.Zero(t, other.openViewCalls)
	require.Zero(t, other.searchCalls)
	require.Zero(t, pb.openCalls.Load())
}
