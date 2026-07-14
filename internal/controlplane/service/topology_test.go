package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// mkStatusInstance 落库一个指定角色与状态的实例（供拓扑/健康计数测试）。
func mkStatusInstance(t *testing.T, db *gorm.DB, name string, role model.InstanceRole, status model.InstanceStatus) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: 1, Type: model.InstanceTypeMinecraftJava, Role: role,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "x", Status: status, ServerPort: 25565,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestTopology_AggregatesAllProxiesWithRegistrations 验证 Topology() 一次返全量 proxy 及其注册，
// registrations 与对其单发 List(proxyID) 逐字段同构。
func TestTopology_AggregatesAllProxiesWithRegistrations(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)

	proxyA := mkRoleInstance(t, db, "velocity-a", model.InstanceRoleProxy)
	proxyB := mkRoleInstance(t, db, "velocity-b", model.InstanceRoleProxy)
	lobby := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)
	world := mkRoleInstance(t, db, "world", model.InstanceRoleBackend)

	// proxyA 注册 lobby + world；proxyB 注册 lobby（M:N 共享后端）。
	_, err := svc.Create(proxyA.ID, CreateRegistrationRequest{BackendID: lobby.ID, Alias: "lobby"})
	require.NoError(t, err)
	_, err = svc.Create(proxyA.ID, CreateRegistrationRequest{BackendID: world.ID, Alias: "world"})
	require.NoError(t, err)
	_, err = svc.Create(proxyB.ID, CreateRegistrationRequest{BackendID: lobby.ID, Alias: "shared-lobby"})
	require.NoError(t, err)

	proxies, existing, err := svc.Topology()
	require.NoError(t, err)
	require.Len(t, proxies, 2)

	// 保序 id asc：proxyA 先于 proxyB。
	require.Equal(t, proxyA.ID, proxies[0].ID)
	require.Equal(t, proxyB.ID, proxies[1].ID)
	require.Len(t, proxies[0].Registrations, 2)
	require.Len(t, proxies[1].Registrations, 1)

	// existing 集合含全部 proxy 与被注册后端。
	require.True(t, existing[proxyA.ID])
	require.True(t, existing[proxyB.ID])
	require.True(t, existing[lobby.ID])
	require.True(t, existing[world.ID])

	// 同构性：拓扑里 proxyA 的 registrations 与单发 List(proxyA) 逐字段一致。
	single, err := svc.List(proxyA.ID)
	require.NoError(t, err)
	require.Equal(t, single, proxies[0].Registrations)

	// 后端概要正确内联。
	require.NotNil(t, proxies[0].Registrations[0].Backend)
	require.Equal(t, lobby.ID, proxies[0].Registrations[0].Backend.ID)
	require.Equal(t, model.InstanceRoleBackend, proxies[0].Registrations[0].Backend.Role)
}

// TestTopology_RegistrationsSortedByPriority 验证注册按 priority asc, id asc 排序（与 List 一致）。
func TestTopology_RegistrationsSortedByPriority(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)
	proxy := mkRoleInstance(t, db, "velocity", model.InstanceRoleProxy)
	b1 := mkRoleInstance(t, db, "b1", model.InstanceRoleBackend)
	b2 := mkRoleInstance(t, db, "b2", model.InstanceRoleBackend)
	b3 := mkRoleInstance(t, db, "b3", model.InstanceRoleBackend)

	p20 := 20
	p10 := 10
	p30 := 30
	_, err := svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: b1.ID, Alias: "b1", Priority: &p20})
	require.NoError(t, err)
	_, err = svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: b2.ID, Alias: "b2", Priority: &p10})
	require.NoError(t, err)
	_, err = svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: b3.ID, Alias: "b3", Priority: &p30})
	require.NoError(t, err)

	proxies, _, err := svc.Topology()
	require.NoError(t, err)
	require.Len(t, proxies, 1)
	got := []int{}
	for _, r := range proxies[0].Registrations {
		got = append(got, r.Priority)
	}
	require.Equal(t, []int{10, 20, 30}, got)
}

