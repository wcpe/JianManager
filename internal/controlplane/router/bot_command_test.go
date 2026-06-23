package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 回归：docs/API.md 记载 POST /api/v1/bots/:id/command，但路由此前未注册，
// 实测返回 404 {"error":"NOT_FOUND","message":"接口不存在"}。以下用例断言路由已注册且行为正确。

// TestBot_Command_RouteRegistered Bot + 无 Worker 连接：应到达处理器并返回 503，
// 而非 404「接口不存在」（证明路由已注册）。
func TestBot_Command_RouteRegistered(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	id := makeBot(t, db, inst, "b1", model.BotStatusConnected, "idle")

	w := makeRequest(r, "POST", "/api/v1/bots/"+itoa(id)+"/command", map[string]interface{}{"command": "/tp 0 64 0"}, token)

	require.NotEqual(t, http.StatusNotFound, w.Code, "路由应已注册，不应返回 404")
	assert.NotContains(t, w.Body.String(), "接口不存在")
	// 无 Worker 连接 → 同步委托失败 → 503。
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// TestBot_Command_MissingCommand 缺 command 字段返回 400。
func TestBot_Command_MissingCommand(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	inst := createInstanceViaAPI(t, r, token, 1, createGroupViaAPI(t, r, token, "g"))
	id := makeBot(t, db, inst, "b1", model.BotStatusConnected, "idle")

	w := makeRequest(r, "POST", "/api/v1/bots/"+itoa(id)+"/command", map[string]interface{}{}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestBot_Command_NotFound 不存在的 Bot 返回 404。
func TestBot_Command_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/bots/99999/command", map[string]interface{}{"command": "/tp 0 64 0"}, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestBot_Command_Unauthorized 未登录返回 401。
func TestBot_Command_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "POST", "/api/v1/bots/1/command", map[string]interface{}{"command": "/tp 0 64 0"}, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
