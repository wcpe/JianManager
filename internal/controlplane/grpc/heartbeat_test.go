package grpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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
