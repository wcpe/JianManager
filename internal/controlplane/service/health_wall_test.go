package service

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newHealthWallSvc 建一个只带 db 的读服务（健康墙只读 CP 快照、零 Worker RPC，故无 gRPC 依赖）。
// 「零 Worker RPC」由 TestHealthWall_NoWorkerRPCAndBoundedQueries 以有界 DB 查询计数佐证。
func newHealthWallSvc(t *testing.T) *PlatformObservabilityService {
	t.Helper()
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.AlertRule{}, &model.AlertEvent{}))
	return NewPlatformObservabilityService(svc.db)
}

func TestHealthWall_LevelsSortingAndDrilldown(t *testing.T) {
	svc := newHealthWallSvc(t)
	base := metricBase()
	fresh := base.Add(-10 * time.Second)
	stale := base.Add(-120 * time.Second)

	require.NoError(t, svc.db.Create(&[]model.Node{
		{Name: "offline-1", UUID: "n-offline", Status: model.NodeStatusOffline},
		{Name: "stale-1", UUID: "n-stale", Status: model.NodeStatusOnline, LastHeartbeat: &stale},
		{Name: "degraded-crash", UUID: "n-deg", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, CPUUsage: 0.1, MemoryUsedMB: 10, MemoryMB: 100, DiskUsage: 0.1},
		{Name: "degraded-highcpu", UUID: "n-cpu", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, CPUUsage: 0.95, MemoryUsedMB: 10, MemoryMB: 100, DiskUsage: 0.1},
		{Name: "healthy-1", UUID: "n-good", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, CPUUsage: 0.2, MemoryUsedMB: 20, MemoryMB: 100, DiskUsage: 0.3},
	}).Error)

	var deg, good, offline model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "n-deg").First(&deg).Error)
	require.NoError(t, svc.db.Where("uuid = ?", "n-good").First(&good).Error)
	require.NoError(t, svc.db.Where("uuid = ?", "n-offline").First(&offline).Error)

	// 实例计数与崩溃 → degraded。
	require.NoError(t, svc.db.Create(&[]model.Instance{
		{UUID: "i-run", NodeID: deg.ID, Name: "run", Status: model.InstanceStatusRunning},
		{UUID: "i-crash", NodeID: deg.ID, Name: "crash", Status: model.InstanceStatusCrashed},
		{UUID: "i-stop", NodeID: good.ID, Name: "stop", Status: model.InstanceStatusStopped},
	}).Error)

	// 归属 crashed 实例的实例级活跃告警应归因回该节点。
	rule := &model.AlertRule{Name: "inst-crash", TriggerType: model.AlertTriggerInstanceCrash, TargetType: "instance", Level: "warn"}
	require.NoError(t, svc.db.Create(rule).Error)
	var crashInst model.Instance
	require.NoError(t, svc.db.Where("uuid = ?", "i-crash").First(&crashInst).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{RuleID: rule.ID, TargetID: crashInst.ID, Level: "warn", TriggerType: model.AlertTriggerInstanceCrash, Message: "crash"}).Error)

	got, err := svc.HealthWallAt(base, "level")
	require.NoError(t, err)
	require.Len(t, got.Nodes, 5)

	byID := map[uint]HealthWallNode{}
	for _, n := range got.Nodes {
		byID[n.NodeID] = n
	}
	assert.Equal(t, HealthLevelOffline, byID[offline.ID].Level)
	assert.Equal(t, "offline", byID[offline.ID].Freshness)
	assert.Equal(t, "/monitoring?node=n-offline", byID[offline.ID].Href)
	assert.Nil(t, byID[offline.ID].CPUPct, "离线节点不给资源水位")

	var staleNode model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "n-stale").First(&staleNode).Error)
	assert.Equal(t, HealthLevelStale, byID[staleNode.ID].Level)

	assert.Equal(t, HealthLevelDegraded, byID[deg.ID].Level)
	assert.Equal(t, 1, byID[deg.ID].Running)
	assert.Equal(t, 1, byID[deg.ID].Crashed)
	assert.Equal(t, 1, byID[deg.ID].ActiveAlerts)
	assert.NotNil(t, byID[deg.ID].CPUPct)
	assert.InDelta(t, 10, *byID[deg.ID].CPUPct, 0.01)

	var cpuNode model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "n-cpu").First(&cpuNode).Error)
	assert.Equal(t, HealthLevelDegraded, byID[cpuNode.ID].Level, "CPU ≥90% 判为降级")

	assert.Equal(t, HealthLevelHealthy, byID[good.ID].Level)

	// 默认按 severity 降序：offline 最前。
	assert.Equal(t, offline.ID, got.Nodes[0].NodeID)

	// 按 cpu 排序：降级高 CPU 节点应靠前。
	sorted, err := svc.HealthWallAt(base, "cpu")
	require.NoError(t, err)
	assert.Equal(t, cpuNode.ID, sorted.Nodes[0].NodeID)
}

