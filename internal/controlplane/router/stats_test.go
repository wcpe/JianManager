package router

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 对外只读统计契约 HTTP 测试。
//
// 覆盖 spec §6 的验收项：六组接口可调、双鉴权（Agent Token / JWT）、
// 分页与时间窗超限的明确拒绝、响应头、空数组而非 null。

// setupStatsTest 建立带实例与指标的测试环境。
func setupStatsTest(t *testing.T) (db *gorm.DB, r *gin.Engine, adminJWT string, inst *model.Instance, series model.MetricSeries) {
	t.Helper()
	db = setupTestDB(t)
	r = setupTestRouter(db)
	adminJWT = getAdminToken(t, r)
	node := createTestNode(t, db)
	inst = &model.Instance{
		NodeID: node.ID, UUID: "stats-inst-uuid", Name: "stats-inst",
		Type: model.InstanceTypeMinecraftJava, ProcessType: "direct",
		StartCommand: "java -jar server.jar", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)
	series = model.MetricSeries{
		NodeUUID: node.UUID, InstanceID: inst.UUID,
		Scope: model.MetricScopeInstance, MetricKey: model.MetricInstCPUPct, Unit: "pct",
	}
	require.NoError(t, db.Create(&series).Error)
	return db, r, adminJWT, inst, series
}

// statsJSONBody 解析响应体为通用 map。
func statsJSONBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}

// statsIssueAgentToken 经管理面签发一个 Agent Token（V1，含全部实例 ID）。
func statsIssueAgentToken(t *testing.T, r *gin.Engine, adminJWT string, instanceIDs []uint) string {
	t.Helper()
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "stats-reader",
		"scopedInstanceIds": instanceIDs,
	})
	return plain
}

// TestStatsRoutes_Registered 六条 /stats/* 路由全部注册（与 OpenAPI 声明一一对应）。
func TestStatsRoutes_Registered(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	want := map[string]bool{
		"GET /api/v1/stats/overview":  false,
		"GET /api/v1/stats/instances": false,
		"GET /api/v1/stats/nodes":     false,
		"GET /api/v1/stats/logs":      false,
		"GET /api/v1/stats/crashes":   false,
		"GET /api/v1/stats/changes":   false,
	}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, found := range want {
		require.True(t, found, "路由未注册：%s", key)
	}
}

// TestStats_JWTAccess 六组接口都能用用户 JWT 调用并返回契约结构。
func TestStats_JWTAccess(t *testing.T) {
	db, r, jwt, inst, series := setupStatsTest(t)
	base := time.Now().UTC()
	require.NoError(t, db.Create(&model.MetricRollup5m{
		SeriesID: series.ID, BucketTS: base.Add(-10 * time.Minute).Truncate(5 * time.Minute), Avg: 42.5, Count: 1,
	}).Error)
	require.NoError(t, db.Create(&model.LogEntry{
		Source: model.LogSourceInstance, Level: model.LogLevelInfo, InstanceID: inst.ID,
		NodeID: inst.NodeID, Stream: "stdout", Message: "hello", Time: base.Add(-time.Minute),
	}).Error)
	require.NoError(t, db.Create(&model.InstanceCrashStat{
		InstanceID: inst.ID, BucketDay: base.Format("2006-01-02"), RootCause: "OOM",
		Signature: "sig", Count: 3,
	}).Error)
	require.NoError(t, db.Create(&model.InstanceRollingOp{
		Action: "restart", State: model.RollingStateRunning, Requested: 5, Succeeded: 2,
	}).Error)

	// overview
	w := makeRequest(r, "GET", "/api/v1/stats/overview", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	ov := statsJSONBody(t, w.Body.Bytes())
	require.Contains(t, ov, "nodes")
	require.Contains(t, ov, "instances")
	require.Contains(t, ov, "resources")
	require.Contains(t, ov, "probes")
	// 边界检查：不得出现任何业务语义字段。
	for _, forbidden := range []string{"domain", "economy", "activity", "balance", "revenue"} {
		require.NotContains(t, ov, forbidden, "overview 不得含业务语义字段 %s", forbidden)
	}

	// instances
	w = makeRequest(r, "GET", "/api/v1/stats/instances?resolution=5m&page=1&pageSize=100", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var seriesResp service.StatsMetricSeriesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &seriesResp))
	require.Len(t, seriesResp.Series, 1)
	require.Equal(t, inst.ID, seriesResp.Series[0].InstanceID)
	require.Equal(t, "cpu_pct", seriesResp.Series[0].Metric)
	require.Len(t, seriesResp.Series[0].Points, 1)
	require.InDelta(t, 42.5, seriesResp.Series[0].Points[0].V, 1e-9)
	// 响应头（spec §4.5）。
	require.Equal(t, "1", w.Header().Get("X-Total-Count"))
	require.Equal(t, "1", w.Header().Get("X-Page"))
	require.Equal(t, "100", w.Header().Get("X-Page-Size"))

	// nodes（无节点级序列 → 空数组）
	w = makeRequest(r, "GET", "/api/v1/stats/nodes?resolution=5m", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var nodeResp service.StatsMetricSeriesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &nodeResp))
	require.NotNil(t, nodeResp.Series, "series 必须是 [] 而非 null")
	require.Contains(t, w.Body.String(), `"series":[]`)

	// logs
	w = makeRequest(r, "GET", "/api/v1/stats/logs?page=1&pageSize=50", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var logResp service.StatsLogQueryResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &logResp))
	require.Len(t, logResp.Entries, 1)
	require.Equal(t, "INFO", logResp.Entries[0].Level)
	require.Equal(t, "hello", logResp.Entries[0].Message)

	// crashes
	w = makeRequest(r, "GET", "/api/v1/stats/crashes?days=30", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var crashResp service.StatsCrashTrendResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &crashResp))
	require.Len(t, crashResp.ByDay, 1)
	require.Equal(t, 3, crashResp.ByDay[0].Count)
	require.Len(t, crashResp.ByReason, 1)
	require.Equal(t, "OOM", crashResp.ByReason[0].Reason)

	// changes
	w = makeRequest(r, "GET", "/api/v1/stats/changes?page=1&pageSize=50", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var changeResp service.StatsChangeListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &changeResp))
	require.Len(t, changeResp.Changes, 1)
	require.Equal(t, "rolling", changeResp.Changes[0].Status)
	require.Equal(t, 5, changeResp.Changes[0].TotalTargets)
}

