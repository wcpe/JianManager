package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBotRuntimeMetricSampler_SampleAtWritesCurrentSnapshot(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	base := metricBase()
	fresh := base.Add(-89 * time.Second)
	workerRSS, botRSS := int64(100), int64(200)
	workerCPU, botCPU, eventLoop := 1.5, 2.5, 3.5
	active, connecting, capacity := int32(4), int32(2), int32(50)
	node := model.Node{
		Name: "node-a", Status: model.NodeStatusOnline, LastHeartbeat: &fresh,
		ManagedRuntimeObservedAt: &fresh,
		WorkerProcessRSSBytes: &workerRSS, WorkerProcessCPUPct: &workerCPU,
		BotWorkerRSSBytes: &botRSS, BotWorkerCPUPct: &botCPU,
		BotActiveCount: &active, BotConnectingCount: &connecting,
		BotEventLoopP95MS: &eventLoop, BotCapacityMax: &capacity, BotAvailable: true,
	}
	require.NoError(t, svc.db.Create(&node).Error)

	sampler := NewBotRuntimeMetricSampler(svc.db, svc)
	require.NoError(t, sampler.SampleAt(base))

	_, series, err := svc.QuerySeries(SeriesQuery{
		Scope: model.MetricScopeNode, NodeUUID: node.UUID,
		From: base.Add(-time.Minute), To: base.Add(time.Minute), Resolution: "raw",
	})
	require.NoError(t, err)
	require.Equal(t, 100.0, *findSeries(series, model.MetricWorkerProcessRSSBytes, "").Points[0].Avg)
	require.Equal(t, 2.5, *findSeries(series, model.MetricBotWorkerCPUPct, "").Points[0].Avg)
	require.Equal(t, 50.0, *findSeries(series, model.MetricBotCapacityMax, "").Points[0].Avg)
}

func TestBotRuntimeMetricSampler_StaleAndUnavailableSnapshotsWriteGaps(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	base := metricBase()
	stale := base.Add(-91 * time.Second)
	fresh := base.Add(-time.Second)
	workerRSS := int64(100)
	staleNode := model.Node{Name: "stale", Status: model.NodeStatusOnline, LastHeartbeat: &stale, ManagedRuntimeObservedAt: &stale, WorkerProcessRSSBytes: &workerRSS}
	unavailableNode := model.Node{Name: "unavailable", Status: model.NodeStatusOnline, LastHeartbeat: &fresh, ManagedRuntimeObservedAt: &fresh, WorkerProcessRSSBytes: &workerRSS, BotAvailable: false, BotUnavailableReason: "未启动"}
	require.NoError(t, svc.db.Create(&staleNode).Error)
	require.NoError(t, svc.db.Create(&unavailableNode).Error)

	sampler := NewBotRuntimeMetricSampler(svc.db, svc)
	require.NoError(t, sampler.SampleAt(base))

	_, staleSeries, err := svc.QuerySeries(SeriesQuery{Scope: model.MetricScopeNode, NodeUUID: staleNode.UUID, From: base.Add(-time.Minute), To: base.Add(time.Minute), Resolution: "raw"})
	require.NoError(t, err)
	require.Nil(t, findSeries(staleSeries, model.MetricWorkerProcessRSSBytes, "").Points[0].Avg)
	_, unavailableSeries, err := svc.QuerySeries(SeriesQuery{Scope: model.MetricScopeNode, NodeUUID: unavailableNode.UUID, From: base.Add(-time.Minute), To: base.Add(time.Minute), Resolution: "raw"})
	require.NoError(t, err)
	require.Equal(t, 100.0, *findSeries(unavailableSeries, model.MetricWorkerProcessRSSBytes, "").Points[0].Avg)
	require.Nil(t, findSeries(unavailableSeries, model.MetricBotWorkerRSSBytes, "").Points[0].Avg)
}

func TestBotRuntimeMetricSampler_StartStopAreIdempotent(t *testing.T) {
	svc := newMetricSvc(t)
	sampler := NewBotRuntimeMetricSampler(svc.db, svc)
	sampler.Start()
	sampler.Start()
	sampler.Stop()
	sampler.Stop()
}
