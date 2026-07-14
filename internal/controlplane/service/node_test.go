package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
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

type fakeNodeMetricsWorker struct {
	workerpb.WorkerServiceClient
	resp *workerpb.GetNodeMetricsResponse
}

func (f *fakeNodeMetricsWorker) GetNodeMetrics(context.Context, *workerpb.GetNodeMetricsRequest, ...grpc.CallOption) (*workerpb.GetNodeMetricsResponse, error) {
	return f.resp, nil
}

func TestNodeService_GetMetrics_UsesWorkerWhenConnected(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &fakeNodeMetricsWorker{resp: &workerpb.GetNodeMetricsResponse{
		CpuUsage:      0.61,
		MemoryUsage:   0.62,
		DiskUsage:     0.63,
		MemoryUsedMb:  7000,
		MemoryTotalMb: 16000,
		DiskUsedMb:    80000,
		DiskTotalMb:   200000,
	}})
	svc := NewNodeService(db)
	svc.SetClientPool(pool)

	got, err := svc.GetMetrics(node.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.61, got.CPUUsage, 0.001)
	require.InDelta(t, 0.62, got.MemoryUsage, 0.001)
	require.InDelta(t, 0.63, got.DiskUsage, 0.001)
	require.Equal(t, int64(7000), got.MemoryUsedMB)
	require.Equal(t, int64(16000), got.MemoryTotalMB)
	require.Equal(t, int64(80000), got.DiskUsedMB)
	require.Equal(t, int64(200000), got.DiskTotalMB)
}

func TestNodeService_GetMetrics_FallsBackToHeartbeatSnapshot(t *testing.T) {
	db := newNodeTestDB(t)
	node := newTestNode(t, db, "n1")
	require.NoError(t, db.Model(node).Updates(map[string]any{
		"cpu_usage":      0.21,
		"memory_usage":   0.22,
		"disk_usage":     0.23,
		"memory_used_mb": 1024,
		"memory_mb":      2048,
		"disk_used_mb":   4096,
		"disk_total_mb":  8192,
	}).Error)
	svc := NewNodeService(db)
	svc.SetClientPool(cpgrpc.NewClientPool())

	got, err := svc.GetMetrics(node.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.21, got.CPUUsage, 0.001)
	require.InDelta(t, 0.22, got.MemoryUsage, 0.001)
	require.InDelta(t, 0.23, got.DiskUsage, 0.001)
	require.Equal(t, int64(1024), got.MemoryUsedMB)
	require.Equal(t, int64(2048), got.MemoryTotalMB)
	require.Equal(t, int64(4096), got.DiskUsedMB)
	require.Equal(t, int64(8192), got.DiskTotalMB)
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
	// 无 Worker 连接：禁用后台异步委托，否则 Stop 的同步 STOPPING 会被异步覆盖为 CRASHED
	// （节点不可达），与下方断言竞态，并向共享内存库泄漏写入。见 InstanceService.Shutdown。
	instSvc.Shutdown()
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

	_, err := svc.Delete(node.ID, false)
	require.Error(t, err)

	require.NoError(t, db.Model(node).Update("status", model.NodeStatusOffline).Error)
	result, err := svc.Delete(node.ID, false)
	require.NoError(t, err)
	require.Zero(t, result.InstancesPurged)

	// 软删除：默认查询不可见，Unscoped 仍可见（记录保留）。
	var visible int64
	db.Model(&model.Node{}).Where("id = ?", node.ID).Count(&visible)
	require.Zero(t, visible)
	var total int64
	db.Unscoped().Model(&model.Node{}).Where("id = ?", node.ID).Count(&total)
	require.Equal(t, int64(1), total)
}

