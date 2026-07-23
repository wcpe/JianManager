package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func openRunIntentDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Node{}, &model.Instance{},
		&model.BotStressSession{}, &model.BotLoadRunEvent{},
	))
	return db
}

func seedV2Session(t *testing.T, db *gorm.DB, state model.BotLoadRunState) *model.BotStressSession {
	t.Helper()
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "w", StartCommand: "java",
	}
	require.NoError(t, db.Create(inst).Error)
	stage := 0
	verdict := model.BotLoadVerdictPending
	maxStable := 0
	st := state
	sess := &model.BotStressSession{
		InstanceID: inst.ID, Name: "run", NamePrefix: "b", BotCount: 10,
		SchemaVersion: 2, Status: MapRunStateToLegacyStatus(state),
		LoadProfile: `{"type":"stable","targetBots":10,"rampUpSeconds":0,"durationSeconds":60}`,
		Thresholds:  `{}`, RunState: &st, CurrentStage: &stage,
		Verdict: &verdict, MaxStableBots: &maxStable, FailureSummary: `{}`,
	}
	require.NoError(t, db.Create(sess).Error)
	return sess
}

func TestBotLoadRunIntentService_StopAndCancel(t *testing.T) {
	db := openRunIntentDB(t)
	svc := NewBotLoadRunIntentService(db)
	ctx := context.Background()

	// running → stopping
	sess := seedV2Session(t, db, model.BotLoadRunRunning)
	out, err := svc.AcceptStop(ctx, sess.ID, "manual")
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunStopping, *out.RunState)
	require.Equal(t, model.BotStressSessionRunning, out.Status)

	// stopping 幂等
	out2, err := svc.AcceptStop(ctx, sess.ID, "again")
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunStopping, *out2.RunState)

	// 可升级 cancel
	out3, err := svc.AcceptCancel(ctx, sess.ID, "abort")
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunCancelling, *out3.RunState)

	// 完成 cancel 终态
	out4, err := svc.ApplyIntent(ctx, sess.ID, BotLoadIntentCancelled, "")
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunCancelled, *out4.RunState)
	require.Equal(t, model.BotStressSessionStopped, out4.Status)
	require.NotNil(t, out4.EndedAt)

	// 终态 stop 拒绝
	_, err = svc.AcceptStop(ctx, sess.ID, "x")
	require.ErrorIs(t, err, ErrBotLoadInvalidState)

	// 事件已写入
	var count int64
	require.NoError(t, db.Model(&model.BotLoadRunEvent{}).Where("stress_session_id = ?", sess.ID).Count(&count).Error)
	require.GreaterOrEqual(t, count, int64(3))
}

func TestBotLoadRunIntentService_ReadyAndStart(t *testing.T) {
	db := openRunIntentDB(t)
	svc := NewBotLoadRunIntentService(db)
	ctx := context.Background()

	sess := seedV2Session(t, db, model.BotLoadRunPending)
	// pending 可直接 ready（兼容）
	out, err := svc.MarkReady(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunReady, *out.RunState)

	// start 仅 ready
	out, err = svc.ApplyIntent(ctx, sess.ID, BotLoadIntentStart, "")
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunStarting, *out.RunState)
	require.Equal(t, model.BotStressSessionPending, out.Status)
}

func TestBotLoadRunIntentService_RejectsV1(t *testing.T) {
	db := openRunIntentDB(t)
	svc := NewBotLoadRunIntentService(db)
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "w", StartCommand: "java",
	}
	require.NoError(t, db.Create(inst).Error)
	sess := &model.BotStressSession{
		InstanceID: inst.ID, Name: "v1", NamePrefix: "b", BotCount: 1,
		SchemaVersion: 1, Status: model.BotStressSessionPending,
	}
	require.NoError(t, db.Create(sess).Error)
	_, err := svc.AcceptStop(context.Background(), sess.ID, "")
	require.ErrorIs(t, err, ErrBotLoadInvalidState)
}
