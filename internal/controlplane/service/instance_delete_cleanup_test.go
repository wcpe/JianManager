package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

type blockingStartDeleteWorker struct {
	workerpb.WorkerServiceClient
	startEntered chan struct{}
	allowStart   chan struct{}
	removeCalled chan struct{}
	removeOnce   sync.Once
}

type blockingDeleteResyncWorker struct {
	workerpb.WorkerServiceClient
	removeEntered  chan struct{}
	allowRemove    chan struct{}
	resyncCalled   chan struct{}
	registerCalled chan struct{}
	startCalled    chan struct{}
	registerOnce   sync.Once
	resyncOnce     sync.Once
	startOnce      sync.Once
}

func (f *blockingStartDeleteWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{}, nil
}

func (f *blockingStartDeleteWorker) PreflightStartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (f *blockingStartDeleteWorker) StartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	close(f.startEntered)
	<-f.allowStart
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (f *blockingStartDeleteWorker) StopInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func (f *blockingStartDeleteWorker) RemoveInstance(context.Context, *workerpb.RemoveInstanceRequest, ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	f.removeOnce.Do(func() { close(f.removeCalled) })
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

func (f *blockingDeleteResyncWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	f.registerOnce.Do(func() { close(f.registerCalled) })
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}

func (f *blockingDeleteResyncWorker) RemoveInstance(context.Context, *workerpb.RemoveInstanceRequest, ...grpc.CallOption) (*workerpb.RemoveInstanceResponse, error) {
	close(f.removeEntered)
	<-f.allowRemove
	return &workerpb.RemoveInstanceResponse{Success: true}, nil
}

func (f *blockingDeleteResyncWorker) ResyncInstances(context.Context, *workerpb.ResyncInstancesRequest, ...grpc.CallOption) (*workerpb.ResyncInstancesResponse, error) {
	f.resyncOnce.Do(func() { close(f.resyncCalled) })
	return &workerpb.ResyncInstancesResponse{}, nil
}

func (f *blockingDeleteResyncWorker) StartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	f.startOnce.Do(func() { close(f.startCalled) })
	return &workerpb.InstanceActionResponse{Success: true}, nil
}

func newDeleteCleanupEnv(t *testing.T) (*InstanceService, *cpgrpc.ClientPool, *model.Node, *model.Instance) {
	t.Helper()
	db := newCloneTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NetworkMember{}, &model.Task{}, &model.InstanceCrashSnapshot{}))
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

// 节点未连接时无法确认 Worker 数据已清理，必须中止并保留实例记录。
func TestDeleteInstanceAbortsWhenNodeDisconnected(t *testing.T) {
	svc, _, _, inst := newDeleteCleanupEnv(t)

	err := svc.Delete(inst.ID)
	require.ErrorIs(t, err, ErrNodeOffline)
	require.Contains(t, err.Error(), "实例记录保留")
	got, getErr := svc.GetByID(inst.ID)
	require.NoError(t, getErr)
	require.Equal(t, inst.ID, got.ID)
}

// 节点状态已离线时即使连接池残留客户端，也不得调用 Worker 或删除 CP 记录。
func TestDeleteInstanceAbortsWhenNodeStatusOffline(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &fakeRemoveWorker{}
	pool.SetWorkerClientForTest(node.UUID, worker)
	require.NoError(t, svc.db.Model(node).Update("status", model.NodeStatusOffline).Error)

	err := svc.Delete(inst.ID)
	require.ErrorIs(t, err, ErrNodeOffline)
	require.Empty(t, worker.removed, "离线节点不得使用连接池中的过期客户端清理")
	_, getErr := svc.GetByID(inst.ID)
	require.NoError(t, getErr)
}

// 节点记录缺失时无法路由 Worker 清理，必须返回明确错误并保留实例。
func TestDeleteInstanceAbortsWhenNodeRecordMissing(t *testing.T) {
	svc, _, node, inst := newDeleteCleanupEnv(t)
	require.NoError(t, svc.db.Unscoped().Delete(node).Error)

	err := svc.Delete(inst.ID)
	require.ErrorIs(t, err, ErrNodeOffline)
	require.Contains(t, err.Error(), "节点记录不存在")
	_, getErr := svc.GetByID(inst.ID)
	require.NoError(t, getErr)
}

