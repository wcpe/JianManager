package router

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
)

// pr61FederationPB 让 HTTP 回归经过真实 proto 适配器而非直接构造 CP 字段。
type pr61FederationPB struct {
	workerpb.WorkerServiceClient
	response *workerpb.LogSearchResponse
	calls    int
}

func (p *pr61FederationPB) LogSearch(ctx context.Context, req *workerpb.LogSearchRequest, opts ...grpc.CallOption) (*workerpb.LogSearchResponse, error) {
	p.calls++
	return p.response, nil
}
func TestPR61FederationGapProtoToHTTPAndCache(t *testing.T) {
	gap := &workerpb.IngestPositionGap{StorageNamespace: "inst:1/stdout", From: 9007199254740993, To: ^uint64(0), ReasonCode: "PERMANENTLY_LOST", Operator: "authenticated-ops", AtUtc: "2026-10-08T00:00:00Z", LogSourceId: "inst:1/old-file", SourceGeneration: "prior-boot"}
	pb := &pr61FederationPB{response: &workerpb.LogSearchResponse{Coverage: &workerpb.LogCoverage{Complete: true, EnumerationState: workerpb.LogEnumerationState_LOG_ENUMERATION_EXHAUSTED, Targets: []*workerpb.LogCoverageTarget{{TargetId: "inst:1", State: workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS}}}, Quality: &workerpb.LogQuality{DuplicateQuality: workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_EXACT, StatsQuality: workerpb.LogStatsQuality_LOG_QUALITY_STATS_EXACT}, Exhausted: true, IngestPositionGaps: []*workerpb.IngestPositionGap{gap}}}
	resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "trusted-node-uuid", Readiness: logcoord.ReadyOnline}}}
	dialer := logcoord.NewTunnelDialer(func(context.Context, string) (workerpb.WorkerServiceClient, error) { return pb, nil })
	r, _ := setupFederationRouter(t, logcoord.New(resolver, dialer))
	token := getAdminToken(t, r)
	path := "/api/v1/logs/federation?instanceId=1&from=2026-10-08T00:00:00Z&to=2026-10-09T00:00:00Z"
	for i := 0; i < 2; i++ {
		w := makeRequest(r, "GET", path, nil, token)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		body := parseJSON(t, w)
		gaps, ok := body["ingest_position_gaps"].([]any)
		require.True(t, ok, "%s", w.Body.String())
		require.Len(t, gaps, 1)
		g := gaps[0].(map[string]any)
		require.Equal(t, "trusted-node-uuid", g["worker_id"])
		require.Equal(t, "9007199254740993", g["from"])
		require.Equal(t, "18446744073709551615", g["to"])
		require.Equal(t, gap.GetStorageNamespace(), g["storage_namespace"])
		require.Equal(t, gap.GetReasonCode(), g["reason_code"])
		require.Equal(t, gap.GetOperator(), g["operator"])
		require.Equal(t, gap.GetAtUtc(), g["at_utc"])
		require.Equal(t, gap.GetLogSourceId(), g["log_source_id"])
		require.Equal(t, gap.GetSourceGeneration(), g["source_generation"])
		require.Equal(t, true, body["coverage"].(map[string]any)["complete"])
		require.Equal(t, "exact", body["quality"].(map[string]any)["duplicate_quality"])
		require.Empty(t, body["notes"])
	}
	require.Equal(t, 1, pb.calls, "空items但有永久缺口也应保留在缓存")
	// 旧 Worker 缺失字段正常返回，不得推断源代次或改变覆盖质量。
	pb.response.IngestPositionGaps = nil
	w := makeRequest(r, "GET", path+"&keyword=different", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	_, exists := body["ingest_position_gaps"]
	require.False(t, exists)
}
func TestPR61FederationTopLevelBudgetToHTTP(t *testing.T) {
	pb := &pr61FederationPB{response: &workerpb.LogSearchResponse{Coverage: &workerpb.LogCoverage{Complete: false, PartialReasons: []string{"BUDGET_EXCEEDED"}, EnumerationState: workerpb.LogEnumerationState_LOG_ENUMERATION_OPEN, Targets: []*workerpb.LogCoverageTarget{{TargetId: "inst:1", State: workerpb.LogCoverageState_LOG_COVERAGE_SUCCESS}}}, Exhausted: true}}
	resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "trusted-node", Readiness: logcoord.ReadyOnline}}}
	r, _ := setupFederationRouter(t, logcoord.New(resolver, logcoord.NewTunnelDialer(func(context.Context, string) (workerpb.WorkerServiceClient, error) { return pb, nil })))
	w := makeRequest(r, "GET", "/api/v1/logs/federation?instanceId=1", nil, getAdminToken(t, r))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.Equal(t, true, body["budget_cut"])
	require.Equal(t, false, body["exhausted"])
	require.Empty(t, body["next_cursor"])
	cov := body["coverage"].(map[string]any)
	require.Equal(t, false, cov["complete"])
	require.Contains(t, cov["partial_reasons"], "BUDGET_EXCEEDED")
}
