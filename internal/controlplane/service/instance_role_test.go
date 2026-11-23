package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 实例角色可变更（纠正建实例时的误选，如 BungeeCord 被建成 backend 导致
// 群组拓扑与代理注册都认不出它）+ 新增 beacon 角色（配套服务实例，非 MC 群组服角色）。

func newInstanceRoleDB(t *testing.T) (*InstanceService, *model.Instance) {
	t.Helper()
	db := newInstanceUpdateSyncDB(t)
	node := &model.Node{UUID: "node-role", Name: "role-node", Host: "127.0.0.1", Status: model.NodeStatusOnline, OS: "linux"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "role-target", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "java -jar server.jar", WorkDir: "var/servers/role-target",
		Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	return NewInstanceService(db, NewGroupService(db), nil), inst
}

// TestInstanceUpdate_ChangesRole 验证角色可被改成代理，并真正落库。
func TestInstanceUpdate_ChangesRole(t *testing.T) {
	svc, inst := newInstanceRoleDB(t)

	proxy := model.InstanceRoleProxy
	updated, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &proxy})
	require.NoError(t, err)
	require.Equal(t, model.InstanceRoleProxy, updated.Role)

	// 复查读回，确认非仅内存改写。
	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, model.InstanceRoleProxy, got.Role)
}

// TestInstanceUpdate_RejectsInvalidRole 验证非法角色被拒绝而非静默回落。
// 与 Create 的 grandfather 语义（非法值回落 universal）刻意不同：Update 是显式改角色，
// 静默回落会让调用方误以为改成功。
func TestInstanceUpdate_RejectsInvalidRole(t *testing.T) {
	svc, inst := newInstanceRoleDB(t)

	invalid := model.InstanceRole("not-a-role")
	_, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &invalid})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidInstanceRole), "应返回 ErrInvalidInstanceRole，实际: %v", err)

	// 原值不变。
	got, err := svc.GetByID(inst.ID)
	require.NoError(t, err)
	require.Equal(t, model.InstanceRoleBackend, got.Role, "非法值不得改动原角色")
}

// TestInstanceUpdate_NilRoleKeepsCurrent 验证不传 role（nil）时保持不变。
func TestInstanceUpdate_NilRoleKeepsCurrent(t *testing.T) {
	svc, inst := newInstanceRoleDB(t)

	name := "renamed-only"
	updated, err := svc.Update(inst.ID, UpdateInstanceFields{Name: &name})
	require.NoError(t, err)
	require.Equal(t, "renamed-only", updated.Name)
	require.Equal(t, model.InstanceRoleBackend, updated.Role, "未传 role 时角色应保持原值")
}

// TestValidInstanceRole_IncludesBeacon 验证 beacon 是合法角色，且枚举校验覆盖全部四类。
func TestValidInstanceRole_IncludesBeacon(t *testing.T) {
	for _, r := range []model.InstanceRole{
		model.InstanceRoleBackend, model.InstanceRoleProxy,
		model.InstanceRoleUniversal, model.InstanceRoleBeacon,
	} {
		require.True(t, model.ValidInstanceRole(r), "%s 应为合法角色", r)
	}
	require.False(t, model.ValidInstanceRole(""), "空串不是合法角色")
	require.False(t, model.ValidInstanceRole("proxy "), "带空格不合法（不做 trim 容忍）")
}

// TestAggregateInstances_IncludesBeaconRoleKey 验证聚合计数预置了 beacon 键。
// 前端筛选 chip 依赖该键存在，缺键会让 chip 显示为空而非 0。
func TestAggregateInstances_IncludesBeaconRoleKey(t *testing.T) {
	svc, _ := newInstanceRoleDB(t)

	agg, err := svc.AggregateInstances(nil, InstanceSearchParams{})
	require.NoError(t, err)
	for _, key := range []string{"backend", "proxy", "universal", "beacon"} {
		_, ok := agg.ByRole[key]
		require.True(t, ok, "ByRole 应预置 %q 键", key)
	}
}
