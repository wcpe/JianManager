package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeRemoveWorker 记录 RemoveInstance 调用的伪 Worker 客户端。
type fakeRemoveWorker struct {
	workerpb.WorkerServiceClient
	removed  []*workerpb.RemoveInstanceRequest
	respErr  string
	grpcFail bool
}

func (f *fakeRemoveWorker) RemoveInstance(_ context.Context, req *workerpb.RemoveInstanceRequest, _ ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	f.removed = append(f.removed, req)
	if f.grpcFail {
		return nil, errors.New("网络中断")
	}
	if f.respErr != "" {
		return &workerpb.RemoveInstanceResponse{Success: false, Error: f.respErr}, nil
	}
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

func newDeleteCleanupEnv(t *testing.T) (*InstanceService, *cpgrpc.ClientPool, *model.Node, *model.Instance) {
	t.Helper()
	db := newCloneTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NetworkMember{}))
	pool := cpgrpc.NewClientPool()
	svc := NewInstanceService(db, nil, pool)
	t.Cleanup(svc.Shutdown)

	node := &model.Node{UUID: "del-node", Name: "del-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "paper-a", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar",
		Status: model.InstanceStatusStopped, WorkDir: "var/servers/paper-a-8b4cb747",
	}
	require.NoError(t, db.Create(inst).Error)
	return svc, pool, node, inst
}

// 删除实例必须委托 Worker 清理工作目录（复现真机走查缺陷：删除后 worker 目录残留）。
func TestDeleteInstanceRemovesWorkerData(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &fakeRemoveWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Delete(inst.ID))

	require.Len(t, worker.removed, 1, "删除实例必须经 gRPC 让 Worker 清理工作目录")
	require.Equal(t, inst.UUID, worker.removed[0].InstanceUuid)
	require.Equal(t, "var/servers/paper-a-8b4cb747", worker.removed[0].WorkDir)

	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound, "清理成功后记录应删除")
}

// Worker 清理失败时删除必须中止：记录保留，用户可见失败原因（不得静默孤儿化目录）。
func TestDeleteInstanceAbortsWhenWorkerCleanupFails(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &fakeRemoveWorker{respErr: "实例进程仍在运行"}
	pool.SetWorkerClientForTest(node.UUID, worker)

	err := svc.Delete(inst.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "实例进程仍在运行")

	got, gerr := svc.GetByID(inst.ID)
	require.NoError(t, gerr, "清理失败时记录必须保留以便重试")
	require.Equal(t, inst.ID, got.ID)
}

// gRPC 调用失败（节点在线但通信异常）同样中止删除。
func TestDeleteInstanceAbortsWhenWorkerRPCFails(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &fakeRemoveWorker{grpcFail: true}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.Error(t, svc.Delete(inst.ID))
	_, gerr := svc.GetByID(inst.ID)
	require.NoError(t, gerr)
}

// 节点未连接（离线/已失联）时不阻断删除：仅删记录，目录留待节点侧处理。
// 否则失联节点上的实例记录将永远无法删除。
func TestDeleteInstanceProceedsWhenNodeDisconnected(t *testing.T) {
	svc, _, _, inst := newDeleteCleanupEnv(t)

	require.NoError(t, svc.Delete(inst.ID))
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}
