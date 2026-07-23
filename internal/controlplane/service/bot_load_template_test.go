package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func openBotLoadTemplateDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.Node{},
		&model.Instance{},
		&model.BotLoadTemplate{},
		&model.BotStressSession{},
	))
	return db
}

func seedBotLoadTemplateFixtures(t *testing.T, db *gorm.DB) (user *model.User, inst *model.Instance) {
	t.Helper()
	user = &model.User{Username: "tpl-user", Password: "x", Role: model.RolePlatformAdmin}
	require.NoError(t, db.Create(user).Error)
	node := &model.Node{Name: "n", Host: "127.0.0.1", Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	inst = &model.Instance{
		NodeID: node.ID, Name: "i", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "w", StartCommand: "java",
	}
	require.NoError(t, db.Create(inst).Error)
	return user, inst
}

func validTemplateInput(name string) BotLoadTemplateInput {
	return BotLoadTemplateInput{
		Name:            name,
		Description:     "desc",
		CommandSchedule: json.RawMessage(`{"commands":[{"id":"c1","atMs":0,"command":"/say hi"}],"durationMs":1000,"jitterMs":0}`),
		LoadProfile:     json.RawMessage(`{"type":"stable","targetBots":10,"rampUpSeconds":5,"durationSeconds":60}`),
		Thresholds:      json.RawMessage(`{"minOnlineRate":0.99,"minCommandSentRate":0.99,"minScheduleCompletionRate":0.99,"minWorkerHealthRate":0.99,"minBarrierArrivalRate":0.99,"maxScheduleLagP95Ms":1000,"maxProcessCrashes":0}`),
		Tags:            []string{"smoke"},
	}
}

func TestBotLoadTemplateService_CRUDAndNameConflict(t *testing.T) {
	db := openBotLoadTemplateDB(t)
	user, _ := seedBotLoadTemplateFixtures(t, db)
	svc := NewBotLoadTemplateService(db)

	created, err := svc.Create(user.ID, validTemplateInput("my-tpl"))
	require.NoError(t, err)
	require.Equal(t, "my-tpl", created.Name)
	require.Equal(t, user.ID, created.CreatedBy)
	require.Equal(t, []string{"smoke"}, created.Tags)
	require.Equal(t, "stable", created.LoadProfile.Type)

	// 同名冲突
	_, err = svc.Create(user.ID, validTemplateInput("my-tpl"))
	require.ErrorIs(t, err, ErrBotLoadTemplateNameConflict)

	// 获取
	got, err := svc.Get(created.ID, user.ID, true)
	require.NoError(t, err)
	require.Equal(t, created.UUID, got.UUID)

	// 更新
	upd := validTemplateInput("my-tpl-v2")
	upd.Description = "updated"
	updated, err := svc.Update(created.ID, user.ID, true, upd)
	require.NoError(t, err)
	require.Equal(t, "my-tpl-v2", updated.Name)
	require.Equal(t, "updated", updated.Description)

	// 列表
	list, err := svc.List(user.ID, true, BotLoadTemplateListQuery{Page: 1, PageSize: 10, Tag: "smoke"})
	require.NoError(t, err)
	require.Equal(t, int64(1), list.Total)
	require.Len(t, list.Items, 1)

	// 软删后可复用名称
	require.NoError(t, svc.Delete(created.ID, user.ID, true))
	recreated, err := svc.Create(user.ID, validTemplateInput("my-tpl-v2"))
	require.NoError(t, err)
	require.NotEqual(t, created.ID, recreated.ID)

	// 删除后 404
	_, err = svc.Get(created.ID, user.ID, true)
	require.ErrorIs(t, err, ErrBotLoadTemplateNotFound)
}

func TestBotLoadTemplateService_Ownership(t *testing.T) {
	db := openBotLoadTemplateDB(t)
	owner, _ := seedBotLoadTemplateFixtures(t, db)
	other := &model.User{Username: "other", Password: "x", Role: model.RoleMember}
	require.NoError(t, db.Create(other).Error)
	svc := NewBotLoadTemplateService(db)

	created, err := svc.Create(owner.ID, validTemplateInput("owned"))
	require.NoError(t, err)

	// 非管理员不可读他人模板
	_, err = svc.Get(created.ID, other.ID, false)
	require.ErrorIs(t, err, ErrBotLoadTemplateNotFound)

	// 管理员可读
	_, err = svc.Get(created.ID, other.ID, true)
	require.NoError(t, err)

	// 非管理员列表仅自己
	list, err := svc.List(other.ID, false, BotLoadTemplateListQuery{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(0), list.Total)
}

func TestBotLoadTemplateService_CreateRunFromTemplate(t *testing.T) {
	db := openBotLoadTemplateDB(t)
	user, inst := seedBotLoadTemplateFixtures(t, db)
	svc := NewBotLoadTemplateService(db)

	tpl, err := svc.Create(user.ID, validTemplateInput("run-tpl"))
	require.NoError(t, err)

	cfg := json.RawMessage(`{"server":"127.0.0.1","port":25565,"auth":"offline"}`)
	sess, err := svc.CreateRunFromTemplate(tpl.ID, user.ID, true, inst.ID, "run-1", "bot", cfg, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 2, sess.SchemaVersion)
	require.NotNil(t, sess.TemplateID)
	require.Equal(t, tpl.ID, *sess.TemplateID)
	require.Equal(t, 10, sess.BotCount)
	require.NotNil(t, sess.RunState)
	require.Equal(t, model.BotLoadRunPending, *sess.RunState)
	require.NotNil(t, sess.Verdict)
	require.Equal(t, model.BotLoadVerdictPending, *sess.Verdict)
	require.NotEmpty(t, sess.CommandScheduleSnap)
	require.NotEmpty(t, sess.LoadProfile)
	require.NotEmpty(t, sess.Thresholds)
	require.Equal(t, model.BotStressSessionPending, sess.Status)

	// override profile
	profileOverride := json.RawMessage(`{"type":"spike","targetBots":20,"connectWindowSeconds":10,"holdSeconds":30}`)
	sess2, err := svc.CreateRunFromTemplate(tpl.ID, user.ID, true, inst.ID, "run-2", "bot", cfg, nil, profileOverride, nil)
	require.NoError(t, err)
	require.Equal(t, 20, sess2.BotCount)

	// 拒绝凭据
	_, err = svc.CreateRunFromTemplate(tpl.ID, user.ID, true, inst.ID, "run-bad", "bot",
		json.RawMessage(`{"server":"127.0.0.1","port":25565,"auth":"offline","password":"secret"}`), nil, nil, nil)
	require.ErrorIs(t, err, ErrBotStressSessionInvalid)
}

func TestBotLoadTemplateService_RejectsInvalidProfile(t *testing.T) {
	db := openBotLoadTemplateDB(t)
	user, _ := seedBotLoadTemplateFixtures(t, db)
	svc := NewBotLoadTemplateService(db)

	input := validTemplateInput("bad")
	input.LoadProfile = json.RawMessage(`{"type":"stable","targetBots":0,"rampUpSeconds":0,"durationSeconds":10}`)
	_, err := svc.Create(user.ID, input)
	require.ErrorIs(t, err, ErrBotLoadProfileInvalid)
}
