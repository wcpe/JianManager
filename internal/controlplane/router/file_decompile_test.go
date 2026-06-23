package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// FR-075 归档浏览与反编译 CP 端点：参数校验 / 路径越界 / 权限 / 节点未连接。
// 真机端到端（真 jar/反编译）由 worker grpc 真机测试覆盖；本层只断 CP 转发前的边界。

func TestArchiveEntries_MissingPath(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/files/archive/entries", nil, adminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestArchiveEntries_NodeNotConnected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	// 合法路径但无连接的 Worker（测试 harness 的 ClientPool 为空）→ 422。
	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/files/archive/entries?path=plugins/Foo.jar", nil, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestArchiveEntries_CrossGroupForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupB := createGroupViaAPI(t, r, adminToken, "组B")
	aliceToken := getMemberToken(t, r, "alice-dec", "password123")
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instB)+"/files/archive/entries?path=plugins/Foo.jar", nil, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestArchiveRead_MissingParams(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	// 缺 entry。
	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(instID)+"/files/archive/read?path=plugins/Foo.jar", nil, adminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDecompile_MissingPath(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/files/decompile",
		map[string]interface{}{}, adminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDecompile_TraversalRejected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/files/decompile",
		map[string]interface{}{"path": "../../etc/passwd.class"}, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, "BUSINESS_ERROR", body["error"])
}

func TestDecompile_NodeNotConnected(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)
	instID := createInstanceViaAPI(t, r, adminToken, 1, 0)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/files/decompile",
		map[string]interface{}{"path": "plugins/Foo.jar", "entry": "com/example/Foo.class"}, adminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestDecompile_CrossGroupForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	createTestNode(t, db)

	groupB := createGroupViaAPI(t, r, adminToken, "组C")
	aliceToken := getMemberToken(t, r, "alice-dec2", "password123")
	instB := createInstanceViaAPI(t, r, adminToken, 1, groupB)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instB)+"/files/decompile",
		map[string]interface{}{"path": "plugins/Foo.jar"}, aliceToken)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
