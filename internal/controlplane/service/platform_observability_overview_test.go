package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestPlatformObservabilityOverview_AggregatesBoundedSharedRuntime(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.AlertEvent{}, &model.Task{}))
	base := metricBase()
	freshAt := base.Add(-30 * time.Second)
	staleAt := base.Add(-91 * time.Second)
	botRSS := int64(120)
	workerRSS := int64(240)
	botCPU := 3.5
	workerCPU := 1.5
	active := int32(20)
	connecting := int32(1)
	eventLoop := 4.2
	maxEventLoop := 7.5
	require.NoError(t, svc.db.Create(&[]model.Node{
		{Name: "fresh-a", UUID: "node-fresh-a", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, ManagedRuntimeObservedAt: &freshAt, CPUUsage: 0.4, CPUCores: 4, LoadAvg1: 1, MemoryUsedMB: 1024, MemoryMB: 4096, BotAvailable: true, BotWorkerRSSBytes: &botRSS, WorkerProcessRSSBytes: &workerRSS, BotWorkerCPUPct: &botCPU, WorkerProcessCPUPct: &workerCPU, BotActiveCount: &active, BotConnectingCount: &connecting, BotEventLoopP95MS: &eventLoop},
		{Name: "fresh-b", UUID: "node-fresh-b", Status: model.NodeStatusOnline, LastHeartbeat: &freshAt, ManagedRuntimeObservedAt: &freshAt, CPUUsage: 0.2, CPUCores: 4, LoadAvg1: 1, MemoryUsedMB: 512, MemoryMB: 4096, BotAvailable: true, BotWorkerRSSBytes: &botRSS, WorkerProcessRSSBytes: &workerRSS, BotWorkerCPUPct: &botCPU, WorkerProcessCPUPct: &workerCPU, BotActiveCount: &active, BotConnectingCount: &connecting, BotEventLoopP95MS: &maxEventLoop},
		{Name: "stale", UUID: "node-stale", Status: model.NodeStatusOnline, LastHeartbeat: &staleAt, ManagedRuntimeObservedAt: &staleAt},
		{Name: "offline", UUID: "node-offline", Status: model.NodeStatusOffline},
	}).Error)
	var fresh model.Node
	require.NoError(t, svc.db.Where("uuid = ?", "node-fresh-a").First(&fresh).Error)
	for i := 0; i < 6; i++ {
		require.NoError(t, svc.db.Create(&model.Instance{UUID: "crashed-" + string(rune('a'+i)), NodeID: fresh.ID, Name: "crashed", Status: model.InstanceStatusCrashed}).Error)
		require.NoError(t, svc.db.Create(&model.AlertEvent{Level: "warn", Message: "active", FiredAt: base.Add(time.Duration(i) * time.Second)}).Error)
		require.NoError(t, svc.db.Create(&model.Task{TaskID: "failed-" + string(rune('a'+i)), State: model.TaskStateFailed, Title: "failed", UpdatedAt: base.Add(time.Duration(i) * time.Second)}).Error)
	}

	got, err := NewPlatformObservabilityService(svc.db).OverviewAt(base)
	require.NoError(t, err)
	require.Equal(t, 4, got.Health.NodeCount)
	require.Equal(t, 2, got.Health.OnlineNodeCount)
	require.Equal(t, 1, got.Health.StaleNodeCount)
	require.Equal(t, 1, got.Health.OfflineNodeCount)
	require.Equal(t, 6, got.Health.CrashedInstanceCount)
	require.Equal(t, true, got.Bots.SharedRuntime)
	require.Equal(t, 2, got.Bots.NodeCount)
	require.Equal(t, 7.5, *got.Bots.EventLoopP95MS)
	require.Equal(t, int64(240), *got.Bots.BotWorkerRSSBytes)
	require.Len(t, got.Bots.Unavailable, 2)
	require.Len(t, got.Alerts, 5)
	require.Len(t, got.Tasks, 5)
	require.Len(t, got.Exceptions, 5)
}

func TestPlatformObservabilityOverview_NoAvailableBotRuntimeIsUnavailable(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.AlertEvent{}, &model.Task{}))

	got, err := NewPlatformObservabilityService(svc.db).OverviewAt(metricBase())
	require.NoError(t, err)
	require.Zero(t, got.Bots.NodeCount)
	require.Nil(t, got.Bots.EventLoopP95MS)
	require.Len(t, got.Bots.Unavailable, 1)
	require.Equal(t, "暂无可用 Bot Worker 运行时快照", got.Bots.Unavailable[0].Reason)
}
