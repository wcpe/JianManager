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
