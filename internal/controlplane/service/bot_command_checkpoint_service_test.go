package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

func setupCheckpointDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{},
		&model.Instance{},
		&model.Bot{},
		&model.BotStressSession{},
		&model.BotLoadCommandCheckpoint{},
	))
	node := &model.Node{Name: "exec", Host: "127.0.0.1", Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{NodeID: node.ID, Name: "t", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "wd", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 1}
	require.NoError(t, db.Create(session).Error)
	botRow := &model.Bot{InstanceID: instance.ID, Name: "b1", Status: model.BotStatusPending}
	require.NoError(t, db.Create(botRow).Error)
	t.Setenv("IGNORE", "")
	return db
}

func TestBotLoadCommandCheckpointService_EnsureAndMarkSent(t *testing.T) {
	db := setupCheckpointDB(t)
	svc := NewBotLoadCommandCheckpointService(db)
	session := model.BotStressSession{}
	require.NoError(t, db.First(&session).Error)
	plan := &CommandSchedulePlan{
		DurationMS: 1000,
		JitterMS:   0,
	}
	plan.Occurrences = []CommandScheduleOccurrence{
		{CommandID: "a", Occurrence: 0},
		{CommandID: "b", Occurrence: 0},
	}
	scheduleRunID := "11111111-2222-3333-4444-555555555555"
	runUUID := "22222222-3333-4444-5555-666666666666"
	botUUID := "00000000-0000-0000-0000-000000000001"
	err := svc.EnsureOccurrences(context.Background(), session.ID, runUUID, botUUID, "command-schedule", scheduleRunID, "ct", 1, plan.Occurrences, map[string]struct{}{commandOccurrenceKey("b", 0): {}})
	require.NoError(t, err)
	var rows []model.BotLoadCommandCheckpoint
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1, "skip 的 occurrence 不写入")
	require.Equal(t, "a", rows[0].CommandID)
	require.Equal(t, model.BotLoadCommandCheckpointPrepared, rows[0].Status)

	require.NoError(t, svc.MarkSent(context.Background(), runUUID, botUUID, "command-schedule", "a", 0, 1, 5000))
	require.NoError(t, db.First(&rows[0]).Error)
	require.Equal(t, model.BotLoadCommandCheckpointSent, rows[0].Status)
	require.Equal(t, 1, rows[0].Attempt)
	require.NotNil(t, rows[0].SentAtUnixMs)

	// 二次 Ensure 不会创建重复行
	require.NoError(t, svc.EnsureOccurrences(context.Background(), session.ID, runUUID, botUUID, "command-schedule", scheduleRunID, "ct", 2, plan.Occurrences, map[string]struct{}{commandOccurrenceKey("b", 0): {}}))
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.EqualValues(t, 2, rows[0].Generation, "重新 Ensure 必须更新 generation")
}

func TestBotLoadCommandCheckpointService_MarkFailed(t *testing.T) {
	db := setupCheckpointDB(t)
	svc := NewBotLoadCommandCheckpointService(db)
	session := model.BotStressSession{}
	require.NoError(t, db.First(&session).Error)
	plan := &CommandSchedulePlan{DurationMS: 1000}
	plan.Occurrences = []CommandScheduleOccurrence{{CommandID: "a", Occurrence: 0}}
	scheduleRunID := "11111111-2222-3333-4444-555555555555"
	runUUID := "22222222-3333-4444-5555-666666666666"
	botUUID := "00000000-0000-0000-0000-000000000001"
	require.NoError(t, svc.EnsureOccurrences(context.Background(), session.ID, runUUID, botUUID, "command-schedule", scheduleRunID, "ct", 1, plan.Occurrences, nil))
	require.NoError(t, svc.MarkFailed(context.Background(), runUUID, botUUID, "command-schedule", "a", 0, model.BotLoadCommandCheckpointFailed, 3, "COMMAND_SEND_FAILED"))
	var row model.BotLoadCommandCheckpoint
	require.NoError(t, db.First(&row).Error)
	require.Equal(t, model.BotLoadCommandCheckpointFailed, row.Status)
	require.Equal(t, "COMMAND_SEND_FAILED", row.ErrorCode)
	require.Equal(t, 3, row.Attempt)
}

func TestBotCommandScheduleSnapshot_RoundTrip(t *testing.T) {
	plan, err := NormalizeCommandSchedule(sampleCommandSchedule())
	require.NoError(t, err)
	scheduleRunID := "11111111-2222-3333-4444-555555555555"
	jitterSeed := "20260720"
	botUUID := "00000000-0000-0000-0000-000000000001"
	require.NoError(t, FinalizeCommandSchedulePlan(plan, scheduleRunID, jitterSeed, "command-schedule", botUUID, CommandScheduleTemplateContext{RunID: "1", CorrelationToken: "ct"}, nil))
	snapshot, err := BotCommandScheduleSnapshot(plan)
	require.NoError(t, err)
	require.NotEmpty(t, snapshot)
	// 同一 plan 二次生成应一致
	snapshot2, err := BotCommandScheduleSnapshot(plan)
	require.NoError(t, err)
	require.Equal(t, snapshot, snapshot2)
}

func TestBotCommandScheduleSnapshot_RejectsNil(t *testing.T) {
	_, err := BotCommandScheduleSnapshot(nil)
	require.Error(t, err)
}