package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 验证 /instances/search 与 /:id 静态/参数同级共存（gin 不 panic）+ 分页信封 + 鉴权。
func TestInstance_Search_PaginatedEnvelope(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	for i := 0; i < 3; i++ {
		require.NoError(t, db.Create(&model.Instance{
			NodeID: 1, Name: "srv-" + itoa(uint(i)), Type: model.InstanceTypeGeneric,
			ProcessType: model.ProcessTypeDirect, StartCommand: "x",
			Status: model.InstanceStatusStopped, Role: model.InstanceRoleUniversal,
		}).Error)
	}

	w := makeRequest(r, "GET", "/api/v1/instances/search?page=1&pageSize=2", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(3), resp["total"])
	assert.Equal(t, float64(1), resp["page"])
	assert.Equal(t, float64(2), resp["pageSize"])
	items, ok := resp["items"].([]interface{})
	require.True(t, ok, "items 应为数组")
	assert.Len(t, items, 2)

	// 末页余 1
	w = makeRequest(r, "GET", "/api/v1/instances/search?page=2&pageSize=2", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	resp = parseJSON(t, w)
	items, _ = resp["items"].([]interface{})
	assert.Len(t, items, 1)
}

// 验证 /instances/aggregate 计数信封。
func TestInstance_Aggregate_Counts(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	for i, st := range []model.InstanceStatus{model.InstanceStatusRunning, model.InstanceStatusRunning, model.InstanceStatusStopped} {
		require.NoError(t, db.Create(&model.Instance{
			NodeID: 1, Name: "a" + itoa(uint(i)), Type: model.InstanceTypeGeneric,
			ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: st, Role: model.InstanceRoleUniversal,
		}).Error)
	}

	w := makeRequest(r, "GET", "/api/v1/instances/aggregate", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(3), resp["total"])
	byStatus, ok := resp["byStatus"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(2), byStatus["RUNNING"])
	assert.Equal(t, float64(1), byStatus["STOPPED"])
	assert.Equal(t, float64(0), byStatus["CRASHED"], "缺席状态零补 0")
}

// 无权限令牌应 403（鉴权门）。
func TestInstance_Search_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	// 不带 token
	w := makeRequest(r, "GET", "/api/v1/instances/search", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
