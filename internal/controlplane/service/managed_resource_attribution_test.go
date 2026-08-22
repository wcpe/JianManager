package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestMetric_ResourceAttribution_OnlyReturnsFreshManagedRuntime(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	base := metricBase()
	freshAt := base.Add(-30 * time.Second)
	staleAt := base.Add(-91 * time.Second)
	capacity := int32(50)
	require.NoError(t, svc.db.Create(&[]model.Node{
		{Name: "fresh", UUID: "node-fresh", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, CPUUsage: 0.4, MemoryUsedMB: 1024, MemoryMB: 4096, BotAvailable: true, BotCapacityMax: &capacity},
		{Name: "stale", UUID: "node-stale", Status: model.NodeStatusOnline, LastHeartbeat: &staleAt, CPUUsage: 0.9, MemoryUsedMB: 8192, MemoryMB: 8192},
	}).Error)
	var freshNode model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "node-fresh").First(&freshNode).Error)
	inst := model.Instance{NodeID: freshNode.ID, UUID: "instance-fresh", Name: "lobby", Status: model.InstanceStatusRunning}
	require.NoError(t, svc.db.Create(&inst).Error)
	require.NoError(t, svc.db.Create(&[]model.ProcessMetricSnapshot{
		{NodeUUID: freshNode.UUID, InstanceUUID: inst.UUID, PID: 11, Name: "java", CPUPercent: 30, RSSBytes: 100, SampledAt: freshAt},
		{NodeUUID: freshNode.UUID, InstanceUUID: inst.UUID, PID: 12, Name: "helper", CPUPercent: 10, RSSBytes: 50, SampledAt: freshAt},
	}).Error)

	got, err := svc.ResourceAttributionAt(base, ResourceAttributionQuery{Sort: "memory", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Nodes, 2)
	require.Equal(t, ResourceFreshnessFresh, got.Nodes[0].Status)
	require.Equal(t, ResourceFreshnessStale, got.Nodes[1].Status)
	require.NotNil(t, got.Nodes[0].MemoryUsedBytes)
	require.Equal(t, &capacity, got.Nodes[0].BotWorker.CapacityMax)
	require.Nil(t, got.Nodes[1].MemoryUsedBytes, "陈旧节点不得把旧资源伪装成实时值")
	require.Nil(t, got.Nodes[1].BotWorker.CapacityMax, "陈旧节点不得把旧容量伪装成实时值")
	require.Len(t, got.TopInstances, 1)
	require.Equal(t, inst.ID, got.TopInstances[0].InstanceID)
	require.Equal(t, uint64(150), got.TopInstances[0].RSSBytes)
	require.Len(t, got.TopProcesses, 2)
	require.Equal(t, int32(11), got.TopProcesses[0].PID)
}

func TestMetric_ResourceAttribution_EmptyAndAllOffline(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	base := metricBase()

	// 空节点：整体不可用，TopN 为空。
	got, err := svc.ResourceAttributionAt(base, ResourceAttributionQuery{})
	require.NoError(t, err)
	require.Equal(t, ResourceFreshnessUnavailable, got.Freshness)
	require.Empty(t, got.Nodes)
	require.Empty(t, got.TopInstances)
	require.Empty(t, got.TopProcesses)

	// 全离线节点：整体离线；节点状态 Offline，资源不可用。
	require.NoError(t, svc.db.Create(&model.Node{
		UUID: "node-off", Name: "offline", Status: model.NodeStatusOffline, CPUUsage: 0.5, MemoryUsedMB: 2048, MemoryMB: 4096,
	}).Error)
	got, err = svc.ResourceAttributionAt(base, ResourceAttributionQuery{})
	require.NoError(t, err)
	require.Equal(t, ResourceFreshnessOffline, got.Freshness)
	require.Len(t, got.Nodes, 1)
	require.Equal(t, ResourceFreshnessOffline, got.Nodes[0].Status)
	require.Nil(t, got.Nodes[0].MemoryUsedBytes, "离线节点资源不可用")
}

func TestMetric_ResourceAttribution_ProcessesOnlyFromFreshNodes(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	base := metricBase()
	freshAt := base.Add(-30 * time.Second)
	staleAt := base.Add(-91 * time.Second)

	require.NoError(t, svc.db.Create(&[]model.Node{
		{UUID: "node-fresh-p", Name: "fresh-p", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, CPUUsage: 0.3, MemoryUsedMB: 512, MemoryMB: 4096},
		{UUID: "node-stale-p", Name: "stale-p", Status: model.NodeStatusOnline, LastHeartbeat: &staleAt, CPUUsage: 0.9, MemoryUsedMB: 4096, MemoryMB: 4096},
	}).Error)
	var freshNode model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "node-fresh-p").First(&freshNode).Error)
	var staleNode model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "node-stale-p").First(&staleNode).Error)
	freshInst := model.Instance{NodeID: freshNode.ID, UUID: "inst-fresh-p", Name: "fresh-inst", Status: model.InstanceStatusRunning}
	staleInst := model.Instance{NodeID: staleNode.ID, UUID: "inst-stale-p", Name: "stale-inst", Status: model.InstanceStatusRunning}
	require.NoError(t, svc.db.Create(&freshInst).Error)
	require.NoError(t, svc.db.Create(&staleInst).Error)
	require.NoError(t, svc.db.Create(&[]model.ProcessMetricSnapshot{
		{NodeUUID: freshNode.UUID, InstanceUUID: freshInst.UUID, PID: 21, Name: "java", CPUPercent: 40, RSSBytes: 200, SampledAt: freshAt},
		{NodeUUID: staleNode.UUID, InstanceUUID: staleInst.UUID, PID: 22, Name: "legacy", CPUPercent: 80, RSSBytes: 400, SampledAt: freshAt},
	}).Error)

	got, err := svc.ResourceAttributionAt(base, ResourceAttributionQuery{Sort: "memory", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, ResourceFreshnessFresh, got.Freshness)
	// 陈旧节点的进程与实例不得出现在 TopN。
	require.Len(t, got.TopInstances, 1)
	require.Equal(t, freshInst.ID, got.TopInstances[0].InstanceID)
	require.Len(t, got.TopProcesses, 1)
	require.Equal(t, int32(21), got.TopProcesses[0].PID)
	require.NotContains(t, got.TopProcesses[0].Name, "legacy", "陈旧节点进程须被过滤")
}

func TestMetric_ResourceAttribution_LimitCap(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	base := metricBase()
	freshAt := base.Add(-10 * time.Second)
	capacity := int32(10)
	// 7 个新鲜节点，Limit=3 只返回 3 个。
	nodes := make([]model.Node, 0, 7)
	for i := 1; i <= 7; i++ {
		uuid := fmt.Sprintf("node-cap-%d", i)
		nodes = append(nodes, model.Node{
			UUID: uuid, Name: uuid, Status: model.NodeStatusOnline,
			LastHeartbeat: &freshAt, CPUUsage: 0.2, MemoryUsedMB: 256, MemoryMB: 4096,
			BotAvailable: true, BotCapacityMax: &capacity,
		})
	}
	require.NoError(t, svc.db.Create(&nodes).Error)
	got, err := svc.ResourceAttributionAt(base, ResourceAttributionQuery{Limit: 3})
	require.NoError(t, err)
	require.Len(t, got.Nodes, 3, "Limit 应限制节点返回数")
	require.Equal(t, ResourceFreshnessFresh, got.Freshness)
}
