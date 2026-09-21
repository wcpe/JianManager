package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestInstanceUpdate_RoleTypeCoherenceFR454 FR-454（第二轮复审 N1）经 HTTP 层验证：
//   - 改 role=beacon 时即便显式传 type=minecraft_java，也必须被归一为 generic 且 probe_port 归零；
//   - 非法 type 返回 400 且不落库；
//   - beacon→backend 需同时显式传 type=minecraft_java 才能恢复为可加载探针的 MC 服务端。
func TestInstanceUpdate_RoleTypeCoherenceFR454(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusStopped)

	// 先把它做成「MC 后端 + 已分配探针端口」。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", id).
		Updates(map[string]any{
			"type":       model.InstanceTypeMinecraftJava,
			"role":       model.InstanceRoleBackend,
			"probe_port": 29940,
		}).Error)

	var got model.Instance

	// 改 role=beacon 且（错误地）传 type=minecraft_java：type 归一为 generic，probe_port 归零。
	w := makeRequest(r, "PUT", "/api/v1/instances/"+itoa(id), map[string]any{
		"role": "beacon", "type": "minecraft_java",
	}, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	assert.Equal(t, "generic", resp["type"])
	assert.Equal(t, "beacon", resp["role"])
	assert.Equal(t, float64(0), resp["probePort"])
	require.NoError(t, db.First(&got, id).Error)
	assert.Equal(t, model.InstanceTypeGeneric, got.Type)
	assert.Zero(t, got.ProbePort)

	// 非法 type → 400 且不落库。
	w = makeRequest(r, "PUT", "/api/v1/instances/"+itoa(id), map[string]any{"type": "weird_type"}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.NoError(t, db.First(&got, id).Error)
	assert.Equal(t, model.InstanceTypeGeneric, got.Type, "非法 type 不得落库")

	// beacon→backend：显式传 type=minecraft_java 恢复为探针适用的 MC 服务端（修回入口）。
	w = makeRequest(r, "PUT", "/api/v1/instances/"+itoa(id), map[string]any{
		"role": "backend", "type": "minecraft_java",
	}, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, db.First(&got, id).Error)
	assert.Equal(t, model.InstanceTypeMinecraftJava, got.Type)
	assert.Equal(t, model.InstanceRoleBackend, got.Role)
}
