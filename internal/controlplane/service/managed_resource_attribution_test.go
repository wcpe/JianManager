package service

import (
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
