package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newProbeUpdateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{}, &model.Network{}, &model.NetworkMember{}, &model.GroupInstance{},
	))
	return db
}

func mkProbeInstance(t *testing.T, db *gorm.DB, name string, nodeID uint) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusStopped, ProbePort: 29940,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// TestProbeUpdate_Status_NotFound 实例不存在返回 gorm.ErrRecordNotFound。
func TestProbeUpdate_Status_NotFound(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)

	_, err := svc.Status(999)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestProbeUpdate_Status_ConnAndVersion 验证状态：连接由 checker 决定，
// 未配置制品库时明确报告不可下发，lastPushedAt 推送前为 nil。
func TestProbeUpdate_Status_ConnAndVersion(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)

	// 未注入 checker：一律未连入。
	st, err := svc.Status(inst.ID)
	require.NoError(t, err)
	require.False(t, st.ProbeConnected)
	require.Nil(t, st.LastPushedAt, "未推送过 lastPushedAt 应为 nil")
	require.False(t, st.EmbeddedAvailable)
	require.Equal(t, "制品版本库未配置", st.VersionError)
	require.Equal(t, inst.UUID, st.InstanceUUID)

	// 注入 checker：仅该实例 UUID 视为已连入。
	svc.SetConnChecker(func(uuid string) bool { return uuid == inst.UUID })
	st, err = svc.Status(inst.ID)
	require.NoError(t, err)
	require.True(t, st.ProbeConnected)
}

// TestProbeUpdate_LastPushed 标记推送后 Status 的 lastPushedAt 非空且接近当前时间。
func TestProbeUpdate_LastPushed(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)

	before := time.Now().Add(-time.Second)
	svc.markPushed(inst.UUID)

	st, err := svc.Status(inst.ID)
	require.NoError(t, err)
	require.NotNil(t, st.LastPushedAt)
	require.True(t, st.LastPushedAt.After(before))
}

// TestProbeUpdate_NotEmbedded 未配置制品库时 Update/Batch 返回 ErrProbeNotEmbedded。
func TestProbeUpdate_NotEmbedded(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)

	_, err := svc.Update(inst.ID)
	require.ErrorIs(t, err, ErrProbeNotEmbedded)

	_, err = svc.Batch(ProbeUpdateBatchRequest{IDs: []uint{inst.ID}}, nil, false, nil)
	require.ErrorIs(t, err, ErrProbeNotEmbedded)
}

// TestProbeUpdate_ResolveTargets_Skipped 请求 IDs 中不存在的实例计入 skipped（存在性隐藏）。
func TestProbeUpdate_ResolveTargets_Skipped(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	a := mkProbeInstance(t, db, "a", 1)
	b := mkProbeInstance(t, db, "b", 1)

	// 请求 3 个 id，其中一个不存在 → skipped=1。
	insts, skipped, err := svc.resolveTargets(ProbeUpdateBatchRequest{IDs: []uint{a.ID, b.ID, 9999}}, nil, false)
	require.NoError(t, err)
	require.Len(t, insts, 2)
	require.Equal(t, 1, skipped)
}

// TestProbeUpdate_ResolveTargets_ScopeIsolation 越权实例被资源隔离剔除并计入 skipped。
func TestProbeUpdate_ResolveTargets_ScopeIsolation(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	a := mkProbeInstance(t, db, "a", 1)
	b := mkProbeInstance(t, db, "b", 1)

	// scope=true 且仅 a 可见：请求 a+b → 命中 a，b 越权计入 skipped。
	insts, skipped, err := svc.resolveTargets(
		ProbeUpdateBatchRequest{IDs: []uint{a.ID, b.ID}}, []uint{a.ID}, true)
	require.NoError(t, err)
	require.Len(t, insts, 1)
	require.Equal(t, a.ID, insts[0].ID)
	require.Equal(t, 1, skipped)

	// 空可见集合（scope=true, scopeIDs 空）→ 强制空结果。
	insts, _, err = svc.resolveTargets(
		ProbeUpdateBatchRequest{IDs: []uint{a.ID}}, nil, true)
	require.NoError(t, err)
	require.Len(t, insts, 0)
}

