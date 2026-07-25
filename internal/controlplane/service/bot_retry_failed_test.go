package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"gorm.io/gorm"
)

type retryFakeDispatcher struct {
	calls int
	last  *workerpb.ApplyBotBatchRequest
}

func (d *retryFakeDispatcher) ApplyBotCommandSchedules(context.Context, string, *workerpb.ApplyBotCommandSchedulesRequest) (*workerpb.ApplyBotCommandSchedulesResponse, error) {
	return &workerpb.ApplyBotCommandSchedulesResponse{}, nil
}

func (d *retryFakeDispatcher) CancelBotCommandSchedules(context.Context, string, *workerpb.CancelBotCommandSchedulesRequest) (*workerpb.CancelBotCommandSchedulesResponse, error) {
	return &workerpb.CancelBotCommandSchedulesResponse{}, nil
}

func (d *retryFakeDispatcher) ApplyBotBatch(_ context.Context, _ string, request *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	d.calls++
	d.last = request
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

func newRetryHarness(t *testing.T) (*BotLoadExecutionService, *gorm.DB, *model.BotStressSession, *model.Bot, *model.Node, *retryFakeDispatcher) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "retry.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{},
		&model.Bot{}, &model.AuditLog{}, &model.User{},
	))
	// AuditLog.UserID NOT NULL，幂等审计 UserID=0 需要兼容；部分库允许 0。
	node := &model.Node{Name: "n1", Host: "127.0.0.1", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/i", StartCommand: "java",
	}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{
		InstanceID: instance.ID, Name: "load", NamePrefix: "load", BotCount: 1,
		Status: model.BotStressSessionRunning, Config: `{"server":"127.0.0.1","port":25565}`,
	}
	require.NoError(t, db.Create(session).Error)
	executorID := node.ID
	bot := &model.Bot{
		InstanceID: instance.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorID,
		Name: "load-001", Status: model.BotStatusError, DesiredState: model.BotDesiredRunning,
		DesiredStateGeneration: 2, ConfigHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LastError: "RUNTIME_MISSING", Config: session.Config,
	}
	require.NoError(t, db.Create(bot).Error)

	dispatcher := &retryFakeDispatcher{}
	svc := NewBotLoadExecutionService(db, nil, nil, nil, dispatcher, BotLoadGoroutineRunner{}, botFleetTestClock{now: time.Now().UTC()})
	return svc, db, session, bot, node, dispatcher
}

func TestRetryFailed_BumpsGenerationAndDispatches(t *testing.T) {
	svc, db, session, bot, _, dispatcher := newRetryHarness(t)
	reqID := uuid.NewString()
	result, err := svc.RetryFailed(context.Background(), session.ID, BotLoadRetryRequest{RequestID: reqID})
	require.NoError(t, err)
	require.Equal(t, 1, result.Requested)
	require.Equal(t, 1, result.Accepted)
	require.Equal(t, 1, dispatcher.calls)
	require.NotNil(t, dispatcher.last)
	require.Equal(t, "running", dispatcher.last.Assignments[0].DesiredState)
	require.Equal(t, int64(3), dispatcher.last.Assignments[0].Generation)

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, int64(3), loaded.DesiredStateGeneration)
	require.Equal(t, model.BotDesiredRunning, loaded.DesiredState)
	require.Empty(t, loaded.LastError)
	require.Equal(t, model.BotStatusPending, loaded.Status)
}

func TestRetryFailed_IdempotentSameRequestID(t *testing.T) {
	svc, db, session, bot, _, dispatcher := newRetryHarness(t)
	// AuditLog 需要合法 user 行时可能失败；若 UserID=0 无 FK 约束 SQLite 可通过。
	reqID := uuid.NewString()
	first, err := svc.RetryFailed(context.Background(), session.ID, BotLoadRetryRequest{RequestID: reqID})
	require.NoError(t, err)
	require.Equal(t, 1, first.Accepted)

	// 手动把 generation 再 +1 模拟外部变更；幂等应直接返回不 dispatch。
	require.NoError(t, db.Model(bot).Update("desired_state_generation", 99).Error)
	second, err := svc.RetryFailed(context.Background(), session.ID, BotLoadRetryRequest{RequestID: reqID})
	require.NoError(t, err)
	require.Equal(t, first.Accepted, second.Accepted)
	require.Equal(t, 1, dispatcher.calls) // 不再次 dispatch

	var loaded model.Bot
	require.NoError(t, db.First(&loaded, bot.ID).Error)
	require.Equal(t, int64(99), loaded.DesiredStateGeneration)
}

func TestRetryFailed_RejectsInvalidState(t *testing.T) {
	svc, db, session, _, _, _ := newRetryHarness(t)
	require.NoError(t, db.Model(session).Update("status", model.BotStressSessionStopped).Error)
	_, err := svc.RetryFailed(context.Background(), session.ID, BotLoadRetryRequest{RequestID: uuid.NewString()})
	require.ErrorIs(t, err, ErrBotLoadInvalidState)
}

func TestRetryFailed_RequiresUUIDRequestID(t *testing.T) {
	svc, _, session, _, _, _ := newRetryHarness(t)
	_, err := svc.RetryFailed(context.Background(), session.ID, BotLoadRetryRequest{RequestID: "not-uuid"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "UUID")
}
