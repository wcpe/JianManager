package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"gorm.io/gorm"
)

type reconcileFakeDispatcher struct {
	requests []*workerpb.ApplyBotBatchRequest
}

func (d *reconcileFakeDispatcher) ApplyBotCommandSchedules(context.Context, string, *workerpb.ApplyBotCommandSchedulesRequest) (*workerpb.ApplyBotCommandSchedulesResponse, error) {
	return &workerpb.ApplyBotCommandSchedulesResponse{}, nil
}

func (d *reconcileFakeDispatcher) ApplyBotBatch(_ context.Context, _ string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	d.requests = append(d.requests, request)
	results := make([]*workerpb.ApplyBotBatchItemResult, 0, len(request.Assignments))
	for _, a := range request.Assignments {
		results = append(results, &workerpb.ApplyBotBatchItemResult{
			BotUuid: a.BotUuid, Accepted: true, Status: "accepted",
		})
	}
	return &workerpb.ApplyBotBatchResponse{
		BatchId: request.BatchId, IdempotencyKey: request.IdempotencyKey, Results: results,
	}, nil
}

func TestReconcileBotFleetSnapshot_CreatesMissingRunning(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "rec.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{},
	))
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/i", StartCommand: "java",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "s", NamePrefix: "s", BotCount: 1,
		Status: model.BotStressSessionRunning, Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(session).Error)
	executorID := node.ID
	bot := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorID,
		Name: "missing", Status: model.BotStatusDisconnected, DesiredState: model.BotDesiredRunning,
		DesiredStateGeneration: 4, ConfigHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Config: session.Config,
	}
	require.NoError(t, db.Create(bot).Error)

	dispatcher := &reconcileFakeDispatcher{}
	svc := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, BotLoadGoroutineRunner{}, botFleetTestClock{now: time.Now().UTC()})
	// snapshot 为空 → desired running 缺失 → 应 Apply running
	err = svc.ReconcileBotFleetSnapshot(context.Background(), node.ID, node.UUID, session.UUID, &workerpb.GetBotFleetSnapshotResponse{})
	require.NoError(t, err)
	require.NotEmpty(t, dispatcher.requests)
	found := false
	for _, req := range dispatcher.requests {
		for _, a := range req.Assignments {
			if a.BotUuid == bot.UUID && a.DesiredState == "running" {
				found = true
				require.Equal(t, int64(4), a.Generation)
			}
		}
	}
	require.True(t, found, "应下发 missing running assignment")
}

func TestReconcileBotFleetSnapshot_StopsOrphanWithSessionMatch(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "orphan.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{},
	))
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/i", StartCommand: "java",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "s", NamePrefix: "s", BotCount: 0,
		Status: model.BotStressSessionRunning, Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(session).Error)

	dispatcher := &reconcileFakeDispatcher{}
	svc := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, BotLoadGoroutineRunner{}, botFleetTestClock{now: time.Now().UTC()})
	snapshot := &workerpb.GetBotFleetSnapshotResponse{
		Bots: []*workerpb.BotRuntimeSnapshot{{
			BotUuid: "orphan-bot", SessionUuid: session.UUID, Generation: 1,
			ConfigHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Status: "connected",
		}},
	}
	require.NoError(t, svc.ReconcileBotFleetSnapshot(context.Background(), node.ID, node.UUID, session.UUID, snapshot))
	require.NotEmpty(t, dispatcher.requests)
	require.Equal(t, "stopped", dispatcher.requests[0].Assignments[0].DesiredState)
	require.Equal(t, "orphan-bot", dispatcher.requests[0].Assignments[0].BotUuid)
}