// Delete 实例守卫（FR-309）：离线节点名下仍有实例、未 force 时拒绝，
// 错误携带实例清单（id/name/status），节点与实例均不动。
func TestNodeService_Delete_WithInstances_Rejected(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")
	require.NoError(t, db.Model(node).Update("status", model.NodeStatusOffline).Error)

	inst := &model.Instance{NodeID: node.ID, Name: "orphan-candidate", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)

	_, err := svc.Delete(node.ID, false)
	require.Error(t, err)

	var hasInst *NodeHasInstancesError
	require.ErrorAs(t, err, &hasInst)
	require.Len(t, hasInst.Instances, 1)
	require.Equal(t, inst.ID, hasInst.Instances[0].ID)
	require.Equal(t, "orphan-candidate", hasInst.Instances[0].Name)
	require.Equal(t, model.InstanceStatusStopped, hasInst.Instances[0].Status)

	// 节点与实例都还在（拒绝即零副作用）。
	var nodeCount, instCount int64
	db.Model(&model.Node{}).Where("id = ?", node.ID).Count(&nodeCount)
	require.Equal(t, int64(1), nodeCount)
	db.Model(&model.Instance{}).Where("id = ?", inst.ID).Count(&instCount)
	require.Equal(t, int64(1), instCount)
}

// Delete force 级联（FR-309）：离线节点 force=true 时软删名下实例记录及其关联行，
// 只清平台记录不触碰远端文件；其它节点的实例不受影响。
func TestNodeService_Delete_ForceCascadesOfflineNode(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")
	other := newTestNode(t, db, "n2")
	require.NoError(t, db.Model(node).Update("status", model.NodeStatusOffline).Error)

	inst1 := &model.Instance{NodeID: node.ID, Name: "i1", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	inst2 := &model.Instance{NodeID: node.ID, Name: "i2", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusCrashed}
	otherInst := &model.Instance{NodeID: other.ID, Name: "other", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst1).Error)
	require.NoError(t, db.Create(inst2).Error)
	require.NoError(t, db.Create(otherInst).Error)
	// 关联行随实例级联（与 InstanceService.Delete 的记录级联口径一致）。
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 1, InstanceID: inst1.ID}).Error)
	require.NoError(t, db.Create(&model.ServerRegistration{ProxyID: inst1.ID, BackendID: inst2.ID, Alias: "b1"}).Error)
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: 1, InstanceID: inst2.ID}).Error)

	result, err := svc.Delete(node.ID, true)
	require.NoError(t, err)
	require.Equal(t, 2, result.InstancesPurged)

	// 节点与名下实例软删（默认查询不可见）。
	var count int64
	db.Model(&model.Node{}).Where("id = ?", node.ID).Count(&count)
	require.Zero(t, count)
	db.Model(&model.Instance{}).Where("node_id = ?", node.ID).Count(&count)
	require.Zero(t, count)
	// 关联行已清。
	db.Model(&model.GroupInstance{}).Where("instance_id = ?", inst1.ID).Count(&count)
	require.Zero(t, count)
	db.Model(&model.ServerRegistration{}).Where("proxy_id = ? OR backend_id = ?", inst1.ID, inst2.ID).Count(&count)
	require.Zero(t, count)
	db.Model(&model.NetworkMember{}).Where("instance_id = ?", inst2.ID).Count(&count)
	require.Zero(t, count)
	// 其它节点的实例不受影响。
	db.Model(&model.Instance{}).Where("id = ?", otherInst.ID).Count(&count)
	require.Equal(t, int64(1), count)
}

// Delete 在线节点 force 无效（FR-309 语义）：在线节点无论是否 force 一律拒绝——
// force 仅为离线节点的孤儿记录兜底，活节点必须先排空断开 Worker 走正常下线。
func TestNodeService_Delete_OnlineForceStillRejected(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeService(db)
	node := newTestNode(t, db, "n1")
	inst := &model.Instance{NodeID: node.ID, Name: "i1", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(inst).Error)

	_, err := svc.Delete(node.ID, true)
	require.Error(t, err)

	// 零副作用：节点与实例都在。
	var count int64
	db.Model(&model.Node{}).Where("id = ?", node.ID).Count(&count)
	require.Equal(t, int64(1), count)
	db.Model(&model.Instance{}).Where("id = ?", inst.ID).Count(&count)
	require.Equal(t, int64(1), count)
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
