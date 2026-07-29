package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestManagedProcessDetail_InvalidPIDReturns400(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	inst := createManagedProcessTestInstance(t, db, createTestNode(t, db))

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(inst.ID)+"/processes/not-a-pid", nil, token)
	require.Equalf(t, http.StatusBadRequest, w.Code, "响应体: %s", w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, "INVALID_PID", resp["error"])
}

func TestManagedProcessAction_RequiresConfirmBeforeWorker(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	inst := createManagedProcessTestInstance(t, db, createTestNode(t, db))

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(inst.ID)+"/processes/1234/actions", map[string]any{
		"action": "kill_tree",
	}, token)
	require.Equalf(t, http.StatusConflict, w.Code, "响应体: %s", w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, "CONFIRM_REQUIRED", resp["error"])
}

func TestManagedProcessDetail_MemberWithoutAccessIsHidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "process-reader", "password123")
	inst := createManagedProcessTestInstance(t, db, createTestNode(t, db))

	// 管理员能抵达新端点；节点未连接时应是业务降级，不应是未挂路由。
	adminResp := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(inst.ID)+"/processes/1234", nil, adminToken)
	require.NotEqualf(t, http.StatusNotFound, adminResp.Code, "管理员请求不应被路由或存在性隐藏: %s", adminResp.Body.String())

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(inst.ID)+"/processes/1234", nil, memberToken)
	require.Equalf(t, http.StatusNotFound, w.Code, "响应体: %s", w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, "NOT_FOUND", resp["error"])
}

func createManagedProcessTestInstance(t *testing.T, db *gorm.DB, node *model.Node) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "受管进程测试实例",
		Type:         model.InstanceTypeMinecraftJava,
		ProcessType:  model.ProcessTypeDirect,
		Status:       model.InstanceStatusRunning,
		StartCommand: "java -jar server.jar",
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}
