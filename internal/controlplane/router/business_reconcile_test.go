package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// reconcileRequest 是对账接口的最小请求体（domain/action 由调用方指定，JM 不预设）。
func reconcileRequest(instanceIDs []uint, action, requestID string) map[string]any {
	return map[string]any{
		"domain":    "activity",
		"action":    action,
		"payload":   map[string]any{"activityId": "spring-2026"},
		"target":    map[string]any{"instanceIds": instanceIDs},
		"requestId": requestID,
	}
}

// reconcileResults 取响应里的 results 并把每项转成 map（便于逐项断言）。
func reconcileResults(t *testing.T, res map[string]any) []map[string]any {
	t.Helper()
	raw, ok := res["results"].([]any)
	require.Truef(t, ok, "响应缺 results 数组: %+v", res)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		require.Truef(t, ok, "results 项结构异常: %+v", item)
		out = append(out, m)
	}
	return out
}

// TestBusinessReconcile_Route_Registered 路由已注册且不需要写权限：
// 只要目标为空就返回 400 TARGET_EMPTY，而不是 404/403。
func TestBusinessReconcile_Route_Registered(t *testing.T) {
	r, _, admin, _, _ := newBatchRouterFixture(t, 0)

	w := makeRequest(r, "POST", "/api/v1/business/reconcile", map[string]any{
		"domain":    "activity",
		"action":    "snapshot",
		"target":    map[string]any{"instanceIds": []uint{999999}},
		"requestId": "11111111-1111-4111-8111-111111111111",
	}, admin)
	require.Equalf(t, http.StatusBadRequest, w.Code, "目标为空应 400: %s", w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, "TARGET_EMPTY", res["code"])
}

// TestBusinessReconcile_Route_MissingTarget target 缺失 → 400 INVALID_REQUEST。
func TestBusinessReconcile_Route_MissingTarget(t *testing.T) {
	r, _, admin, _, _ := newBatchRouterFixture(t, 0)

	w := makeRequest(r, "POST", "/api/v1/business/reconcile", map[string]any{
		"domain":    "activity",
		"action":    "snapshot",
		"requestId": "22222222-2222-4222-8222-222222222222",
	}, admin)
	require.Equalf(t, http.StatusBadRequest, w.Code, "缺 target 应 400: %s", w.Body.String())
	assert.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["code"])
}

// TestBusinessReconcile_Route_MissingRequestID requestId 缺失 → 400 INVALID_REQUEST。
func TestBusinessReconcile_Route_MissingRequestID(t *testing.T) {
	r, _, admin, _, ids := newBatchRouterFixture(t, 1)

	body := reconcileRequest(ids, "snapshot", "")
	w := makeRequest(r, "POST", "/api/v1/business/reconcile", body, admin)
	require.Equalf(t, http.StatusBadRequest, w.Code, "缺 requestId 应 400: %s", w.Body.String())
	assert.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["code"])
}

// TestBusinessReconcile_Route_OfflineInstance_ExplicitError
// 端到端（无 Worker 连接）：实例被判 offline，逐实例给出明确错误，整体仍 200。
func TestBusinessReconcile_Route_OfflineInstance_ExplicitError(t *testing.T) {
	r, db, admin, _, ids := newBatchRouterFixture(t, 2)
	// 让第二台离线。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", ids[1]).
		Update("status", model.InstanceStatusStopped).Error)

	w := makeRequest(r, "POST", "/api/v1/business/reconcile",
		reconcileRequest(ids, "snapshot", "33333333-3333-4333-8333-333333333333"), admin)
	require.Equalf(t, http.StatusOK, w.Code, "部分失败不应整体失败: %s", w.Body.String())

	res := parseJSON(t, w)
	assert.Equal(t, "33333333-3333-4333-8333-333333333333", res["requestId"])
	results := reconcileResults(t, res)
	require.Len(t, results, 2)
	// 第一台无 Worker 连接 → 节点未连（offline 口径）；第二台停止态 → INSTANCE_OFFLINE。
	for _, item := range results {
		assert.Equal(t, false, item["success"])
		assert.Contains(t, item["error"], "INSTANCE_OFFLINE")
	}
}

// TestBusinessReconcile_Route_SyncReturnsResults 同步模式返回逐实例结果集（无 Worker 连接时全失败，
// 但结构必须完整：instanceId / success / durationMs 都在）。
func TestBusinessReconcile_Route_SyncReturnsResults(t *testing.T) {
	r, _, admin, _, ids := newBatchRouterFixture(t, 2)

	w := makeRequest(r, "POST", "/api/v1/business/reconcile",
		reconcileRequest(ids, "snapshot", "44444444-4444-4444-8444-444444444444"), admin)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	results := reconcileResults(t, parseJSON(t, w))
	require.Len(t, results, 2)
	for i, item := range results {
		assert.Equal(t, float64(ids[i]), item["instanceId"])
		assert.NotNil(t, item["success"])
		assert.NotNil(t, item["durationMs"])
	}
}

