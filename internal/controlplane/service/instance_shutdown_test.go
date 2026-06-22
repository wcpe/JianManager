package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Shutdown 后生命周期动作只保留同步状态转换，不再 fire-and-forget 异步委托。
// 回归守卫：修复前 Stop 的同步 STOPPING 会被后台 delegateToWorker 因节点不可达异步覆盖为
// CRASHED（并可能在 DB 关闭后仍写库），导致 drain 等用例偶发失败。此处给足时间窗口，
// 断言状态未被覆盖，确保 Shutdown 真正禁用了异步委托。
func TestInstanceService_Shutdown_DisablesAsyncDelegation(t *testing.T) {
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

	require.NoError(t, svc.Stop(inst.ID))

	// 留出窗口：若异步委托被回归性地重新启用，goroutine 会在此期间把状态覆盖为 CRASHED。
	time.Sleep(150 * time.Millisecond)

	var got model.Instance
	require.NoError(t, db.First(&got, inst.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, got.Status)
}
