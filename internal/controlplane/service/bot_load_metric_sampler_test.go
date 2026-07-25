package service

import (
	"context"
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
		&model.Bot{}, &model.BotLoadCommandCheckpoint{}, &model.BotLoadMetricSample{},
	))
	return db
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
