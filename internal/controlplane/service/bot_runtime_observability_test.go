package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestMetric_QueryBotRuntimeFiltersSessionNodesAndDeclaresSharedRuntime(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}))
	base := metricBase()
	target := model.Node{Name: "target", Status: model.NodeStatusOnline}
	executor := model.Node{Name: "executor", Status: model.NodeStatusOnline}
	other := model.Node{Name: "other", Status: model.NodeStatusOnline}
	require.NoError(t, svc.db.Create(&target).Error)
	require.NoError(t, svc.db.Create(&executor).Error)
	require.NoError(t, svc.db.Create(&other).Error)
	instance := model.Instance{NodeID: target.ID, Name: "server", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar"}
	require.NoError(t, svc.db.Create(&instance).Error)
	session := model.BotStressSession{InstanceID: instance.ID, Name: "run", NamePrefix: "bot", BotCount: 1}
	require.NoError(t, svc.db.Create(&session).Error)
	batch := model.BotLoadBatch{StressSessionID: session.ID, ExecutorNodeID: executor.ID, Ordinal: 1, PlannedCount: 1, ConnectStartAt: base, ConnectIntervalMS: 1, IdempotencyKey: "batch"}
	require.NoError(t, svc.db.Create(&batch).Error)
	value := 128.0
	require.NoError(t, svc.Ingest([]Sample{
		{NodeUUID: target.UUID, Scope: model.MetricScopeNode, MetricKey: model.MetricBotWorkerRSSBytes, Unit: "bytes", TS: base, Value: &value},
		{NodeUUID: executor.UUID, Scope: model.MetricScopeNode, MetricKey: model.MetricBotWorkerRSSBytes, Unit: "bytes", TS: base, Value: &value},
		{NodeUUID: other.UUID, Scope: model.MetricScopeNode, MetricKey: model.MetricBotWorkerRSSBytes, Unit: "bytes", TS: base, Value: &value},
	}))

	got, err := svc.QueryBotRuntime(BotRuntimeQuery{SessionID: session.ID, From: base.Add(-time.Minute), To: base.Add(time.Minute), Resolution: "raw"})
	require.NoError(t, err)
	require.True(t, got.SharedRuntime)
	require.Len(t, got.Nodes, 2)
	require.Equal(t, []uint{target.ID, executor.ID}, []uint{got.Nodes[0].NodeID, got.Nodes[1].NodeID})
	require.NotEmpty(t, got.Notice)
}

func TestMetric_QueryBotRuntimeWritesUnavailableMetadataForStaleSnapshot(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	base := metricBase()
	stale := base.Add(-91 * time.Second)
	node := model.Node{Name: "stale", Status: model.NodeStatusOnline, LastHeartbeat: &stale, ManagedRuntimeObservedAt: &stale, BotUnavailableReason: "未启动"}
	require.NoError(t, svc.db.Create(&node).Error)

	got, err := svc.QueryBotRuntime(BotRuntimeQuery{NodeID: node.ID, From: base.Add(-time.Minute), To: base.Add(time.Minute), Resolution: "raw"})
	require.NoError(t, err)
	require.Len(t, got.Unavailable, 1)
	require.Equal(t, node.ID, got.Unavailable[0].NodeID)
}
