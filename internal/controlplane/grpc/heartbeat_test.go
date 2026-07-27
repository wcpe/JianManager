package grpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type heartbeatTestStream struct {
	grpc.ServerStream
	ctx  context.Context
	reqs []*workerpb.HeartbeatRequest
	sent []*workerpb.HeartbeatResponse
}

func newHeartbeatTestStream(ctx context.Context, reqs ...*workerpb.HeartbeatRequest) *heartbeatTestStream {
	return &heartbeatTestStream{ctx: ctx, reqs: reqs}
}

func (s *heartbeatTestStream) Context() context.Context {
	return s.ctx
}

func (s *heartbeatTestStream) Recv() (*workerpb.HeartbeatRequest, error) {
	if len(s.reqs) == 0 {
		return nil, io.EOF
	}
	req := s.reqs[0]
	s.reqs = s.reqs[1:]
	return req, nil
}

func (s *heartbeatTestStream) Send(resp *workerpb.HeartbeatResponse) error {
	s.sent = append(s.sent, resp)
	return nil
}

func ctxWithHeartbeatSecret(secret string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{nodeSecretHeader: secret}))
}

func newHeartbeatHandler(t *testing.T) (*cpgrpc.ControlPlaneHandler, *gorm.DB) {
	t.Helper()
	h, db, _ := newIdentityRegisterHandler(t)
	require.NoError(t, db.AutoMigrate(&model.Instance{}))
	return h, db
}

func seedHeartbeatNode(t *testing.T, db *gorm.DB, statusValue model.NodeStatus, lastHeartbeat time.Time) *model.Node {
	t.Helper()
	node := &model.Node{
		UUID:          "node-heartbeat-test",
		Name:          "heartbeat-node",
		Host:          "127.0.0.1",
		GRPCPort:      0,
		WSPort:        0,
		Secret:        "node-secret-ok",
		Status:        statusValue,
		LastHeartbeat: &lastHeartbeat,
	}
	require.NoError(t, db.Create(node).Error)
	return node
}

// TestHeartbeat_ValidSecretUpdatesNode 覆盖 FR-029：正确 node-secret 的心跳会把节点置在线并更新资源指标。
func TestHeartbeat_ValidSecretUpdatesNode(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	oldHeartbeat := time.Now().Add(-2 * time.Minute)
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, oldHeartbeat)

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"), &workerpb.HeartbeatRequest{
		NodeUuid:         node.UUID,
		CpuUsage:         0.42,
		MemoryUsage:      0.51,
		DiskUsage:        0.63,
		MemoryUsedMb:     1024,
		DiskUsedMb:       2048,
		NetworkBytesSent: 12345,
		NetworkBytesRecv: 67890,
		LoadAvg1:         1.25,
	})

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF), "单次测试流处理完一拍后应以 EOF 退出")
	require.Len(t, stream.sent, 1, "成功心跳应返回一条响应")
	require.NotZero(t, stream.sent[0].Timestamp)

	var fromDB model.Node
	require.NoError(t, db.Where("uuid = ?", node.UUID).First(&fromDB).Error)
	require.Equal(t, model.NodeStatusOnline, fromDB.Status)
	require.NotNil(t, fromDB.LastHeartbeat)
	require.True(t, fromDB.LastHeartbeat.After(oldHeartbeat), "心跳时间应推进")
	require.InDelta(t, 0.42, float64(fromDB.CPUUsage), 0.001)
	require.InDelta(t, 0.51, float64(fromDB.MemoryUsage), 0.001)
	require.InDelta(t, 0.63, float64(fromDB.DiskUsage), 0.001)
	require.Equal(t, int64(1024), fromDB.MemoryUsedMB)
	require.Equal(t, int64(2048), fromDB.DiskUsedMB)
	require.Equal(t, int64(12345), fromDB.NetworkBytesSent)
	require.Equal(t, int64(67890), fromDB.NetworkBytesRecv)
	require.InDelta(t, 1.25, fromDB.LoadAvg1, 0.001)
}

