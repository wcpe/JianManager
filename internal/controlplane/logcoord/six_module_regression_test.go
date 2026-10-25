package logcoord

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
)

// 原审查反例转入正式套件；真实 Service、PB 与适配层共同验证 CP 契约。
type review05PB struct {
	workerpb.WorkerServiceClient
	svc            *grpcsvc.Service
	fieldsComplete bool
	facetsComplete bool
	searchCalls    atomic.Int32
	openCalls      atomic.Int32
	requestMu      sync.Mutex
	searchViews    []string
}

func (p *review05PB) LogCreateView(ctx context.Context, q *workerpb.LogCreateViewRequest, _ ...grpc.CallOption) (*workerpb.LogCreateViewResponse, error) {
	p.openCalls.Add(1)
	return p.svc.LogCreateView(ctx, q)
}
func (p *review05PB) LogSearch(ctx context.Context, q *workerpb.LogSearchRequest, _ ...grpc.CallOption) (*workerpb.LogSearchResponse, error) {
	p.searchCalls.Add(1)
	p.requestMu.Lock()
	p.searchViews = append(p.searchViews, q.GetQuery().GetView().GetViewId())
	p.requestMu.Unlock()
	return p.svc.LogSearch(ctx, q)
}
func (p *review05PB) LogStats(ctx context.Context, q *workerpb.LogStatsRequest, _ ...grpc.CallOption) (*workerpb.LogStatsResponse, error) {
	return p.svc.LogStats(ctx, q)
}
func (p *review05PB) LogFields(ctx context.Context, q *workerpb.LogFieldsRequest, _ ...grpc.CallOption) (*workerpb.LogFieldsResponse, error) {
	out, err := p.svc.LogFields(ctx, q)
	p.fieldsComplete = out.GetCoverage().GetComplete()
	return out, err
}
func (p *review05PB) LogFacets(ctx context.Context, q *workerpb.LogFacetsRequest, _ ...grpc.CallOption) (*workerpb.LogFacetsResponse, error) {
	out, err := p.svc.LogFacets(ctx, q)
	p.facetsComplete = out.GetCoverage().GetComplete()
	return out, err
}

type review05Range struct {
	workerquery.FuncRangeClient
	fieldsFn func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery) (workerquery.FieldsResult, error)
	facetsFn func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery, []string, uint32) (workerquery.RangeFacetsResult, error)
}

func (r *review05Range) Fields(ctx context.Context, rng workerquery.AuthoritativeRange, q workerquery.RangeQuery) (workerquery.FieldsResult, error) {
	return r.fieldsFn(ctx, rng, q)
}
func (r *review05Range) Facets(ctx context.Context, rng workerquery.AuthoritativeRange, q workerquery.RangeQuery, dims []string, limit uint32) (workerquery.RangeFacetsResult, error) {
	return r.facetsFn(ctx, rng, q, dims, limit)
}

func review05Catalog(t *testing.T) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
	rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "m1", CoverageComplete: true, QueryLocationDirID: "hot-1", QueryGeneration: 1, ClosedVisibleSeq: map[string]uint64{key.String(): 20}, SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "src-a", SourceGeneration: "g1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 20}}}
	require.NoError(t, cat.Put(rec))
	return cat, key
}
func review05Query() Query {
	return Query{AuthorizedTargetIDs: []string{"inst:1"}, PrincipalKey: "review-user", PermissionScope: "review-scope", FromUTC: "2026-09-22T00:00:00Z", ToUTC: "2026-09-23T00:00:00Z", Budget: QueryBudget{Limit: 1}}
}
func review05Coordinator(t *testing.T, cat *catalog.Catalog, client workerquery.RangeClient) (*Coordinator, *review05PB) {
	t.Helper()
	planner := workerquery.NewPlanner(cat, nil)
	pb := &review05PB{svc: grpcsvc.New(workerquery.NewService(planner, client, "review05"), planner)}
	resolver := &fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = NewWorkerClientAdapter(pb)
	return New(resolver, dialer), pb
}

