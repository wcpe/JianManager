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

func openProjectionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.Bot{},
		&model.BotLoadActionResult{}, &model.BotLoadCommandCheckpoint{}, &model.BotLoadRunEvent{},
	))
	return db
}

func TestBotLoadProjection_ListFailuresAndEvents(t *testing.T) {
	db := openProjectionDB(t)
	node := &model.Node{UUID: "n-p", Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, UUID: "i-p", Name: "i", WorkDir: t.TempDir(), Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(inst).Error)
	runState := model.BotLoadRunRunning
	sess := &model.BotStressSession{
		UUID: "run-proj", InstanceID: inst.ID, Name: "p", NamePrefix: "p", BotCount: 1,
		SchemaVersion: 2, Status: model.BotStressSessionRunning, RunState: &runState,
	}
	require.NoError(t, db.Create(sess).Error)
	exec := node.ID
	bot := &model.Bot{
		UUID: "bot-proj", InstanceID: inst.ID, StressSessionID: &sess.ID, ExecutorNodeID: &exec,
		Name: "b1", Status: model.BotStatusError, Config: `{}`, Behavior: "idle",
		LastError: "connect timeout",
	}
	require.NoError(t, db.Create(bot).Error)
	ended := time.Date(2026, 7, 25, 7, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&model.BotLoadActionResult{
		StressSessionID: sess.ID, BotID: bot.ID, CohortKey: "all", StepID: "wait-room",
		ActionRunID: "00000000-0000-4000-8000-0000000000f1", Attempt: 1,
		Status: model.BotLoadActionFailed, ErrorCode: "ACTION_TARGET_NOT_FOUND", Message: "no target",
		StartedAt: ended.Add(-time.Second), EndedAt: &ended,
	}).Error)
	require.NoError(t, db.Create(&model.BotLoadCommandCheckpoint{
		StressSessionID: sess.ID, RunUUID: sess.UUID, BotUUID: bot.UUID,
		StepID: "command-schedule", CommandID: "say", Occurrence: 0, Generation: 1,
		ScheduleRunID: "00000000-0000-4000-8000-0000000000aa",
		ActionRunID:   "00000000-0000-4000-8000-0000000000f2",
		Status:        model.BotLoadCommandCheckpointTimedOut,
		ErrorCode:     "COMMAND_DEADLINE_EXCEEDED",
	}).Error)
	require.NoError(t, db.Create(&model.BotLoadRunEvent{
		StressSessionID: sess.ID, RunUUID: sess.UUID, Type: model.BotLoadRunEventRunState,
		OccurredAt: ended, PayloadJSON: `{"runState":"running"}`,
	}).Error)
	require.NoError(t, db.Create(&model.BotLoadRunEvent{
		StressSessionID: sess.ID, RunUUID: sess.UUID, Type: model.BotLoadRunEventCommandSend,
		OccurredAt: ended.Add(time.Second), PayloadJSON: `{"mode":"item","status":"failed"}`,
	}).Error)

	svc := NewBotLoadProjectionService(db)
	fails, err := svc.ListFailures(context.Background(), sess.ID, ListFailuresQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), fails.Total)
	require.Len(t, fails.Items, 2)

	// category filter
	onlyTarget, err := svc.ListFailures(context.Background(), sess.ID, ListFailuresQuery{Page: 1, PageSize: 10, Category: "target"})
	require.NoError(t, err)
	require.Equal(t, int64(1), onlyTarget.Total)
	require.Equal(t, "target", onlyTarget.Items[0].Category)

	events, err := svc.ListEvents(context.Background(), sess.ID, ListEventsQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), events.Total)
	require.NotEqual(t, "0", events.SnapshotEventID)
	require.Len(t, events.Items, 2)
	// 第二页在同一 snapshot 下稳定
	page2, err := svc.ListEvents(context.Background(), sess.ID, ListEventsQuery{
		Page: 2, PageSize: 1, SnapshotEventID: events.SnapshotEventID,
	})
	require.NoError(t, err)
	require.Equal(t, events.SnapshotEventID, page2.SnapshotEventID)
	require.Len(t, page2.Items, 1)
}

func TestBotLoadProjection_ListBotsAndRecent(t *testing.T) {
	db := openProjectionDB(t)
	node := &model.Node{UUID: "n-b", Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, UUID: "i-b", Name: "i", WorkDir: t.TempDir(), Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(inst).Error)
	sess := &model.BotStressSession{
		UUID: "run-bots", InstanceID: inst.ID, Name: "b", NamePrefix: "b", BotCount: 2,
		SchemaVersion: 2, Status: model.BotStressSessionRunning,
	}
	require.NoError(t, db.Create(sess).Error)
	exec := node.ID
	for i := range 2 {
		require.NoError(t, db.Create(&model.Bot{
			UUID: "bot-b-" + string(rune('a'+i)), InstanceID: inst.ID, StressSessionID: &sess.ID,
			ExecutorNodeID: &exec, Name: "b-" + string(rune('1'+i)), Status: model.BotStatusConnected,
			Config: `{}`, Behavior: "idle",
		}).Error)
	}
	require.NoError(t, db.Create(&model.BotLoadRunEvent{
		StressSessionID: sess.ID, RunUUID: sess.UUID, Type: model.BotLoadRunEventRunState,
		OccurredAt: time.Now().UTC(), PayloadJSON: `{"runState":"running"}`,
	}).Error)

	svc := NewBotLoadProjectionService(db)
	bots, err := svc.ListBots(context.Background(), sess.ID, ListBotsQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), bots.Total)
	require.Len(t, bots.Items, 2)
	require.Equal(t, "connected", bots.Items[0].Status)

	filtered, err := svc.ListBots(context.Background(), sess.ID, ListBotsQuery{Page: 1, PageSize: 10, Status: "connected"})
	require.NoError(t, err)
	require.Equal(t, int64(2), filtered.Total)

	recent, err := svc.RecentEvents(context.Background(), sess.ID, 5)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	require.Equal(t, model.BotLoadRunEventRunState, recent[0].Type)
}