// TestHeartbeat_KeepsDamagedOnStoppedReport 覆盖 FR-342 真机回归：DAMAGED（搭建失败损毁）是
// CP 侧生命周期态，Worker 不感知、对已注册但未运行的实例上报 STOPPED。心跳同步不得把
// DAMAGED 降级为 STOPPED（否则损毁徽章与启动守卫在下一个心跳即失效）；上报运行类状态
//（进程确实活着）时才允许覆盖。
func TestHeartbeat_KeepsDamagedOnStoppedReport(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	node := seedHeartbeatNode(t, db, model.NodeStatusOnline, time.Now())

	damaged := &model.Instance{
		UUID: "inst-damaged", Name: "fr342-damaged", NodeID: node.ID,
		Status: model.InstanceStatusDamaged, StatusReason: "搭建未完成：下载核心失败",
	}
	require.NoError(t, db.Create(damaged).Error)

	// Worker 上报 STOPPED：损毁态必须保留。
	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"), &workerpb.HeartbeatRequest{
		NodeUuid:  node.UUID,
		Instances: []*workerpb.InstanceState{{InstanceUuid: damaged.UUID, State: "STOPPED"}},
	})
	require.True(t, errors.Is(h.Heartbeat(stream), io.EOF))

	var fromDB model.Instance
	require.NoError(t, db.Where("uuid = ?", damaged.UUID).First(&fromDB).Error)
	require.Equal(t, model.InstanceStatusDamaged, fromDB.Status, "心跳上报 STOPPED 不得降级 DAMAGED")

	// Worker 上报 RUNNING（进程确实活着）：允许覆盖损毁态，以 Worker 实况为准。
	stream2 := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"), &workerpb.HeartbeatRequest{
		NodeUuid:  node.UUID,
		Instances: []*workerpb.InstanceState{{InstanceUuid: damaged.UUID, State: "RUNNING"}},
	})
	require.True(t, errors.Is(h.Heartbeat(stream2), io.EOF))
	require.NoError(t, db.Where("uuid = ?", damaged.UUID).First(&fromDB).Error)
	require.Equal(t, model.InstanceStatusRunning, fromDB.Status, "运行类上报应照常同步")
}

// TestHeartbeat_WrongSecretRejected 覆盖 FR-029：错误 node-secret 的心跳必须拒绝，且不得更新节点状态。
func TestHeartbeat_WrongSecretRejected(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	oldHeartbeat := time.Now().Add(-2 * time.Minute)
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, oldHeartbeat)

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("wrong-secret"), &workerpb.HeartbeatRequest{
		NodeUuid: node.UUID,
		CpuUsage: 0.99,
	})

	err := h.Heartbeat(stream)
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Empty(t, stream.sent, "鉴权失败不得返回成功响应")

	var fromDB model.Node
	require.NoError(t, db.Where("uuid = ?", node.UUID).First(&fromDB).Error)
	require.Equal(t, model.NodeStatusOffline, fromDB.Status)
	require.InDelta(t, 0, float64(fromDB.CPUUsage), 0.001)
	require.Equal(t, oldHeartbeat.Unix(), fromDB.LastHeartbeat.Unix())
}

// TestHeartbeat_MissingSecretRejected 验证匿名心跳不能改变节点状态或触发后续副作用。
func TestHeartbeat_MissingSecretRejected(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	oldHeartbeat := time.Now().Add(-2 * time.Minute)
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, oldHeartbeat)

	stream := newHeartbeatTestStream(context.Background(), &workerpb.HeartbeatRequest{
		NodeUuid: node.UUID,
		CpuUsage: 0.99,
	})

	err := h.Heartbeat(stream)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, stream.sent)

	var fromDB model.Node
	require.NoError(t, db.Where("uuid = ?", node.UUID).First(&fromDB).Error)
	require.Equal(t, model.NodeStatusOffline, fromDB.Status)
	require.InDelta(t, 0, float64(fromDB.CPUUsage), 0.001)
	require.Equal(t, oldHeartbeat.Unix(), fromDB.LastHeartbeat.Unix())
}

