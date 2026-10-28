package router

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// mkBatchRouterGroup 通过平台管理员 API 建实例组织分组（FR-165 分组树），返回分组 ID。
func mkBatchRouterGroup(t *testing.T, r *gin.Engine, admin string, name string, parentID *uint) uint {
	t.Helper()
	body := map[string]any{"name": name}
	if parentID != nil {
		body["parentId"] = *parentID
	}
	w := makeRequest(r, "POST", "/api/v1/instance-groups", body, admin)
	require.Equalf(t, http.StatusCreated, w.Code, "创建组织分组失败: %s", w.Body.String())
	return uint(parseJSON(t, w)["id"].(float64))
}

// addInstancesToOrgGroup 把实例加入组织分组（FR-165）。
func addInstancesToOrgGroup(t *testing.T, r *gin.Engine, admin string, groupID uint, instanceIDs ...uint) {
	t.Helper()
	w := makeRequest(r, "POST", "/api/v1/instance-groups/"+itoa(groupID)+"/members",
		map[string]any{"instanceIds": instanceIDs}, admin)
	require.Equalf(t, http.StatusOK, w.Code, "加入组织分组失败: %s", w.Body.String())
}

// newBatchRouterFixture 建路由 + 管理员 + 一个组织分组，并在分组里放 n 个实例。
func newBatchRouterFixture(t *testing.T, n int) (*gin.Engine, *gorm.DB, string, uint, []uint) {
	t.Helper()
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	orgGroup := mkBatchRouterGroup(t, r, admin, "cluster-main", nil)

	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		g := createGroupViaAPI(t, r, admin, "rbac-g"+itoa(uint(i)))
		ids = append(ids, createInstanceViaAPI(t, r, admin, node.ID, g))
	}
	if n > 0 {
		addInstancesToOrgGroup(t, r, admin, orgGroup, ids...)
		// API 创建的实例默认 STOPPED；本文件的用例大多假定实例可下发，
		// 需要离线场景的用例会自己把某台改回 STOPPED。
		require.NoError(t, db.Model(&model.Instance{}).Where("id IN ?", ids).
			Update("status", model.InstanceStatusRunning).Error)
	}
	return r, db, admin, orgGroup, ids
}

// TestBusinessBatchPreview_LowRisk 低风险预览：返回计数与 risk，不签发 token。
func TestBusinessBatchPreview_LowRisk(t *testing.T) {
	r, _, admin, orgGroup, ids := newBatchRouterFixture(t, 3)

	w := makeRequest(r, "POST", "/api/v1/business/batch/preview", map[string]any{
		"target":     map[string]any{"groupIds": []uint{orgGroup}, "includeDescendants": true},
		"actionRisk": "WRITE",
	}, admin)
	require.Equalf(t, http.StatusOK, w.Code, "预览应 200: %s", w.Body.String())
	res := parseJSON(t, w)

	resolved := res["resolved"].(map[string]any)
	assert.Equal(t, float64(3), resolved["instanceCount"])
	assert.Equal(t, false, resolved["truncated"])
	assert.Len(t, resolved["instanceIds"], len(ids))

	risk := res["risk"].(map[string]any)
	assert.Equal(t, "low", risk["level"])
	assert.Equal(t, false, risk["requiresConfirmation"])
	assert.NotEmpty(t, risk["reasons"])
	_, hasToken := res["confirmToken"]
	assert.False(t, hasToken, "低风险不应签发确认令牌")

	groups := res["groups"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, "cluster-main", groups[0].(map[string]any)["name"])
	assert.Equal(t, "cluster-main", groups[0].(map[string]any)["path"])
}

// TestBusinessBatchPreview_TargetEmpty 空分组预览返回 TARGET_EMPTY（400）。
func TestBusinessBatchPreview_TargetEmpty(t *testing.T) {
	r, _, admin, orgGroup, _ := newBatchRouterFixture(t, 0)
	w := makeRequest(r, "POST", "/api/v1/business/batch/preview", map[string]any{
		"target": map[string]any{"groupIds": []uint{orgGroup}},
	}, admin)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Equal(t, "TARGET_EMPTY", parseJSON(t, w)["code"])
}

// TestBusinessBatchPreview_DestructiveGivesToken DESTRUCTIVE 动作预览 → critical + 签发 token。
func TestBusinessBatchPreview_DestructiveGivesToken(t *testing.T) {
	r, _, admin, orgGroup, _ := newBatchRouterFixture(t, 1)
	w := makeRequest(r, "POST", "/api/v1/business/batch/preview", map[string]any{
		"target":     map[string]any{"groupIds": []uint{orgGroup}},
		"actionRisk": "DESTRUCTIVE",
	}, admin)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	res := parseJSON(t, w)
	risk := res["risk"].(map[string]any)
	assert.Equal(t, "critical", risk["level"])
	assert.Equal(t, true, risk["requiresReason"])
	token, _ := res["confirmToken"].(string)
	assert.True(t, strings.HasPrefix(token, "bpt_"), "应签发带前缀的确认令牌")
	assert.NotEmpty(t, res["expiresAt"])
}

