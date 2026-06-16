package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstance_Create_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "测试实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	assert.Equal(t, http.StatusCreated, w.Code)

	resp := parseJSON(t, w)
	assert.Equal(t, "测试实例", resp["name"])
	assert.Equal(t, "STOPPED", resp["status"])
	assert.NotNil(t, resp["uuid"])
}

func TestInstance_Create_InvalidRequest(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{
		"name": "缺少必要字段",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestInstance_List_Empty(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/instances", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 0)
}

func TestInstance_List_WithData(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	createBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "实例1",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", createBody, token)
	require.Equal(t, http.StatusCreated, w.Code)

	w = makeRequest(r, "GET", "/api/v1/instances", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 1)
}

func TestInstance_Get_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	createBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "测试实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", createBody, token)
	require.Equal(t, http.StatusCreated, w.Code)

	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSON(t, w)
	assert.Equal(t, "测试实例", resp["name"])
	assert.Equal(t, "STOPPED", resp["status"])
}

func TestInstance_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/instances/999", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestInstance_Update_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	createBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "原始名称",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", createBody, token)
	require.Equal(t, http.StatusCreated, w.Code)

	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	newName := "更新后名称"
	updateBody := map[string]interface{}{"name": newName}
	w = makeRequest(r, "PUT", "/api/v1/instances/"+itoa(id), updateBody, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSON(t, w)
	assert.Equal(t, "更新后名称", resp["name"])
}

func TestInstance_Delete_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	createBody := map[string]interface{}{
		"nodeId":       1,
		"name":         "待删除实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", createBody, token)
	require.Equal(t, http.StatusCreated, w.Code)

	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	w = makeRequest(r, "DELETE", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 确认已删除
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
