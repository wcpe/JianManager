package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newNodeTestDB 为节点服务测试准备内存库（FR-048）。
func newNodeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{},
		&model.Instance{},
		&model.GroupInstance{},
		&model.ServerRegistration{},
		&model.NetworkMember{},
	))
	return db
}

func newTestNode(t *testing.T, db *gorm.DB, name string) *model.Node {
	t.Helper()
	node := &model.Node{Name: name, Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s-" + name, Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	return node
}

// SetMaintenance 置维护应翻转标记，再次置 false 应解除。
func TestNodeService_SetMaintenance_Toggle(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")

	got, err := svc.SetMaintenance(node.ID, true)
	require.NoError(t, err)
	require.True(t, got.Maintenance)

	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.True(t, fromDB.Maintenance)

	got, err = svc.SetMaintenance(node.ID, false)
	require.NoError(t, err)
	require.False(t, got.Maintenance)
}

// 维护模式不改变节点在线/离线状态（两者正交）。
func TestNodeService_SetMaintenance_KeepsStatus(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")

	_, err := svc.SetMaintenance(node.ID, true)
	require.NoError(t, err)

	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.Equal(t, model.NodeStatusOnline, fromDB.Status)
	require.True(t, fromDB.Maintenance)
}

// 不存在的节点置维护返回 ErrNodeNotFound。
func TestNodeService_SetMaintenance_NotFound(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)

	_, err := svc.SetMaintenance(999, true)
	require.ErrorIs(t, err, ErrNodeNotFound)
}

// ScheduleAllowed：普通节点放行，维护节点返回 ErrNodeInMaintenance。
func TestNodeService_ScheduleAllowed(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")

	require.NoError(t, svc.ScheduleAllowed(node.ID))

	_, err := svc.SetMaintenance(node.ID, true)
	require.NoError(t, err)
	require.ErrorIs(t, svc.ScheduleAllowed(node.ID), ErrNodeInMaintenance)

	require.ErrorIs(t, svc.ScheduleAllowed(999), ErrNodeNotFound)
}

// Drain 停止节点上运行中/启动中的实例，跳过已停止实例；只影响目标节点。
func TestNodeService_Drain_StopsRunning(t *testing.T) {
	db := newNodeTestDB(t)
	nodeSvc := NewNodeService(db)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	nodeSvc.SetInstanceService(instSvc)

	node := newTestNode(t, db, "n1")
	other := newTestNode(t, db, "n2")

	running := &model.Instance{NodeID: node.ID, Name: "run", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusRunning}
	starting := &model.Instance{NodeID: node.ID, Name: "starting", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStarting}
	stopped := &model.Instance{NodeID: node.ID, Name: "stopped", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	otherRunning := &model.Instance{NodeID: other.ID, Name: "other", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(running).Error)
	require.NoError(t, db.Create(starting).Error)
	require.NoError(t, db.Create(stopped).Error)
	require.NoError(t, db.Create(otherRunning).Error)

	result, err := nodeSvc.Drain(node.ID)
	require.NoError(t, err)
	// 仅 RUNNING 被停止（状态机只允许 RUNNING→STOPPING，STARTING 为瞬态不强停）。
	require.Equal(t, 1, result.StoppedCount)
	require.ElementsMatch(t, []uint{running.ID}, result.Stopped)

	// 目标节点的运行实例进入 STOPPING（Stop 同步部分的状态转换）。
	var r1 model.Instance
	require.NoError(t, db.First(&r1, running.ID).Error)
	require.Equal(t, model.InstanceStatusStopping, r1.Status)

	// STARTING 实例不被强停；已停止实例不动；其它节点实例不受影响。
	var st1, s1, o1 model.Instance
	require.NoError(t, db.First(&st1, starting.ID).Error)
	require.Equal(t, model.InstanceStatusStarting, st1.Status)
	require.NoError(t, db.First(&s1, stopped.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, s1.Status)
	require.NoError(t, db.First(&o1, otherRunning.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, o1.Status)
}

// 无运行实例时排空返回 0，不报错。
func TestNodeService_Drain_NoRunning(t *testing.T) {
	db := newNodeTestDB(t)
	nodeSvc := NewNodeService(db)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	nodeSvc.SetInstanceService(instSvc)

	node := newTestNode(t, db, "n1")
	result, err := nodeSvc.Drain(node.ID)
	require.NoError(t, err)
	require.Zero(t, result.StoppedCount)
}

// 不存在的节点排空返回 ErrNodeNotFound。
func TestNodeService_Drain_NotFound(t *testing.T) {
	db := newNodeTestDB(t)
	nodeSvc := NewNodeService(db)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())
	nodeSvc.SetInstanceService(instSvc)

	_, err := nodeSvc.Drain(999)
	require.ErrorIs(t, err, ErrNodeNotFound)
}

// Delete：在线节点拒绝下线，离线节点可下线（软删除保留记录）。
func TestNodeService_Delete_OnlineRejected(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")

	require.Error(t, svc.Delete(node.ID))

	require.NoError(t, db.Model(node).Update("status", model.NodeStatusOffline).Error)
	require.NoError(t, svc.Delete(node.ID))

	// 软删除：默认查询不可见，Unscoped 仍可见（记录保留）。
	var visible int64
	db.Model(&model.Node{}).Where("id = ?", node.ID).Count(&visible)
	require.Zero(t, visible)
	var total int64
	db.Unscoped().Model(&model.Node{}).Where("id = ?", node.ID).Count(&total)
	require.Equal(t, int64(1), total)
}

// 调度拦截：维护模式节点拒绝创建实例，返回 ErrNodeInMaintenance。
func TestInstanceService_Create_RejectsMaintenanceNode(t *testing.T) {
	db := newNodeTestDB(t)
	nodeSvc := NewNodeService(db)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())

	node := newTestNode(t, db, "n1")
	_, err := nodeSvc.SetMaintenance(node.ID, true)
	require.NoError(t, err)

	_, err = instSvc.Create(CreateInstanceRequest{
		NodeID:       node.ID,
		Name:         "i1",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "echo hi",
	})
	require.ErrorIs(t, err, ErrNodeInMaintenance)

	// 维护节点上不应残留实例。
	var n int64
	db.Model(&model.Instance{}).Where("node_id = ?", node.ID).Count(&n)
	require.Zero(t, n)
}

// 调度拦截：解除维护后可正常创建实例。
func TestInstanceService_Create_AllowsAfterUncordon(t *testing.T) {
	db := newNodeTestDB(t)
	nodeSvc := NewNodeService(db)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())

	node := newTestNode(t, db, "n1")
	_, err := nodeSvc.SetMaintenance(node.ID, true)
	require.NoError(t, err)
	_, err = nodeSvc.SetMaintenance(node.ID, false)
	require.NoError(t, err)

	inst, err := instSvc.Create(CreateInstanceRequest{
		NodeID:       node.ID,
		Name:         "i1",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "echo hi",
	})
	require.NoError(t, err)
	require.NotZero(t, inst.ID)
}

// 调度拦截：目标节点不存在时不在创建期硬失败（沿用既有行为，仅维护模式拦截）。
func TestInstanceService_Create_NodeNotFound_NotBlocked(t *testing.T) {
	db := newNodeTestDB(t)
	instSvc := NewInstanceService(db, NewGroupService(db), cpgrpc.NewClientPool())

	_, err := instSvc.Create(CreateInstanceRequest{
		NodeID:       999,
		Name:         "i1",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "echo hi",
	})
	require.NoError(t, err)
}
