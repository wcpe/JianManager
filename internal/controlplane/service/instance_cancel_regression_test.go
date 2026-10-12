package service

import (
	"context"
	"github.com/stretchr/testify/require"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

type uncertainLifecycleWorker struct {
	workerpb.WorkerServiceClient
	actionErr     error
	queryState    string
	beforeReturn  func()
	waitForCancel bool
	entered       chan struct{}
}

func (w *uncertainLifecycleWorker) CreateInstance(context.Context, *workerpb.CreateInstanceRequest, ...grpc.CallOption) (*workerpb.CreateInstanceResponse, error) {
	return &workerpb.CreateInstanceResponse{Success: true}, nil
}
func (w *uncertainLifecycleWorker) StartInstance(ctx context.Context, _ *workerpb.InstanceActionRequest, _ ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	if w.waitForCancel {
		close(w.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if w.beforeReturn != nil {
		w.beforeReturn()
	}
	return nil, w.actionErr
}
func (w *uncertainLifecycleWorker) RestartInstance(ctx context.Context, r *workerpb.InstanceActionRequest, o ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return w.StartInstance(ctx, r, o...)
}
func (w *uncertainLifecycleWorker) StopInstance(ctx context.Context, r *workerpb.InstanceActionRequest, o ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	return w.StartInstance(ctx, r, o...)
}
func (w *uncertainLifecycleWorker) GetInstanceStatus(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.GetInstanceStatusResponse, error) {
	if w.queryState == "" {
		return nil, status.Error(codes.Unavailable, "未知")
	}
	return &workerpb.GetInstanceStatusResponse{State: w.queryState}, nil
}

// CP 关闭与批量请求取消必须传入 Worker；返回不确定时现有状态查询可确认真实结果。
func TestRV016CPContextAndQueryConvergence(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "控制面关闭", true: "批量取消"}[batch], func(t *testing.T) {
			db := newNodeTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Task{}))
			node := newTestNode(t, db, "节点")
			pool := cpgrpc.NewClientPool()
			w := &uncertainLifecycleWorker{waitForCancel: true, entered: make(chan struct{}), queryState: "RUNNING"}
			pool.SetWorkerClientForTest(node.UUID, w)
			svc := NewInstanceService(db, NewGroupService(db), pool)
			inst := &model.Instance{NodeID: node.ID, Node: *node, Name: "实例", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDocker, StartCommand: "sleep 60", Status: model.InstanceStatusStarting}
			require.NoError(t, db.Create(inst).Error)
			done := make(chan error, 1)
			if batch {
				b := NewInstanceBatchService(db, pool)
				b.SetInstanceService(svc)
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					_, err := b.BatchContext(ctx, InstanceBatchRequest{Action: InstanceBatchRestart, IDs: []uint{inst.ID}}, nil, false)
					done <- err
				}()
				<-w.entered
				cancel()
			} else {
				require.True(t, svc.spawnDelegate(inst, "start", 0, func() {}))
				<-w.entered
				go func() { svc.Shutdown(); done <- nil }()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("CP ctx 未贯穿 Worker 委托")
			}
			svc.Shutdown()
			got, err := svc.GetByID(inst.ID)
			require.NoError(t, err)
			require.Equal(t, model.InstanceStatusRunning, got.Status, "响应取消后应通过现有状态查询收敛实际已启动事实")
		})
	}
}

// 取消或传输断开并不证明服务器已崩溃；查询失败时保留待确认状态。
func TestRV016CPUncertainLifecycleNotCrashed(t *testing.T) {
	for _, code := range []codes.Code{codes.Canceled, codes.DeadlineExceeded, codes.Unavailable} {
		for _, batch := range []bool{false, true} {
			t.Run(code.String()+map[bool]string{false: "单实例", true: "批量"}[batch], func(t *testing.T) {
				db := newNodeTestDB(t)
				require.NoError(t, db.AutoMigrate(&model.Task{}))
				node := newTestNode(t, db, "节点")
				pool := cpgrpc.NewClientPool()
				w := &uncertainLifecycleWorker{actionErr: status.Error(code, "请求结果未知")}
				pool.SetWorkerClientForTest(node.UUID, w)
				svc := NewInstanceService(db, NewGroupService(db), pool)
				defer svc.Shutdown()
				inst := &model.Instance{NodeID: node.ID, Node: *node, Name: "实例", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDocker, StartCommand: "sleep 60", Status: model.InstanceStatusStarting}
				require.NoError(t, db.Create(inst).Error)
				if batch {
					b := NewInstanceBatchService(db, pool)
					b.SetInstanceService(svc)
					require.Error(t, b.delegateBatchOne(InstanceBatchRequest{Action: InstanceBatchRestart}, inst))
				} else {
					svc.delegateToWorker(inst, "start")
				}
				got, err := svc.GetByID(inst.ID)
				require.NoError(t, err)
				require.Equal(t, model.InstanceStatusStarting, got.Status, "未知传输结果应由状态查询/心跳收敛而不是置崩溃")
			})
		}
	}
}

// 查询确认已启动可收敛，但不得覆盖 RPC 返回前的新心跳状态。
func TestRV016CPLateFailureDoesNotOverwriteHeartbeat(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "单实例", true: "批量"}[batch], func(t *testing.T) {
			db := newNodeTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.Task{}))
			node := newTestNode(t, db, "节点")
			pool := cpgrpc.NewClientPool()
			svc := NewInstanceService(db, NewGroupService(db), pool)
			defer svc.Shutdown()
			inst := &model.Instance{NodeID: node.ID, Node: *node, Name: "实例", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDocker, StartCommand: "sleep 60", Status: model.InstanceStatusStarting}
			require.NoError(t, db.Create(inst).Error)
			w := &uncertainLifecycleWorker{actionErr: status.Error(codes.Unavailable, "响应丢失"), queryState: "RUNNING", beforeReturn: func() { require.NoError(t, db.Model(inst).Update("status", model.InstanceStatusStopped).Error) }}
			pool.SetWorkerClientForTest(node.UUID, w)
			if batch {
				b := NewInstanceBatchService(db, pool)
				b.SetInstanceService(svc)
				require.Error(t, b.delegateBatchOne(InstanceBatchRequest{Action: InstanceBatchRestart}, inst))
			} else {
				svc.delegateToWorker(inst, "start")
			}
			got, err := svc.GetByID(inst.ID)
			require.NoError(t, err)
			require.Equal(t, model.InstanceStatusStopped, got.Status)
		})
	}
}
