package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
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

// TestNode_Delete_WithInstances_Conflict 离线节点名下有实例时删除被 409 拒绝并列出实例（FR-309）。
func TestNode_Delete_WithInstances_Conflict(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "orphan-candidate",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "x",
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusConflict, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, "NODE_HAS_INSTANCES", body["error"])
	instances, ok := body["instances"].([]interface{})
	require.True(t, ok)
	require.Len(t, instances, 1)
	first := instances[0].(map[string]interface{})
	assert.Equal(t, "orphan-candidate", first["name"])
	assert.Equal(t, string(model.InstanceStatusStopped), first["status"])

	// 拒绝即零副作用：节点仍在。
	w = makeRequest(r, "GET", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestNode_Delete_ForceCascades 离线节点 ?force=true 级联删除实例记录（FR-309，不清理远端文件）。
func TestNode_Delete_ForceCascades(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "orphan-candidate",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "x",
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID)+"?force=true", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(1), parseJSON(t, w)["instancesPurged"])

	// 节点与实例记录均已软删。
	w = makeRequest(r, "GET", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
	var count int64
	db.Model(&model.Instance{}).Where("node_id = ?", node.ID).Count(&count)
	assert.Zero(t, count)
}

// TestNode_Delete_ForceInvalidValue force 参数非法时 400（FR-309）。
func TestNode_Delete_ForceInvalidValue(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID)+"?force=banana", nil, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestNode_Archived_ListAndGet 下线后主列表消失、归档列表可见（FR-393）。
func TestNode_Archived_ListAndGet(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	// 主列表无此节点。
	w = makeRequest(r, "GET", "/api/v1/nodes", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	active := parseJSONArray(t, w)
	for _, item := range active {
		m := item.(map[string]interface{})
		assert.NotEqual(t, float64(node.ID), m["id"])
	}

	// 归档列表可见。
	w = makeRequest(r, "GET", "/api/v1/nodes/archived", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	archived := parseJSONArray(t, w)
	require.NotEmpty(t, archived)
	found := false
	for _, item := range archived {
		m := item.(map[string]interface{})
		if m["id"] == float64(node.ID) {
			found = true
			assert.NotEmpty(t, m["deletedAt"])
			assert.Equal(t, node.Name, m["name"])
		}
	}
	require.True(t, found)

	// 归档详情。
	w = makeRequest(r, "GET", "/api/v1/nodes/archived/"+itoa(node.ID), nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, float64(node.ID), body["id"])
	assert.NotEmpty(t, body["deletedAt"])
}

// TestNode_Purge_HardDelete 归档清理后双侧不可见（FR-394）。
func TestNode_Purge_HardDelete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)
	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	w = makeRequest(r, "DELETE", "/api/v1/nodes/archived/"+itoa(node.ID), nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "已清理", parseJSON(t, w)["message"])

	w = makeRequest(r, "GET", "/api/v1/nodes/archived/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
	w = makeRequest(r, "GET", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)

	var count int64
	db.Unscoped().Model(&model.Node{}).Where("id = ?", node.ID).Count(&count)
	assert.Zero(t, count)
}

// TestNode_Purge_ActiveRejected 未下线节点不可清理（FR-394）。
func TestNode_Purge_ActiveRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)

	w := makeRequest(r, "DELETE", "/api/v1/nodes/archived/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestNode_Purge_WithInstances_Force 有实例须 force 级联硬删（FR-394）。
func TestNode_Purge_WithInstances_Force(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).Update("status", model.NodeStatusOffline).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "left-over", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	// 下线 force 软删节点+实例。
	w := makeRequest(r, "DELETE", "/api/v1/nodes/"+itoa(node.ID)+"?force=true", nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	// 未 force 清理 → 409。
	w = makeRequest(r, "DELETE", "/api/v1/nodes/archived/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "NODE_HAS_INSTANCES", parseJSON(t, w)["error"])

	w = makeRequest(r, "DELETE", "/api/v1/nodes/archived/"+itoa(node.ID)+"?force=true", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, float64(1), parseJSON(t, w)["instancesPurged"])

	var count int64
	db.Unscoped().Model(&model.Instance{}).Where("node_id = ?", node.ID).Count(&count)
	assert.Zero(t, count)
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