func TestHealthWall_ScalesToSixtyPlusNodes(t *testing.T) {
	svc := newHealthWallSvc(t)
	base := metricBase()
	fresh := base.Add(-5 * time.Second)

	nodes := make([]model.Node, 0, 60)
	for i := 0; i < 60; i++ {
		nodes = append(nodes, model.Node{
			Name: "node", UUID: "scale-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Status: model.NodeStatusOnline, LastHeartbeat: &fresh,
			CPUUsage: 0.5, MemoryUsedMB: 50, MemoryMB: 100, DiskUsage: 0.5,
		})
	}
	require.NoError(t, svc.db.Create(&nodes).Error)

	got, err := svc.HealthWallAt(base, "level")
	require.NoError(t, err)
	assert.Len(t, got.Nodes, 60)
}

func TestHealthWall_Empty(t *testing.T) {
	svc := newHealthWallSvc(t)
	got, err := svc.HealthWallAt(metricBase(), "")
	require.NoError(t, err)
	assert.Empty(t, got.Nodes)
	assert.False(t, got.Truncated)
}

// TestHealthWall_TruncatesAfterSeveritySort 超规模时截断必须发生在 severity 排序之后：
// 高 id 的最严重（离线）节点不能被「按 id 截断」丢弃，并如实回报 truncated 标记。
func TestHealthWall_TruncatesAfterSeveritySort(t *testing.T) {
	svc := newHealthWallSvc(t)
	base := metricBase()
	fresh := base.Add(-5 * time.Second)

	nodes := make([]model.Node, 0, healthWallNodeLimit+1)
	for i := 0; i < healthWallNodeLimit; i++ {
		nodes = append(nodes, model.Node{
			Name: fmt.Sprintf("healthy-%03d", i), UUID: fmt.Sprintf("trunc-%03d", i),
			Status: model.NodeStatusOnline, LastHeartbeat: &fresh,
			CPUUsage: 0.2, MemoryUsedMB: 20, MemoryMB: 100, DiskUsage: 0.2,
		})
	}
	// 追加一台 id 最大的离线节点：按 id 截断会先丢弃它。
	nodes = append(nodes, model.Node{Name: "offline-late", UUID: "trunc-offline", Status: model.NodeStatusOffline})
	require.NoError(t, svc.db.CreateInBatches(&nodes, 100).Error)

	got, err := svc.HealthWallAt(base, "level")
	require.NoError(t, err)
	assert.Len(t, got.Nodes, healthWallNodeLimit)
	assert.True(t, got.Truncated, "超过上限应回报截断标记")
	assert.Equal(t, "trunc-offline", got.Nodes[0].UUID, "最严重的离线节点必须在截断后保留")
}

// TestAttributeAlertNode 覆盖 N3 加固：维度判定优先级为 Scope > TargetType > 触发类型家族，
// 且维度完全缺失时不做「节点兜底」，避免把 instance 的 target_id 当节点 ID 错归因。
func TestAttributeAlertNode(t *testing.T) {
	instNode := uint(42)
	instNodePtr := &instNode

	tests := []struct {
		name         string
		targetID     uint
		targetType   string
		scope        string
		triggerType  string
		instanceNode *uint
		wantID       uint
		wantOK       bool
	}{
		{"显式节点目标按节点 ID", 5, "node", "", "", nil, 5, true},
		{"离线触发按节点 ID", 5, "", "", model.AlertTriggerNodeOffline, nil, 5, true},
		{"scope=node 按节点 ID", 5, "", "node", model.AlertTriggerSaturation, nil, 5, true},
		{"显式实例目标映射到所属节点", 9, "instance", "", "", instNodePtr, 42, true},
		{"空 target_type + scope=instance 映射到所属节点", 9, "", "instance", model.AlertTriggerSaturation, instNodePtr, 42, true},
		{"空 target_type + 实例家族触发映射到所属节点", 9, "", "", model.AlertTriggerInstanceCrash, instNodePtr, 42, true},
		{"实例目标映射不到实例则不归因", 9, "instance", "", "", nil, 0, false},
		{"维度完全缺失不归因（不做节点兜底）", 9, "", "", model.AlertTriggerSaturation, instNodePtr, 0, false},
		{"维度完全缺失且无实例映射不归因", 9, "", "", "", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotOK := attributeAlertNode(tt.targetID, tt.targetType, tt.scope, tt.triggerType, tt.instanceNode)
			assert.Equal(t, tt.wantOK, gotOK)
			if tt.wantOK {
				assert.Equal(t, tt.wantID, gotID)
			}
		})
	}
}

