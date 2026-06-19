package service

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func newCloneTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.ServerRegistration{}, &model.GroupInstance{}))
	return db
}

func TestPatchProperties(t *testing.T) {
	content := "# 注释\nserver-port=25565\nmotd=Old\nlevel-name=world\nrcon.port=25575\n"
	out := patchProperties(content, map[string]string{"server-port": "25600", "motd": "New", "rcon.password": "secret"})
	require.Contains(t, out, "server-port=25600")
	require.Contains(t, out, "motd=New")
	require.Contains(t, out, "level-name=world") // 保留未涉及键
	require.Contains(t, out, "# 注释")            // 保留注释
	require.Contains(t, out, "rcon.password=secret") // 缺失键追加
	require.Equal(t, 1, strings.Count(out, "server-port=")) // 不重复
}

func TestClone_DryRunAndGuards(t *testing.T) {
	db := newCloneTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	regSvc := NewRegistrationService(db)
	svc := NewCloneService(db, nil, instSvc, regSvc)

	src := &model.Instance{
		Name: "lobby", NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java", Status: model.InstanceStatusStopped,
		ServerPort: 25565, RCONPort: 25575, QueryPort: 25565,
	}
	require.NoError(t, db.Create(src).Error)

	// dryRun：预览分配，不落盘
	res, err := svc.Clone(context.Background(), src.ID, CloneInstanceRequest{Name: "lobby-2", DryRun: true})
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Nil(t, res.Instance)
	require.NotZero(t, res.Allocated.ServerPort)
	require.NotEqual(t, src.ServerPort, res.Allocated.ServerPort) // 新端口避开占用
	require.Contains(t, res.Excluded, "session.lock")

	// 非 backend → 拒绝
	uni := &model.Instance{Name: "u", NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(uni).Error)
	_, err = svc.Clone(context.Background(), uni.ID, CloneInstanceRequest{Name: "u2", DryRun: true})
	require.ErrorIs(t, err, ErrSourceNotBackend)

	// 运行中 → 拒绝
	running := &model.Instance{Name: "r", NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(running).Error)
	_, err = svc.Clone(context.Background(), running.ID, CloneInstanceRequest{Name: "r2", DryRun: true})
	require.ErrorIs(t, err, ErrSourceRunning)
}