// TestBusinessBatch_Preview_ScopeHidesInstances 无任何可访问实例的成员预览他人分组 →
// TARGET_EMPTY（存在性隐藏：既不越权操作，也不泄露该分组有没有实例）。
func TestBusinessBatch_Preview_ScopeHidesInstances(t *testing.T) {
	r, _, _, orgGroup, _ := newBatchRouterFixture(t, 3)
	member := getMemberToken(t, r, "batch-nobody", "password123")
	w := makeRequest(r, "POST", "/api/v1/business/batch/preview", map[string]any{
		"target": map[string]any{"groupIds": []uint{orgGroup}},
	}, member)
	require.Equalf(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Equal(t, "TARGET_EMPTY", parseJSON(t, w)["code"])
}

// TestBusinessBatch_ExecuteForbidden 批量执行需 instance:business:write，普通成员被拒（403）。
func TestBusinessBatch_ExecuteForbidden(t *testing.T) {
	r, _, _, orgGroup, _ := newBatchRouterFixture(t, 1)
	member := getMemberToken(t, r, "batch-nobody", "password123")
	w := makeRequest(r, "POST", "/api/v1/business/batch", map[string]any{
		"domain": "activity", "action": "start", "requestId": "req-forbidden",
		"target": map[string]any{"groupIds": []uint{orgGroup}}, "actionRisk": "WRITE",
	}, member)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, int64(0), countAudit(t, setupTestDB(t), "business.batch"), "被拦下的请求不该留执行审计")
}

// TestBusinessBatch_ExecuteSync 同步执行：节点未连 → 全部 offline，仍 200。
func TestBusinessBatch_ExecuteSync(t *testing.T) {
	r, db, admin, orgGroup, ids := newBatchRouterFixture(t, 3)
	// 刻意留一台离线：验证「含离线实例」不影响整体成败，只体现在 offline 计数里。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", ids[0]).
		Update("status", model.InstanceStatusStopped).Error)
	w := makeRequest(r, "POST", "/api/v1/business/batch", map[string]any{
		"domain":     "activity",
		"action":     "start",
		"payload":    map[string]any{"activityId": "spring"},
		"requestId":  "req-sync-1",
		"target":     map[string]any{"groupIds": []uint{orgGroup}},
		"actionRisk": "WRITE",
	}, admin)
	require.Equalf(t, http.StatusOK, w.Code, "部分失败/离线不应整体失败: %s", w.Body.String())
	assert.Equal(t, "3", w.Header().Get("X-Total-Count"))

	res := parseJSON(t, w)
	assert.Equal(t, "req-sync-1", res["requestId"])
	assert.Equal(t, float64(3), res["total"])
	// 测试环境节点无 gRPC 连接 → 未下发，全部归 offline（含那台本就停机的）
	assert.Equal(t, float64(3), res["offline"])
	assert.Equal(t, float64(0), res["failed"])
	results := res["results"].([]any)
	require.Len(t, results, len(ids))
	for _, raw := range results {
		assert.Equal(t, "offline", raw.(map[string]any)["status"])
	}
}

// TestBusinessBatch_ExecuteIdempotentReplay 路由层幂等重放标记。
func TestBusinessBatch_ExecuteIdempotentReplay(t *testing.T) {
	r, _, admin, orgGroup, _ := newBatchRouterFixture(t, 2)
	body := map[string]any{
		"domain": "activity", "action": "start", "requestId": "req-replay",
		"target": map[string]any{"groupIds": []uint{orgGroup}}, "actionRisk": "WRITE",
	}
	first := makeRequest(r, "POST", "/api/v1/business/batch", body, admin)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.NotContains(t, parseJSON(t, first), "idempotentReplay")

	second := makeRequest(r, "POST", "/api/v1/business/batch", body, admin)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Equal(t, true, parseJSON(t, second)["idempotentReplay"])
}

// TestBusinessBatch_ExecuteMissingRequestID requestId 必填。
func TestBusinessBatch_ExecuteMissingRequestID(t *testing.T) {
	r, _, admin, orgGroup, _ := newBatchRouterFixture(t, 1)
	w := makeRequest(r, "POST", "/api/v1/business/batch", map[string]any{
		"domain": "activity", "action": "start",
		"target": map[string]any{"groupIds": []uint{orgGroup}},
	}, admin)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestBusinessBatch_ExecuteConfirmationRequired 跨大区无 token → 409 CONFIRMATION_REQUIRED。
func TestBusinessBatch_ExecuteConfirmationRequired(t *testing.T) {
	r, db, admin, cluster, ids := newBatchRouterFixture(t, 1)
	// 建两个大区，各挂一个实例 → 跨大区
	regionA := mkBatchRouterGroup(t, r, admin, "region-a", &cluster)
	regionB := mkBatchRouterGroup(t, r, admin, "region-b", &cluster)
	addInstancesToOrgGroup(t, r, admin, regionA, ids[0])
	node := createTestNodeWithSuffix(t, db, "-b")
	g := createGroupViaAPI(t, r, admin, "rbac-b")
	other := createInstanceViaAPI(t, r, admin, node.ID, g)
	addInstancesToOrgGroup(t, r, admin, regionB, other)

	w := makeRequest(r, "POST", "/api/v1/business/batch", map[string]any{
		"domain": "activity", "action": "start", "requestId": "req-conf",
		"target":     map[string]any{"groupIds": []uint{cluster}, "includeDescendants": true},
		"actionRisk": "WRITE",
	}, admin)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "CONFIRMATION_REQUIRED", parseJSON(t, w)["code"])
}

