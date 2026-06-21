package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDownloadArchive_EmptyPaths paths 为空时返回 400。
func TestDownloadArchive_EmptyPaths(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/files/archive",
		map[string]interface{}{"paths": []string{}}, adminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDownloadArchive_TraversalRejected 含 .. 的路径在服务层被路径校验拒绝（422，不触达 Worker）。
func TestDownloadArchive_TraversalRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/files/archive",
		map[string]interface{}{"paths": []string{"../secret.txt"}}, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, "BUSINESS_ERROR", body["error"])
}

// TestDownloadArchive_NodeNotConnected 合法路径但无连接的 Worker：返回 422（节点未连接）。
// 测试 harness 的 ClientPool 为空，没有任何 Worker 连接。
func TestDownloadArchive_NodeNotConnected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/files/archive",
		map[string]interface{}{"paths": []string{"server.properties", "plugins"}}, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestDownloadArchive_CrossGroupForbidden 跨组成员对别组实例批量下载被拒（404，不泄露存在性）。
func TestDownloadArchive_CrossGroupForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupB := createGroupViaAPI(t, r, adminToken, "组B")
	aliceToken := getMemberToken(t, r, "alice-arc", "password123")
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instB)+"/files/archive",
		map[string]interface{}{"paths": []string{"server.properties"}}, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
