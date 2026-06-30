package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestInstanceService_StopFromStarting 复现并守护 #5：处于 STARTING 的实例点「停止」时，
// 状态机须允许 STARTING→STOPPING 并下发停止；修复前该转换不在 validTransitions，
// Stop 直接被「无效的状态转换」拦下、不通知 Worker，docker 容器继续运行、终端不停。
func TestInstanceService_StopFromStarting(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	svc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	svc.Shutdown() // 禁用异步委托，仅观测同步状态转换

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "starting",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDocker,
		StartCommand: "x",
		Status:       model.InstanceStatusStarting,
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, svc.Stop(inst.ID), "STARTING 状态应允许停止")

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status)
}

// TestInstanceService_UpdateStatusFromTo_GuardsConcurrentStop 守护 #5 竞态：
// 启动委托回来前用户已点停止（状态被同步置为 STOPPING）时，迟到的 start 成功
// 不得把实例「复活」回 RUNNING。
func TestInstanceService_UpdateStatusFromTo_GuardsConcurrentStop(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	svc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	svc.Shutdown()

	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "race",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDocker,
		StartCommand: "x",
		Status:       model.InstanceStatusStopping, // 用户已发起停止
	}
	require.NoError(t, db.Create(inst).Error)

	// 迟到的 start 成功：from=STARTING 不匹配当前 STOPPING，应不改动。
	svc.updateStatusFromTo(inst.ID, model.InstanceStatusStarting, model.InstanceStatusRunning)
	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status, "停止中不应被迟到的 start 复活")

	// 正常路径：仍处 STARTING 时 start 成功应置 RUNNING。
	require.NoError(t, db.Model(&got).Update("status", model.InstanceStatusStarting).Error)
	svc.updateStatusFromTo(inst.ID, model.InstanceStatusStarting, model.InstanceStatusRunning)
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status)
}
