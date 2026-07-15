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

// fakeLifecycleWorker 假 Worker（FR-314 后启动链路带同步预检）：预检放行、注册/启动即成功，
// 使生命周期用例聚焦路由层状态转换语义而非预检分支（预检分支见 service 层与下方 409 用例）。
type fakeLifecycleWorker struct {
	workerpb.WorkerServiceClient
}

func (f *fakeLifecycleWorker) PreflightStartInstance(ctx context.Context, in *workerpb.InstanceActionRequest, opts ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (f *fakeLifecycleWorker) CreateInstance(ctx context.Context, in *workerpb.CreateInstanceRequest, opts ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{}, nil
}

func (f *fakeLifecycleWorker) StartInstance(ctx context.Context, in *workerpb.InstanceActionRequest, opts ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// TestInstance_Create_Unauthorized 未登录创建实例返回 401。
func TestInstance_Create_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "未授权实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestInstance_Delete_NotFound 删除不存在的实例返回错误。
func TestInstance_Delete_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "DELETE", "/api/v1/instances/999", nil, token)
	// canManageInstance 对 admin 返回 true，但 service 层返回 not found 导致 500
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestInstance_Start_StoppedToStarting 停止状态的实例可启动（预检放行经 fake Worker，FR-314）。
func TestInstance_Start_StoppedToStarting(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &fakeLifecycleWorker{})

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "启动测试实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 启动实例（STOPPED → STARTING，后台异步委托 Worker）
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/start", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestInstance_Start_AlreadyStarting 已处于 STARTING 状态再次启动返回错误（预检放行经 fake Worker）。
func TestInstance_Start_AlreadyStarting(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &fakeLifecycleWorker{})

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "重复启动实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 第一次启动成功
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/start", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 第二次启动失败（STARTING 不能再次启动）
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/start", nil, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestInstance_Start_NodeOffline 节点未连接时启动被同步预检拦截，返回 409 NODE_OFFLINE（FR-314）。
func TestInstance_Start_NodeOffline(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db) // 空连接池 = 节点未连接
	token := getAdminToken(t, r)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "离线节点实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/start", nil, token)
	assert.Equal(t, http.StatusConflict, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "NODE_OFFLINE", resp["error"])
}

// TestInstance_Stop_RunningToStopping 运行中实例可停止并进入 STOPPING。
func TestInstance_Stop_RunningToStopping(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "停止成功实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 测试环境没有真实 Worker，直接模拟运行中实例以验证 HTTP API 成功停止路径。
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", id).Update("status", model.InstanceStatusRunning).Error)

	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/stop", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, "STOPPING", resp["status"])
}

// TestInstance_Stop_InvalidTransition 停止状态的实例再次停止返回错误。
func TestInstance_Stop_InvalidTransition(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "停止失败实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 停止一个已停止的实例应返回错误
	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/stop", nil, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestInstance_Delete_Running_Stopped 测试运行中的实例不能删除，停止后可删除。
func TestInstance_Delete_Running_Stopped(t *testing.T) {
	db := setupTestDB(t)
	r, pool := setupDeleteTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       node.ID,
		"name":         "删除链路实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 直接将状态改为 RUNNING（模拟运行中）
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", id).Update("status", model.InstanceStatusRunning).Error)

	// 运行中的实例不能删除
	w = makeRequest(r, "DELETE", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// 将状态改回 STOPPED
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", id).Update("status", model.InstanceStatusStopped).Error)
	connectDeleteTestWorker(pool, node.UUID)

	// 停止后可删除
	w = makeRequest(r, "DELETE", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 确认已删除
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestInstance_Metrics_Unavailable 实例指标在无 Worker 连接时返回 503。
func TestInstance_Metrics_Unavailable(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "指标实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	created := parseJSON(t, w)
	id := uint(created["id"].(float64))

	// 无 Worker 连接时获取指标应返回 503
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/metrics", nil, token)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// TestInstance_FilterByStatus 实例列表支持按状态过滤。
func TestInstance_FilterByStatus(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)

	// 创建两个实例
	for _, name := range []string{"实例A", "实例B"} {
		body := map[string]interface{}{
			"nodeId":       1,
			"name":         name,
			"type":         "minecraft_java",
			"processType":  "direct",
			"startCommand": "java -jar server.jar",
		}
		w := makeRequest(r, "POST", "/api/v1/instances", body, token)
		require.Equal(t, http.StatusCreated, w.Code)
	}

	// 将第二个实例改为 RUNNING
	w := makeRequest(r, "GET", "/api/v1/instances", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	instances := parseJSONArray(t, w)
	require.Len(t, instances, 2)
	secondID := uint(instances[1].(map[string]interface{})["id"].(float64))
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", secondID).Update("status", model.InstanceStatusRunning).Error)

	// 按 STOPPED 过滤
	w = makeRequest(r, "GET", "/api/v1/instances?status=STOPPED", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 1)
	assert.Equal(t, "实例A", resp[0].(map[string]interface{})["name"])
}

// TestInstance_FilterByNode 实例列表支持按节点过滤。
func TestInstance_FilterByNode(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNode(t, db)
	createTestNodeWithSuffix(t, db, "node-2")

	// 在节点 1 创建实例
	body := map[string]interface{}{
		"nodeId":       1,
		"name":         "节点1实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)

	// 在节点 2 创建实例
	body["nodeId"] = 2
	body["name"] = "节点2实例"
	w = makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)

	// 按节点 1 过滤
	w = makeRequest(r, "GET", "/api/v1/instances?nodeId=1", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 1)
	assert.Equal(t, "节点1实例", resp[0].(map[string]interface{})["name"])
}
