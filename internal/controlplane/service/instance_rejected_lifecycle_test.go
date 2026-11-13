package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
)

type rejectedLifecycleWorker struct {
	uncertainLifecycleWorker
}

func (w *rejectedLifecycleWorker) StartInstance(context.Context, *workerpb.InstanceActionRequest, ...grpc.CallOption) (*workerpb.InstanceActionResponse, error) {
	if w.beforeReturn != nil {
		w.beforeReturn()
	}
	return &workerpb.InstanceActionResponse{Success: false, Error: "旧部署残留需接管/清理"}, nil
}

// Worker 明确返回失败时，状态查询只确认运行态，不得把已知的失败结果改写成结果未知。
func TestLifecycleRejectedStartKeepsDefiniteFailure(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, queryState := range []string{"CRASHED", "", "WEIRD", "RUNNING"} {
			t.Run(map[bool]string{false: "单实例", true: "批量"}[batch]+"/"+queryState, func(t *testing.T) {
				db := newNodeTestDB(t)
				require.NoError(t, db.AutoMigrate(&model.Task{}))
				node := newTestNode(t, db, "节点")
				pool := cpgrpc.NewClientPool()
				w := &rejectedLifecycleWorker{uncertainLifecycleWorker: uncertainLifecycleWorker{queryState: queryState}}
				pool.SetWorkerClientForTest(node.UUID, w)
				svc := NewInstanceService(db, NewGroupService(db), pool)
				defer svc.Shutdown()
				inst := &model.Instance{NodeID: node.ID, Node: *node, Name: "实例", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDocker, StartCommand: "sleep 60", Status: model.InstanceStatusStarting}
				require.NoError(t, db.Create(inst).Error)
				if batch {
					b := NewInstanceBatchService(db, pool)
					require.ErrorContains(t, b.delegateBatchOne(InstanceBatchRequest{Action: InstanceBatchStart}, inst), "旧部署残留需接管/清理")
				} else {
					svc.delegateToWorker(inst, "start")
				}
				got, err := svc.GetByID(inst.ID)
				require.NoError(t, err)
				require.NotContains(t, got.StatusReason, "操作结果待确认")
				require.Contains(t, got.StatusReason, "启动失败")
				require.Contains(t, got.StatusReason, "旧部署残留需接管/清理")
				require.Empty(t, got.StatusReasonSource)
				switch queryState {
				case "CRASHED", "RUNNING":
					require.Equal(t, model.InstanceStatus(queryState), got.Status)
				default:
					require.Equal(t, model.InstanceStatusStarting, got.Status)
					require.Contains(t, got.StatusReason, "操作失败，状态待心跳收敛")
				}
			})
		}
	}
}
