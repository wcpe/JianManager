package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// FR-441：coreType=binary 经既有 /instances/provision/server 端点分流。
//
// 关键回归保护：mcVersion 从 `binding:"required"` 下移为条件校验后，
// MC 路径缺参**必须仍是 400 INVALID_REQUEST**（既有前端/脚本依赖该行为），
// 而 binary 不带 mcVersion 必须被放行。

// binaryRouteWorker 在既有假 Worker 上补 FetchBinary：binary 路径只用到 CreateInstance + FetchBinary。
type binaryRouteWorker struct {
	fakeFR034RouteWorker
	fetchMu  sync.Mutex
	fetchReq *workerpb.FetchBinaryRequest
}

func (w *binaryRouteWorker) FetchBinary(_ context.Context, in *workerpb.FetchBinaryRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[workerpb.FetchBinaryProgress], error) {
	w.fetchMu.Lock()
	w.fetchReq = in
	w.fetchMu.Unlock()
	return &routeBinaryStream{}, nil
}

func (w *binaryRouteWorker) lastFetch() *workerpb.FetchBinaryRequest {
	w.fetchMu.Lock()
	defer w.fetchMu.Unlock()
	return w.fetchReq
}

// routeBinaryStream 回一帧「成功」终态后 EOF（编排路径只关心终态语义）。
type routeBinaryStream struct {
	grpc.ClientStream
	sent bool
}

func (s *routeBinaryStream) Recv() (*workerpb.FetchBinaryProgress, error) {
	if s.sent {
		return nil, io.EOF
	}
	s.sent = true
	return &workerpb.FetchBinaryProgress{Done: true, Success: true, Size: 1 << 20}, nil
}

// setupFR441Router 在本文件的测试里自建路由：与 FR-034 用例共用大部分装配，
// 差别只在**注入任务中心**——binary 是强制异步的，未接线任务中心时会被明确拒绝
// （见 TestFR441BinaryRequiresTaskService），故端到端用例必须先接线。
func setupFR441Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr441", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	coreSvc := service.NewCoreService()

	taskSvc := service.NewTaskService(db)
	taskSvc.SetNotificationService(service.NewNotificationService(db))
	provSvc := service.NewProvisionService(db, pool, instanceSvc, coreSvc, nil)
	provSvc.SetTaskService(taskSvc)

	return Setup(&Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Group:         groupSvc,
		Node:          nodeSvc,
		Instance:      instanceSvc,
		InstanceBatch: service.NewInstanceBatchService(db, pool),
		Audit:         service.NewAuditService(db),
		Authz:         authzSvc,
		Core:          coreSvc,
		Provision:     provSvc,
	}, jwtCfg.Secret)
}

// TestFR441BinaryRequiresTaskService 未接线任务中心时明确拒绝（强制异步，不降级为同步）。
func TestFR441BinaryRequiresTaskService(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	// 这里刻意用 FR-034 的装配（未接任务中心）。
	r := setupFR034Router(t, db, pool, service.NewCoreService())
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &binaryRouteWorker{})

	resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
		"nodeId": node.ID, "name": "bin-no-tasks", "coreType": "binary",
		"binarySource": map[string]any{"kind": "url", "url": "https://example.com/b", "filename": "b"},
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
	require.Contains(t, parseJSON(t, resp)["message"], "强制异步")
}