// TestHeartbeat_BindsStreamToFirstAuthenticatedUUID 同一心跳流不得借共享密钥切换节点身份。
func TestHeartbeat_BindsStreamToFirstAuthenticatedUUID(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	first := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))
	second := &model.Node{
		UUID: "node-second-heartbeat", Name: "heartbeat-node-second", Host: "127.0.0.2", GRPCPort: 0, WSPort: 0,
		Secret: "node-secret-ok", Status: model.NodeStatusOffline,
	}
	require.NoError(t, db.Create(second).Error)

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"),
		&workerpb.HeartbeatRequest{NodeUuid: first.UUID, CpuUsage: 0.2},
		&workerpb.HeartbeatRequest{NodeUuid: second.UUID, CpuUsage: 0.9},
	)

	err := h.Heartbeat(stream)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Len(t, stream.sent, 1)

	var fromDB model.Node
	require.NoError(t, db.Where("uuid = ?", second.UUID).First(&fromDB).Error)
	require.Equal(t, model.NodeStatusOffline, fromDB.Status)
	require.InDelta(t, 0, float64(fromDB.CPUUsage), 0.001)
}

// TestOfflineDetector_MarksStaleNodeOffline 覆盖 FR-029：超过 90 秒无心跳的在线节点会被标记离线。
func TestOfflineDetector_MarksStaleNodeOffline(t *testing.T) {
	_, db := newHeartbeatHandler(t)
	stale := time.Now().Add(-2 * time.Minute)
	fresh := time.Now()

	staleNode := seedHeartbeatNode(t, db, model.NodeStatusOnline, stale)
	freshNode := &model.Node{
		UUID: "node-fresh-heartbeat", Name: "fresh-node", Host: "127.0.0.1", GRPCPort: 0, WSPort: 0,
		Secret: "fresh-secret", Status: model.NodeStatusOnline, LastHeartbeat: &fresh,
	}
	require.NoError(t, db.Create(freshNode).Error)

	service.NewNodeService(db).CheckOfflineNodes()

	var staleFromDB model.Node
	require.NoError(t, db.Where("uuid = ?", staleNode.UUID).First(&staleFromDB).Error)
	require.Equal(t, model.NodeStatusOffline, staleFromDB.Status)

	var freshFromDB model.Node
	require.NoError(t, db.Where("uuid = ?", freshNode.UUID).First(&freshFromDB).Error)
	require.Equal(t, model.NodeStatusOnline, freshFromDB.Status)
}

// orphanObserveSpy 记录反向对账入口调用（FR-326），不落库。
type orphanObserveSpy struct {
	nodes []string
	sizes []int
}

func (s *orphanObserveSpy) ObserveHeartbeat(nodeUUID string, reported []*workerpb.InstanceState) {
	s.nodes = append(s.nodes, nodeUUID)
	s.sizes = append(s.sizes, len(reported))
}

// TestHeartbeat_InvokesOrphanReverseReconcile 正向对账之后注入的反向对账观察被调用；
// 未注入时心跳仍成功（兼容/测试默认）。
func TestHeartbeat_InvokesOrphanReverseReconcile(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	node := seedHeartbeatNode(t, db, model.NodeStatusOnline, time.Now())
	spy := &orphanObserveSpy{}
	h.SetOrphanRuntimeIngester(spy)

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"), &workerpb.HeartbeatRequest{
		NodeUuid: node.UUID,
		Instances: []*workerpb.InstanceState{
			{InstanceUuid: "ghost", State: "RUNNING", Pid: 99},
		},
	})
	require.True(t, errors.Is(h.Heartbeat(stream), io.EOF))
	require.Equal(t, []string{node.UUID}, spy.nodes)
	require.Equal(t, []int{1}, spy.sizes)

	// 未注入：旧行为不崩（同一测试用独立 handler，避免与固定 node UUID 冲突）。
	h2, db2 := newHeartbeatHandlerWithoutSharedName(t)
	n2 := seedHeartbeatNode(t, db2, model.NodeStatusOnline, time.Now())
	stream2 := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"), &workerpb.HeartbeatRequest{
		NodeUuid: n2.UUID,
	})
	require.True(t, errors.Is(h2.Heartbeat(stream2), io.EOF))
}

// newHeartbeatHandlerWithoutSharedName 用唯一 DSN，避免同测试内多次 newHeartbeatHandler 撞 shared memory。
func newHeartbeatHandlerWithoutSharedName(t *testing.T) (*cpgrpc.ControlPlaneHandler, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "-orphan-no-ingester?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeEnrollToken{}, &model.Instance{}))
	h := cpgrpc.NewControlPlaneHandler(db, cpgrpc.NewClientPool())
	return h, db
}
