package logcoord

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakePBClient 可编程 workerpb 客户端（仅覆盖 Log* RPC）。
type fakePBClient struct {
	workerpb.WorkerServiceClient
	searchResp *workerpb.LogSearchResponse
	searchErr  error
	statsResp  *workerpb.LogStatsResponse
	statsErr   error
	facetsResp *workerpb.LogFacetsResponse
	facetsErr  error
	lastSearch *workerpb.LogSearchRequest
}

func (f *fakePBClient) LogSearch(_ context.Context, in *workerpb.LogSearchRequest, _ ...grpc.CallOption) (*workerpb.LogSearchResponse, error) {
	f.lastSearch = in
	return f.searchResp, f.searchErr
}

func (f *fakePBClient) LogStats(_ context.Context, _ *workerpb.LogStatsRequest, _ ...grpc.CallOption) (*workerpb.LogStatsResponse, error) {
	return f.statsResp, f.statsErr
}

func (f *fakePBClient) LogFacets(_ context.Context, _ *workerpb.LogFacetsRequest, _ ...grpc.CallOption) (*workerpb.LogFacetsResponse, error) {
	return f.facetsResp, f.facetsErr
}

func TestLogCoord_ParseDuplicateQuality_CaseInsensitive(t *testing.T) {
	assert.Equal(t, DupExact, ParseDuplicateQuality("exact"))
	assert.Equal(t, DupExact, ParseDuplicateQuality("EXACT"))
	assert.Equal(t, DupExact, ParseDuplicateQuality("Exact"))
	assert.Equal(t, DupUnresolved, ParseDuplicateQuality("unresolved"))
	assert.Equal(t, DupUnresolved, ParseDuplicateQuality("UNRESOLVED"))
	assert.Equal(t, DupConflict, ParseDuplicateQuality("Conflict"))
	assert.Equal(t, DupUnspecified, ParseDuplicateQuality(""))
	assert.Equal(t, DupUnspecified, ParseDuplicateQuality("nope"))
}

func TestParseStatsQuality_CaseInsensitive(t *testing.T) {
	assert.Equal(t, StatsQExact, ParseStatsQuality("Exact"))
	assert.Equal(t, StatsQPartial, ParseStatsQuality("PARTIAL"))
	assert.Equal(t, StatsQUnavailable, ParseStatsQuality("unavailable"))
}

func TestLogCoord_QualityBlocksExport_Unresolved(t *testing.T) {
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupUnresolved, StatsQuality: StatsQExact}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: "UNRESOLVED", StatsQuality: StatsQExact}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: StatsQPartial}))
	assert.True(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: "Unavailable"}))
	assert.False(t, QualityBlocksExport(Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}))
	assert.False(t, QualityBlocksExport(Quality{DuplicateQuality: "exact", StatsQuality: "exact"}))
}

func TestCoverageStateFromProto(t *testing.T) {
	assert.Equal(t, CoverageSuccess, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS))
	assert.Equal(t, CoveragePartial, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL))
	assert.Equal(t, CoverageOffline, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_OFFLINE))
	assert.Equal(t, CoverageUnspecified, CoverageStateFromProto(workerpb.LogCoverageState_LOG_COVERAGE_STATE_UNSPECIFIED))
}

func TestLogCoord_WorkerClientAdapter_SearchMapsProto(t *testing.T) {
	pb := &fakePBClient{
		searchResp: &workerpb.LogSearchResponse{
			RequestId: "r1",
			View: &workerpb.LogQueryViewRef{
				ViewId:       "cv_1",
				OrderVersion: OrderVersion,
			},
			Coverage: &workerpb.LogCoverage{
				Complete: false,
				Targets: []*workerpb.LogCoverageTarget{
					{
						TargetId:          "inst:1",
						State:             workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS,
						ClosedVisibleSeq:  "src/g:100",
						CatalogGeneration: "7",
					},
					{
						TargetId: "inst:2",
						State:    workerpb.LogCoverageState_LOG_COVERAGE_OFFLINE,
						Reasons:  []string{"dial_failed"},
					},
				},
			},
			Items: []*workerpb.LogEvent{
				{
					EventId:          "e1",
					LogSourceId:      "src",
					SourceGeneration: "g",
					RecordStart:      "10",
					RecordEnd:        "20",
					EventTimeUtc:     "2026-09-20T12:00:00Z",
					Level:            "INFO",
					Message:          "hello",
				},
			},
			Exhausted: true,
		},
	}
	adapter := NewWorkerClientAdapter(pb)
	resp, err := adapter.Search(context.Background(), WorkerSearchRequest{
		TargetIDs:    []string{"inst:1", "inst:2"},
		FromUTC:      "2026-09-20T00:00:00Z",
		ToUTC:        "2026-09-21T00:00:00Z",
		ViewID:       "cv_1",
		OrderVersion: OrderVersion,
		Budget:       QueryBudget{Limit: 50},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "cv_1", resp.ViewID)
	assert.True(t, resp.Exhausted)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "e1", resp.Items[0].EventID)
	assert.Equal(t, uint64(10), resp.Items[0].RecordStart)
	require.Len(t, resp.Targets, 2)
	assert.Equal(t, CoverageSuccess, resp.Targets[0].State)
	assert.Equal(t, "src/g:100", resp.Targets[0].ClosedVisibleSeq)
	assert.Equal(t, CoverageOffline, resp.Targets[1].State)

	// 请求侧映射
	require.NotNil(t, pb.lastSearch)
	require.NotNil(t, pb.lastSearch.GetQuery())
	assert.Equal(t, []string{"inst:1", "inst:2"}, pb.lastSearch.GetQuery().GetAuthorizedTargets().GetTargetIds())
	assert.Equal(t, OrderVersion, pb.lastSearch.GetQuery().GetView().GetOrderVersion())
	assert.Equal(t, uint32(50), pb.lastSearch.GetQuery().GetBudget().GetLimit())
}

