package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTemplate_CreateListDelete 模板创建→列表→删除完整流程。
func TestTemplate_CreateListDelete(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 创建模板
	tplBody := map[string]interface{}{
		"name":         "Paper 1.20",
		"type":         "minecraft_java",
		"description":  "Paper 服务端模板",
		"startCommand": "java -jar paper.jar",
		"downloadUrl":  "https://example.com/paper.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/templates", tplBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "Paper 1.20", resp["name"])
	assert.Equal(t, "java -jar paper.jar", resp["startCommand"])
	tplID := uint(resp["id"].(float64))

	// 列表
	w = makeRequest(r, "GET", "/api/v1/templates", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	templates := parseJSONArray(t, w)
	require.Len(t, templates, 1)

	// 删除
	w = makeRequest(r, "DELETE", "/api/v1/templates/"+itoa(tplID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 删除后列表为空
	w = makeRequest(r, "GET", "/api/v1/templates", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	templates = parseJSONArray(t, w)
	assert.Len(t, templates, 0)
}

// TestTemplate_Create_MissingFields 缺少必要字段返回 400。
func TestTemplate_Create_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 缺少 startCommand 等必填字段
	w := makeRequest(r, "POST", "/api/v1/templates", map[string]interface{}{"name": "缺字段"}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestTemplate_Delete_InvalidID 非法 ID 返回 400。
func TestTemplate_Delete_InvalidID(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "DELETE", "/api/v1/templates/abc", nil, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
