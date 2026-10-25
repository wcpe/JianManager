package logcoord

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// meteredMetadataRange 以真正的 Reader 字节计数验证成本，不把预留份额冒充实际读取。
type meteredMetadataRange struct {
	workerquery.FuncRangeClient
	bytes     atomic.Uint64
	calls     atomic.Int32
	wait      bool
	stops     atomic.Int32
	reasons   []string
	truncated bool
}

func (m *meteredMetadataRange) read(ctx context.Context, budget workerquery.Budget) (uint64, error) {
	m.calls.Add(1)
	if m.wait {
		<-ctx.Done()
		m.stops.Add(1)
		return 0, ctx.Err()
	}
	source := bytes.NewReader(bytes.Repeat([]byte("实际读取"), 256))
	n, err := io.CopyN(io.Discard, source, int64(budget.MaxBytes))
	if err == io.EOF {
		err = nil
	}
	m.bytes.Add(uint64(n))
	return uint64(n), err
}

func (m *meteredMetadataRange) Fields(ctx context.Context, _ workerquery.AuthoritativeRange, q workerquery.RangeQuery) (workerquery.FieldsResult, error) {
	n, err := m.read(ctx, q.Budget)
	return workerquery.FieldsResult{Fields: []string{"level"}, Bytes: n, CoverageReasons: append([]string(nil), m.reasons...), Truncated: m.truncated}, err
}

func (m *meteredMetadataRange) Facets(ctx context.Context, _ workerquery.AuthoritativeRange, q workerquery.RangeQuery, _ []string, _ uint32) (workerquery.RangeFacetsResult, error) {
	n, err := m.read(ctx, q.Budget)
	return workerquery.RangeFacetsResult{Values: []workerquery.FacetValue{{Dimension: "level", Value: "INFO", Count: 1}}, Bytes: n, CoverageReasons: append([]string(nil), m.reasons...), Truncated: m.truncated}, err
}

func metadataMeterFixture(t *testing.T, wait bool) (*Coordinator, *fakeDialer, []*meteredMetadataRange, Query) {
	t.Helper()
	resolver := &fakeTargetResolver{}
	dialer := newFakeDialer()
	var meters []*meteredMetadataRange
	for index := 1; index <= 2; index++ {
		workerID := fmt.Sprintf("w%d", index)
		cat, _ := review05Catalog(t)
		planner := workerquery.NewPlanner(cat, nil)
		meter := &meteredMetadataRange{wait: wait}
		meters = append(meters, meter)
		pb := &review05PB{svc: grpcsvc.New(workerquery.NewService(planner, meter, workerID), planner)}
		dialer.clients[workerID] = NewWorkerClientAdapter(pb)
		resolver.targets = append(resolver.targets, TargetInfo{ID: "inst:1@" + workerID, WorkerID: workerID, WorkerTargetID: "inst:1", AuthorizationID: "inst:1", Readiness: ReadyOnline})
	}
	query := review05Query()
	query.Budget = QueryBudget{Limit: 2, MaxBytes: 100, MaxFanout: 2}
	coord := New(resolver, dialer)
	view, err := coord.CreateView(context.Background(), query)
	require.NoError(t, err)
	query.ViewID = view.ViewID
	return coord, dialer, meters, query
}

func TestSixModuleMetadataBudgetMeasuresActualReaderBytes(t *testing.T) {
	for _, operation := range []string{"fields", "facets"} {
		for _, fanout := range []uint32{1, 2} {
			t.Run(fmt.Sprintf("%s/扇出%d", operation, fanout), func(t *testing.T) {
				coord, _, meters, query := metadataMeterFixture(t, false)
				query.Budget.MaxFanout = fanout
				var coverage Coverage
				if operation == "fields" {
					out, err := coord.Fields(context.Background(), query)
					require.NoError(t, err)
					coverage = out.Coverage
				} else {
					out, err := coord.Facets(context.Background(), FacetsQuery{Query: query, Dimensions: []string{"level"}})
					require.NoError(t, err)
					coverage = out.Coverage
				}
				var actual uint64
				var calls int32
				for _, meter := range meters {
					actual += meter.bytes.Load()
					calls += meter.calls.Load()
				}
				t.Logf("整请求额度=%d，Reader实读=%d，Range读取次数=%d", query.Budget.MaxBytes, actual, calls)
				require.Equal(t, uint64(100), actual)
				require.LessOrEqual(t, actual, query.Budget.MaxBytes)
				require.Equal(t, int32(fanout), calls)
				if fanout == 1 {
					require.False(t, coverage.Complete)
					require.Contains(t, coverage.PartialReasons, "BUDGET_EXCEEDED")
				}
			})
		}
	}
}

func TestSixModuleMetadataZeroShareDoesNotDialOrOpenView(t *testing.T) {
	for _, operation := range []string{"fields", "facets"} {
		t.Run(operation, func(t *testing.T) {
			coord, dialer, meters, query := metadataMeterFixture(t, false)
			before := dialer.callCount("w2")
			query.Budget.MaxBytes = 1
			if operation == "fields" {
				_, err := coord.Fields(context.Background(), query)
				require.NoError(t, err)
			} else {
				_, err := coord.Facets(context.Background(), FacetsQuery{Query: query, Dimensions: []string{"level"}})
				require.NoError(t, err)
			}
			require.Equal(t, before, dialer.callCount("w2"))
			require.Zero(t, meters[1].calls.Load())
			require.Equal(t, uint64(1), meters[0].bytes.Load())
		})
	}
}

