package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

func newBotFreshnessHarness(t *testing.T) (*gorm.DB, *model.Node, *model.Bot, time.Time) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "fresh.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{},
	))

	node := &model.Node{Name: "executor", Host: "127.0.0.1", Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "target", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 1,
		Status: model.BotStressSessionRunning,
	}
	require.NoError(t, db.Create(session).Error)
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-15 * time.Second)
	executorID := node.ID
	bot := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorID,
		Name: "load-001", Status: model.BotStatusConnected, DesiredState: model.BotDesiredRunning,
		DesiredStateGeneration: 1, ConfigHash: "hash", LastSeenAt: &seen,
	}
	require.NoError(t, db.Create(bot).Error)
	return db, node, bot, now
}

func TestBotFreshnessService_MarkStaleConnectedToDisconnected(t *testing.T) {
	db, _, bot, now := newBotFreshnessHarness(t)
	svc := NewBotFreshnessServiceWithRepository(
		newGormBotFreshnessRepository(db), botFleetTestClock{now: now},
		10*time.Second, 90*time.Second,
	)
	require.NoError(t, svc.Sweep(context.Background()))

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotStatusDisconnected, loaded.Status)
	require.Equal(t, botStatusErrorCodeStale, loaded.LastError)
	require.Equal(t, model.BotDesiredRunning, loaded.DesiredState)
}

func TestBotFreshnessService_DoesNotStaleRecentLastSeen(t *testing.T) {
	db, _, bot, now := newBotFreshnessHarness(t)
	recent := now.Add(-3 * time.Second)
	require.NoError(t, db.Model(bot).Update("last_seen_at", recent).Error)

	svc := NewBotFreshnessServiceWithRepository(
		newGormBotFreshnessRepository(db), botFleetTestClock{now: now},
		10*time.Second, 90*time.Second,
	)
	require.NoError(t, svc.Sweep(context.Background()))

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
}

func TestBotFreshnessService_RuntimeMissingAfterLongWindow(t *testing.T) {
	db, _, bot, now := newBotFreshnessHarness(t)
	old := now.Add(-100 * time.Second)
	require.NoError(t, db.Model(bot).Updates(map[string]any{
		"status": model.BotStatusDisconnected, "last_seen_at": old, "last_error": botStatusErrorCodeStale,
	}).Error)

	svc := NewBotFreshnessServiceWithRepository(
		newGormBotFreshnessRepository(db), botFleetTestClock{now: now},
		10*time.Second, 90*time.Second,
	)
	require.NoError(t, svc.Sweep(context.Background()))

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotStatusError, loaded.Status)
	require.Equal(t, botStatusErrorCodeMissing, loaded.LastError)
}

func TestBotFreshnessService_StoppedDesiredNotStaled(t *testing.T) {
	db, _, bot, now := newBotFreshnessHarness(t)
	require.NoError(t, db.Model(bot).Update("desired_state", model.BotDesiredStopped).Error)

	svc := NewBotFreshnessServiceWithRepository(
		newGormBotFreshnessRepository(db), botFleetTestClock{now: now},
		10*time.Second, 90*time.Second,
	)
	require.NoError(t, svc.Sweep(context.Background()))

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
}

func TestBotFreshnessService_MarkNodeOfflineAggregates(t *testing.T) {
	db, node, bot, now := newBotFreshnessHarness(t)
	svc := NewBotFreshnessServiceWithRepository(
		newGormBotFreshnessRepository(db), botFleetTestClock{now: now},
		10*time.Second, 90*time.Second,
	)
	require.NoError(t, svc.MarkNodeOffline(context.Background(), node.ID))

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, model.BotStatusDisconnected, loaded.Status)
	require.Equal(t, botStatusErrorCodeOffline, loaded.LastError)
}

func TestBotFreshnessSweeper_StopReleasesGoroutine(t *testing.T) {
	db, _, _, now := newBotFreshnessHarness(t)
	svc := NewBotFreshnessServiceWithRepository(
		newGormBotFreshnessRepository(db), botFleetTestClock{now: now},
		10*time.Second, 90*time.Second,
	)
	sweeper := &BotFreshnessSweeper{service: svc, interval: 50 * time.Millisecond}
	sweeper.Start()
	sweeper.Start() // 幂等
	time.Sleep(80 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		sweeper.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 未在时限内返回")
	}
}
