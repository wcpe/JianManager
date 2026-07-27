package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func openMetricSamplerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{},
		&model.Bot{}, &model.BotLoadBatch{}, &model.BotLoadCommandCheckpoint{}, &model.BotLoadMetricSample{},
		&model.MetricSeries{}, &model.MetricSampleRaw{},
	))
	return db
}

type metricSamplerCapacityProvider struct {
	snapshot *BotLoadCapacitySnapshot
}

func (p metricSamplerCapacityProvider) Snapshot(context.Context, uint) (*BotLoadCapacitySnapshot, error) {
	return p.snapshot, nil
}

type metricSamplerTargetResourceProvider struct {
	resource BotLoadTargetProcessResource
}

func (p metricSamplerTargetResourceProvider) GetTargetProcessResource(context.Context, string, string) (BotLoadTargetProcessResource, error) {
	return p.resource, nil
}

func seedMetricSession(t *testing.T, db *gorm.DB) (*model.BotStressSession, *model.Node) {
	t.Helper()
	node := &model.Node{UUID: "node-metric", Name: "m", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, UUID: "inst-metric", Name: "i", WorkDir: t.TempDir(),
		Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(inst).Error)
	runState := model.BotLoadRunRunning
	stage := 0
	sess := &model.BotStressSession{
		UUID: "run-metric", InstanceID: inst.ID, Name: "metric-run", NamePrefix: "m",
		BotCount: 2, SchemaVersion: 2, Status: model.BotStressSessionRunning,
		RunState: &runState, CurrentStage: &stage,
	}
	require.NoError(t, db.Create(sess).Error)
	for i, st := range []model.BotStatus{model.BotStatusConnected, model.BotStatusConnecting} {
		b := &model.Bot{
			UUID: "bot-metric-" + string(rune('a'+i)), InstanceID: inst.ID, StressSessionID: &sess.ID,
			Name: "m-" + string(rune('1'+i)), Status: st, Config: `{}`, Behavior: "idle",
		}
		require.NoError(t, db.Create(b).Error)
	}
	// 2 checkpoint：1 prepared + 1 sent
	require.NoError(t, db.Create(&model.BotLoadCommandCheckpoint{
		StressSessionID: sess.ID, RunUUID: sess.UUID, BotUUID: "bot-metric-a",
		StepID: "command-schedule", CommandID: "say", Occurrence: 0, Generation: 1,
		ScheduleRunID: "00000000-0000-4000-8000-0000000000aa",
		ActionRunID:   "00000000-0000-4000-8000-0000000000a1",
		Status:        model.BotLoadCommandCheckpointPrepared,
	}).Error)
	require.NoError(t, db.Create(&model.BotLoadCommandCheckpoint{
		StressSessionID: sess.ID, RunUUID: sess.UUID, BotUUID: "bot-metric-a",
		StepID: "command-schedule", CommandID: "say", Occurrence: 1, Generation: 1,
		ScheduleRunID: "00000000-0000-4000-8000-0000000000aa",
		ActionRunID:   "00000000-0000-4000-8000-0000000000a2",
		Status:        model.BotLoadCommandCheckpointSent,
	}).Error)
	return sess, node
}

func TestBotLoadMetricSampler_SampleSessionWritesCounts(t *testing.T) {
	db := openMetricSamplerDB(t)
	sess, _ := seedMetricSession(t, db)
	now := time.Date(2026, 7, 25, 3, 0, 7, 0, time.UTC) // 截断到 5s → 03:00:05
	sampler := NewBotLoadMetricSampler(db, botFleetTestClock{now: now})

	require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))

	var rows []model.BotLoadMetricSample
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 7, 25, 3, 0, 5, 0, time.UTC), rows[0].SampledAt.UTC())
	require.Contains(t, rows[0].CountsJSON, `"connected":1`)
	require.Contains(t, rows[0].CountsJSON, `"connecting":1`)
	require.Contains(t, rows[0].CommandJSON, `"sent":1`)
	require.Contains(t, rows[0].CommandJSON, `"prepared":1`)

	// 同窗再采：仍 1 行（幂等 upsert）
	require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)

	// 下一窗
	sampler.clock = botFleetTestClock{now: now.Add(5 * time.Second)}
	require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 2)
}

