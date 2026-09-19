package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newSuperAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.RoleTemplate{}, &model.RolePermission{},
		&model.UserRoleBinding{}, &model.UserPermissionOverride{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestSuperAdmin_Unique_CreateSecond(t *testing.T) {
	db := newSuperAdminTestDB(t)
	svc := NewUserService(db)
	svc.SetPasswordCostForTest(4)

	if _, err := svc.Create("admin1", "Password@123", model.RolePlatformAdmin, model.UserStatusActive); err != nil {
		t.Fatalf("first admin: %v", err)
	}
	_, err := svc.Create("admin2", "Password@123", model.RolePlatformAdmin, model.UserStatusActive)
	if !errors.Is(err, ErrSuperAdminUnique) {
		t.Fatalf("want ErrSuperAdminUnique, got %v", err)
	}

	// 普通角色仍可创建
	if _, err := svc.Create("member1", "Password@123", model.RoleMember, model.UserStatusActive); err != nil {
		t.Fatalf("member create: %v", err)
	}
	// 升为超管 → 拒绝
	member, _ := svc.GetByID(mustUserID(t, db, "member1"))
	r := model.RolePlatformAdmin
	_, err = svc.Update(member.ID, &r, nil, nil)
	if !errors.Is(err, ErrSuperAdminUnique) {
		t.Fatalf("promote to admin want unique err, got %v", err)
	}
}

func TestSuperAdmin_CannotDemoteOrCheat(t *testing.T) {
	db := newSuperAdminTestDB(t)
	svc := NewUserService(db)
	svc.SetPasswordCostForTest(4)
	admin, err := svc.Create("admin", "Password@123", model.RolePlatformAdmin, model.UserStatusActive)
	if err != nil {
		t.Fatal(err)
	}
	// 唯一超管不可降级
	viewer := model.RoleGroupViewer
	if _, err := svc.Update(admin.ID, &viewer, nil, nil); !errors.Is(err, ErrSuperAdminLocked) && !errors.Is(err, ErrSuperAdminUnique) {
		t.Fatalf("demote unique admin should fail, got %v", err)
	}

	perm := NewPermissionService(db)
	if err := perm.SeedSystemRoles(); err != nil {
		t.Fatal(err)
	}
	// 清空 platform_admin 模板 → 读侧仍全量
	var pa model.RoleTemplate
	if err := db.Where("key = ?", model.RoleKeyPlatformAdmin).First(&pa).Error; err != nil {
		t.Fatal(err)
	}
	if err := perm.replaceRoleNodes(pa.ID, nil); err != nil {
		t.Fatal(err)
	}
	nodes, _ := perm.RoleNodes(pa.ID)
	if len(nodes) != len(AllPermissionNodes()) {
		t.Fatalf("platform_admin RoleNodes should be full after wipe, got %d", len(nodes))
	}
	// Effective 短路
	eff, key, err := perm.EffectiveForUser(admin.ID, model.RolePlatformAdmin)
	if err != nil || key != model.RoleKeyPlatformAdmin || len(eff) != len(AllPermissionNodes()) {
		t.Fatalf("effective admin key=%s n=%d err=%v", key, len(eff), err)
	}
	// 覆盖 / 绑定拒绝
	if err := perm.SetUserOverrides(admin.ID, []model.UserPermissionOverride{
		{Node: "user.manage", Effect: model.OverrideDeny},
	}); !errors.Is(err, ErrSuperAdminLocked) {
		t.Fatalf("override deny should fail: %v", err)
	}
	gv := model.RoleTemplate{Key: "custom_x", Name: "x", IsSystem: false}
	if err := db.Create(&gv).Error; err != nil {
		t.Fatal(err)
	}
	if err := perm.BindUserRole(admin.ID, gv.ID); !errors.Is(err, ErrSuperAdminLocked) {
		t.Fatalf("bind non-admin should fail: %v", err)
	}
	if err := perm.DeleteRole(pa.ID); err == nil {
		t.Fatal("delete platform_admin should fail")
	}
}

func mustUserID(t *testing.T, db *gorm.DB, username string) uint {
	t.Helper()
	var u model.User
	if err := db.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatal(err)
	}
	return u.ID
}
