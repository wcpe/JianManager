package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 桥能力接入的三条路由：状态码、降级形态与权限口径。
//
// 这里只验证「HTTP 契约层」——service 层的语义（缓存 TTL / stale / 覆盖）在
// internal/controlplane/service/bridge_status_test.go 里覆盖。

// TestBridgeStatusRoute_NodeOfflineDegrades 节点未连：200 + connected=false，不返回 5xx。
func TestBridgeStatusRoute_NodeOfflineDegrades(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db) // 仅入库，无 gRPC 连接
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/bridge/status", nil, admin)
	require.Equal(t, http.StatusOK, w.Code, "桥状态是排障接口，探针不在不该 5xx: %s", w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, false, res["connected"])
	assert.Equal(t, float64(instID), res["instanceId"])
	assert.NotEmpty(t, res["error"], "应给出降级原因")
	// 契约字段必须存在（前端按 schema 渲染，缺字段会渲染成 undefined）。
	assert.Contains(t, res, "reconnectCount")
	assert.Contains(t, res, "queueDepth")
	assert.Contains(t, res, "droppedFrames")
	assert.Contains(t, res, "pendingReloads")
	assert.Contains(t, res, "lastDisconnectReason")
}

// TestBridgeStatusRoute_NotFound 实例不存在：404。
func TestBridgeStatusRoute_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/instances/99999/bridge/status", nil, admin)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestBridgeSubscriptionRoute_ProbeUnavailable 探针未连：409 PROBE_UNAVAILABLE
// （OpenAPI 对 PUT subscription 明确规定，不能假装订阅成功）。
func TestBridgeSubscriptionRoute_ProbeUnavailable(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	body := map[string]any{"topics": []string{"activity.*"}, "domains": []string{"activity"}}
	w := makeRequest(r, "PUT", "/api/v1/instances/"+itoa(instID)+"/bridge/subscription", body, admin)
	require.Equal(t, http.StatusConflict, w.Code, "探针未连时必须 409: %s", w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, "PROBE_UNAVAILABLE", res["error"])
}

// TestBridgeSubscriptionRoute_BadBody 请求体非法：400。
func TestBridgeSubscriptionRoute_BadBody(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	w := makeRequest(r, "PUT", "/api/v1/instances/"+itoa(instID)+"/bridge/subscription", "not-an-object", admin)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBusinessStatusRoute_StaleWhenProbeOffline 探针不可达：200 + stale=true + 空域（不 5xx）。
func TestBusinessStatusRoute_StaleWhenProbeOffline(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/business/status", nil, admin)
	require.Equal(t, http.StatusOK, w.Code, "面板要能渲染，域状态不可达不该 5xx: %s", w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, true, res["stale"], "探针不可达且无缓存时应置 stale")
	assert.Equal(t, false, res["probeConnected"])
	assert.Equal(t, float64(instID), res["instanceId"])
	domains, ok := res["domains"].([]any)
	require.True(t, ok, "domains 必须是数组（不能是 null）")
	assert.Empty(t, domains)
}

// TestBridgeRoutes_RequireInstanceRead 权限口径：空权限树 403；默认 member 有 instance.read
// 但不在组内 → 存在性隐藏 404（与 server-state 等实例级只读端点一致）。
func TestBridgeRoutes_RequireInstanceRead(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	paths := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/instances/" + itoa(instID) + "/bridge/status", nil},
		{"PUT", "/api/v1/instances/" + itoa(instID) + "/bridge/subscription", map[string]any{"topics": []string{"a"}}},
		{"GET", "/api/v1/instances/" + itoa(instID) + "/business/status", nil},
	}

	// 空权限树用户 → 403。
	perm := service.NewAuthzService(db).Permissions()
	u, err := service.NewUserService(db).Create("bridge_zero", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	role, err := perm.CreateRole("bridge-zero-role", "", nil)
	require.NoError(t, err)
	require.NoError(t, perm.BindUserRole(u.ID, role.ID))
	zeroTok := loginTestUser(t, r, "bridge_zero", "password123")

	for _, p := range paths {
		t.Run("forbidden "+p.method+" "+p.path, func(t *testing.T) {
			w := makeRequest(r, p.method, p.path, p.body, zeroTok)
			assert.Equal(t, http.StatusForbidden, w.Code, "三条桥路由都必须要求 instance.read")
		})
	}

	// 默认 member（含 instance.read）但不在组内 → 404（存在性隐藏）。
	member := getMemberToken(t, r, "bridgemember", "password123")
	for _, p := range paths {
		t.Run("hidden "+p.method+" "+p.path, func(t *testing.T) {
			w := makeRequest(r, p.method, p.path, p.body, member)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	}
}

// TestBridge_BusinessStatusBatch 批量域状态：等长同序、去重、越权与上限。
func TestBridge_BusinessStatusBatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "batch-g")
	id1 := createInstanceViaAPI(t, r, adminTok, node.ID, g)
	id2 := createInstanceViaAPI(t, r, adminTok, node.ID, g)

	// 两实例都查 → 等长同序，且不因探针未连而整体失败（降级成 stale）。
	w := makeRequest(r, http.MethodPost, "/api/v1/instances/business/status/batch", map[string]any{
		"instanceIds": []uint{id1, id2},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	results, ok := body["results"].([]any)
	require.True(t, ok, "results 应是数组，实际=%v", body["results"])
	require.Len(t, results, 2, "应与入参等长")
	first := results[0].(map[string]any)
	require.Equal(t, float64(id1), first["instanceId"], "应保持入参顺序")

	// 重复 ID 去重：传 3 个（含 1 个重复）→ 返回 2 条。
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/business/status/batch", map[string]any{
		"instanceIds": []uint{id1, id2, id1},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, parseJSON(t, w)["results"], 2, "重复 ID 应被去重")

	// 不存在的实例 → 该条标 notFound，其余正常（不整体失败）。
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/business/status/batch", map[string]any{
		"instanceIds": []uint{id1, 999999},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	r2 := parseJSON(t, w)["results"].([]any)
	require.Len(t, r2, 2)
	require.Equal(t, true, r2[1].(map[string]any)["notFound"], "不存在的实例应标 notFound")

	// 空列表 → 空数组（不是 null，也不是 400）。
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/business/status/batch", map[string]any{
		"instanceIds": []uint{},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 超上限 → 400 BATCH_TOO_LARGE，且带 maxCount。
	tooMany := make([]uint, 51)
	for i := range tooMany {
		tooMany[i] = uint(i + 1)
	}
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/business/status/batch", map[string]any{
		"instanceIds": tooMany,
	}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "BATCH_TOO_LARGE", parseJSON(t, w)["error"])
	require.Equal(t, float64(50), parseJSON(t, w)["maxCount"])
}

// TestBridge_BusinessStatusBatch_RouteNotShadowed 批量路由不能被 :id 通配吃掉。
//
// 若注册顺序错误，`POST /instances/business/status/batch` 会被
// `/instances/:id/business/status` 匹配成 id="business"，返回 "无效的 ID"。
// 这条测试锁死"批量路由可达"。
func TestBridge_BusinessStatusBatch_RouteNotShadowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/business/status/batch", map[string]any{
		"instanceIds": []uint{},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// 关键：不能是 "无效的 ID"（那是被 :id 通配匹配的特征）。
	require.NotEqual(t, "INVALID_ID", parseJSON(t, w)["error"], "批量路由被 :id 通配吃掉了")
}
