package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/authz.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Group{}, &model.GroupMember{}, &model.GroupQuota{},
		&model.Instance{}, &model.GroupInstance{}, &model.Bot{}, &model.Backup{},
	))
	// Windows 下 sqlite 文件句柄需显式关闭，否则 t.TempDir 清理失败
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestUserAccess_HasPermission(t *testing.T) {
	tests := []struct {
		name  string
		access *UserAccess
		node  PermissionNode
		want  bool
	}{
		{
			name:  "平台管理员拥有全部权限",
			access: &UserAccess{IsPlatformAdmin: true},
			node:  PermUserManage,
			want:  true,
		},
		{
			name:  "组成员可读实例",
			access: &UserAccess{MemberGroupIDs: map[uint]struct{}{1: {}}, AccessibleGroups: map[uint]struct{}{1: {}}},
			node:  PermInstanceRead,
			want:  true,
		},
		{
			name:  "无组的成员不可读实例",
			access: &UserAccess{},
			node:  PermInstanceRead,
			want:  false,
		},
		{
			name:  "组成员不可管理用户",
			access: &UserAccess{MemberGroupIDs: map[uint]struct{}{1: {}}, AccessibleGroups: map[uint]struct{}{1: {}}},
			node:  PermUserManage,
			want:  false,
		},
		{
			name:  "组管理员可管理组成员",
			access: &UserAccess{AdminGroupIDs: map[uint]struct{}{1: {}}, AccessibleGroups: map[uint]struct{}{1: {}}},
			node:  PermGroupMemberWrite,
			want:  true,
		},
		{
			name:  "普通成员不可管理组成员",
			access: &UserAccess{MemberGroupIDs: map[uint]struct{}{1: {}}, AccessibleGroups: map[uint]struct{}{1: {}}},
			node:  PermGroupMemberWrite,
			want:  false,
		},
		{
			name:  "组管理（创建/删除）仅平台管理员",
			access: &UserAccess{AdminGroupIDs: map[uint]struct{}{1: {}}, AccessibleGroups: map[uint]struct{}{1: {}}},
			node:  PermGroupManage,
			want:  false,
		},
		{
			name:  "组成员可持有业务高危写（FR-121，资源由 CanAccessInstance 收敛）",
			access: &UserAccess{MemberGroupIDs: map[uint]struct{}{1: {}}, AccessibleGroups: map[uint]struct{}{1: {}}},
			node:  PermInstanceBusinessWrite,
			want:  true,
		},
		{
			name:  "无组用户不可持有业务高危写",
			access: &UserAccess{},
			node:  PermInstanceBusinessWrite,
			want:  false,
		},
		{
			name:  "平台管理员拥有业务高危写",
			access: &UserAccess{IsPlatformAdmin: true},
			node:  PermInstanceBusinessWrite,
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.access.HasPermission(tt.node))
		})
	}
}

func TestAuthz_CanAccessInstance(t *testing.T) {
	db := newAuthzTestDB(t)
	svc := NewAuthzService(db)

	// 构造：组 1 有实例 1（已分配）；组 2 有实例 2（未分配）；实例 3 无组
	group1 := &model.Group{Name: "g1", UUID: "u-g1"}
	group2 := &model.Group{Name: "g2", UUID: "u-g2"}
	require.NoError(t, db.Create(group1).Error)
	require.NoError(t, db.Create(group2).Error)

	inst1 := &model.Instance{UUID: "u-i1", NodeID: 1, Name: "i1", Type: "generic", ProcessType: "direct", StartCommand: "x"}
	inst2 := &model.Instance{UUID: "u-i2", NodeID: 1, Name: "i2", Type: "generic", ProcessType: "direct", StartCommand: "x"}
	inst3 := &model.Instance{UUID: "u-i3", NodeID: 1, Name: "i3", Type: "generic", ProcessType: "direct", StartCommand: "x"}
	require.NoError(t, db.Create(inst1).Error)
	require.NoError(t, db.Create(inst2).Error)
	require.NoError(t, db.Create(inst3).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: group1.ID, InstanceID: inst1.ID}).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: group2.ID, InstanceID: inst2.ID}).Error)

	platformAdmin := &UserAccess{IsPlatformAdmin: true}
	memberOfGroup1 := &UserAccess{
		AccessibleGroups: map[uint]struct{}{group1.ID: {}},
		MemberGroupIDs:   map[uint]struct{}{group1.ID: {}},
	}
	otherMember := &UserAccess{
		AccessibleGroups: map[uint]struct{}{group2.ID: {}},
		MemberGroupIDs:   map[uint]struct{}{group2.ID: {}},
	}

	tests := []struct {
		name      string
		access    *UserAccess
		instance  uint
		want      bool
	}{
		{"平台管理员访问任意实例", platformAdmin, inst1.ID, true},
		{"组1成员访问组1实例", memberOfGroup1, inst1.ID, true},
		{"组1成员访问组2实例被拒", memberOfGroup1, inst2.ID, false},
		{"组2成员访问组1实例被拒", otherMember, inst1.ID, false},
		{"无组实例仅平台管理员可访问", memberOfGroup1, inst3.ID, false},
		{"平台管理员访问无组实例", platformAdmin, inst3.ID, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := svc.CanAccessInstance(tt.access, tt.instance)
			require.NoError(t, err)
			assert.Equal(t, tt.want, ok)
		})
	}
}

func TestAuthz_GetQuotaUsage(t *testing.T) {
	db := newAuthzTestDB(t)
	svc := NewAuthzService(db)

	group := &model.Group{Name: "g", UUID: "u-g"}
	require.NoError(t, db.Create(group).Error)
	require.NoError(t, db.Create(&model.GroupQuota{GroupID: group.ID, MaxInstances: 5, MaxBots: 3, MaxStorageMB: 100}).Error)

	inst := &model.Instance{UUID: "u-i", NodeID: 1, Name: "i", Type: "generic", ProcessType: "direct", StartCommand: "x"}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: group.ID, InstanceID: inst.ID}).Error)
	require.NoError(t, db.Create(&model.Bot{InstanceID: inst.ID, Name: "b1", Status: model.BotStatusPending}).Error)
	require.NoError(t, db.Create(&model.Backup{UUID: "bk1", InstanceID: inst.ID, Name: "bk", FileSizeMB: 40, Status: model.BackupStatusCompleted}).Error)

	usage, err := svc.GetQuotaUsage(group.ID)
	require.NoError(t, err)
	assert.Equal(t, 5, usage.MaxInstances)
	assert.Equal(t, 1, usage.UsedInstances)
	assert.Equal(t, 1, usage.UsedBots)
	assert.Equal(t, 40, usage.UsedStorageMB)

	_, err = svc.GetQuotaUsage(9999)
	assert.ErrorIs(t, err, ErrGroupNotFound)
}
