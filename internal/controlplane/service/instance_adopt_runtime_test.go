package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// adoptCaptureWorker 记录 AdoptForeignRuntime 调用并可控失败，覆盖 CP 侧接管编排（FR-471）。
type adoptCaptureWorker struct {
	workerpb.WorkerServiceClient
	// resp 非 nil 时作为成功/失败响应返回；nil 则按 rpcErr 处理。
	resp   *workerpb.InstanceActionResponse
	rpcErr error

	calls   int
	lastReq *workerpb.InstanceActionRequest
}

func (w *adoptCaptureWorker) AdoptForeignRuntime(_ context.Context, in *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	w.calls++
	w.lastReq = in
	if w.rpcErr != nil {
		return nil, w.rpcErr
	}
	return w.resp, nil
}

// newDriftInstance 造一个带漂移标记的 STOPPED 实例（模拟「平台记 STOPPED、磁盘在跑」）。
func newDriftInstance(t *testing.T, db *gorm.DB, nodeID uint, name string) *model.Instance {
	t.Helper()
	driftAt := time.Now().Add(-time.Minute)
	inst := &model.Instance{
		NodeID: nodeID, Name: name, Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "./server",
		Status:              model.InstanceStatusStopped,
		StatusReason:        "旧原因",
		RuntimeDriftPID:     4242,
		RuntimeDriftCmdline: "tmux: ./java -jar server.jar",
		RuntimeDriftAt:      &driftAt,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestAdoptForeignRuntime_SuccessClearsDrift 成功接管：RPC 成功 → 清漂移、置 RUNNING、清旧原因。
func TestAdoptForeignRuntime_SuccessClearsDrift(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &adoptCaptureWorker{resp: &workerpb.InstanceActionResponse{Success: true}}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := newDriftInstance(t, db, node.ID, "adopt-ok")

	require.NoError(t, svc.AdoptForeignRuntime(inst.ID))
	require.Equal(t, 1, worker.calls, "必须向 Worker 下发一次接管 RPC")
	require.NotNil(t, worker.lastReq)
	require.Equal(t, inst.UUID, worker.lastReq.InstanceUuid)

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status, "接管成功后应即时置 RUNNING")
	require.Zero(t, got.RuntimeDriftPID, "漂移 PID 必须清零")
	require.Empty(t, got.RuntimeDriftCmdline, "漂移命令行必须清空")
	require.Nil(t, got.RuntimeDriftAt, "漂移时间必须置空")
	require.Empty(t, got.StatusReason, "接管成功应清空旧的状态原因")
}

// TestAdoptForeignRuntime_WorkerFailureWritesReason 响应 Success=false：返回错误 + 写 status_reason + 保留漂移。
func TestAdoptForeignRuntime_WorkerFailureWritesReason(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &adoptCaptureWorker{resp: &workerpb.InstanceActionResponse{Success: false, Error: "无飘移进程"}}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := newDriftInstance(t, db, node.ID, "adopt-fail-resp")

	err := svc.AdoptForeignRuntime(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "无飘移进程")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "失败不得改变状态")
	require.Contains(t, got.StatusReason, "接管外来运行时失败", "失败原因必须落 status_reason")
	require.EqualValues(t, 4242, got.RuntimeDriftPID, "失败时漂移标记不得被清")
}

// TestAdoptForeignRuntime_RPCErrorWritesReason RPC 传输层错误：同样写原因并返回错误。
func TestAdoptForeignRuntime_RPCErrorWritesReason(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	worker := &adoptCaptureWorker{rpcErr: errors.New("连接中断")}
	pool.SetWorkerClientForTest(node.UUID, worker)

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := newDriftInstance(t, db, node.ID, "adopt-fail-rpc")

	err := svc.AdoptForeignRuntime(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "连接中断")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Contains(t, got.StatusReason, "接管外来运行时失败")
	require.EqualValues(t, 4242, got.RuntimeDriftPID)
}

// TestAdoptForeignRuntime_NodeOffline 节点未连接：返回 ErrNodeOffline，不触碰实例。
func TestAdoptForeignRuntime_NodeOffline(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool() // 未注入任何 Worker 客户端 → 视为离线

	svc := NewInstanceService(db, NewGroupService(db), pool)
	inst := newDriftInstance(t, db, node.ID, "adopt-offline")

	err := svc.AdoptForeignRuntime(inst.ID)
	require.ErrorIs(t, err, ErrNodeOffline)
}

// TestAdoptForeignRuntime_InstanceNotFound 实例不存在：返回实例不存在错误。
func TestAdoptForeignRuntime_InstanceNotFound(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &adoptCaptureWorker{resp: &workerpb.InstanceActionResponse{Success: true}})

	svc := NewInstanceService(db, NewGroupService(db), pool)

	err := svc.AdoptForeignRuntime(99999)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}
