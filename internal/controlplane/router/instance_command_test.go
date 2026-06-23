package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 回归：docs/API.md 记载 POST /api/v1/instances/:id/command，但路由此前未注册，
// 实测返回 404 {"error":"NOT_FOUND","message":"接口不存在"}。以下用例断言路由已注册且行为正确。

// TestInstance_Command_RouteRegistered RUNNING 实例 + 无 Worker 连接：应到达处理器并返回 503，
// 而非 404「接口不存在」（证明路由已注册）。
func TestInstance_Command_RouteRegistered(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "running", model.InstanceStatusRunning)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/command", map[string]interface{}{"command": "say hello"}, token)

	require.NotEqual(t, http.StatusNotFound, w.Code, "路由应已注册，不应返回 404")
	assert.NotContains(t, w.Body.String(), "接口不存在")
	// 无 Worker 连接 → 同步委托失败 → 503。
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// TestInstance_Command_NotRunning 非 RUNNING 实例下发命令返回 422。
func TestInstance_Command_NotRunning(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "stopped", model.InstanceStatusStopped)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/command", map[string]interface{}{"command": "say hi"}, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "未运行")
}

// TestInstance_Command_MissingCommand 缺 command 字段返回 400。
func TestInstance_Command_MissingCommand(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "running", model.InstanceStatusRunning)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/command", map[string]interface{}{}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestInstance_Command_NotFound 不存在的实例返回 404（平台管理员经存在性校验落到 service）。
func TestInstance_Command_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/instances/99999/command", map[string]interface{}{"command": "say hi"}, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestInstance_Command_Unauthorized 未登录返回 401。
func TestInstance_Command_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "POST", "/api/v1/instances/1/command", map[string]interface{}{"command": "say hi"}, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