func TestBotLoadMetricSampler_SampleSessionWritesExecutorAndTargetResources(t *testing.T) {
	db := openMetricSamplerDB(t)
	sess, node := seedMetricSession(t, db)
	node.MemoryMB = 4096
	require.NoError(t, db.Save(node).Error)
	secondNode := &model.Node{UUID: "node-executor", Name: "executor", Host: "127.0.0.2", Secret: "s", MemoryMB: 8192}
	require.NoError(t, db.Create(secondNode).Error)
	require.NoError(t, db.Create(&model.BotLoadBatch{
		StressSessionID: sess.ID, ExecutorNodeID: secondNode.ID, Ordinal: 0, PlannedCount: 2,
		IdempotencyKey: "metric-executor-batch", ConnectStartAt: time.Now().UTC(), ConnectIntervalMS: 100,
	}).Error)
	var bots []model.Bot
	require.NoError(t, db.Where("stress_session_id = ?", sess.ID).Find(&bots).Error)
	for i := range bots {
		require.NoError(t, db.Model(&bots[i]).Update("executor_node_id", secondNode.ID).Error)
	}

	now := time.Date(2026, 7, 25, 3, 0, 5, 0, time.UTC)
	insertSamplerMetric(t, db, secondNode.UUID, "", model.MetricScopeNode, model.MetricNodeMemUsed, now.Add(-10*time.Second), 2048*1024*1024)
	insertSamplerMetric(t, db, secondNode.UUID, "", model.MetricScopeNode, model.MetricNodeCPUPct, now.Add(-10*time.Second), 42.5)
	insertSamplerMetric(t, db, node.UUID, "", model.MetricScopeNode, model.MetricNodeMemUsed, now.Add(-10*time.Second), 1024*1024*1024)
	insertSamplerMetric(t, db, node.UUID, "", model.MetricScopeNode, model.MetricNodeCPUPct, now.Add(-10*time.Second), 12.5)
	insertSamplerMetric(t, db, node.UUID, "inst-metric", model.MetricScopeInstance, model.MetricInstHeapUsed, now.Add(-10*time.Second), 512*1024*1024)
	insertSamplerMetric(t, db, node.UUID, "inst-metric", model.MetricScopeInstance, model.MetricInstHeapMax, now.Add(-10*time.Second), 1024*1024*1024)
	insertSamplerMetric(t, db, node.UUID, "inst-metric", model.MetricScopeInstance, model.MetricInstTPS, now.Add(-10*time.Second), 19.8)

	workerRSS := int64(654321)
	targetRSS := int64(987654321)
	targetCPU := 13.5
	targetUptime := 3600.5
	provider := metricSamplerCapacityProvider{snapshot: &BotLoadCapacitySnapshot{NodeCapacities: []BotLoadNodeCapacity{{
		NodeID: secondNode.ID, BotWorkerReady: true, RSSBytes: 123456, EventLoopP95MS: 4.5, WorkerProcessRSSBytes: &workerRSS,
	}}}}
	sampler := NewBotLoadMetricSampler(db, botFleetTestClock{now: now})
	sampler.SetCapacityProvider(provider)
	sampler.SetTargetResourceProvider(metricSamplerTargetResourceProvider{resource: BotLoadTargetProcessResource{
		ProcessRSSBytes: &targetRSS, CPUPercent: &targetCPU, UptimeSeconds: &targetUptime,
	}})
	require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))

	var row model.BotLoadMetricSample
	require.NoError(t, db.Where("stress_session_id = ?", sess.ID).First(&row).Error)
	var executors []map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.ExecutorJSON), &executors))
	require.Len(t, executors, 1)
	require.EqualValues(t, secondNode.ID, executors[0]["nodeId"])
	require.EqualValues(t, 2, executors[0]["activeBots"])
	require.EqualValues(t, 123456, executors[0]["botWorkerRssBytes"])
	require.EqualValues(t, 4.5, executors[0]["eventLoopP95Ms"])
	require.EqualValues(t, 2048*1024*1024, executors[0]["nodeMemUsedBytes"])
	require.EqualValues(t, 8192*1024*1024, executors[0]["nodeMemTotalBytes"])
	require.EqualValues(t, 42.5, executors[0]["nodeCpuPercent"])
	require.EqualValues(t, workerRSS, executors[0]["workerProcessRssBytes"])

	require.NotNil(t, row.TargetLegacyJSON)
	var target map[string]any
	require.NoError(t, json.Unmarshal([]byte(*row.TargetLegacyJSON), &target))
	require.EqualValues(t, 19.8, target["tps"])
	resource := target["targetResource"].(map[string]any)
	require.EqualValues(t, 512*1024*1024, resource["heapUsedBytes"])
	require.EqualValues(t, 4096*1024*1024, resource["hostMemTotalBytes"])
	require.EqualValues(t, targetRSS, resource["processRssBytes"])
	require.EqualValues(t, targetCPU, resource["cpuPercent"])
	require.EqualValues(t, targetUptime, resource["uptimeSeconds"])
}