func TestWorkerClientAdapter_Unimplemented(t *testing.T) {
	pb := &fakePBClient{searchErr: status.Error(codes.Unimplemented, "method LogSearch not implemented")}
	adapter := NewWorkerClientAdapter(pb)
	resp, err := adapter.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"t1"}})
	require.NoError(t, err)
	assert.True(t, resp.Unsupported)
}

func TestWorkerClientAdapter_NextCursorIsPaginationNotTruncation(t *testing.T) {
	pb := &fakePBClient{searchResp: &workerpb.LogSearchResponse{
		View: &workerpb.LogQueryViewRef{ViewId: "wv-1"},
		Coverage: &workerpb.LogCoverage{Complete: true, Targets: []*workerpb.LogCoverageTarget{{
			TargetId: "node:1", State: workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS,
		}}},
		NextCursor: "next", Exhausted: false,
	}}
	resp, err := NewWorkerClientAdapter(pb).Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"node:1"}})
	require.NoError(t, err)
	require.False(t, resp.Truncated)
	require.False(t, resp.Exhausted)
	require.Equal(t, "next", resp.NextCursor)
}

func TestWorkerClientAdapter_ErrorMapping(t *testing.T) {
	pb := &fakePBClient{
		searchResp: &workerpb.LogSearchResponse{
			Error: &workerpb.LogError{
				Code:    workerpb.LogErrorCode_LOG_ERR_DUPLICATE_UNRESOLVED,
				Message: "duplicate unresolved",
			},
			Coverage: &workerpb.LogCoverage{
				Targets: []*workerpb.LogCoverageTarget{{
					TargetId: "t1",
					State:    workerpb.LogCoverageState_LOG_COVERAGE_PARTIAL,
				}},
			},
		},
	}
	adapter := NewWorkerClientAdapter(pb)
	resp, err := adapter.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"t1"}})
	require.NoError(t, err)
	assert.Equal(t, "duplicate unresolved", resp.Error)
	require.Len(t, resp.Targets, 1)
	assert.Equal(t, CoveragePartial, resp.Targets[0].State)
}

func TestTunnelDialer_NotWired(t *testing.T) {
	d := NewTunnelDialer(nil)
	_, err := d.Dial(context.Background(), "w1")
	require.Error(t, err)
}

func TestTunnelDialer_Wired(t *testing.T) {
	pb := &fakePBClient{searchResp: &workerpb.LogSearchResponse{Exhausted: true}}
	d := NewTunnelDialer(func(_ context.Context, workerID string) (workerpb.WorkerServiceClient, error) {
		if workerID != "w1" {
			return nil, errors.New("unknown")
		}
		return pb, nil
	})
	cli, err := d.Dial(context.Background(), "w1")
	require.NoError(t, err)
	resp, err := cli.Search(context.Background(), WorkerSearchRequest{TargetIDs: []string{"t"}})
	require.NoError(t, err)
	assert.True(t, resp.Exhausted)
}

func TestLogCoord_SearchPersistsClosedVisibleSeqOnView(t *testing.T) {
	t1, t2 := "inst-a", "inst-b"
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: t1, WorkerID: "w1", Readiness: ReadyOnline},
		{ID: t2, WorkerID: "w2", Readiness: ReadyOffline, HistoricalHolder: true},
	}}
	w1 := &fakeWorker{
		workerID: "w1",
		searchItems: []Event{
			mkEvent("e1", "src-a", "g1", 100, "2026-09-20T12:00:00Z", "INFO", "ok"),
		},
		searchTarget: []WorkerTargetResult{
			successTarget(t1, "w1", "src-a/g1:100", "7"),
		},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	dialer.fail["w2"] = errors.New("offline")

	coord := New(resolver, dialer)
	resp, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{t1, t2},
		Budget:              QueryBudget{Limit: 50},
	})
	require.NoError(t, err)

	// View 必须固化 coverage 上的 closed_visible_seq 向量
	require.NotNil(t, resp.View.ClosedVisibleSeq)
	assert.Equal(t, "src-a/g1:100", resp.View.ClosedVisibleSeq[t1])
	// 存储视图同步
	stored, ok := coord.GetView(resp.View.ViewID)
	require.True(t, ok)
	assert.Equal(t, "src-a/g1:100", stored.ClosedVisibleSeq[t1])
}