func TestReview05FastFirstPageIsNotSnapshotBound(t *testing.T) {
	cat, key := review05Catalog(t)
	rows := []logtypes.Event{
		{EventID: "first", EventTimeUTC: "2026-09-22T12:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: 0, End: 10}},
		{EventID: "old", EventTimeUTC: "2026-09-22T10:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: 10, End: 20}},
	}
	rangeClient := workerquery.FuncRangeClient{SearchFn: func(_ context.Context, _ workerquery.AuthoritativeRange, q workerquery.RangeQuery) (workerquery.RangeResult, error) {
		res := workerquery.RangeResult{Exhausted: true, DuplicateQuality: workerquery.DupExact, StatsQuality: workerquery.StatsExact}
		for _, row := range rows {
			if row.Record.End <= q.ClosedVisibleSeq {
				res.Items = append(res.Items, row)
			}
		}
		return res, nil
	}}
	coord, _ := review05Coordinator(t, cat, rangeClient)
	q := review05Query()
	q.Fast = true
	first, err := coord.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, "first", first.Items[0].EventID)
	require.NotEmpty(t, first.NextCursor)
	require.NotEmpty(t, first.View.WorkerViewIDs["w1"])
	require.Equal(t, "20", first.View.ClosedVisibleSeq["inst:1"])
	stored, ok := coord.GetView(first.View.ViewID)
	require.True(t, ok)
	require.Equal(t, first.View.WorkerViewIDs, stored.WorkerViewIDs)
	require.Equal(t, first.View.ClosedVisibleSeq, stored.ClosedVisibleSeq)
	// 首屏之后到达一个事件时间早于首屏锚点的新接纳行。
	rows = append(rows, logtypes.Event{EventID: "late-after-snapshot", EventTimeUTC: "2026-09-22T11:00:00Z", Source: logtypes.SourceIdentity{LogSourceID: "src-a", SourceGeneration: "g1"}, Record: logtypes.RecordRange{Start: 20, End: 30}})
	rec, _ := cat.Get(key)
	rec.PublishedProjection.ManifestVersion = "m2"
	rec.PublishedProjection.ClosedVisibleSeq[key.String()] = 30
	rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 30
	require.NoError(t, cat.Put(rec))
	q.ViewID, q.Cursor = first.View.ViewID, first.NextCursor
	second, err := coord.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	t.Logf("相同CP view=%s，首屏实际Worker引用已固定，次页读到=%s，complete=%v pending=%v", second.View.ViewID, second.Items[0].EventID, second.Coverage.Complete, second.Coverage.Pending)
	require.Equal(t, "old", second.Items[0].EventID, "固定视图次页不能读到首屏之后新接纳的行")
}

func TestReview05FieldsFacetsLoseTopLevelCoverage(t *testing.T) {
	for _, operation := range []string{"fields", "facets"} {
		t.Run(operation, func(t *testing.T) {
			cat, _ := review05Catalog(t)
			client := &review05Range{}
			client.fieldsFn = func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery) (workerquery.FieldsResult, error) {
				return workerquery.FieldsResult{Bytes: 64, CoverageReasons: []string{workerquery.ReasonBudgetExceeded}}, nil
			}
			client.facetsFn = func(context.Context, workerquery.AuthoritativeRange, workerquery.RangeQuery, []string, uint32) (workerquery.RangeFacetsResult, error) {
				return workerquery.RangeFacetsResult{Bytes: 64, Values: []workerquery.FacetValue{{Dimension: "level", Value: "INFO", Count: 7}}, CoverageReasons: []string{workerquery.ReasonBudgetExceeded}}, nil
			}
			coord, pb := review05Coordinator(t, cat, client)
			if operation == "fields" {
				out, err := coord.Fields(context.Background(), review05Query())
				require.NoError(t, err)
				require.False(t, pb.fieldsComplete)
				t.Logf("Worker complete=%v；CP complete=%v reasons=%v", pb.fieldsComplete, out.Coverage.Complete, out.Coverage.PartialReasons)
				require.False(t, out.Coverage.Complete, "Worker部分字段枚举不能变成CP完整结果")
			} else {
				out, err := coord.Facets(context.Background(), FacetsQuery{Query: review05Query(), Dimensions: []string{"level"}})
				require.NoError(t, err)
				require.False(t, pb.facetsComplete)
				t.Logf("Worker complete=%v；CP complete=%v quality=%+v reasons=%v truncated=%v", pb.facetsComplete, out.Coverage.Complete, out.Quality, out.Coverage.PartialReasons, out.Truncated)
				require.False(t, out.Coverage.Complete, "Worker部分维度统计不能变成CP完整结果")
			}
		})
	}
}