func TestBotLoadMetricSampler_SampleSessionMarksStaleNodeMetricsUnavailable(t *testing.T) {
	db := openMetricSamplerDB(t)
	sess, _ := seedMetricSession(t, db)
	executor := &model.Node{UUID: "node-stale", Name: "stale", Host: "127.0.0.3", Secret: "s", MemoryMB: 4096}
	require.NoError(t, db.Create(executor).Error)
	require.NoError(t, db.Create(&model.BotLoadBatch{
		StressSessionID: sess.ID, ExecutorNodeID: executor.ID, Ordinal: 0, PlannedCount: 1,
		IdempotencyKey: "metric-stale-batch", ConnectStartAt: time.Now().UTC(), ConnectIntervalMS: 100,
	}).Error)
	now := time.Date(2026, 7, 25, 3, 0, 5, 0, time.UTC)
	insertSamplerMetric(t, db, executor.UUID, "", model.MetricScopeNode, model.MetricNodeMemUsed, now.Add(-31*time.Second), 1)
	sampler := NewBotLoadMetricSampler(db, botFleetTestClock{now: now})
	require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))

	var row model.BotLoadMetricSample
	require.NoError(t, db.Where("stress_session_id = ?", sess.ID).First(&row).Error)
	var executors []map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.ExecutorJSON), &executors))
	require.Len(t, executors, 1)
	require.Nil(t, executors[0]["nodeMemUsedBytes"])
	require.Contains(t, executors[0]["unavailable"], "nodeMemUsedBytes:METRIC_STALE")
}

func insertSamplerMetric(t *testing.T, db *gorm.DB, nodeUUID, instanceID string, scope model.MetricScope, key string, ts time.Time, value float64) {
	t.Helper()
	series := model.MetricSeries{NodeUUID: nodeUUID, InstanceID: instanceID, Scope: scope, MetricKey: key, Unit: "test", LastSeenAt: ts}
	require.NoError(t, db.Create(&series).Error)
	require.NoError(t, db.Create(&model.MetricSampleRaw{SeriesID: series.ID, TS: ts, Value: &value}).Error)
}

func TestBotLoadMetricSampler_ListMetricsResolution(t *testing.T) {
	db := openMetricSamplerDB(t)
	sess, _ := seedMetricSession(t, db)
	base := time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)
	sampler := NewBotLoadMetricSampler(db, botFleetTestClock{now: base})
	for i := 0; i < 6; i++ {
		sampler.clock = botFleetTestClock{now: base.Add(time.Duration(i) * 5 * time.Second)}
		require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))
	}
	res, err := sampler.ListMetrics(context.Background(), sess.ID, nil, nil, "raw")
	require.NoError(t, err)
	require.Equal(t, "raw", res.Resolution)
	require.Len(t, res.Items, 6)

	res15, err := sampler.ListMetrics(context.Background(), sess.ID, nil, nil, "15s")
	require.NoError(t, err)
	require.Equal(t, "15s", res15.Resolution)
	require.LessOrEqual(t, len(res15.Items), 3)
	require.NotEmpty(t, res15.Items[0].Counts)
}
