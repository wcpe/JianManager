package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func newNetTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}, &model.ServerRegistration{}, &model.Network{}, &model.NetworkMember{}, &model.GroupInstance{}))
	return db
}

func TestNetwork_CRUDAndMembers(t *testing.T) {
	db := newNetTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	svc := NewNetworkService(db, instSvc)

	n, err := svc.Create("survival", "生存大区")
	require.NoError(t, err)
	require.NotEmpty(t, n.UUID)

	// 重名冲突
	_, err = svc.Create("survival", "")
	require.ErrorIs(t, err, ErrNetworkNameConflict)

	// 添加成员：去重 + 跳过不存在
	b1 := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)
	b2 := mkRoleInstance(t, db, "smp", model.InstanceRoleBackend)
	added, detail, err := svc.AddMembers(n.ID, []uint{b1.ID, b2.ID, b1.ID, 9999})
	require.NoError(t, err)
	require.Equal(t, 2, added)
	require.Len(t, detail.Members, 2)

	// 再加已存在 → added 0（幂等）
	added, _, err = svc.AddMembers(n.ID, []uint{b1.ID})
	require.NoError(t, err)
	require.Equal(t, 0, added)

	// 列表含成员数
	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 2, list[0].MemberCount)

	// 移除成员
	require.NoError(t, svc.RemoveMember(n.ID, b1.ID))
	detail, err = svc.Get(n.ID)
	require.NoError(t, err)
	require.Len(t, detail.Members, 1)

	// 一个实例可属于多个群组（非独占软标签）
	n2, err := svc.Create("creative", "")
	require.NoError(t, err)
	_, _, err = svc.AddMembers(n2.ID, []uint{b2.ID})
	require.NoError(t, err)
	var memberCount int64
	db.Model(&model.NetworkMember{}).Where("instance_id = ?", b2.ID).Count(&memberCount)
	require.Equal(t, int64(2), memberCount)
}

// 删除群组：成员关系清除，但成员实例与其 server_registrations 不受影响（ADR-007）。
func TestNetwork_DeleteDoesNotAffectRegistrations(t *testing.T) {
	db := newNetTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	netSvc := NewNetworkService(db, instSvc)
	regSvc := NewRegistrationService(db)

	proxy := mkRoleInstance(t, db, "velocity", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)
	_, err := regSvc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend.ID})
	require.NoError(t, err)

	n, err := netSvc.Create("grp", "")
	require.NoError(t, err)
	_, _, err = netSvc.AddMembers(n.ID, []uint{backend.ID})
	require.NoError(t, err)

	require.NoError(t, netSvc.Delete(n.ID))

	var regCount int64
	db.Model(&model.ServerRegistration{}).Where("proxy_id = ?", proxy.ID).Count(&regCount)
	require.Equal(t, int64(1), regCount)
	var instCount int64
	db.Model(&model.Instance{}).Where("id = ?", backend.ID).Count(&instCount)
	require.Equal(t, int64(1), instCount)
	// 群组成员关系已清除
	var memberCount int64
	db.Model(&model.NetworkMember{}).Where("network_id = ?", n.ID).Count(&memberCount)
	require.Equal(t, int64(0), memberCount)
	// 群组本身已软删
	_, err = netSvc.Get(n.ID)
	require.ErrorIs(t, err, ErrNetworkNotFound)
}

func TestNetwork_BatchAction(t *testing.T) {
	db := newNetTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	svc := NewNetworkService(db, instSvc)

	// 非法动作 / 群组不存在
	n, err := svc.Create("grp", "")
	require.NoError(t, err)
	_, err = svc.BatchAction(n.ID, "explode")
	require.ErrorIs(t, err, ErrInvalidBatchAction)
	_, err = svc.BatchAction(9999, "start")
	require.ErrorIs(t, err, ErrNetworkNotFound)

	// 计数：两个 RUNNING 成员对 start 因状态非法同步失败（不触发委托 goroutine）
	r1 := &model.Instance{Name: "a", NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusRunning}
	r2 := &model.Instance{Name: "b", NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(r1).Error)
	require.NoError(t, db.Create(r2).Error)
	_, _, err = svc.AddMembers(n.ID, []uint{r1.ID, r2.ID})
	require.NoError(t, err)

	res, err := svc.BatchAction(n.ID, "start")
	require.NoError(t, err)
	require.Equal(t, 2, res.Total)
	require.Equal(t, 0, res.Succeeded)
	require.Equal(t, 2, res.Failed)
	require.Len(t, res.Results, 2)
}