// TestHealthWall_AlertAttributionEmptyTargetType 端到端佐证 N3：TargetType 空但作用域为实例的
// 活跃告警，不得因 node/instance 的 target_id 数字空间重叠而错归因到同号节点。
func TestHealthWall_AlertAttributionEmptyTargetType(t *testing.T) {
	svc := newHealthWallSvc(t)
	base := metricBase()
	fresh := base.Add(-5 * time.Second)

	require.NoError(t, svc.db.Create(&[]model.Node{
		{Name: "node-a", UUID: "n-a", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, MemoryMB: 100, DiskUsage: 0.1},
		{Name: "node-b", UUID: "n-b", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, MemoryMB: 100, DiskUsage: 0.1},
	}).Error)
	var nodeA, nodeB model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "n-a").First(&nodeA).Error)
	require.NoError(t, svc.db.Where("uuid = ?", "n-b").First(&nodeB).Error)

	// 实例 id 与 nodeA.id 重叠（两表各自自增），用于复现错归因场景。
	inst := &model.Instance{UUID: "i-1", NodeID: nodeB.ID, Name: "inst", Status: model.InstanceStatusRunning}
	require.NoError(t, svc.db.Create(inst).Error)
	require.Equal(t, nodeA.ID, inst.ID, "前置：实例 id 须与 nodeA id 重叠以复现错归因")

	// 规则一：TargetType 缺失但 Scope=instance → 应归因到实例所属节点。
	instRule := &model.AlertRule{Name: "sat-inst", TriggerType: model.AlertTriggerSaturation, Scope: "instance", Level: "warn"}
	require.NoError(t, svc.db.Create(instRule).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{RuleID: instRule.ID, TargetID: inst.ID, Level: "warn", TriggerType: model.AlertTriggerSaturation, Message: "实例饱和度"}).Error)

	// 规则二：维度完全缺失 → 无法安全判别，不应归因到任何节点。
	ambiguous := &model.AlertRule{Name: "sat-amb", TriggerType: model.AlertTriggerSaturation, Level: "warn"}
	require.NoError(t, svc.db.Create(ambiguous).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{RuleID: ambiguous.ID, TargetID: inst.ID, Level: "warn", TriggerType: model.AlertTriggerSaturation, Message: "歧义饱和度"}).Error)

	// 规则三：显式节点目标 → 按 target 归因到该节点。
	nodeRule := &model.AlertRule{Name: "node-metric", TriggerType: model.AlertTriggerMetric, TargetType: "node", Level: "warn"}
	require.NoError(t, svc.db.Create(nodeRule).Error)
	require.NoError(t, svc.db.Create(&model.AlertEvent{RuleID: nodeRule.ID, TargetID: nodeB.ID, Level: "warn", TriggerType: model.AlertTriggerMetric, Message: "节点指标"}).Error)

	got, err := svc.HealthWallAt(base, "level")
	require.NoError(t, err)
	byID := map[uint]HealthWallNode{}
	for _, n := range got.Nodes {
		byID[n.NodeID] = n
	}
	assert.Equal(t, 0, byID[nodeA.ID].ActiveAlerts, "实例作用域告警不得错归因到同号的节点")
	assert.Equal(t, 2, byID[nodeB.ID].ActiveAlerts, "实例作用域告警归因到实例所属节点，节点级告警按 target 归因")
}