// 记录真实CP调度传给每个Worker的读取预算。
type review05BudgetWorker struct {
	*fakeWorker
	muBudget    sync.Mutex
	readBudgets []QueryBudget
}

func (w *review05BudgetWorker) Fields(ctx context.Context, req WorkerSearchRequest) (*WorkerFieldsResponse, error) {
	w.muBudget.Lock()
	w.readBudgets = append(w.readBudgets, req.Budget)
	w.muBudget.Unlock()
	return w.fakeWorker.Fields(ctx, req)
}
func (w *review05BudgetWorker) Facets(ctx context.Context, req WorkerSearchRequest, dims []string, n uint32) (*WorkerFacetsResponse, error) {
	w.muBudget.Lock()
	w.readBudgets = append(w.readBudgets, req.Budget)
	w.muBudget.Unlock()
	return w.fakeWorker.Facets(ctx, req, dims, n)
}
func TestReview05FieldsFacetsDuplicateRequestBudget(t *testing.T) {
	for _, operation := range []string{"fields", "facets"} {
		t.Run(operation, func(t *testing.T) {
			resolver := &fakeTargetResolver{}
			dialer := newFakeDialer()
			var workers []*review05BudgetWorker
			for n := 1; n <= 2; n++ {
				id, wid := fmt.Sprintf("inst:%d", n), fmt.Sprintf("w%d", n)
				resolver.targets = append(resolver.targets, TargetInfo{ID: id, WorkerID: wid, Readiness: ReadyOnline})
				w := &review05BudgetWorker{fakeWorker: &fakeWorker{workerID: wid, searchTarget: []WorkerTargetResult{successTarget(id, wid, "20", "1")}, facetTargets: []WorkerTargetResult{successTarget(id, wid, "20", "1")}}}
				workers = append(workers, w)
				dialer.clients[wid] = w
			}
			coord := New(resolver, dialer)
			q := review05Query()
			q.AuthorizedTargetIDs = []string{"inst:1", "inst:2"}
			q.Budget = QueryBudget{Limit: 2, MaxBytes: 100, MaxFanout: 2}
			view, err := coord.CreateView(context.Background(), q)
			require.NoError(t, err)
			q.ViewID = view.ViewID
			q.Budget.MaxFanout = 1
			if operation == "fields" {
				_, err = coord.Fields(context.Background(), q)
			} else {
				_, err = coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"level"}})
			}
			require.NoError(t, err)
			var reserved uint64
			calls := 0
			for _, w := range workers {
				for _, b := range w.readBudgets {
					reserved += b.MaxBytes
					calls++
				}
			}
			t.Logf("总请求max_bytes=%d max_fanout=%d，实际读取RPC=%d，子RPC预算合计=%d", q.Budget.MaxBytes, q.Budget.MaxFanout, calls, reserved)
			require.LessOrEqual(t, reserved, q.Budget.MaxBytes, "整请求预算不能复制给各Worker")
			require.LessOrEqual(t, calls, 1, "复用view也必须执行本次max_fanout")
		})
	}
}