// TestBusinessBatch_ExecuteStream SSE 流式：progress 单调 + done 数值一致。
func TestBusinessBatch_ExecuteStream(t *testing.T) {
	r, _, admin, orgGroup, ids := newBatchRouterFixture(t, 5)
	body := map[string]any{
		"domain": "activity", "action": "start", "requestId": "req-stream",
		"target": map[string]any{"groupIds": []uint{orgGroup}}, "actionRisk": "WRITE",
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "/api/v1/business/batch", strings.NewReader(string(raw)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+admin)

	w := newFlushRecorder()
	r.ServeHTTP(w, req)

	require.Equalf(t, http.StatusOK, w.Code, "流式应 200: %s", w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")
	assert.Equal(t, "no", w.Header().Get("X-Accel-Buffering"))

	events := parseSSEEvents(t, w.Body.String())
	require.NotEmpty(t, events)

	var (
		progresses []map[string]any
		doneEvent  map[string]any
	)
	for _, ev := range events {
		switch ev.event {
		case "progress":
			progresses = append(progresses, ev.data)
		case "done":
			doneEvent = ev.data
		case "error":
			t.Fatalf("成功路径不该收到 error 帧: %v", ev.data)
		}
	}
	require.NotNil(t, doneEvent, "必须收到 done 帧")
	require.NotEmpty(t, progresses, "必须收到至少一帧 progress")

	prevDone := 0.0
	for i, p := range progresses {
		cur := p["done"].(float64)
		assert.GreaterOrEqual(t, cur, prevDone, "第 %d 帧 done 应单调不减", i)
		prevDone = cur
		assert.Equal(t, float64(len(ids)), p["total"])
	}

	last := progresses[len(progresses)-1]
	assert.Equal(t, float64(len(ids)), last["done"], "末帧 progress 的 done 应等于总数")
	assert.Equal(t, doneEvent["succeeded"], last["succeeded"], "done 与末帧 progress 数值须一致")
	assert.Equal(t, doneEvent["failed"], last["failed"])
	assert.Equal(t, doneEvent["offline"], last["offline"])

	assert.Equal(t, float64(len(ids)), doneEvent["total"])
	assert.Len(t, doneEvent["results"], len(ids))
	// 测试环境无 Worker 连接 → 全部 offline
	assert.Equal(t, float64(len(ids)), doneEvent["offline"])
}

// TestBusinessBatch_ExecuteStream_TokenErrorIsJSON token 校验失败必须返回 JSON 错误而不是 SSE 帧。
func TestBusinessBatch_ExecuteStream_TokenErrorIsJSON(t *testing.T) {
	r, _, admin, orgGroup, _ := newBatchRouterFixture(t, 1)
	body := map[string]any{
		"domain": "activity", "action": "purge", "requestId": "req-badtoken",
		"confirmToken": "bpt_not-a-real-token",
		"actionRisk":   "DESTRUCTIVE", "reason": "x",
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "/api/v1/business/batch", strings.NewReader(string(raw)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+admin)

	w := newFlushRecorder()
	r.ServeHTTP(w, req)

	assert.NotContains(t, w.Header().Get("Content-Type"), "text/event-stream",
		"确认门槛失败不该进入流式阶段")
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, "TOKEN_EXPIRED", res["code"])
	assert.NotContains(t, w.Body.String(), "event:", "不应写出任何 SSE 帧")

	_ = orgGroup
}

// sseEvent 解析后的单帧。
type sseEvent struct {
	event string
	data  map[string]any
}

// parseSSEEvents 解析 `event: x\ndata: {...}\n\n` 帧序列。
func parseSSEEvents(t *testing.T, raw string) []sseEvent {
	t.Helper()
	var out []sseEvent
	var curEvent string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			curEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			require.NoErrorf(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data),
				"帧 data 应为合法 JSON: %s", line)
			out = append(out, sseEvent{event: curEvent, data: data})
			curEvent = ""
		}
	}
	require.NoError(t, scanner.Err())
	return out
}

// newFlushRecorder 构造流式响应记录器。
//
// 直接用 httptest.ResponseRecorder：它已实现 http.Flusher，
// 与 http.NewResponseController 的 Flush 路径兼容，无需自建包装。
func newFlushRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
