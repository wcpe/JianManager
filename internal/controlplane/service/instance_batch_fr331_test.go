package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-331：搭建中实例硬性禁止启动——批量入口补漏。
// 真机根因：单实例 Start 已有在途搭建闸（FR-319 二轮②），但批量 start/restart
// 走 InstanceBatchService.delegateBatchOne 直发 Worker RPC，完全绕过该闸，
// 搭建中实例可经批量入口被启动到半截 jar。本组测试钉死批量入口同样被拦，
// 且拦截不把实例回写 CRASHED（实例本身无恙，只是尚不可启动）。

// newBatchGateHarness 建「STOPPED 实例 + 关联的 running provision 任务」现场。
func newBatchGateHarness(t *testing.T) (svc *InstanceBatchService, instID uint) {
	t.Helper()
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}))
	node := &model.Node{UUID: "node-batch-gate-" + t.Name(), Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{Name: "prov-batch", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "t-prov-batch", NodeID: node.ID, InstanceID: inst.ID,
		Kind: model.TaskKindProvision, State: model.TaskStateRunning}).Error)
	return NewInstanceBatchService(db, cpgrpc.NewClientPool()), inst.ID
}

// TestInstanceBatch_Start_BlockedWhileProvisionInFlight 批量 start：provision 未终态 → 计入
// failed 带「搭建中」明细，且实例状态保持 STOPPED（不因闸拦回写 CRASHED）。
func TestInstanceBatch_Start_BlockedWhileProvisionInFlight(t *testing.T) {
	svc, instID := newBatchGateHarness(t)

	res, err := svc.Batch(InstanceBatchRequest{Action: InstanceBatchStart, IDs: []uint{instID}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed)
	require.Equal(t, 0, res.Succeeded)
	require.Len(t, res.Errors, 1)
	require.Contains(t, res.Errors[0].Error, "搭建中")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, instID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "闸拦不应把实例回写 CRASHED")
}

// TestInstanceBatch_Restart_BlockedWhileProvisionInFlight 批量 restart 同样以启动收尾，
// 搭建中一并拦截（restart 的 Worker RPC 会把停着的实例拉起来）。
func TestInstanceBatch_Restart_BlockedWhileProvisionInFlight(t *testing.T) {
	svc, instID := newBatchGateHarness(t)

	res, err := svc.Batch(InstanceBatchRequest{Action: InstanceBatchRestart, IDs: []uint{instID}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed)
	require.Contains(t, res.Errors[0].Error, "搭建中")

	var got model.Instance
	require.NoError(t, svc.db.First(&got, instID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status)
}

// TestInstanceBatch_Start_ReleasedAfterProvisionTerminal 任务终态后放行：闸不再拦，
// 后续因 Worker 未连接在委托层失败（既有语义，回写 CRASHED），但错误不再是「搭建中」。
func TestInstanceBatch_Start_ReleasedAfterProvisionTerminal(t *testing.T) {
	svc, instID := newBatchGateHarness(t)
	require.NoError(t, svc.db.Model(&model.Task{}).Where("task_id = ?", "t-prov-batch").
		Update("state", model.TaskStateSucceeded).Error)

	res, err := svc.Batch(InstanceBatchRequest{Action: InstanceBatchStart, IDs: []uint{instID}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed)
	require.Len(t, res.Errors, 1)
	require.NotContains(t, res.Errors[0].Error, "搭建中", "任务终态后不应再被搭建中闸拦")
	require.Contains(t, res.Errors[0].Error, "未连接")
}

// TestInstanceBatch_Stop_NotGatedByProvision stop/kill 不受搭建闸影响（只拦「启动类」动作）：
// 搭建中实例本就 STOPPED，停不出乱子；且强停通道永不因闸失效。
func TestInstanceBatch_Stop_NotGatedByProvision(t *testing.T) {
	svc, instID := newBatchGateHarness(t)

	res, err := svc.Batch(InstanceBatchRequest{Action: InstanceBatchStop, IDs: []uint{instID}}, nil, false)
	require.NoError(t, err)
	require.Len(t, res.Errors, 1)
	require.NotContains(t, res.Errors[0].Error, "搭建中")
}