// 删除必须等待同实例在途启动委托结束，禁止先清理并删除记录后又被旧启动指针重新注册。
func TestDeleteInstanceWaitsForInFlightStartDelegate(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &blockingStartDeleteWorker{
		startEntered: make(chan struct{}),
		allowStart:   make(chan struct{}),
		removeCalled: make(chan struct{}),
	}
	pool.SetWorkerClientForTest(node.UUID, worker)

	require.NoError(t, svc.Start(inst.ID))
	select {
	case <-worker.startEntered:
	case <-time.After(time.Second):
		t.Fatal("启动委托未进入 Worker")
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- svc.Delete(inst.ID) }()
	removedPrematurely := false
	select {
	case <-worker.removeCalled:
		removedPrematurely = true
	case <-time.After(100 * time.Millisecond):
	}

	close(worker.allowStart)
	require.NoError(t, <-deleteDone)
	require.False(t, removedPrematurely, "在途启动尚未结束时不得清理实例")
	_, err := svc.GetByID(inst.ID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

// 删除先持锁时，重连重推必须在锁内重读并剔除已删实例，不能把旧快照重新登记到 Worker。
func TestResyncNodeSkipsInstanceDeletedWhileWaiting(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &blockingDeleteResyncWorker{
		removeEntered:  make(chan struct{}),
		allowRemove:    make(chan struct{}),
		resyncCalled:   make(chan struct{}),
		registerCalled: make(chan struct{}),
	}
	pool.SetWorkerClientForTest(node.UUID, worker)

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- svc.Delete(inst.ID) }()
	select {
	case <-worker.removeEntered:
	case <-time.After(time.Second):
		t.Fatal("删除未进入 Worker 清理")
	}

	resyncDone := make(chan struct{})
	go func() {
		svc.ResyncNode(node.UUID)
		close(resyncDone)
	}()
	require.Eventually(t, func() bool {
		svc.operationLocksMu.Lock()
		defer svc.operationLocksMu.Unlock()
		lock := svc.operationLocks[inst.ID]
		return lock != nil && lock.refs == 2
	}, time.Second, 10*time.Millisecond, "重连重推应已读取旧快照并等待实例锁")
	close(worker.allowRemove)

	require.NoError(t, <-deleteDone)
	select {
	case <-resyncDone:
	case <-time.After(time.Second):
		t.Fatal("重连重推未结束")
	}
	select {
	case <-worker.resyncCalled:
		t.Fatal("已删除实例不得从旧快照重推到 Worker")
	default:
	}
}

// 配置更新持有同一生命周期锁，不能在删除后用旧快照重注册已删除实例。
func TestBatchStartWaitsForDeleteLock(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &blockingDeleteResyncWorker{
		removeEntered:  make(chan struct{}),
		allowRemove:    make(chan struct{}),
		resyncCalled:   make(chan struct{}),
		registerCalled: make(chan struct{}),
		startCalled:    make(chan struct{}),
	}
	pool.SetWorkerClientForTest(node.UUID, worker)
	batch := NewInstanceBatchService(svc.db, pool)
	batch.SetInstanceService(svc)

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- svc.Delete(inst.ID) }()
	select {
	case <-worker.removeEntered:
	case <-time.After(time.Second):
		t.Fatal("删除未进入 Worker 清理")
	}

	batchDone := make(chan *InstanceBatchResult, 1)
	go func() {
		result, err := batch.Batch(InstanceBatchRequest{Action: InstanceBatchStart, IDs: []uint{inst.ID}}, nil, false)
		if err != nil {
			batchDone <- &InstanceBatchResult{Failed: 1, Errors: []InstanceBatchError{{Error: err.Error()}}}
			return
		}
		batchDone <- result
	}()
	select {
	case <-worker.startCalled:
		t.Fatal("删除持锁期间不得发送批量启动")
	case <-time.After(100 * time.Millisecond):
	}

	close(worker.allowRemove)
	require.NoError(t, <-deleteDone)
	result := <-batchDone
	require.Equal(t, 1, result.Failed)
	require.Contains(t, result.Errors[0].Error, "实例不存在")
}

// 配置更新持有同一生命周期锁，不能在删除后用旧快照重注册已删除实例。
func TestUpdateInstanceWaitsForDeleteLock(t *testing.T) {
	svc, pool, node, inst := newDeleteCleanupEnv(t)
	worker := &blockingDeleteResyncWorker{
		removeEntered:  make(chan struct{}),
		allowRemove:    make(chan struct{}),
		resyncCalled:   make(chan struct{}),
		registerCalled: make(chan struct{}),
	}
	pool.SetWorkerClientForTest(node.UUID, worker)

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- svc.Delete(inst.ID) }()
	select {
	case <-worker.removeEntered:
	case <-time.After(time.Second):
		t.Fatal("删除未进入 Worker 清理")
	}

	name := "更新后的名称"
	updateDone := make(chan error, 1)
	go func() {
		_, err := svc.Update(inst.ID, UpdateInstanceFields{StartCommand: &name})
		updateDone <- err
	}()
	select {
	case <-worker.registerCalled:
		t.Fatal("删除持锁期间不得重注册实例")
	case <-time.After(100 * time.Millisecond):
	}

	close(worker.allowRemove)
	require.NoError(t, <-deleteDone)
	require.ErrorIs(t, <-updateDone, ErrInstanceNotFound)
}
