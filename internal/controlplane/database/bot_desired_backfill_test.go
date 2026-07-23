package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

func TestBackfillBotDesiredState_ActiveSessionRunning(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "backfill.db")
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
		InstanceID: instance.ID, Name: "s", NamePrefix: "s", BotCount: 2,
		Status: model.BotStressSessionRunning,
	}
	require.NoError(t, db.Create(session).Error)
	// 模拟 AutoMigrate 默认 stopped 后的历史行。
	active := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, Name: "active",
		Status: model.BotStatusConnected, DesiredState: model.BotDesiredStopped, DesiredStateGeneration: 1,
	}
	stopped := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, Name: "stopped",
		Status: model.BotStatusStopped, DesiredState: model.BotDesiredStopped, DesiredStateGeneration: 0,
	}
	orphan := &model.Bot{
		InstanceID: instance.ID, Name: "orphan",
		Status: model.BotStatusConnected, DesiredState: model.BotDesiredStopped, DesiredStateGeneration: 1,
	}
	require.NoError(t, db.Create(active).Error)
	require.NoError(t, db.Create(stopped).Error)
	require.NoError(t, db.Create(orphan).Error)

	require.NoError(t, backfillBotDesiredState(db))
	// 幂等再跑一次。
	require.NoError(t, backfillBotDesiredState(db))

	var loadedActive, loadedStopped, loadedOrphan model.Bot
	require.NoError(t, db.First(&loadedActive, active.ID).Error)
	require.NoError(t, db.First(&loadedStopped, stopped.ID).Error)
	require.NoError(t, db.First(&loadedOrphan, orphan.ID).Error)
	require.Equal(t, model.BotDesiredRunning, loadedActive.DesiredState)
	require.Equal(t, model.BotDesiredStopped, loadedStopped.DesiredState)
	require.Equal(t, model.BotDesiredStopped, loadedOrphan.DesiredState)
	require.Equal(t, int64(1), loadedStopped.DesiredStateGeneration)
}

func TestBackfillBotDesiredState_LegacyTableAfterMigrate(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-backfill.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}))
	require.NoError(t, db.Exec(`CREATE TABLE bots (
		id integer PRIMARY KEY AUTOINCREMENT,
		uuid char(36) NOT NULL,
		instance_id integer NOT NULL,
		stress_session_id integer,
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
	node := &model.Node{Name: "legacy", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "legacy-i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/legacy", StartCommand: "java",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "legacy-s", NamePrefix: "lg", BotCount: 1,
		Status: model.BotStressSessionRunning,
	}
	require.NoError(t, db.Create(session).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO bots (uuid, instance_id, stress_session_id, name, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"legacy-bot", instance.ID, session.ID, "legacy", model.BotStatusConnected, now, now,
	).Error)

	require.NoError(t, db.AutoMigrate(&model.Bot{}))
	require.NoError(t, backfillBotDesiredState(db))

	var loaded model.Bot
	require.NoError(t, db.Where("uuid = ?", "legacy-bot").First(&loaded).Error)
	require.Equal(t, model.BotDesiredRunning, loaded.DesiredState)
	require.Equal(t, int64(1), loaded.DesiredStateGeneration)
	require.Zero(t, loaded.ReconnectCount)
}
