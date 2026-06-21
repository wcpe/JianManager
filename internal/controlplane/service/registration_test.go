package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newRegTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.ServerRegistration{}, &model.Network{}, &model.NetworkMember{}, &model.GroupInstance{}))
	return db
}

// mkRoleInstance 直接落库一个带角色的实例（绕过 InstanceService.Create 的 Worker 注册），供关系测试使用。
func mkRoleInstance(t *testing.T, db *gorm.DB, name string, role model.InstanceRole) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: role,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func TestRegistration_CreateAndConstraints(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)

	proxy := mkRoleInstance(t, db, "velocity", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "Lobby One", model.InstanceRoleBackend)

	// 正常注册：alias 缺省取后端名 slug，priority 缺省追加
	view, err := svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend.ID})
	require.NoError(t, err)
	require.Equal(t, "lobby-one", view.Alias)
	require.Equal(t, 0, view.Priority)
	require.True(t, view.Enabled)
	require.NotNil(t, view.Backend)
	require.Equal(t, backend.ID, view.Backend.ID)

	// 重复注册同一后端 → ErrAlreadyRegistered
	_, err = svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend.ID, Alias: "other"})
	require.ErrorIs(t, err, ErrAlreadyRegistered)

	// alias 冲突
	backend2 := mkRoleInstance(t, db, "lobby-two", model.InstanceRoleBackend)
	_, err = svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend2.ID, Alias: "lobby-one"})
	require.ErrorIs(t, err, ErrAliasConflict)

	// 非法 alias
	_, err = svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend2.ID, Alias: "Bad Alias"})
	require.ErrorIs(t, err, ErrInvalidAlias)

	// 角色校验：把后端当代理 / 把代理当后端
	_, err = svc.Create(backend.ID, CreateRegistrationRequest{BackendID: backend2.ID})
	require.ErrorIs(t, err, ErrNotAProxy)
	_, err = svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: proxy.ID})
	require.ErrorIs(t, err, ErrNotABackend)

	// priority 自动递增到末尾
	view2, err := svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend2.ID, Alias: "lobby-2"})
	require.NoError(t, err)
	require.Equal(t, 1, view2.Priority)
}

func TestRegistration_MtoN(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)
	proxyA := mkRoleInstance(t, db, "velocity-a", model.InstanceRoleProxy)
	proxyB := mkRoleInstance(t, db, "velocity-b", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)

	_, err := svc.Create(proxyA.ID, CreateRegistrationRequest{BackendID: backend.ID})
	require.NoError(t, err)
	// 同一后端可注册进另一个代理（M:N）
	_, err = svc.Create(proxyB.ID, CreateRegistrationRequest{BackendID: backend.ID})
	require.NoError(t, err)

	regs, err := svc.ListByBackend(backend.ID)
	require.NoError(t, err)
	require.Len(t, regs, 2)
}

func TestRegistration_UpdateAndDelete(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)
	proxy := mkRoleInstance(t, db, "velocity", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)
	view, err := svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend.ID})
	require.NoError(t, err)

	newAlias := "hub"
	newPriority := 5
	updated, err := svc.Update(proxy.ID, view.ID, UpdateRegistrationRequest{Alias: &newAlias, Priority: &newPriority})
	require.NoError(t, err)
	require.Equal(t, "hub", updated.Alias)
	require.Equal(t, 5, updated.Priority)

	// 删除后列表为空
	require.NoError(t, svc.Delete(proxy.ID, view.ID))
	regs, err := svc.List(proxy.ID)
	require.NoError(t, err)
	require.Empty(t, regs)

	// 删除不存在 → ErrRegistrationNotFound
	require.ErrorIs(t, svc.Delete(proxy.ID, 9999), ErrRegistrationNotFound)
}

// 删除实例级联清除其作为代理/后端的注册关系。
func TestRegistration_CascadeOnInstanceDelete(t *testing.T) {
	db := newRegTestDB(t)
	regSvc := NewRegistrationService(db)
	instSvc := NewInstanceService(db, nil, nil)
	proxy := mkRoleInstance(t, db, "velocity", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)
	_, err := regSvc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend.ID})
	require.NoError(t, err)

	require.NoError(t, instSvc.Delete(backend.ID))
	var count int64
	db.Model(&model.ServerRegistration{}).Count(&count)
	require.Equal(t, int64(0), count)
}
