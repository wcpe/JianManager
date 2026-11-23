package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Shutdown 后生命周期请求必须**明确失败**，不再假装受理。
//
// 回归守卫（与 restart 假成功同源）：旧实现在此丢弃 spawnDelegate 的返回值并 return nil，
// 调用方拿到「成功」而 Worker 侧一步都没执行、实例停在过渡态直到心跳纠正。现在关闭中的请求
// 返回错误，调用方（含审批执行器）能立刻知道「本次动作未执行」，而不必等 uptime 对比才发现。
//
// 注：测试中「禁用异步委托、只观测同步状态转换」的诉求改用 SetDelegateBypassForTest ——
// 它同样不产生异步副作用，但不把服务置为关闭态。
func TestInstanceService_Shutdown_RejectsLifecycleActions(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	svc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	svc.Shutdown()

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "run",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "x",
		Status:       model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)

	err := svc.Stop(inst.ID)
	require.Error(t, err, "控制面关闭中不得假装受理生命周期动作")
	require.Contains(t, err.Error(), "控制面正在关闭", "错误须说明「本次动作未执行」，而非笼统失败")

	// 同步状态转换仍已发生，但不允许任何异步回写覆盖它。
	time.Sleep(150 * time.Millisecond)

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status)
}

// TestInstanceService_DelegateBypass_KeepsAsyncOff 守护委托旁路：打开后生命周期动作只做同步
// 状态转换、无异步副作用，且仍返回成功——这正是原先由 Shutdown 兼办的那个测试诉求。
func TestInstanceService_DelegateBypass_KeepsAsyncOff(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	svc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	svc.SetDelegateBypassForTest(true)

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "bypass",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "x",
		Status:       model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, svc.Stop(inst.ID), "旁路下动作仍应被受理")

	time.Sleep(150 * time.Millisecond)

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status, "旁路必须不产生任何异步回写")
}
