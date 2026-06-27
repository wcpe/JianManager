package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNodeRuntime_ArtifactCache_RequiresAdmin 非平台管理员访问制品缓存端点回 403（FR-178）。
func TestNodeRuntime_ArtifactCache_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r) // 先建管理员，避免后续注册被首启引导拦截
	memberToken := getMemberToken(t, r, "member1", "password123")
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes/"+itoa(node.ID)+"/artifact-cache", nil, memberToken)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestNodeRuntime_ArtifactCache_OfflineNode 管理员访问离线节点的缓存端点回 503 NODE_OFFLINE（FR-178）。
func TestNodeRuntime_ArtifactCache_OfflineNode(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db) // 测试无 Worker 连接 → 离线

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes/"+itoa(node.ID)+"/artifact-cache", nil, token)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

// TestNodeRuntime_Browse_OfflineNode 目录浏览对离线节点回 503（FR-178）。
func TestNodeRuntime_Browse_OfflineNode(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes/"+itoa(node.ID)+"/browse?path=/opt", nil, token)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

// TestNodeRuntime_Catalog_MissingVendor 缺 vendor 回 400（FR-178）。
func TestNodeRuntime_Catalog_MissingVendor(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes/"+itoa(node.ID)+"/jdk/catalog", nil, token)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestNodeRuntime_SetCap_RejectsNegative 设负上限回 400（FR-178）。
func TestNodeRuntime_SetCap_RejectsNegative(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodPut, "/api/v1/nodes/"+itoa(node.ID)+"/artifact-cache/cap",
		map[string]any{"capBytes": -1}, token)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