// TestTopology_BackendMissingTolerated 验证后端实例已删时 backend 为 nil（容错，不阻断拓扑）。
func TestTopology_BackendMissingTolerated(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)
	proxy := mkRoleInstance(t, db, "velocity", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)
	_, err := svc.Create(proxy.ID, CreateRegistrationRequest{BackendID: backend.ID, Alias: "lobby"})
	require.NoError(t, err)

	// 直接删后端实例行（保留注册关系，模拟悬空）。
	require.NoError(t, db.Delete(&model.Instance{}, backend.ID).Error)

	proxies, existing, err := svc.Topology()
	require.NoError(t, err)
	require.Len(t, proxies, 1)
	require.Len(t, proxies[0].Registrations, 1)
	require.Nil(t, proxies[0].Registrations[0].Backend)
	// 已删后端不计入 existing。
	require.False(t, existing[backend.ID])
	require.True(t, existing[proxy.ID])
}

// TestTopology_EmptyProxies 验证无 proxy 时返回空切片（非 nil），existing 空。
func TestTopology_EmptyProxies(t *testing.T) {
	db := newRegTestDB(t)
	svc := NewRegistrationService(db)
	// 仅有后端实例、无 proxy。
	mkRoleInstance(t, db, "lobby", model.InstanceRoleBackend)

	proxies, existing, err := svc.Topology()
	require.NoError(t, err)
	require.NotNil(t, proxies)
	require.Empty(t, proxies)
	require.Empty(t, existing)
}

// TestNetworkList_MemberStatusCounts 验证 List 聚合出正确的五态计数桶、悬空成员剔除、零补齐。
func TestNetworkList_MemberStatusCounts(t *testing.T) {
	db := newNetTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	svc := NewNetworkService(db, instSvc)

	n, err := svc.Create("survival", "")
	require.NoError(t, err)

	running := mkStatusInstance(t, db, "run", model.InstanceRoleBackend, model.InstanceStatusRunning)
	running2 := mkStatusInstance(t, db, "run2", model.InstanceRoleBackend, model.InstanceStatusRunning)
	stopped := mkStatusInstance(t, db, "stop", model.InstanceRoleBackend, model.InstanceStatusStopped)
	crashed := mkStatusInstance(t, db, "crash", model.InstanceRoleBackend, model.InstanceStatusCrashed)
	starting := mkStatusInstance(t, db, "starting", model.InstanceRoleProxy, model.InstanceStatusStarting)

	_, _, err = svc.AddMembers(n.ID, []uint{running.ID, running2.ID, stopped.ID, crashed.ID, starting.ID})
	require.NoError(t, err)

	// 加一条悬空成员关系（实例不存在）——应被 JOIN 剔除，不计入 memberCount/桶。
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: n.ID, InstanceID: 99999}).Error)

	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	got := list[0]
	require.Equal(t, 2, got.MemberStatus.Running)
	require.Equal(t, 1, got.MemberStatus.Stopped)
	require.Equal(t, 1, got.MemberStatus.Crashed)
	require.Equal(t, 1, got.MemberStatus.Starting)
	require.Equal(t, 0, got.MemberStatus.Stopping)
	// memberCount = 五桶之和（悬空成员不计）。
	require.Equal(t, 5, got.MemberCount)
}

// TestNetworkList_EmptyMembersZeroFilled 验证无成员群组的计数桶全 0、memberCount 0。
func TestNetworkList_EmptyMembersZeroFilled(t *testing.T) {
	db := newNetTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	svc := NewNetworkService(db, instSvc)

	_, err := svc.Create("empty", "")
	require.NoError(t, err)

	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 0, list[0].MemberCount)
	require.Equal(t, MemberStatusCounts{}, list[0].MemberStatus)
}

// TestNetwork_TopoBriefs 验证拓扑分组概要：成员归属正确、悬空成员按 existing 剔除。
func TestNetwork_TopoBriefs(t *testing.T) {
	db := newNetTestDB(t)
	instSvc := NewInstanceService(db, nil, nil)
	svc := NewNetworkService(db, instSvc)

	proxy := mkRoleInstance(t, db, "p", model.InstanceRoleProxy)
	backend := mkRoleInstance(t, db, "b", model.InstanceRoleBackend)
	n, err := svc.Create("survival", "")
	require.NoError(t, err)
	_, _, err = svc.AddMembers(n.ID, []uint{proxy.ID, backend.ID})
	require.NoError(t, err)
	// 悬空成员
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: n.ID, InstanceID: 88888}).Error)

	existing := map[uint]bool{proxy.ID: true, backend.ID: true}
	briefs, err := svc.TopoBriefs(existing)
	require.NoError(t, err)
	require.Len(t, briefs, 1)
	require.Equal(t, n.ID, briefs[0].ID)
	require.Equal(t, "survival", briefs[0].Name)
	require.ElementsMatch(t, []uint{proxy.ID, backend.ID}, briefs[0].MemberInstanceIDs)
}
