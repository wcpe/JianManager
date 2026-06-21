package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newPlayerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Instance{}, &model.Network{}, &model.NetworkMember{}, &model.BanRecord{},
	))
	return db
}

// mkBackend 落库一个指定状态的后端实例，供玩家管理测试控制可达性。
func mkBackend(t *testing.T, db *gorm.DB, name string, status model.InstanceStatus) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: status,
		RCONPort: 25575, RCONPassword: "pw",
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func TestParsePlayerList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty online", "There are 0 of a max of 20 players online:", []string{}},
		{"two players", "There are 2 of a max of 20 players online: alice, bob", []string{"alice", "bob"}},
		{"trailing spaces", "There are 1 of a max of 20 players online:  carol ", []string{"carol"}},
		{"color codes", "§eThere are §a1§e players online: §bdave", []string{"dave"}},
		{"uuid suffix stripped", "players online: eve (1234-5678), frank", []string{"eve", "frank"}},
		{"no colon", "garbage output", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePlayerList(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseWhitelist(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "There are no whitelisted players", []string{}},
		{"two", "There are 2 whitelisted players: alice, bob", []string{"alice", "bob"}},
		{"colors", "§a3 whitelisted: x, y, z", []string{"x", "y", "z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWhitelist(tt.in)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCleanColors(t *testing.T) {
	require.Equal(t, "hello", cleanColors("§ahe§bllo"))
	require.Equal(t, "plain", cleanColors("plain"))
	require.Equal(t, "", cleanColors("§a§b"))
}

func TestPlayerService_Ban_RecordScopes(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)

	op := &model.User{Username: "admin", Role: model.RolePlatformAdmin}
	require.NoError(t, db.Create(op).Error)

	// global 范围：无在线后端时 fanout 为空，但记录仍应入库（平台台账）。
	res, err := svc.Ban("griefer", PlayerActionScope{Reason: "破坏"}, op.ID, nil, false)
	require.NoError(t, err)
	require.Equal(t, "ban", res.Action)
	require.Equal(t, 0, res.Total) // 无运行中的后端

	var recs []model.BanRecord
	require.NoError(t, db.Find(&recs).Error)
	require.Len(t, recs, 1)
	require.Equal(t, "griefer", recs[0].PlayerName)
	require.Equal(t, "破坏", recs[0].Reason)
	require.Equal(t, model.BanScopeGlobal, recs[0].Scope)
	require.Equal(t, op.ID, recs[0].OperatorID)
	require.True(t, recs[0].Active)
	require.NotEmpty(t, recs[0].UUID)

	// instance 范围：记录 scope=instance + scopeID。
	b := mkBackend(t, db, "lobby", model.InstanceStatusStopped)
	_, err = svc.Ban("cheater", PlayerActionScope{InstanceID: b.ID}, op.ID, nil, false)
	require.NoError(t, err)
	var instRec model.BanRecord
	require.NoError(t, db.Where("player_name = ?", "cheater").First(&instRec).Error)
	require.Equal(t, model.BanScopeInstance, instRec.Scope)
	require.Equal(t, b.ID, instRec.ScopeID)

	// network 范围：记录 scope=network + scopeID。
	net := &model.Network{Name: "survival"}
	require.NoError(t, db.Create(net).Error)
	_, err = svc.Ban("spammer", PlayerActionScope{NetworkID: net.ID}, op.ID, nil, false)
	require.NoError(t, err)
	var netRec model.BanRecord
	require.NoError(t, db.Where("player_name = ?", "spammer").First(&netRec).Error)
	require.Equal(t, model.BanScopeNetwork, netRec.Scope)
	require.Equal(t, net.ID, netRec.ScopeID)
}

func TestPlayerService_Unban_DeactivatesRecords(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)
	op := &model.User{Username: "admin", Role: model.RolePlatformAdmin}
	require.NoError(t, db.Create(op).Error)

	_, err := svc.Ban("target", PlayerActionScope{Reason: "r"}, op.ID, nil, false)
	require.NoError(t, err)

	_, err = svc.Unban("target", PlayerActionScope{}, nil, false)
	require.NoError(t, err)

	var rec model.BanRecord
	require.NoError(t, db.Where("player_name = ?", "target").First(&rec).Error)
	require.False(t, rec.Active)
	require.NotNil(t, rec.UnbannedAt)
}

func TestPlayerService_ListBans_Filter(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)
	op := &model.User{Username: "admin", Role: model.RolePlatformAdmin}
	require.NoError(t, db.Create(op).Error)

	_, _ = svc.Ban("alice", PlayerActionScope{}, op.ID, nil, false)
	_, _ = svc.Ban("bob", PlayerActionScope{}, op.ID, nil, false)
	// 解封 bob → Active=false
	_, _ = svc.Unban("bob", PlayerActionScope{}, nil, false)

	all, err := svc.ListBans(BanFilter{})
	require.NoError(t, err)
	require.Len(t, all, 2)
	// Preload Operator 应带出用户名。
	require.Equal(t, "admin", all[0].Operator.Username)

	activeOnly, err := svc.ListBans(BanFilter{ActiveOnly: true})
	require.NoError(t, err)
	require.Len(t, activeOnly, 1)
	require.Equal(t, "alice", activeOnly[0].PlayerName)

	name := "ali"
	byName, err := svc.ListBans(BanFilter{PlayerName: &name})
	require.NoError(t, err)
	require.Len(t, byName, 1)
	require.Equal(t, "alice", byName[0].PlayerName)
}

