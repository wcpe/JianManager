package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNode_List_Empty(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 0)
}

func TestNode_List_WithData(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSONArray(t, w)
	require.Len(t, resp, 1)
	assert.Equal(t, "test-node", resp[0].(map[string]interface{})["name"])
}

func TestNode_Get_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, "GET", "/api/v1/nodes/"+itoa(node.ID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSON(t, w)
	assert.Equal(t, "test-node", resp["name"])
	assert.Equal(t, "127.0.0.1", resp["host"])
	assert.Equal(t, float64(1), resp["status"]) // NodeStatusOnline = 1
}

func TestNode_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/nodes/999", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestNode_Metrics_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	require.NoError(t, db.Model(node).Updates(map[string]any{
		"cpu_usage":      0.42,
		"memory_usage":   0.5,
		"disk_usage":     0.25,
		"memory_used_mb": 4096,
		"memory_mb":      8192,
		"disk_used_mb":   51200,
		"disk_total_mb":  102400,
	}).Error)

	w := makeRequest(r, "GET", "/api/v1/nodes/"+itoa(node.ID)+"/metrics", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSON(t, w)
	assert.InDelta(t, 0.42, resp["cpuUsage"], 0.001)
	assert.InDelta(t, 0.5, resp["memoryUsage"], 0.001)
	assert.InDelta(t, 0.25, resp["diskUsage"], 0.001)
	assert.Equal(t, float64(4096), resp["memoryUsedMb"])
	assert.Equal(t, float64(8192), resp["memoryTotalMb"])
	assert.Equal(t, float64(51200), resp["diskUsedMb"])
	assert.Equal(t, float64(102400), resp["diskTotalMb"])
}

func TestNode_Metrics_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/nodes/999/metrics", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestNode_List_MultipleNodes(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	createTestNode(t, db)
	createTestNodeWithSuffix(t, db, "node-2")

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 2)
}