// TestFR441ProvisionRequestValidation 参数校验与 MC 路径回归保护。
func TestFR441ProvisionRequestValidation(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR441Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &binaryRouteWorker{})

	t.Run("MC 核心缺 mcVersion 仍为 400（不回归）", func(t *testing.T) {
		resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
			"nodeId": node.ID, "name": "no-version", "coreType": "paper",
		}, adminToken)
		require.Equal(t, http.StatusBadRequest, resp.Code)
		require.Equal(t, "INVALID_REQUEST", parseJSON(t, resp)["error"])
	})

	t.Run("其它必填缺失仍为 400", func(t *testing.T) {
		resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
			"nodeId": node.ID, "coreType": "paper",
		}, adminToken)
		require.Equal(t, http.StatusBadRequest, resp.Code)
	})

	t.Run("binary 缺来源为 422（已过绑定，卡在来源校验）", func(t *testing.T) {
		resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
			"nodeId": node.ID, "name": "bin-no-src", "coreType": "binary",
		}, adminToken)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
		require.Equal(t, "PROVISION_FAILED", parseJSON(t, resp)["error"])
		require.Contains(t, parseJSON(t, resp)["message"], "binarySource.kind")
	})

	t.Run("binary 缺 mcVersion 不被 400 拦下", func(t *testing.T) {
		resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
			"nodeId": node.ID, "name": "bin-ok", "coreType": "binary",
			"binarySource": map[string]any{
				"kind": "url", "url": "https://example.com/beacon", "filename": "beacon-1.1.0-linux-amd64",
			},
		}, adminToken)
		require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())
	})

	t.Run("binary 越界 node_file 为 422 且不建实例", func(t *testing.T) {
		var before int64
		require.NoError(t, db.Model(&model.Instance{}).Count(&before).Error)

		resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
			"nodeId": node.ID, "name": "bin-escape", "coreType": "binary",
			"binarySource": map[string]any{
				"kind": "node_file", "nodePath": "/etc/shadow", "filename": "shadow",
			},
		}, adminToken)
		require.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
		require.Contains(t, parseJSON(t, resp)["message"], "受控放行目录之外")

		var after int64
		require.NoError(t, db.Model(&model.Instance{}).Count(&after).Error)
		require.Equal(t, before, after, "越界来源不应建实例")
	})
}

// TestFR441BinaryProvisionRouteEndToEnd 端到端（真库 + 假 Worker）：走 binary 分支建实例、
// 任务终态成功、实例为 generic/universal/未绑 JDK、启动命令由文件名派生（验收项 4/7/8）。
func TestFR441BinaryProvisionRouteEndToEnd(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR441Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	worker := &binaryRouteWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
		"nodeId": node.ID, "name": "fr441-beacon", "coreType": "binary",
		"binarySource": map[string]any{
			"kind": "url", "url": "https://example.com/beacon", "filename": "beacon-1.1.0-linux-amd64",
		},
	}, adminToken)
	require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())

	var wrapper struct {
		Instance model.Instance `json:"instance"`
		TaskID   string         `json:"taskId"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &wrapper))
	require.NotZero(t, wrapper.Instance.ID)
	require.NotEmpty(t, wrapper.TaskID)
	require.Equal(t, model.InstanceTypeGeneric, wrapper.Instance.Type, "二进制不是 MC Java 进程")
	require.Equal(t, model.InstanceRoleUniversal, wrapper.Instance.Role)
	require.Zero(t, wrapper.Instance.JDKID, "编译型二进制不绑定 JDK")
	require.Equal(t, "./beacon-1.1.0-linux-amd64", wrapper.Instance.StartCommand)

	require.Eventually(t, func() bool {
		var task model.Task
		if err := db.Where("task_id = ?", wrapper.TaskID).First(&task).Error; err != nil {
			return false
		}
		return task.State == model.TaskStateSucceeded
	}, 3*time.Second, 20*time.Millisecond, "二进制搭建任务应进入 succeeded")

	var task model.Task
	require.NoError(t, db.Where("task_id = ?", wrapper.TaskID).First(&task).Error)
	require.Equal(t, model.TaskKindBinaryProvision, task.Kind)

	// 取件请求已下发：目标文件名、来源类别与地址均随请求直达 Worker。
	req := worker.lastFetch()
	require.NotNil(t, req)
	require.Equal(t, "beacon-1.1.0-linux-amd64", req.DestFilename)
	require.Equal(t, "url", req.SourceKind)
	require.Equal(t, "https://example.com/beacon", req.DownloadUrl)
}

// TestFR441BinaryNodeFileDefaultClosed 未配置放行根时 node_file 一律拒绝（默认安全的关闭态）。
func TestFR441BinaryNodeFileDefaultClosed(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR441Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &binaryRouteWorker{})

	resp := makeRequest(r, http.MethodPost, "/api/v1/instances/provision/server", map[string]any{
		"nodeId": node.ID, "name": "nf-default", "coreType": "binary",
		"binarySource": map[string]any{"kind": "node_file", "nodePath": "/opt/binaries/beacon", "filename": "beacon"},
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
	require.Contains(t, parseJSON(t, resp)["message"], "受控放行目录之外")
}
