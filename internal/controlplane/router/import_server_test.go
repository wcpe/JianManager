package router

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeImportRouterWorker 端点测试用伪 Worker：探测按预置应答。
type fakeImportRouterWorker struct {
	workerpb.WorkerServiceClient
	inspectResp *workerpb.InspectServerDirResponse
}

func (f *fakeImportRouterWorker) InspectServerDir(_ context.Context, _ *workerpb.InspectServerDirRequest, _ ...grpc.CallOption) (*workerpb.InspectServerDirResponse, error) {
	return f.inspectResp, nil
}

func (f *fakeImportRouterWorker) CreateInstance(_ context.Context, _ *workerpb.CreateInstanceRequest, _ ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

// 非平台管理员访问导入端点回 403（FR-302：平台管理员专属）。
func TestImportServer_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-import", "password123")

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/import/inspect",
		map[string]any{"nodeId": 1, "path": "/srv/old"}, memberToken)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = makeRequest(r, http.MethodPost, "/api/v1/instances/import",
		map[string]any{"nodeId": 1, "path": "/srv/old", "mode": "in_place", "name": "x", "jarPath": "server.jar"}, memberToken)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// 离线节点回 503；节点不存在回 404。
func TestImportServer_NodeStates(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db) // 无 Worker 连接 → 离线

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/import/inspect",
		map[string]any{"nodeId": node.ID, "path": "/srv/old"}, token)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())

	w = makeRequest(r, http.MethodPost, "/api/v1/instances/import/inspect",
		map[string]any{"nodeId": 9999, "path": "/srv/old"}, token)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// 非法请求体（缺 path / 非法 mode）回 400。
func TestImportServer_InvalidBody(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/import/inspect", map[string]any{"nodeId": 1}, token)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	w = makeRequest(r, http.MethodPost, "/api/v1/instances/import",
		map[string]any{"nodeId": 1, "path": "/srv", "mode": "yolo", "name": "x", "jarPath": "a.jar"}, token)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// Worker 守卫拒绝（不存在路径等）回 422 IMPORT_REJECTED（spec：不存在路径 4xx）；
// 探测成功 + 导入成功回 201 且写两条审计。
func TestImportServer_EndToEndWithFakeWorker(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	worker := &fakeImportRouterWorker{inspectResp: &workerpb.InspectServerDirResponse{Success: false, Error: "无法访问: 不存在"}}
	pool.SetWorkerClientForTest(node.UUID, worker)

	// 守卫拒绝 → 422。
	w := makeRequest(r, http.MethodPost, "/api/v1/instances/import/inspect",
		map[string]any{"nodeId": node.ID, "path": "/srv/nope"}, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "IMPORT_REJECTED")

	// 探测成功 → 200 + 审计 instance.import.inspect。
	worker.inspectResp = &workerpb.InspectServerDirResponse{
		Success:      true,
		Jars:         []*workerpb.ImportJarCandidate{{Path: "server.jar", Size: 10}},
		ServerPort:   25565,
		EulaAccepted: true,
		PropsFound:   true,
	}
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/import/inspect",
		map[string]any{"nodeId": node.ID, "path": "/srv/old"}, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	assert.EqualValues(t, 25565, resp["serverPort"])

	// 就地导入 → 201 + 实例落库（就地标记）+ 审计 instance.import。
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/import",
		map[string]any{"nodeId": node.ID, "path": "/srv/old", "mode": "in_place", "name": "老服", "jarPath": "server.jar"}, token)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	// FR-323：响应形状为 {instance, taskId}（就地接管 taskId 空）。
	created := parseJSON(t, w)
	inst, ok := created["instance"].(map[string]any)
	require.True(t, ok, "响应应含 instance 对象: %s", w.Body.String())
	assert.Equal(t, true, inst["workDirInPlace"])
	assert.Equal(t, "/srv/old", inst["workDir"])
	assert.Equal(t, "", created["taskId"], "就地接管同步完成，taskId 为空")

	var inspectAudit, importAudit int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.import.inspect").Count(&inspectAudit).Error)
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "instance.import").Count(&importAudit).Error)
	assert.EqualValues(t, 1, inspectAudit, "探测成功应写审计")
	assert.EqualValues(t, 1, importAudit, "导入成功应写审计")
}