func TestPlayerService_ReachableBackends_OnlyRunningAndScoped(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)

	running := mkBackend(t, db, "run", model.InstanceStatusRunning)
	mkBackend(t, db, "stopped", model.InstanceStatusStopped)
	// proxy 不应计入后端集合。
	proxy := &model.Instance{Name: "velocity", NodeID: 1, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon, StartCommand: "x",
		Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(proxy).Error)

	// 不收敛（平台管理员）：只返回运行中的后端。
	all, err := svc.reachableBackends(nil, false)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, running.ID, all[0].ID)

	// 收敛但可见集合为空：返回空。
	none, err := svc.reachableBackends([]uint{}, true)
	require.NoError(t, err)
	require.Len(t, none, 0)

	// 收敛到不含 running：返回空（权限交集）。
	other, err := svc.reachableBackends([]uint{9999}, true)
	require.NoError(t, err)
	require.Len(t, other, 0)

	// 收敛包含 running：返回它。
	scoped, err := svc.reachableBackends([]uint{running.ID}, true)
	require.NoError(t, err)
	require.Len(t, scoped, 1)
}

func TestPlayerService_NetworkBackends(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)

	net := &model.Network{Name: "survival"}
	require.NoError(t, db.Create(net).Error)
	b1 := mkBackend(t, db, "b1", model.InstanceStatusRunning)
	b2 := mkBackend(t, db, "b2", model.InstanceStatusStopped) // 非运行，排除
	outside := mkBackend(t, db, "outside", model.InstanceStatusRunning)
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: net.ID, InstanceID: b1.ID}).Error)
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: net.ID, InstanceID: b2.ID}).Error)

	got, err := svc.networkBackends(net.ID, nil, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, b1.ID, got[0].ID)

	// outside 不在群组，确保不被误纳入。
	for _, g := range got {
		require.NotEqual(t, outside.ID, g.ID)
	}
}

func TestPlayerService_ResolveTargets_ScopePrecedence(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)
	b := mkBackend(t, db, "b", model.InstanceStatusRunning)

	// 指定实例但不在可见集合 → ErrNoReachableBackend。
	_, err := svc.resolveTargets(PlayerActionScope{InstanceID: b.ID}, []uint{9999}, true)
	require.ErrorIs(t, err, ErrNoReachableBackend)

	// 指定实例且可见 → 命中该实例。
	got, err := svc.resolveTargets(PlayerActionScope{InstanceID: b.ID}, []uint{b.ID}, true)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, b.ID, got[0].ID)

	// 指定不存在的实例 → ErrInstanceNotFound。
	_, err = svc.resolveTargets(PlayerActionScope{InstanceID: 4242}, nil, false)
	require.ErrorIs(t, err, ErrInstanceNotFound)
}

func TestPlayerService_Action_EmptyPlayerRejected(t *testing.T) {
	db := newPlayerTestDB(t)
	svc := NewPlayerService(db, nil)
	_, err := svc.Kick("  ", PlayerActionScope{}, nil, false)
	require.Error(t, err)
	_, err = svc.Ban("", PlayerActionScope{}, 1, nil, false)
	require.Error(t, err)
}
