package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-323 启动闸缺口修复：导入（kind=import，migrate 搬迁）/克隆（kind=clone，目录拷贝）
// 任务化后与 provision 一样带 instance_id 关联、在途 statusReason 标「导入中/克隆中」，
// 但启动闸只滤 kind=provision——搬迁/拷贝在途的实例仍可从单实例与批量入口启动到
// 半截工作目录。本组测试钉死：import/clone 未终态 → 单启与批量 start/restart 均被拒
// 且错误文案按 kind 区分；任务终态后放行；闸拦不把实例回写 CRASHED。

// newLongOpGateDB 建「STOPPED 实例 + 关联的 running 长操作任务」现场。
func newLongOpGateDB(t *testing.T, kind string) (*gorm.DB, uint) {
	t.Helper()
	db := newInstanceTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.TaskLog{}, &model.Notification{}))
	node := &model.Node{UUID: "node-longop-" + t.Name(), Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{Name: "longop-gate", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.Task{TaskID: "t-longop", NodeID: node.ID, InstanceID: inst.ID,
		Kind: kind, State: model.TaskStateRunning}).Error)
	return db, inst.ID
}

// TestStart_BlockedWhileImportOrCloneInFlight 单实例 Start：import/clone 未终态被拒、
// 文案按 kind 区分；任务终态后不再被该闸拦（后续因节点未连接在委托层失败属既有语义）。
func TestStart_BlockedWhileImportOrCloneInFlight(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{model.TaskKindImport, "导入中"},
		{model.TaskKindClone, "克隆中"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			db, instID := newLongOpGateDB(t, tt.kind)
			svc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())

			err := svc.Start(instID)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)

			require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-longop").
				Update("state", model.TaskStateSucceeded).Error)
			if err := svc.Start(instID); err != nil {
				require.NotContains(t, err.Error(), tt.want, "任务终态后不应再被在途闸拦")
			}
		})
	}
}

// TestRestart_BlockedWhileLongOperationInFlight 验证 FR-331 中心化闸：所有复用
// InstanceService.Restart 的单实例、网络、定时与探针重启入口都必须在状态转换前被拦截。
func TestRestart_BlockedWhileLongOperationInFlight(t *testing.T) {
	db, instID := newLongOpGateDB(t, model.TaskKindProvision)
	svc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	t.Cleanup(svc.Shutdown)

	err := svc.Restart(instID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "搭建中")

	var got model.Instance
	require.NoError(t, db.First(&got, instID).Error)
	require.Equal(t, model.InstanceStatusStopped, got.Status, "闸拦必须早于重启状态转换")
}

// TestInstanceBatch_StartRestart_BlockedWhileImportOrCloneInFlight 批量 start/restart：
// import/clone 未终态逐条拒绝计 failed 带 kind 区分文案，实例保持 STOPPED（不回写 CRASHED）。
func TestInstanceBatch_StartRestart_BlockedWhileImportOrCloneInFlight(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		action InstanceBatchAction
		want   string
	}{
		{"import-start", model.TaskKindImport, InstanceBatchStart, "导入中"},
		{"import-restart", model.TaskKindImport, InstanceBatchRestart, "导入中"},
		{"clone-start", model.TaskKindClone, InstanceBatchStart, "克隆中"},
		{"clone-restart", model.TaskKindClone, InstanceBatchRestart, "克隆中"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, instID := newLongOpGateDB(t, tt.kind)
			svc := NewInstanceBatchService(db, cpgrpc.NewClientPool())

			res, err := svc.Batch(InstanceBatchRequest{Action: tt.action, IDs: []uint{instID}}, nil, false)
			require.NoError(t, err)
			require.Equal(t, 1, res.Failed)
			require.Equal(t, 0, res.Succeeded)
			require.Len(t, res.Errors, 1)
			require.Contains(t, res.Errors[0].Error, tt.want)

			var got model.Instance
			require.NoError(t, db.First(&got, instID).Error)
			require.Equal(t, model.InstanceStatusStopped, got.Status, "闸拦不应把实例回写 CRASHED")
		})
	}
}

// TestInstanceBatch_Start_ReleasedAfterImportTerminal 任务终态后批量放行：闸不再拦，
// 后续因 Worker 未连接在委托层失败（既有语义），但错误不再是「导入中」。
func TestInstanceBatch_Start_ReleasedAfterImportTerminal(t *testing.T) {
	db, instID := newLongOpGateDB(t, model.TaskKindImport)
	svc := NewInstanceBatchService(db, cpgrpc.NewClientPool())
	require.NoError(t, db.Model(&model.Task{}).Where("task_id = ?", "t-longop").
		Update("state", model.TaskStateSucceeded).Error)

	res, err := svc.Batch(InstanceBatchRequest{Action: InstanceBatchStart, IDs: []uint{instID}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed)
	require.Len(t, res.Errors, 1)
	require.NotContains(t, res.Errors[0].Error, "导入中", "任务终态后不应再被在途闸拦")
	require.Contains(t, res.Errors[0].Error, "未连接")
}