func TestSixModuleMetadataCancellationReachesSameWorkerContext(t *testing.T) {
	for _, operation := range []string{"fields", "facets"} {
		t.Run(operation, func(t *testing.T) {
			coord, dialer, meters, query := metadataMeterFixture(t, true)
			before := dialer.callCount("w2")
			query.Budget.TimeoutMS = 30
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if operation == "fields" {
				_, _ = coord.Fields(ctx, query)
			} else {
				_, _ = coord.Facets(ctx, FacetsQuery{Query: query, Dimensions: []string{"level"}})
			}
			require.Eventually(t, func() bool { return meters[0].stops.Load() == 1 }, time.Second, time.Millisecond)
			require.Equal(t, before, dialer.callCount("w2"), "取消后不能再拨下一条 Worker")
			require.Zero(t, meters[1].calls.Load())
		})
	}
}

func TestSixModuleEmptyFacetsKeepWireTruncationAndWorstSummary(t *testing.T) {
	coverage := &workerpb.LogCoverage{Complete: true, EnumerationState: workerpb.LogEnumerationState_LOG_ENUMERATION_EXHAUSTED,
		Targets: []*workerpb.LogCoverageTarget{{TargetId: "inst:1", State: workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS}}}
	mapped, err := mapFacetsResponse(&workerpb.LogFacetsResponse{View: &workerpb.LogQueryViewRef{ViewId: "worker-view-w1"}, Coverage: coverage, Truncated: true}, nil)
	require.NoError(t, err)
	require.Empty(t, mapped.Dimensions)
	require.True(t, mapped.Truncated)
	worker := &emptyFacetWorker{fakeWorker: &fakeWorker{workerID: "w1"}, response: mapped}
	dialer := newFakeDialer()
	dialer.clients["w1"] = worker
	coord := New(&fakeTargetResolver{targets: []TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: ReadyOnline}}}, dialer)
	out, err := coord.Facets(context.Background(), FacetsQuery{Query: review05Query(), Dimensions: []string{"level"}})
	require.NoError(t, err)
	require.True(t, out.Truncated)
	require.Equal(t, StatsQPartial, out.Quality.StatsQuality)
	merged := Coverage{Complete: true, EnumerationState: EnumExhausted}
	mergeWorkerCoverage(&merged, &Coverage{Complete: false, EnumerationState: EnumCancelled, PartialReasons: []string{"CANCELLED"}})
	mergeWorkerCoverage(&merged, mapped.Coverage)
	require.False(t, merged.Complete)
	require.Equal(t, EnumCancelled, merged.EnumerationState)
}

func TestSixModuleMetadataMultipleWorkersPreserveTopLevelPartial(t *testing.T) {
	for _, operation := range []string{"fields", "facets"} {
		t.Run(operation, func(t *testing.T) {
			coord, _, meters, query := metadataMeterFixture(t, false)
			meters[1].reasons = []string{workerquery.ReasonRangeUnavailable}
			query.Budget.MaxBytes = 1 << 20
			var coverage Coverage
			if operation == "fields" {
				out, err := coord.Fields(context.Background(), query)
				require.NoError(t, err)
				coverage = out.Coverage
			} else {
				out, err := coord.Facets(context.Background(), FacetsQuery{Query: query, Dimensions: []string{"level"}})
				require.NoError(t, err)
				coverage = out.Coverage
				require.Equal(t, StatsQPartial, out.Quality.StatsQuality)
			}
			require.False(t, coverage.Complete)
			require.Contains(t, coverage.PartialReasons, "RANGE_UNAVAILABLE")
			require.Len(t, coverage.Targets, 2)
			for _, target := range coverage.Targets {
				require.Equal(t, CoverageSuccess, target.State, "不能靠伪造逐目标失败掩盖顶层摘要丢失")
			}
		})
	}
}

// 顶层取消优先于另一 Worker 的截断，不能被处理次序降级回 OPEN。
func TestSixModuleMetadataWorstEnumerationSurvivesTruncation(t *testing.T) {
	coord, dialer, _, query := metadataMeterFixture(t, false)
	for index, workerID := range []string{"w1", "w2"} {
		state := EnumExhausted
		if index == 1 {
			state = EnumCancelled
		}
		response := &WorkerFacetsResponse{ViewID: "empty", Coverage: &Coverage{Complete: index == 0, EnumerationState: state}, Truncated: index == 0,
			Targets: []WorkerTargetResult{successTarget("inst:1", workerID, "20", "1")}}
		stored, _ := coord.GetView(query.ViewID)
		response.ViewID = stored.WorkerViewIDs[workerID]
		dialer.clients[workerID] = &emptyFacetWorker{fakeWorker: &fakeWorker{workerID: workerID}, response: response}
	}
	out, err := coord.Facets(context.Background(), FacetsQuery{Query: query, Dimensions: []string{"level"}})
	require.NoError(t, err)
	require.Equal(t, EnumCancelled, out.Coverage.EnumerationState)
	require.True(t, out.Truncated)
	require.Equal(t, StatsQPartial, out.Quality.StatsQuality)
}

type emptyFacetWorker struct {
	*fakeWorker
	response *WorkerFacetsResponse
}

func (w *emptyFacetWorker) Facets(context.Context, WorkerSearchRequest, []string, uint32) (*WorkerFacetsResponse, error) {
	return w.response, nil
}