// TestHealthWall_NoWorkerRPCAndBoundedQueries 验收 #3：健康墙只读 CP 已持久化快照，绝不触发
// Worker RPC，且一次查询给全（无逐台 N+1）。服务无 gRPC 依赖，故以「有界 DB 查询计数」作为
// 可核验证据：查询次数为常数、不随节点数增长。
func TestHealthWall_NoWorkerRPCAndBoundedQueries(t *testing.T) {
	svc := newHealthWallSvc(t)
	base := metricBase()
	fresh := base.Add(-5 * time.Second)

	nodes := make([]model.Node, 0, 60)
	for i := 0; i < 60; i++ {
		nodes = append(nodes, model.Node{
			Name: fmt.Sprintf("rpc-%02d", i), UUID: fmt.Sprintf("rpcuuid-%02d", i),
			Status: model.NodeStatusOnline, LastHeartbeat: &fresh,
			CPUUsage: 0.5, MemoryUsedMB: 50, MemoryMB: 100, DiskUsage: 0.5,
		})
	}
	require.NoError(t, svc.db.Create(&nodes).Error)

	var queryCount atomic.Int32
	const callbackName = "test:count-health-wall-queries"
	require.NoError(t, svc.db.Callback().Query().Before("gorm:query").Register(callbackName, func(*gorm.DB) {
		queryCount.Add(1)
	}))
	t.Cleanup(func() { _ = svc.db.Callback().Query().Remove(callbackName) })

	measure := func() int32 {
		queryCount.Store(0)
		_, err := svc.HealthWallAt(base, "level")
		require.NoError(t, err)
		return queryCount.Load()
	}
	few := measure() // 60 台节点

	// 再翻倍到 120 台：查询次数必须保持不变（恒定常数，无逐台 N+1 / 无 Worker RPC）。
	more := make([]model.Node, 0, 60)
	for i := 0; i < 60; i++ {
		more = append(more, model.Node{
			Name: fmt.Sprintf("rpc2-%02d", i), UUID: fmt.Sprintf("rpcuuid2-%02d", i),
			Status: model.NodeStatusOnline, LastHeartbeat: &fresh,
			CPUUsage: 0.5, MemoryUsedMB: 50, MemoryMB: 100, DiskUsage: 0.5,
		})
	}
	require.NoError(t, svc.db.Create(&more).Error)
	queryCount.Store(0)
	got, err := svc.HealthWallAt(base, "level")
	require.NoError(t, err)
	require.Len(t, got.Nodes, 120)
	grown := queryCount.Load()

	assert.Equal(t, few, grown, "查询次数不得随节点数增长（无 N+1）")
	assert.LessOrEqual(t, grown, int32(3), "健康墙查询应为常数级有界（无 Worker RPC）")
}

// FR-461/FR-459 验收 7：假死实例状态仍是 RUNNING（进程在、只是不响应），只按 status 分级
// 会让它在健康墙上完全不可见；健康墙须把「RUNNING 但 status_reason 非空」计为降级。
func TestHealthWall_DegradedCountsHangingInstances(t *testing.T) {
	svc := newHealthWallSvc(t)
	base := metricBase()
	fresh := base.Add(-10 * time.Second)

	require.NoError(t, svc.db.Create(&[]model.Node{
		{Name: "hang-node", UUID: "n-hang", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, CPUUsage: 0.1, MemoryUsedMB: 10, MemoryMB: 100, DiskUsage: 0.1},
		{Name: "idle-node", UUID: "n-idle", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, CPUUsage: 0.1, MemoryUsedMB: 10, MemoryMB: 100, DiskUsage: 0.1},
	}).Error)

	var hang, idle model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "n-hang").First(&hang).Error)
	require.NoError(t, svc.db.Where("uuid = ?", "n-idle").First(&idle).Error)

	require.NoError(t, svc.db.Create(&[]model.Instance{
		// 假死：RUNNING 但巡检已写原因。
		{UUID: "i-hang", NodeID: hang.ID, Name: "hang", Status: model.InstanceStatusRunning,
			StatusReason: "假死：进程在但 tcp 响应探测连续失败 3 次"},
		// 健康 RUNNING：无原因。
		{UUID: "i-ok", NodeID: hang.ID, Name: "ok", Status: model.InstanceStatusRunning},
		// 另一个节点全健康。
		{UUID: "i-idle", NodeID: idle.ID, Name: "idle", Status: model.InstanceStatusRunning},
	}).Error)

	got, err := svc.HealthWallAt(base, "level")
	require.NoError(t, err)

	byUUID := map[string]HealthWallNode{}
	for _, n := range got.Nodes {
		byUUID[n.UUID] = n
	}
	assert.Equal(t, 1, byUUID["n-hang"].Degraded, "假死实例应计入 degraded")
	assert.Equal(t, 2, byUUID["n-hang"].Running, "RUNNING 计数不受影响")
	assert.Equal(t, HealthLevelDegraded, byUUID["n-hang"].Level, "存在假死实例的节点应降级")
	assert.Equal(t, 0, byUUID["n-idle"].Degraded)
	assert.Equal(t, HealthLevelHealthy, byUUID["n-idle"].Level)
}
