package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newRbacHardeningDB(t *testing.T) *gorm.DB {
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

// R-10 / R-11 / R-13 / R-09 service 层硬化。
func TestPermissionHardening_TransactionErrorsAndKeys(t *testing.T) {
	db := newRbacHardeningDB(t)
	perm := NewPermissionService(db)
	mustNoErr := func(err error, msg string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
	}
	mustTrue := func(cond bool, msg string) {
		t.Helper()
		if !cond {
			t.Fatal(msg)
		}
	}

	// R-13 连续同名 CreateRole 不冲突
	r1, err := perm.CreateRole("ops", "", []string{"log.read"})
	mustNoErr(err, "create r1")
	r2, err := perm.CreateRole("ops", "", []string{"log.read"})
	mustNoErr(err, "create r2")
	mustTrue(r1.Key != r2.Key, "keys must differ")

	// R-10 非法覆盖：事务内不落库
	u, err := NewUserService(db).Create("ovu", "password123", model.RoleMember, model.UserStatusActive)
	mustNoErr(err, "create user")
	mustNoErr(perm.SeedSystemRoles(), "seed")
	err = perm.SetUserOverrides(u.ID, []model.UserPermissionOverride{
		{Node: "log.read", Effect: model.OverrideAllow},
		{Node: "not.a.node", Effect: model.OverrideDeny},
	})
	mustTrue(errors.Is(err, ErrOverrideInvalid), "invalid override must fail")
	list, _ := perm.UserOverrides(u.ID)
	mustTrue(len(list) == 0, "failed override must not leave partial rows")

	// R-11 系统角色删除错误类型
	var sys model.RoleTemplate
	mustNoErr(db.Where("key = ?", model.RoleKeyMember).First(&sys).Error, "seed member")
	err = perm.DeleteRole(sys.ID)
	mustTrue(errors.Is(err, ErrRoleSystem), "system role delete → ErrRoleSystem")
	var pa model.RoleTemplate
	mustNoErr(db.Where("key = ?", model.RoleKeyPlatformAdmin).First(&pa).Error, "seed pa")
	err = perm.DeleteRole(pa.ID)
	mustTrue(errors.Is(err, ErrRoleSystem), "platform_admin delete → ErrRoleSystem")

	// R-15 RoleNodes 不存在角色
	_, err = perm.RoleNodes(99999)
	mustTrue(errors.Is(err, gorm.ErrRecordNotFound), "missing role → not found")

	// 合法覆盖事务提交
	err = perm.SetUserOverrides(u.ID, []model.UserPermissionOverride{
		{Node: "log.read", Effect: model.OverrideDeny},
	})
	mustNoErr(err, "valid override")
	list, _ = perm.UserOverrides(u.ID)
	mustTrue(len(list) == 1 && list[0].Node == "log.read", "override persisted")
}