// TestBusinessReconcile_Route_StreamPerInstanceFrames
// 流式模式：逐实例 result 帧 + 一个 done 帧（带完整结果集）。
func TestBusinessReconcile_Route_StreamPerInstanceFrames(t *testing.T) {
	r, _, admin, _, ids := newBatchRouterFixture(t, 3)

	body, err := json.Marshal(reconcileRequest(ids, "snapshot", "55555555-5555-4555-8555-555555555555"))
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/api/v1/business/reconcile", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+admin)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equalf(t, http.StatusOK, w.Code, "流式应 200: %s", w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")

	events := parseSSEEvents(t, w.Body.String())
	resultFrames := 0
	var doneFrame map[string]any
	for _, ev := range events {
		switch ev.event {
		case "result":
			resultFrames++
		case "done":
			doneFrame = ev.data
		case "error":
			t.Fatalf("不应出现 error 帧: %+v", ev.data)
		}
	}
	assert.Equal(t, 3, resultFrames, "每个实例应各推一帧")
	require.NotNil(t, doneFrame, "必须有 done 帧")
	assert.Equal(t, "55555555-5555-4555-8555-555555555555", doneFrame["requestId"])
	results, ok := doneFrame["results"].([]any)
	require.True(t, ok)
	assert.Len(t, results, 3, "done 帧应带完整结果集")
}

// TestBusinessReconcile_Route_ScopeConverged 越权目标不进入结果（跨组隔离下沉在解析层）。
func TestBusinessReconcile_Route_ScopeConverged(t *testing.T) {
	r, _, admin, _, ids := newBatchRouterFixture(t, 2)

	// 平台管理员不受限，这里只验证正常路径能返回全部目标；
	// 越权收敛的服务层用例见 service 包的 ScopeConverged。
	w := makeRequest(r, "POST", "/api/v1/business/reconcile",
		reconcileRequest(ids, "snapshot", "66666666-6666-4666-8666-666666666666"), admin)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Len(t, reconcileResults(t, parseJSON(t, w)), 2)
}

// TestBusinessReconcile_Route_PermissionDenied 非管理员且无实例权限 → 403。
func TestBusinessReconcile_Route_PermissionDenied(t *testing.T) {
	r, _, _, _, ids := newBatchRouterFixture(t, 1)

	// 无 token：应被 JWT 中间件拦成 401。
	w := makeRequest(r, "POST", "/api/v1/business/reconcile",
		reconcileRequest(ids, "snapshot", "77777777-7777-4777-8777-777777777777"), "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

// TestBusinessReconcile_Route_IdempotentReplay 同 requestId 重放：第二次响应标记 replay。
func TestBusinessReconcile_Route_IdempotentReplay(t *testing.T) {
	r, _, admin, _, ids := newBatchRouterFixture(t, 1)
	body := reconcileRequest(ids, "snapshot", "88888888-8888-4888-8888-888888888888")

	first := makeRequest(r, "POST", "/api/v1/business/reconcile", body, admin)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.NotEqual(t, true, parseJSON(t, first)["idempotentReplay"])

	second := makeRequest(r, "POST", "/api/v1/business/reconcile", body, admin)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Equal(t, true, parseJSON(t, second)["idempotentReplay"])
}

// TestBusinessReconcile_Route_IdempotencyConflict 同 requestId 不同 payload → 409。
func TestBusinessReconcile_Route_IdempotencyConflict(t *testing.T) {
	r, _, admin, _, ids := newBatchRouterFixture(t, 1)
	body := reconcileRequest(ids, "snapshot", "99999999-9999-4999-8999-999999999999")

	first := makeRequest(r, "POST", "/api/v1/business/reconcile", body, admin)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	body["payload"] = map[string]any{"activityId": "another"}
	second := makeRequest(r, "POST", "/api/v1/business/reconcile", body, admin)
	require.Equalf(t, http.StatusConflict, second.Code, "payload 冲突应 409: %s", second.Body.String())
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSED", parseJSON(t, second)["code"])
}

// TestBusinessReconcile_Route_InvalidBody 非法 JSON → 400。
func TestBusinessReconcile_Route_InvalidBody(t *testing.T) {
	r, _, admin, _, _ := newBatchRouterFixture(t, 0)

	req := httptest.NewRequest("POST", "/api/v1/business/reconcile", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+admin)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["code"])
}
