package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestProbeUpdate_RoutesRegisterNoConflict 路由注册不 panic（静态段 /instances/probe/update
// 与参数段 /instances/:id/probe/update 共存）。setupTestRouter 内部 Setup 即触发注册。
func TestProbeUpdate_RoutesRegisterNoConflict(t *testing.T) {
	db := setupTestDB(t)
	require.NotPanics(t, func() { setupTestRouter(db) })
}

// TestProbeUpdate_Status_OK 拥有者查询单实例探针状态返回 200 + 内嵌信息。
func TestProbeUpdate_Status_OK(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusStopped)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/probe/update", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, float64(id), m["instanceId"])
	assert.Equal(t, cpembed.ProbeEmbeddedVersion, m["embeddedVersion"])
	// 内嵌可用性与本环境是否 make embed-probe 一致。
	assert.Equal(t, cpembed.ServerProbeJarInfo().Available, m["embeddedAvailable"])
	// 未注入 connChecker（测试路由），未连入。
	assert.Equal(t, false, m["probeConnected"])
	assert.Nil(t, m["lastPushedAt"])
}

// TestProbeUpdate_Status_NotFound 不存在的实例返回 404（存在性隐藏）。
func TestProbeUpdate_Status_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/instances/99999/probe/update", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestProbeUpdate_Update_NotEmbeddedOr422 单实例推送：本环境无内嵌 jar 时返回 422 PROBE_NOT_EMBEDDED；
// 若已内嵌则因测试无 Worker 连接返回 422 BUSINESS_ERROR。两种都不 panic 且为 422。
func TestProbeUpdate_Update_NotEmbeddedOr422(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusStopped)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/probe/update", map[string]any{}, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	m := parseJSON(t, w)
	if cpembed.ServerProbeJarInfo().Available {
		assert.Equal(t, "BUSINESS_ERROR", m["error"], "已内嵌 jar 时因无 Worker 连接而业务失败")
	} else {
		assert.Equal(t, "PROBE_NOT_EMBEDDED", m["error"])
	}
}

// TestProbeUpdate_Update_Forbidden 无 instance:operate 权限被拒。
func TestProbeUpdate_Update_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	bobToken := getMemberToken(t, r, "bob", "password123") // 不属于任何组

	w := makeRequest(r, "POST", "/api/v1/instances/1/probe/update", map[string]any{}, bobToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestProbeUpdate_Batch_Validation 批量缺 ids/filter → 400。
func TestProbeUpdate_Batch_Validation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/instances/probe/update", map[string]any{}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestProbeUpdate_Batch_Forbidden 无 instance:operate 权限的批量被拒。
func TestProbeUpdate_Batch_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	bobToken := getMemberToken(t, r, "bob", "password123")

	body := map[string]any{"ids": []uint{1}}
	w := makeRequest(r, "POST", "/api/v1/instances/probe/update", body, bobToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// TestProbeUpdate_Batch_NotEmbedded 本环境无内嵌 jar 时批量整体 422 PROBE_NOT_EMBEDDED。
func TestProbeUpdate_Batch_NotEmbedded(t *testing.T) {
	if cpembed.ServerProbeJarInfo().Available {
		t.Skip("已内嵌探针 jar，跳过未内嵌整体拒绝用例")
	}
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusRunning)

	body := map[string]any{"ids": []uint{id}}
	w := makeRequest(r, "POST", "/api/v1/instances/probe/update", body, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, "PROBE_NOT_EMBEDDED", m["error"])
}
