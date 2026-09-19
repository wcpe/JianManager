package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newPermTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.RoleTemplate{}, &model.RolePermission{},
		&model.UserRoleBinding{}, &model.UserPermissionOverride{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestPermissionService_EffectiveForUser_ViewerDeny(t *testing.T) {
	db := newPermTestDB(t)
	perm := NewPermissionService(db)
	if err := perm.SeedSystemRoles(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	user := model.User{Username: "v1", Password: "x", Role: model.RoleGroupViewer, Status: model.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	nodes, roleKey, err := perm.EffectiveForUser(user.ID, user.Role)
	if err != nil {
		t.Fatal(err)
	}
	if roleKey != model.RoleKeyGroupViewer {
		t.Fatalf("roleKey=%s", roleKey)
	}
	if _, ok := nodes["file.write"]; ok {
		t.Fatal("viewer should not have file.write")
	}
	if _, ok := nodes["log.read"]; !ok {
		t.Fatal("viewer should have log.read")
	}

	// 覆盖 allow 额外节点，deny 永胜
	if err := perm.SetUserOverrides(user.ID, []model.UserPermissionOverride{
		{Node: "stats.read", Effect: model.OverrideAllow},
		{Node: "log.read", Effect: model.OverrideDeny},
	}); err != nil {
		t.Fatal(err)
	}
	nodes, _, err = perm.EffectiveForUser(user.ID, user.Role)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := nodes["log.read"]; ok {
		t.Fatal("deny must win")
	}
	if _, ok := nodes["stats.read"]; !ok {
		t.Fatal("allow should add stats.read")
	}
}

func TestPermissionService_PlatformAdminIgnoresDeny(t *testing.T) {
	db := newPermTestDB(t)
	perm := NewPermissionService(db)
	_ = perm.SeedSystemRoles()
	user := model.User{Username: "admin", Password: "x", Role: model.RolePlatformAdmin, Status: model.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	_ = perm.SetUserOverrides(user.ID, []model.UserPermissionOverride{
		{Node: "user.manage", Effect: model.OverrideDeny},
	})
	nodes, key, err := perm.EffectiveForUser(user.ID, user.Role)
	if err != nil {
		t.Fatal(err)
	}
	if key != model.RoleKeyPlatformAdmin {
		t.Fatalf("key=%s", key)
	}
	if _, ok := nodes["user.manage"]; !ok {
		t.Fatal("platform admin cannot be denied")
	}
}

func TestPermissionService_CreateAndBindCustomRole(t *testing.T) {
	db := newPermTestDB(t)
	perm := NewPermissionService(db)
	_ = perm.SeedSystemRoles()
	role, err := perm.CreateRole("只读+日志", "custom", []string{"instance.read", "log.read"})
	if err != nil {
		t.Fatal(err)
	}
	if role.IsSystem {
		t.Fatal("custom role must not be system")
	}
	user := model.User{Username: "u", Password: "x", Role: model.RoleMember, Status: model.UserStatusActive}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := perm.BindUserRole(user.ID, role.ID); err != nil {
		t.Fatal(err)
	}
	nodes, key, err := perm.EffectiveForUser(user.ID, user.Role)
	if err != nil {
		t.Fatal(err)
	}
	if key != role.Key {
		t.Fatalf("key=%s want %s", key, role.Key)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes=%v", nodes)
	}
	// 系统角色不可删
	var admin model.RoleTemplate
	if err := db.Where("key = ?", model.RoleKeyPlatformAdmin).First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := perm.DeleteRole(admin.ID); err == nil {
		t.Fatal("system role delete must fail")
	}
}
