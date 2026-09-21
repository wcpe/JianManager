package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestUpdate_RoleChangeNormalizesTypeAndProbePort FR-454（第二轮复审 N1）：
// 改 role 必须重新归一 type 并同步 probe_port，且**与创建路径共用同一口径**——
// 否则「改 role」会把探索路径刚消灭的脏值（beacon 的 type=minecraft_java）重新制造出来；
// beacon→backend 又因无 Type 字段而永久卡死（新增 UpdateInstanceFields.Type 作为修回入口）。
func TestUpdate_RoleChangeNormalizesTypeAndProbePort(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewInstanceService(db, nil, cpgrpc.NewClientPool())
	t.Cleanup(svc.Shutdown)

	beacon := model.InstanceRoleBeacon
	backend := model.InstanceRoleBackend
	proxy := model.InstanceRoleProxy
	mcJava := model.InstanceTypeMinecraftJava
	generic := model.InstanceTypeGeneric

	seed := func(typ model.InstanceType, role model.InstanceRole, probe int) *model.Instance {
		inst := &model.Instance{
			NodeID: 1, Name: "i-" + string(typ) + "-" + string(role), Type: typ, Role: role,
			ProcessType: model.ProcessTypeDaemon, StartCommand: "x",
			ProbePort: probe, Status: model.InstanceStatusStopped,
		}
		require.NoError(t, db.Create(inst).Error)
		return inst
	}
	reload := func(id uint) model.Instance {
		var got model.Instance
		require.NoError(t, db.First(&got, id).Error)
		return got
	}

	t.Run("role→beacon 归一 type 为 generic 并归零 probe_port", func(t *testing.T) {
		inst := seed(mcJava, backend, 29940)
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &beacon})
		require.NoError(t, err)
		require.Equal(t, model.InstanceTypeGeneric, got.Type)
		require.Equal(t, beacon, got.Role)
		require.Zero(t, got.ProbePort)
		// 内存收敛还不够：库里也必须一致（防「返回收敛了、库里仍是脏值」）。
		loaded := reload(inst.ID)
		require.Equal(t, model.InstanceTypeGeneric, loaded.Type)
		require.Zero(t, loaded.ProbePort)
	})

	t.Run("beacon 是硬不变量：显式传 type=minecraft_java 也被归一", func(t *testing.T) {
		inst := seed(mcJava, backend, 29941)
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &beacon, Type: &mcJava})
		require.NoError(t, err)
		require.Equal(t, model.InstanceTypeGeneric, got.Type)
		require.Zero(t, got.ProbePort)
		require.False(t, model.IsProbeApplicable(got.Type, got.Role))
	})

	t.Run("beacon→backend 显式传 type 可恢复探针适用（修回入口）", func(t *testing.T) {
		inst := seed(generic, beacon, 0)
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &backend, Type: &mcJava})
		require.NoError(t, err)
		require.Equal(t, model.InstanceTypeMinecraftJava, got.Type)
		require.Equal(t, backend, got.Role)
		require.True(t, model.IsProbeApplicable(got.Type, got.Role))
	})

	t.Run("beacon→backend 不传 type 保持 generic（改 role 不替调用方猜 type）", func(t *testing.T) {
		inst := seed(generic, beacon, 0)
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &backend})
		require.NoError(t, err)
		require.Equal(t, model.InstanceTypeGeneric, got.Type, "未显式传 type 时不得反向强推 minecraft_java")
		require.False(t, model.IsProbeApplicable(got.Type, got.Role))
	})

	t.Run("改 role→proxy 归零 probe_port、type 不变", func(t *testing.T) {
		inst := seed(mcJava, backend, 29942)
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Role: &proxy})
		require.NoError(t, err)
		require.Equal(t, model.InstanceTypeMinecraftJava, got.Type)
		require.Zero(t, got.ProbePort)
	})

	t.Run("显式改 type=generic 归零 probe_port", func(t *testing.T) {
		inst := seed(mcJava, backend, 29943)
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Type: &generic})
		require.NoError(t, err)
		require.Equal(t, model.InstanceTypeGeneric, got.Type)
		require.Zero(t, got.ProbePort)
	})

	t.Run("非法 type 直接拒绝且不落库", func(t *testing.T) {
		inst := seed(mcJava, backend, 29944)
		bad := model.InstanceType("weird_type")
		_, err := svc.Update(inst.ID, UpdateInstanceFields{Type: &bad})
		require.ErrorIs(t, err, ErrInvalidInstanceType)
		loaded := reload(inst.ID)
		require.Equal(t, model.InstanceTypeMinecraftJava, loaded.Type)
		require.Equal(t, 29944, loaded.ProbePort)
	})

	t.Run("不改 role/type 的普通更新不触碰 type/probe_port", func(t *testing.T) {
		inst := seed(mcJava, backend, 29945)
		name := "renamed-by-fr454"
		got, err := svc.Update(inst.ID, UpdateInstanceFields{Name: &name})
		require.NoError(t, err)
		require.Equal(t, "renamed-by-fr454", got.Name)
		require.Equal(t, model.InstanceTypeMinecraftJava, got.Type)
		require.Equal(t, 29945, got.ProbePort)
	})
}
