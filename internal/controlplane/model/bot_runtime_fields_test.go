package model_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

func TestBotRuntimeFields_AutoMigrateDefaultsAndRelations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{},
	))

	node := &model.Node{Name: "executor", Host: "127.0.0.1", Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{NodeID: node.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 1}
	require.NoError(t, db.Create(session).Error)

	executorNodeID := node.ID
	bot := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorNodeID,
		Name: "load-001", ConfigHash: "desired-hash", CohortKey: "combat",
	}
	require.NoError(t, db.Create(bot).Error)

	var loaded model.Bot
	require.NoError(t, db.Preload("StressSession").Preload("ExecutorNode").First(&loaded, bot.ID).Error)
	require.Equal(t, int64(1), loaded.DesiredStateGeneration)
	require.Equal(t, model.BotDesiredStopped, loaded.DesiredState)
	require.Zero(t, loaded.ReconnectCount)
	require.Zero(t, loaded.WorkerEpochGeneration)
	require.Zero(t, loaded.LastEventSeq)
	require.Empty(t, loaded.WorkerEpoch)
	require.Nil(t, loaded.LastSeenAt)
	require.Nil(t, loaded.ConnectedAt)
	require.Equal(t, "desired-hash", loaded.ConfigHash)
	require.Equal(t, "combat", loaded.CohortKey)
	require.Equal(t, session.UUID, loaded.StressSession.UUID)
	require.Equal(t, node.ID, loaded.ExecutorNode.ID)
	require.True(t, db.Migrator().HasIndex(&model.Bot{}, "idx_bots_executor_generation"))
}

func TestBotRuntimeFields_AutoMigrateKeepsLegacyRows(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{},
	))
	require.NoError(t, db.Exec(`CREATE TABLE bots (
		id integer PRIMARY KEY AUTOINCREMENT,
		uuid char(36) NOT NULL,
		instance_id integer NOT NULL,
		name varchar(128) NOT NULL,
		status varchar(32) DEFAULT 'pending',
		last_error text,
		config text,
		behavior varchar(64),
		worker_id varchar(128),
		created_at datetime,
		updated_at datetime,
		deleted_at datetime
	)`).Error)

	node := &model.Node{Name: "legacy-node", Host: "127.0.0.1", Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{NodeID: node.ID, Name: "legacy-target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/legacy", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		"INSERT INTO bots (uuid, instance_id, name, status, config, behavior, worker_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		"legacy-bot", instance.ID, "legacy", model.BotStatusConnected, "{}", "idle", node.UUID, now, now,
	).Error)

	require.NoError(t, db.AutoMigrate(&model.Bot{}))
	var loaded model.Bot
	require.NoError(t, db.Where("uuid = ?", "legacy-bot").First(&loaded).Error)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
	require.Equal(t, int64(1), loaded.DesiredStateGeneration)
	require.Equal(t, model.BotDesiredStopped, loaded.DesiredState)
	require.Zero(t, loaded.ReconnectCount)
	require.Zero(t, loaded.WorkerEpochGeneration)
	require.Zero(t, loaded.LastEventSeq)
	require.Nil(t, loaded.LastSeenAt)
	require.Nil(t, loaded.ConnectedAt)
	require.Empty(t, loaded.ConfigHash)
	require.Empty(t, loaded.CohortKey)
}
