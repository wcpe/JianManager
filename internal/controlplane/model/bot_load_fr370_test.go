package model_test

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

func openFR370DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Node{},
		&model.Instance{},
		&model.BotLoadTemplate{},
		&model.BotStressSession{},
		&model.BotLoadMetricSample{},
		&model.BotLoadRunEvent{},
		&model.Bot{},
	))
	return db
}

func TestBotLoadTemplate_ActiveNameKeyUniqueAndSoftDeleteReuse(t *testing.T) {
	db := openFR370DB(t)
	user := &model.User{Username: "u1", Password: "x", Role: model.RolePlatformAdmin}
	require.NoError(t, db.Create(user).Error)

	key := "abc123"
	tpl := &model.BotLoadTemplate{
		CreatedBy: user.ID, ActiveNameKey: &key, Name: "my-tpl",
		Description: "d", CommandSchedule: `{}`, LoadProfile: `{}`, Thresholds: `{}`, Tags: `[]`,
	}
	require.NoError(t, db.Create(tpl).Error)

	// 同 created_by + active_name_key 冲突
	dup := &model.BotLoadTemplate{
		CreatedBy: user.ID, ActiveNameKey: &key, Name: "my-tpl",
		Description: "d", CommandSchedule: `{}`, LoadProfile: `{}`, Thresholds: `{}`, Tags: `[]`,
	}
	require.Error(t, db.Create(dup).Error)

	// 软删后置 null，可复用
	require.NoError(t, db.Model(tpl).Updates(map[string]any{"active_name_key": nil, "deleted_at": time.Now().UTC()}).Error)
	reuse := &model.BotLoadTemplate{
		CreatedBy: user.ID, ActiveNameKey: &key, Name: "my-tpl",
		Description: "d2", CommandSchedule: `{}`, LoadProfile: `{}`, Thresholds: `{}`, Tags: `[]`,
	}
	require.NoError(t, db.Create(reuse).Error)
}

func TestBotStressSession_V2ColumnsAndMetricUnique(t *testing.T) {
	db := openFR370DB(t)
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "w", StartCommand: "java"}
	require.NoError(t, db.Create(inst).Error)

	runState := model.BotLoadRunPending
	verdict := model.BotLoadVerdictPending
	stage := 0
	maxStable := 0
	sess := &model.BotStressSession{
		InstanceID: inst.ID, Name: "run", NamePrefix: "b", BotCount: 10,
		SchemaVersion: 2, Status: model.BotStressSessionPending,
		LoadProfile: `{"type":"stable"}`, Thresholds: `{}`,
		RunState: &runState, CurrentStage: &stage, Verdict: &verdict, MaxStableBots: &maxStable,
		FailureSummary: `{}`,
	}
	require.NoError(t, db.Create(sess).Error)
	require.True(t, db.Migrator().HasColumn(&model.BotStressSession{}, "run_state"))
	require.True(t, db.Migrator().HasColumn(&model.BotStressSession{}, "template_id"))

	ts := time.Now().UTC().Truncate(time.Millisecond)
	sample := &model.BotLoadMetricSample{
		StressSessionID: sess.ID, SampledAt: ts, StageIndex: 0,
		CountsJSON: `{}`, CommandJSON: `{}`, BarrierJSON: `{}`,
		ExecutorJSON: `[]`, LatencyJSON: `{}`, ErrorsJSON: `{}`,
	}
	require.NoError(t, db.Create(sample).Error)
	// 同 timestamp 冲突
	dup := *sample
	dup.ID = 0
	require.Error(t, db.Create(&dup).Error)

	ev := &model.BotLoadRunEvent{
		StressSessionID: sess.ID, RunUUID: sess.UUID,
		Type: model.BotLoadRunEventRunState, OccurredAt: ts, PayloadJSON: `{"runState":"pending"}`,
	}
	require.NoError(t, db.Create(ev).Error)
	require.NotZero(t, ev.ID)
}
