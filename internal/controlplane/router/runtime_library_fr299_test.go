package router

import (
	"context"
	"fmt"
	"net/http"
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

// fakeFR299InstallWorker 假 Worker：InstallRuntime 记录请求并回执成功、RemoveRuntime 成功。
type fakeFR299InstallWorker struct {
	workerpb.WorkerServiceClient
	gotInstall *workerpb.InstallRuntimeRequest
	gotRemove  *workerpb.RemoveRuntimeRequest
}

func (f *fakeFR299InstallWorker) InstallRuntime(_ context.Context, in *workerpb.InstallRuntimeRequest, _ ...grpc.CallOption) (*workerpb.InstallRuntimeResponse, error) {
	f.gotInstall = in
	return &workerpb.InstallRuntimeResponse{Success: true, TaskId: in.TaskId}, nil
}

func (f *fakeFR299InstallWorker) RemoveRuntime(_ context.Context, in *workerpb.RemoveRuntimeRequest, _ ...grpc.CallOption) (*workerpb.RemoveRuntimeResponse, error) {
	f.gotRemove = in
	return &workerpb.RemoveRuntimeResponse{Success: true}, nil
}

func (f *fakeFR299InstallWorker) ListJDKs(context.Context, *workerpb.ListJDKsRequest, ...grpc.CallOption) (*workerpb.ListJDKsResponse, error) {
	return &workerpb.ListJDKsResponse{}, nil
}

// setupFR299Router 组装含任务中心的路由（安装端点需要 TaskService）。
func setupFR299Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := setupFR298Router(t, db, pool)
	return r
}

func TestFR299RuntimeInstallEndpoint(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()

	// 在 FR-298 基础装配上补任务中心注入：直接重建带 tasks 的 Services 太重，
	// 这里复用 setup 后通过服务字段注入（路由持有同一 svc 实例）。
	gin.SetMode(gin.TestMode)
	jdkSvc := service.NewJDKService(db, pool)
	runtimeLibSvc := service.NewRuntimeLibraryService(db, pool, jdkSvc)
	taskSvc := service.NewTaskService(db)
	runtimeLibSvc.SetTaskService(taskSvc)

	svcs := newFR299Services(db, pool, jdkSvc, runtimeLibSvc, taskSvc)
	r := Setup(svcs, "test-secret-key-for-fr299")
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr299", "password123")
	node := createTestNode(t, db)
	fake := &fakeFR299InstallWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	// 非平台管理员 403。
	forbidden := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/install", node.ID), map[string]any{
		"type": "nodejs", "major": 22,
	}, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	// 正常安装：202 + taskId，任务 kind=runtime_install，arch 归一 amd64→x64。
	resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/install", node.ID), map[string]any{
		"type": "nodejs", "major": 22, "arch": "amd64",
	}, adminToken)
	require.Equal(t, http.StatusAccepted, resp.Code, resp.Body.String())
	body := parseJSON(t, resp)
	taskID, _ := body["taskId"].(string)
	require.NotEmpty(t, taskID)
	require.NotNil(t, fake.gotInstall)
	require.Equal(t, "x64", fake.gotInstall.Arch)
	require.Equal(t, taskID, fake.gotInstall.TaskId)

	var task model.Task
	require.NoError(t, db.Where("task_id = ?", taskID).First(&task).Error)
	require.Equal(t, model.TaskKindRuntimeInstall, task.Kind)
	require.Equal(t, model.TaskStateRunning, task.State)

	// 未知 arch 422（FR-289 语义齐平）。
	badArch := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/install", node.ID), map[string]any{
		"type": "nodejs", "major": 22, "arch": "mips64",
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, badArch.Code, badArch.Body.String())

	// 未知类型 422。
	badType := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/install", node.ID), map[string]any{
		"type": "python", "major": 3,
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, badType.Code, badType.Body.String())

	// 节点离线 503。
	offline := createTestNodeWithSuffix(t, db, "fr299-offline")
	offlineResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/install", offline.ID), map[string]any{
		"type": "nodejs", "major": 22,
	}, adminToken)
	require.Equal(t, http.StatusServiceUnavailable, offlineResp.Code, offlineResp.Body.String())

	// 安装写审计。
	var audits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "node.runtime.install").Count(&audits).Error)
	require.GreaterOrEqual(t, audits, int64(1))
}

// 删除托管 nodejs 经统一端点：Worker RemoveRuntime 被调、记录删除（FR-299 语义对齐 JDK）。
func TestFR299DeleteManagedRuntimeRemovesFiles(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR299Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakeFR299InstallWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	rt := &model.NodeRuntime{NodeID: node.ID, Type: "nodejs", Name: "Node.js 22", Version: "22.17.0", Major: 22, Arch: "x64", Path: "/data/opt/runtimes/nodejs-22/bin/node", Managed: true}
	require.NoError(t, db.Create(rt).Error)

	resp := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/runtimes/%d?type=nodejs", node.ID, rt.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	require.NotNil(t, fake.gotRemove, "托管删除应下发 Worker RemoveRuntime")
	require.Equal(t, rt.Path, fake.gotRemove.Path)
	var n int64
	require.NoError(t, db.Model(&model.NodeRuntime{}).Count(&n).Error)
	require.Zero(t, n)
}

// newFR299Services 组装含运行时库 + 任务中心的最小 Services（复用 FR-298 测试基座字段）。
func newFR299Services(db *gorm.DB, pool *cpgrpc.ClientPool, jdkSvc *service.JDKService, runtimeLibSvc *service.RuntimeLibraryService, taskSvc *service.TaskService) *Services {
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr299", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	return &Services{
		Auth:           service.NewAuthService(db, jwtCfg),
		User:           service.NewUserService(db),
		Group:          groupSvc,
		Node:           nodeSvc,
		JDK:            jdkSvc,
		RuntimeLibrary: runtimeLibSvc,
		Task:           taskSvc,
		Instance:       instanceSvc,
		InstanceBatch:  service.NewInstanceBatchService(db, pool),
		Audit:          service.NewAuditService(db),
		Authz:          authzSvc,
	}
}
