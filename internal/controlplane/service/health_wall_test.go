package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newHealthWallSvc 建一个只带 db 的读服务（健康墙只读 CP 快照、零 Worker RPC，故无 gRPC 依赖）。
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
}
