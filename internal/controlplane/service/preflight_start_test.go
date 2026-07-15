package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeWorkerClient 假 WorkerServiceClient（FR-314 CP 预检测试）：仅覆盖预检/注册/启动三方法，
// 其余走嵌入接口（本测试不触达）。
type fakeWorkerClient struct {
	workerpb.WorkerServiceClient
	preflightResp *workerpb.InstanceActionResponse
	preflightErr  error
}

func (f *fakeWorkerClient) PreflightStartInstance(ctx context.Context, in *workerpb.InstanceActionRequest, opts ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return f.preflightResp, f.preflightErr
}

func (f *fakeWorkerClient) CreateInstance(ctx context.Context, in *workerpb.CreateInstanceRequest, opts ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{}, nil
}

func (f *fakeWorkerClient) StartInstance(ctx context.Context, in *workerpb.InstanceActionRequest, opts ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

// newPreflightFixture 建 db + 节点 + 服务（禁用异步委托）+ 一个可启动实例。
func newPreflightFixture(t *testing.T) (*InstanceService, *cpgrpc.ClientPool, *model.Node, *model.Instance) {
	t.Helper()
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	svc := NewInstanceService(db, NewGroupService(db), pool)
	svc.Shutdown()

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "preflight-inst",
		Type:         model.InstanceTypeMinecraftJava,
		ProcessType:  model.ProcessTypeDaemon,
		StartCommand: "java -jar server.jar",
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	return svc, pool, node, inst
}

// 节点未连接（空连接池）→ 预检同步返回 ErrNodeOffline，状态不进 STARTING。
func TestStartPreflight_NodeOffline(t *testing.T) {
	svc, _, _, inst := newPreflightFixture(t)

	err := svc.Start(inst.ID)
	require.ErrorIs(t, err, ErrNodeOffline)

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusStopped, got.Status)
}

// 预检未过 → 返回 *PreflightError，状态保持 STOPPED（不进 STARTING），statusReason 已写。
func TestStartPreflight_Failed(t *testing.T) {
	svc, pool, node, inst := newPreflightFixture(t)
	pool.SetWorkerClientForTest(node.UUID, &fakeWorkerClient{
		preflightResp: &workerpb.InstanceActionResponse{Success: false, Error: "实例未绑定 JDK 且 PATH 上无可用 java"},
	})

	err := svc.Start(inst.ID)
	var pfErr *PreflightError
	require.ErrorAs(t, err, &pfErr)
	assert.Contains(t, pfErr.Reason, "未绑定 JDK")

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusStopped, got.Status, "预检失败状态不得进 STARTING")
	assert.Contains(t, got.StatusReason, "未绑定 JDK", "预检失败原因应写入 statusReason")
}

// 老 Worker 无该 RPC（Unimplemented）→ 跳过预检，正常进入 STARTING（向后兼容）。
func TestStartPreflight_UnimplementedSkips(t *testing.T) {
	svc, pool, node, inst := newPreflightFixture(t)
	pool.SetWorkerClientForTest(node.UUID, &fakeWorkerClient{
		preflightErr: status.Error(codes.Unimplemented, "unknown method PreflightStartInstance"),
	})

	err := svc.Start(inst.ID)
	require.NoError(t, err)

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusStarting, got.Status)
}

// 预检通过 → 进入 STARTING，走既有异步启动路径。
func TestStartPreflight_OK(t *testing.T) {
	svc, pool, node, inst := newPreflightFixture(t)
	pool.SetWorkerClientForTest(node.UUID, &fakeWorkerClient{
		preflightResp: &workerpb.InstanceActionResponse{Success: true},
	})

	err := svc.Start(inst.ID)
	require.NoError(t, err)

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusStarting, got.Status)
}

// 代理无启用后端注册 → 启动前拦截返回 *PreflightError（杜绝 BungeeCord「No servers defined」崩溃），
// 状态保持 STOPPED、原因写入 statusReason；该校验先于节点连通/Worker RPC（空连接池也应命中）。
func TestStartPreflight_ProxyNoBackends(t *testing.T) {
	svc, _, _, inst := newPreflightFixture(t)
	require.NoError(t, svc.db.Model(inst).Update("role", model.InstanceRoleProxy).Error)

	err := svc.Start(inst.ID)
	var pfErr *PreflightError
	require.ErrorAs(t, err, &pfErr)
	assert.Contains(t, pfErr.Reason, "No servers defined")

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusStopped, got.Status, "代理无后端预检失败状态不得进 STARTING")
	assert.Contains(t, got.StatusReason, "后端", "无后端原因应写入 statusReason")
}

// 代理有启用后端注册 → 放行到 Worker 预检 RPC（fake 放行 → STARTING）。
func TestStartPreflight_ProxyWithBackendPasses(t *testing.T) {
	svc, pool, node, inst := newPreflightFixture(t)
	require.NoError(t, svc.db.Model(inst).Update("role", model.InstanceRoleProxy).Error)

	backend := &model.Instance{
		NodeID: node.ID, Name: "backend-1", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar",
		Status: model.InstanceStatusStopped, Role: model.InstanceRoleBackend,
	}
	require.NoError(t, svc.db.Create(backend).Error)
	require.NoError(t, svc.db.Create(&model.ServerRegistration{
		ProxyID: inst.ID, BackendID: backend.ID, Alias: "b1", Enabled: true,
	}).Error)

	pool.SetWorkerClientForTest(node.UUID, &fakeWorkerClient{
		preflightResp: &workerpb.InstanceActionResponse{Success: true},
	})

	err := svc.Start(inst.ID)
	require.NoError(t, err)

	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InstanceStatusStarting, got.Status)
}