// TestStats_AgentTokenAccess 六组接口都能用 Agent Token 调用（spec §6 验收 1）。
func TestStats_AgentTokenAccess(t *testing.T) {
	_, r, jwt, inst, _ := setupStatsTest(t)
	token := statsIssueAgentToken(t, r, jwt, []uint{inst.ID})
	require.Contains(t, token, "jmat_")

	for _, path := range []string{
		"/api/v1/stats/overview",
		"/api/v1/stats/instances?resolution=5m",
		"/api/v1/stats/nodes?resolution=5m",
		"/api/v1/stats/logs",
		"/api/v1/stats/crashes?days=7",
		"/api/v1/stats/changes",
	} {
		w := makeRequest(r, "GET", path, nil, token)
		require.Equal(t, http.StatusOK, w.Code, "path=%s body=%s", path, w.Body.String())
	}
}

// TestStats_AgentTokenScopeConverges Agent Token 只能看到 scope 内实例的数据。
func TestStats_AgentTokenScopeConverges(t *testing.T) {
	db, r, jwt, instA, seriesA := setupStatsTest(t)
	// 第二个实例（不在 token scope 内）。
	instB := &model.Instance{
		NodeID: instA.NodeID, UUID: "stats-inst-b", Name: "stats-inst-b",
		Type: model.InstanceTypeMinecraftJava, ProcessType: "direct",
		StartCommand: "java", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(instB).Error)
	seriesB := model.MetricSeries{
		NodeUUID: seriesA.NodeUUID, InstanceID: instB.UUID,
		Scope: model.MetricScopeInstance, MetricKey: model.MetricInstCPUPct, Unit: "pct",
	}
	require.NoError(t, db.Create(&seriesB).Error)

	base := time.Now().UTC()
	for _, sid := range []uint{seriesA.ID, seriesB.ID} {
		require.NoError(t, db.Create(&model.MetricRollup5m{
			SeriesID: sid, BucketTS: base.Add(-5 * time.Minute).Truncate(5 * time.Minute), Avg: 10, Count: 1,
		}).Error)
	}

	token := statsIssueAgentToken(t, r, jwt, []uint{instA.ID})
	w := makeRequest(r, "GET", "/api/v1/stats/instances?resolution=5m", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp service.StatsMetricSeriesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Series, 1, "只应看到 scope 内实例的序列")
	require.Equal(t, instA.ID, resp.Series[0].InstanceID)

	// 显式请求 scope 外实例：视为不可见（返回空，不泄露存在性）。
	w = makeRequest(r, "GET", "/api/v1/stats/instances?resolution=5m&instanceIds=999999", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out service.StatsMetricSeriesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Empty(t, out.Series)
}

// TestStats_PageSizeExceeded 守 spec §4.2：pageSize > 200 返回 400 PAGE_SIZE_EXCEEDED + maxPageSize。
func TestStats_PageSizeExceeded(t *testing.T) {
	_, r, jwt, _, _ := setupStatsTest(t)

	for _, path := range []string{
		"/api/v1/stats/instances?pageSize=201",
		"/api/v1/stats/nodes?pageSize=201",
		"/api/v1/stats/logs?pageSize=201",
		"/api/v1/stats/changes?pageSize=201",
	} {
		w := makeRequest(r, "GET", path, nil, jwt)
		require.Equal(t, http.StatusBadRequest, w.Code, "path=%s body=%s", path, w.Body.String())
		body := statsJSONBody(t, w.Body.Bytes())
		require.Equal(t, "PAGE_SIZE_EXCEEDED", body["code"], "path=%s", path)
		require.EqualValues(t, 200, body["maxPageSize"], "path=%s", path)
	}

	// 边界值 200 必须放行（不是 >=）。
	w := makeRequest(r, "GET", "/api/v1/stats/instances?pageSize=200", nil, jwt)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestStats_TimeRangeExceeded 守 spec §4.3：超时间窗返回 400 TIME_RANGE_EXCEEDED + maxRange。
func TestStats_TimeRangeExceeded(t *testing.T) {
	_, r, jwt, _, _ := setupStatsTest(t)
	now := time.Now().UTC()

	// 指标：30 天上限。
	from := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	for _, path := range []string{
		"/api/v1/stats/instances?from=" + from,
		"/api/v1/stats/nodes?from=" + from,
	} {
		w := makeRequest(r, "GET", path, nil, jwt)
		require.Equal(t, http.StatusBadRequest, w.Code, "path=%s body=%s", path, w.Body.String())
		body := statsJSONBody(t, w.Body.Bytes())
		require.Equal(t, "TIME_RANGE_EXCEEDED", body["code"], "path=%s", path)
		require.Equal(t, "720h", body["maxRange"], "path=%s", path)
	}

	// 日志：7 天上限。
	logFrom := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	w := makeRequest(r, "GET", "/api/v1/stats/logs?from="+logFrom, nil, jwt)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body := statsJSONBody(t, w.Body.Bytes())
	require.Equal(t, "TIME_RANGE_EXCEEDED", body["code"])
	require.Equal(t, "168h", body["maxRange"])

	// 崩溃：365 天上限。
	w = makeRequest(r, "GET", "/api/v1/stats/crashes?days=400", nil, jwt)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body = statsJSONBody(t, w.Body.Bytes())
	require.Equal(t, "TIME_RANGE_EXCEEDED", body["code"])
	require.NotEmpty(t, body["maxRange"])

	// raw 档超过 1 小时：同样明确拒绝（不静默降级到预聚合档）。
	rawFrom := now.Add(-2 * time.Hour).Format(time.RFC3339)
	w = makeRequest(r, "GET", "/api/v1/stats/instances?resolution=raw&from="+rawFrom, nil, jwt)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body = statsJSONBody(t, w.Body.Bytes())
	require.Equal(t, "TIME_RANGE_EXCEEDED", body["code"])
	require.Equal(t, "1h", body["maxRange"])
}

// TestStats_Unauthorized 无凭据必须 401，不得放行任何统计数据。
func TestStats_Unauthorized(t *testing.T) {
	_, r, _, _, _ := setupStatsTest(t)
	for _, path := range []string{
		"/api/v1/stats/overview",
		"/api/v1/stats/instances",
		"/api/v1/stats/nodes",
		"/api/v1/stats/logs",
		"/api/v1/stats/crashes",
		"/api/v1/stats/changes",
	} {
		w := makeRequest(r, "GET", path, nil, "")
		require.Equal(t, http.StatusUnauthorized, w.Code, "path=%s body=%s", path, w.Body.String())
	}
}

// TestStats_InvalidArguments 形状错误返回 400 INVALID_ARGUMENT。
func TestStats_InvalidArguments(t *testing.T) {
	_, r, jwt, _, _ := setupStatsTest(t)
	cases := []string{
		"/api/v1/stats/instances?resolution=bogus",
		"/api/v1/stats/instances?instanceIds=abc",
		"/api/v1/stats/instances?from=not-a-time",
		"/api/v1/stats/logs?level=TRACE",
		"/api/v1/stats/crashes?days=abc",
		"/api/v1/stats/changes?status=nope",
	}
	for _, path := range cases {
		w := makeRequest(r, "GET", path, nil, jwt)
		require.Equal(t, http.StatusBadRequest, w.Code, "path=%s body=%s", path, w.Body.String())
		body := statsJSONBody(t, w.Body.Bytes())
		require.Equal(t, "INVALID_ARGUMENT", body["code"], "path=%s", path)
	}
}
