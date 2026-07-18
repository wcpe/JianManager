package model_test

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

func TestBotLoadModels_AutoMigrateAndConstraints(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{},
		&model.Instance{},
		&model.BotStressSession{},
		&model.BotLoadBatch{},
		&model.Bot{},
	))

	node := &model.Node{Name: "executor", Host: "127.0.0.1", Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{NodeID: node.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 1}
	require.NoError(t, db.Create(session).Error)

	start := time.Now().UTC().Truncate(time.Millisecond)
	batch := &model.BotLoadBatch{
		StressSessionID:   session.ID,
		ExecutorNodeID:    node.ID,
		Ordinal:           1,
		PlannedCount:      1,
		State:             model.BotLoadBatchPlanned,
		IdempotencyKey:    "run-1-batch-1",
		ConnectStartAt:    start,
		ConnectIntervalMS: 200,
	}
	require.NoError(t, db.Create(batch).Error)

	executorNodeID := node.ID
	loadBatchID := batch.ID
	bot := &model.Bot{
		InstanceID:     instance.ID,
		ExecutorNodeID: &executorNodeID,
		LoadBatchID:    &loadBatchID,
		Name:           "load-001",
		Status:         model.BotStatusPending,
	}
	require.NoError(t, db.Create(bot).Error)

	var loaded model.Bot
	require.NoError(t, db.Preload("ExecutorNode").Preload("LoadBatch").First(&loaded, bot.ID).Error)
	require.NotNil(t, loaded.ExecutorNode)
	require.Equal(t, node.ID, loaded.ExecutorNode.ID)
	require.NotNil(t, loaded.LoadBatch)
	require.Equal(t, batch.ID, loaded.LoadBatch.ID)

	duplicateOrdinal := *batch
	duplicateOrdinal.ID = 0
	duplicateOrdinal.UUID = ""
	duplicateOrdinal.IdempotencyKey = "run-1-batch-other"
	require.Error(t, db.Create(&duplicateOrdinal).Error)

	duplicateKey := *batch
	duplicateKey.ID = 0
	duplicateKey.UUID = ""
	duplicateKey.Ordinal = 2
	require.Error(t, db.Create(&duplicateKey).Error)
}