// TestProbeUpdate_ResolveTargets_Filter filter 模式按节点/状态/角色筛选。
func TestProbeUpdate_ResolveTargets_Filter(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	mkProbeInstance(t, db, "n1-a", 1)
	mkProbeInstance(t, db, "n1-b", 1)
	mkProbeInstance(t, db, "n2-a", 2)

	nodeID := uint(1)
	f := ProbeUpdateBatchFilter{NodeID: &nodeID}
	insts, skipped, err := svc.resolveTargets(ProbeUpdateBatchRequest{Filter: &f}, nil, false)
	require.NoError(t, err)
	require.Len(t, insts, 2, "仅节点 1 的两个实例命中")
	require.Equal(t, 0, skipped, "filter 模式 skipped 恒为 0")
}

// TestProbeUpdate_Update_RejectsProxy 真机回归（对 BungeeCord 代理点「更新探针」失败且原因被吞）：
// ServerProbe 是 Bukkit 插件，代理端无法加载——代理实例单发推送直接拒绝并带明确原因，
// 不再走完整推送链路后在依赖预置阶段才失败。守卫置于内嵌检查之前（不依赖内嵌产物可测）。
func TestProbeUpdate_Update_RejectsProxy(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	proxy := &model.Instance{
		Name: "lobby", NodeID: 1, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(proxy).Error)

	_, err := svc.Update(proxy.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "代理实例不适用", "拒绝原因必须明确指向代理不适用")
}

// TestProbeUpdate_DeployVersionTo_RejectsMissingProbePortWithoutSvc FR-411 守卫：
// probe_port=0 的实例（导入/历史）在未注入 instanceSvc 时，部署必须明确拒绝，
// 绝不写出 `port: 0` 的探针配置（探针绑 OS 随机端口 → 监控永无数据的静默断链）。
func TestProbeUpdate_DeployVersionTo_RejectsMissingProbePortWithoutSvc(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "imported", 1)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("probe_port", 0).Error)
	inst.ProbePort = 0

	err := svc.deployVersionTo(inst, &model.ArtifactVersion{}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "未分配探针端口", "拒绝原因必须明确指向缺失端口")
}

// TestProbeUpdate_DeployVersionTo_AutoAllocatesProbePort FR-411 补口主路径：
// 注入 instanceSvc 后，probe_port=0 的实例在部署前自动补分配端口、落库并刷新传入实例，
// 随后链路继续推进到 Worker 下发阶段（此处 pool 无 Worker，停在「未连接」而非端口错误）。
func TestProbeUpdate_DeployVersionTo_AutoAllocatesProbePort(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	svc := NewProbeUpdateService(db, pool, nil)
	svc.SetInstanceService(NewInstanceService(db, nil, pool))
	inst := mkProbeInstance(t, db, "imported", 1)
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", inst.ID).Update("probe_port", 0).Error)
	inst.ProbePort = 0

	err := svc.deployVersionTo(inst, &model.ArtifactVersion{}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "缺少关联节点", "应越过端口守卫推进到 Worker 下发阶段（测试未建 Node 行）")

	require.Positive(t, inst.ProbePort, "传入实例应已被补分配探针端口")
	var fresh model.Instance
	require.NoError(t, db.First(&fresh, inst.ID).Error)
	require.Equal(t, inst.ProbePort, fresh.ProbePort, "端口应已持久化到实例记录")

	// 再次部署：端口已就位，幂等（不再重复分配——仍停在 Worker 下发阶段）。
	err = svc.deployVersionTo(inst, &model.ArtifactVersion{}, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "缺少关联节点")
}

// TestEnsureProbePort_Idempotent 已有端口实例直返 changed=false，不改任何状态。
func TestEnsureProbePort_Idempotent(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	isvc := NewInstanceService(db, nil, pool)
	inst := mkProbeInstance(t, db, "smp", 1)

	changed, err := isvc.EnsureProbePort(inst)
	require.NoError(t, err)
	require.False(t, changed, "已有端口应幂等直返")
	require.Equal(t, 29940, inst.ProbePort)
}

