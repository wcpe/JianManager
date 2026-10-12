package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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

// CP-8 回归：聚合类同参缓存不得被分页标识打散。
// 同参不同 cursor/page/pageSize 必须落到同一聚合条目，结果相关参数变化仍必须分键。
func TestPR61AggCacheKeyIgnoresPagingParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	keyFor := func(rawQuery string) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		req, err := http.NewRequest("GET", "/api/v1/logs/federation/stats?"+rawQuery, nil)
		require.NoError(t, err)
		c.Request = req
		return fedAggCacheKey(logcoord.Query{
			PrincipalKey: "user:1|role:10|targets:aa", PermissionScope: "platform_admin|role:admin",
			FromUTC: "2026-10-08T00:00:00Z", ToUTC: "2026-10-09T00:00:00Z",
		}, c)
	}

	base := "instanceId=1&groupBy=level&timeBucket=1m"
	require.Equal(t, keyFor(base), keyFor(base+"&cursor=page-2"), "cursor 不得打散聚合条目")
	require.Equal(t, keyFor(base), keyFor(base+"&page=3&pageSize=100"), "page/pageSize 不得打散聚合条目")
	require.Equal(t, keyFor(base), keyFor("timeBucket=1m&groupBy=level&instanceId=1"),
		"参数顺序不同必须归一到同一键")

	// 结果相关参数：任何一个变化都必须分键（不得串味）。
	for _, variant := range []string{
		"instanceId=2&groupBy=level&timeBucket=1m",
		"instanceId=1&groupBy=source&timeBucket=1m",
		"instanceId=1&groupBy=level&timeBucket=5m",
		"instanceId=1&groupBy=level&timeBucket=1m&metricField=bytes",
		"instanceId=1&groupBy=level&timeBucket=1m&keyword=boom",
		"instanceId=1&groupBy=level&timeBucket=1m&level=error",
		"instanceId=1&groupBy=level&timeBucket=1m&onlineOnly=true",
		"instanceId=1&groupBy=level&timeBucket=1m&limit=50",
		"instanceId=1&groupBy=level&timeBucket=1m&viewId=cv_x",
	} {
		require.NotEqual(t, keyFor(base), keyFor(variant), "结果相关参数变化必须分键：%s", variant)
	}
}

// CP-8 行为回归：同参不同 cursor 的 stats 请求只聚合一次（命中回放同一份字节）。
func TestPR61StatsCacheHitsAcrossCursors(t *testing.T) {
	resolver := &fedResolver{targets: []logcoord.TargetInfo{
		{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline},
	}}
	w1 := &fedWorker{targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "src/g:1", "1")}}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": w1}})
	r, _ := setupFederationRouter(t, coord)
	adminToken := getAdminToken(t, r)

	base := "/api/v1/logs/federation/stats?instanceId=1&groupBy=level&timeBucket=1m"
	w := makeRequest(r, "GET", base, nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	first := w.Body.String()
	require.Equal(t, 1, w1.statsN, "首次聚合必须真正扇出")

	w = makeRequest(r, "GET", base+"&cursor=page-2&page=3&pageSize=100", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 1, w1.statsN, "同参不同 cursor 必须命中同一聚合条目")
	require.Equal(t, first, w.Body.String(), "命中必须回放同一份聚合字节")

	w = makeRequest(r, "GET", base+"&timeBucket=5m", nil, adminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 2, w1.statsN, "结果相关参数变化必须重新聚合")
}

// CP-16 回归：热集与预热失败频控必须是 handler 实例状态，不得跨实例串扰。
func TestPR61WarmStateIsPerHandlerInstance(t *testing.T) {
	h1 := &LogFederationHandler{}
	h2 := &LogFederationHandler{}

	h1.noteHotInstance(7)
	require.Equal(t, []uint{7}, h1.hotInstances())
	require.Empty(t, h2.hotInstances(), "另一 handler 不得看到对方的查询热集")

	h2.noteHotInstance(9)
	require.Equal(t, []uint{7}, h1.hotInstances(), "对方的查询不得污染本方热集")
	require.Equal(t, []uint{9}, h2.hotInstances())

	// 零值 handler（未构造）也必须可用：nil map 写入不得 panic。
	var zero LogFederationHandler
	zero.noteHotInstance(11)
	require.Equal(t, []uint{11}, zero.hotInstances())
}
