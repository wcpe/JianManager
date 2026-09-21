package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestProbeUpdate_RoutesRegisterNoConflict 路由注册不 panic（静态段 /instances/probe/update
// 与参数段 /instances/:id/probe/update 共存）。setupTestRouter 内部 Setup 即触发注册。
func TestProbeUpdate_RoutesRegisterNoConflict(t *testing.T) {
	db := setupTestDB(t)
	require.NotPanics(t, func() { setupTestRouter(db) })
}

// TestProbeUpdate_Status_OK 拥有者查询单实例探针状态返回 200。
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
	assert.Equal(t, false, m["embeddedAvailable"])
	assert.Equal(t, "制品版本库未配置", m["versionError"])
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

// TestProbeUpdate_Update_NotEmbeddedOr422 单实例推送在未装配版本库时返回 422。
// 用适用探针的 MC 后端实例——通用二进制/Beacon/代理会被「不适用」前置拒绝（FR-454），
// 不走到版本库检查。
func TestProbeUpdate_Update_NotEmbeddedOr422(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	inst := &model.Instance{
		NodeID: node.ID, Name: "smp", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: g, InstanceID: inst.ID}).Error)

	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(inst.ID)+"/probe/update", map[string]any{}, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	m := parseJSON(t, w)
	assert.Equal(t, "PROBE_NOT_EMBEDDED", m["error"])
}

// TestProbeUpdate_Update_RejectsNonApplicable FR-454：对不适用探针的实例（通用二进制/Beacon/代理）
// 点「更新探针」，路由层返回明确拒绝（422 BUSINESS_ERROR + 可读原因），不推入无效 Bukkit jar。
func TestProbeUpdate_Update_RejectsNonApplicable(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")

	cases := []struct {
		name string
		typ  model.InstanceType
		role model.InstanceRole
		word string
	}{
		{"通用二进制", model.InstanceTypeGeneric, model.InstanceRoleUniversal, "通用二进制实例不适用"},
		{"Beacon", model.InstanceTypeGeneric, model.InstanceRoleBeacon, "Beacon 实例不适用"},
		{"代理", model.InstanceTypeMinecraftJava, model.InstanceRoleProxy, "代理实例不适用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := &model.Instance{
				NodeID: node.ID, Name: "na-" + tc.name, Type: tc.typ, Role: tc.role,
				ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
			}
			require.NoError(t, db.Create(inst).Error)
			require.NoError(t, db.Create(&model.GroupInstance{GroupID: g, InstanceID: inst.ID}).Error)

			w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(inst.ID)+"/probe/update", map[string]any{}, token)
			require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
			m := parseJSON(t, w)
			assert.Equal(t, "BUSINESS_ERROR", m["error"])
			assert.Contains(t, m["message"], tc.word)
		})
	}
}

// TestProbeUpdate_Update_Forbidden FR-432：member 有 instance.operate 能力；
// 无组用户对不可见实例走存在性隐藏 404（不再 403）。
func TestProbeUpdate_Update_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	bobToken := getMemberToken(t, r, "bob", "password123") // 不属于任何组

	w := makeRequest(r, "POST", "/api/v1/instances/1/probe/update", map[string]any{}, bobToken)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestProbeUpdate_Batch_Validation 批量缺 ids/filter → 400。
func TestProbeUpdate_Batch_Validation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/instances/probe/update", map[string]any{}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestProbeUpdate_Batch_Forbidden FR-432：无组 member 有能力无 scope →
// 目标全 skipped；若探针制品未装配，业务层回 422 PROBE_NOT_EMBEDDED（非 403）。
func TestProbeUpdate_Batch_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	getAdminToken(t, r)
	bobToken := getMemberToken(t, r, "bob", "password123")

	body := map[string]any{"ids": []uint{1}}
	w := makeRequest(r, "POST", "/api/v1/instances/probe/update", body, bobToken)
	assert.NotEqual(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestProbeUpdate_Batch_NotEmbedded 未装配版本库时批量整体 422 PROBE_NOT_EMBEDDED。
func TestProbeUpdate_Batch_NotEmbedded(t *testing.T) {
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