// TestProbeUpdate_ResolveTargets_SkipsProxy 批量目标解析静默跳过代理实例（计入 skipped）。
func TestProbeUpdate_ResolveTargets_SkipsProxy(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	backend := mkProbeInstance(t, db, "smp", 1)
	proxy := &model.Instance{
		Name: "gate", NodeID: 1, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(proxy).Error)

	insts, skipped, err := svc.resolveTargets(ProbeUpdateBatchRequest{IDs: []uint{backend.ID, proxy.ID}}, nil, false)
	require.NoError(t, err)
	require.Len(t, insts, 1, "代理被排除，仅后端命中")
	require.Equal(t, backend.ID, insts[0].ID)
	require.Equal(t, 1, skipped, "被排除的代理计入 skipped")

	insts, _, err = svc.resolveTargets(ProbeUpdateBatchRequest{}, nil, false)
	require.NoError(t, err)
	for _, in := range insts {
		require.NotEqual(t, model.InstanceRoleProxy, in.Role, "filter 模式亦不得纳入代理")
	}
}

// TestProbeUpdate_Update_RejectsNonApplicable FR-454：不适用探针的实例（Beacon/通用二进制，及历史
// 误记 type=minecraft_java 的 Beacon）单发推送直接拒绝并带明确原因，不再推入无效 Bukkit jar。
func TestProbeUpdate_Update_RejectsNonApplicable(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)

	cases := []struct {
		name     string
		inst     *model.Instance
		wantWord string
	}{
		{
			name: "Beacon",
			inst: &model.Instance{
				Name: "beacon", NodeID: 1, Type: model.InstanceTypeGeneric,
				Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", Status: model.InstanceStatusStopped,
			},
			wantWord: "Beacon 实例不适用",
		},
		{
			name: "通用二进制",
			inst: &model.Instance{
				Name: "bin", NodeID: 1, Type: model.InstanceTypeGeneric,
				Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", Status: model.InstanceStatusStopped,
			},
			wantWord: "通用二进制实例不适用",
		},
		{
			name: "历史误配 Beacon（type=minecraft_java）",
			inst: &model.Instance{
				Name: "legacy-beacon", NodeID: 1, Type: model.InstanceTypeMinecraftJava,
				Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
				StartCommand: "x", Status: model.InstanceStatusStopped,
			},
			wantWord: "Beacon 实例不适用",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, db.Create(tc.inst).Error)
			_, err := svc.Update(tc.inst.ID)
			require.Error(t, err, "不适用实例必须拒绝而非推入无效 jar")
			require.Contains(t, err.Error(), tc.wantWord)
		})
	}
}

// TestProbeUpdate_ResolveTargets_SkipsNonApplicable FR-454：批量目标解析排除代理/Beacon/通用二进制，
// 仅保留适用实例；被排除者计入 skipped。
func TestProbeUpdate_ResolveTargets_SkipsNonApplicable(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	backend := mkProbeInstance(t, db, "smp", 1)
	beacon := &model.Instance{
		Name: "beacon", NodeID: 1, Type: model.InstanceTypeGeneric,
		Role: model.InstanceRoleBeacon, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	bin := &model.Instance{
		Name: "bin", NodeID: 1, Type: model.InstanceTypeGeneric,
		Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(beacon).Error)
	require.NoError(t, db.Create(bin).Error)

	insts, skipped, err := svc.resolveTargets(
		ProbeUpdateBatchRequest{IDs: []uint{backend.ID, beacon.ID, bin.ID}}, nil, false)
	require.NoError(t, err)
	require.Len(t, insts, 1, "仅适用探针的实例命中")
	require.Equal(t, backend.ID, insts[0].ID)
	require.Equal(t, 2, skipped, "被排除的 beacon/binary 计入 skipped")

	// filter 模式不得纳入不适用实例。
	insts, _, err = svc.resolveTargets(ProbeUpdateBatchRequest{}, nil, false)
	require.NoError(t, err)
	for _, in := range insts {
		require.True(t, model.IsProbeApplicable(in.Type, in.Role), "filter 模式亦不得纳入不适用实例")
	}
}
