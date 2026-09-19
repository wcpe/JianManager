package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TestServerState_RouteRegisters 路由注册不 panic（与 /instances/:id/... 其余参数段共存）。
func TestServerState_RouteRegisters(t *testing.T) {
	db := setupTestDB(t)
	require.NotPanics(t, func() { setupTestRouter(db) })
}

// TestServerState_OK_DegradedNoWorker 拥有者查询：测试环境无 Worker 连接 → 200 + 降级
// （connected=false, available=false, state=null, error=节点未连接），不 5xx。
func TestServerState_OK_DegradedNoWorker(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusRunning)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/server-state", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(id), m["instanceId"])
	assert.Equal(t, false, m["connected"])
	assert.Equal(t, false, m["available"])
	assert.Nil(t, m["state"])
	assert.Equal(t, "节点未连接", m["error"])
}

// TestServerState_NotFound 不存在的实例返回 404（存在性隐藏）。
func TestServerState_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/instances/99999/server-state", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestServerState_Forbidden_NoPermission：空权限树 403；默认 member 有 instance.read 但无组 → 404。
func TestServerState_Forbidden_NoPermission(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusRunning)

	// 默认 member（种子含 instance.read）但不属于组 → 存在性隐藏 404
	bobToken := getMemberToken(t, r, "bob", "password123")
	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/server-state", nil, bobToken)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// 空权限树用户 → 403
	perm := service.NewAuthzService(db).Permissions()
	u, err := service.NewUserService(db).Create("zero_user", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	role, err := perm.CreateRole("zero-role", "", nil)
	require.NoError(t, err)
	require.NoError(t, perm.BindUserRole(u.ID, role.ID))
	zeroTok := loginTestUser(t, r, "zero_user", "password123")
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/server-state", nil, zeroTok)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
