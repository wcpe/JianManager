package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// TestNode_Delete_Online_Online 节点在线时不能删除。
func TestNode_Delete_Online_CannotDelete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	// 在线节点不能删除
	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestNode_Delete_Offline_Success 离线节点可删除。
func TestNode_Delete_Offline_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	// 将节点改为离线
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 确认已删除
	w = makeRequest(r, "GET", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestNode_Delete_NotFound 删除不存在的节点返回错误。
func TestNode_Delete_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/999", nil, token)
	// service 层返回 ErrNodeNotFound，handler 映射为 422 BUSINESS_ERROR
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestNode_List_Empty_AdminOnly 普通成员无法访问节点列表。
func TestNode_List_AdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "nodemem", "password123")

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestNode_Maintenance_Toggle 置/解维护模式翻转标记（FR-048）。
func TestNode_Maintenance_Toggle(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/maintenance", map[string]bool{"enabled": true}, token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, true, parseJSON(t, w)["maintenance"])

	w = makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/maintenance", map[string]bool{"enabled": false}, token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, false, parseJSON(t, w)["maintenance"])
}

// TestNode_Maintenance_AdminOnly 普通成员不能置维护模式。
func TestNode_Maintenance_AdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	node := createTestNode(t, db)
	memberToken := getMemberToken(t, r, "nodemem2", "password123")

	w := makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/maintenance", map[string]bool{"enabled": true}, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestNode_Maintenance_RejectsScheduling 维护中节点拒绝创建实例（调度拦截）。
func TestNode_Maintenance_RejectsScheduling(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	// 置维护
	w := makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/maintenance", map[string]bool{"enabled": true}, token)
	require.Equal(t, http.StatusOK, w.Code)

	// 创建实例应被拒绝（service.ErrNodeInMaintenance 映射为 422）
	body := map[string]interface{}{
		"nodeId":       node.ID,
		"name":         "i1",
		"type":         "generic",
		"processType":  "direct",
		"startCommand": "echo hi",
	}
	w = makeRequest(r, "POST", "/api/v1/instances", body, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "NODE_MAINTENANCE", parseJSON(t, w)["error"])
}

// TestNode_Drain_StopsRunning 排空停止节点上运行实例（FR-048）。
func TestNode_Drain_StopsRunning(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "run",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "x",
		Status:       model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)

	w := makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/drain", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(1), parseJSON(t, w)["stoppedCount"])

	var fromDB model.Instance
	require.NoError(t, db.First(&fromDB, inst.ID).Error)
	assert.Equal(t, model.InstanceStatusStopping, fromDB.Status)
}

// TestNode_Drain_AdminOnly 普通成员不能排空节点。
func TestNode_Drain_AdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	node := createTestNode(t, db)
	memberToken := getMemberToken(t, r, "nodemem3", "password123")

	w := makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/drain", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestNode_Maintenance_Audited 维护操作写入审计日志（FR-048 / FR-015）。
func TestNode_Maintenance_Audited(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, "POST", "/api/v1/nodes/"+itoa(node.ID)+"/maintenance", map[string]bool{"enabled": true}, token)
	require.Equal(t, http.StatusOK, w.Code)

	var count int64
	db.Model(&model.AuditLog{}).Where("action = ? AND target_type = ?", "node.maintenance", "node").Count(&count)
	assert.Equal(t, int64(1), count)
}
