package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
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

// TestProbeUpdate_Status_ConnAndEmbedded 验证状态：连接由 checker 决定，
// 内嵌信息透传，lastPushedAt 推送前为 nil。
func TestProbeUpdate_Status_ConnAndEmbedded(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)

	// 未注入 checker：一律未连入。
	st, err := svc.Status(inst.ID)
	require.NoError(t, err)
	require.False(t, st.ProbeConnected)
	require.Nil(t, st.LastPushedAt, "未推送过 lastPushedAt 应为 nil")
	require.Equal(t, cpembed.ProbeEmbeddedVersion, st.EmbeddedVersion)
	require.Equal(t, cpembed.ServerProbeJarInfo().Available, st.EmbeddedAvailable)
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

// TestProbeUpdate_NotEmbedded 未内嵌 jar 时 Update/Batch 返回 ErrProbeNotEmbedded。
// 本环境 jar 为构建产物（gitignored）通常缺失，命中本用例；若已 make embed-probe 则跳过。
func TestProbeUpdate_NotEmbedded(t *testing.T) {
	if cpembed.ServerProbeJarInfo().Available {
		t.Skip("已内嵌探针 jar，跳过未内嵌路径用例")
	}
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)

	_, err := svc.Update(inst.ID)
	require.ErrorIs(t, err, ErrProbeNotEmbedded)

	_, err = svc.Batch(ProbeUpdateBatchRequest{IDs: []uint{inst.ID}}, nil, false, nil)
	require.ErrorIs(t, err, ErrProbeNotEmbedded)
}

// TestProbeUpdate_DeployTo_NodeNotConnected jar 在位时（强制覆盖）节点未连接的失败路径。
// 通过直接调用 deployTo 绕过内嵌 gate——此处只验证「无节点连接 → 失败」，不依赖真实 jar。
func TestProbeUpdate_DeployTo_NodeNotConnected(t *testing.T) {
	if !cpembed.ServerProbeJarInfo().Available {
		t.Skip("未内嵌探针 jar，deployTo 在 jar 缺失时先于 pool 返回 ErrProbeNotEmbedded（无连接路径无法单测）")
	}
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)
	// 节点在线（过离线快速失败闸）但不在连接池 → 命中「未连接」而非「离线」。
	inst.Node = model.Node{UUID: "node-not-in-pool", Name: "n-online", Status: model.NodeStatusOnline, WSPort: 9102}

	err := svc.deployTo(inst)
	require.Error(t, err)
	require.Contains(t, err.Error(), "未连接")
}

// TestProbeUpdate_DeployTo_NodeOffline 节点离线 → deployTo 先于 jar/pool 快速失败（明确「离线」原因），
// 杜绝连接池残留失活隧道客户端致 DeployServerProbe 空转到 30s 超时（真机「更新探针一直 loading」）。
// 无需内嵌 jar：离线闸置于 jar 检查之前，任何构建下均可命中。
func TestProbeUpdate_DeployTo_NodeOffline(t *testing.T) {
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)
	inst := mkProbeInstance(t, db, "smp", 1)
	inst.Node = model.Node{UUID: "n-off", Name: "node-off", Status: model.NodeStatusOffline}

	err := svc.deployTo(inst)
	require.Error(t, err)
	require.Contains(t, err.Error(), "离线")
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

// TestProbeUpdate_Batch_EmptyTargets 无命中目标时返回零计数、不报错。
func TestProbeUpdate_Batch_EmptyTargets(t *testing.T) {
	if !cpembed.ServerProbeJarInfo().Available {
		t.Skip("未内嵌探针 jar，Batch 在 jar 缺失时整体返回 ErrProbeNotEmbedded")
	}
	db := newProbeUpdateTestDB(t)
	svc := NewProbeUpdateService(db, cpgrpc.NewClientPool(), nil)

	res, err := svc.Batch(ProbeUpdateBatchRequest{IDs: []uint{9999}}, nil, false, nil)
	require.NoError(t, err)
	require.Equal(t, 0, res.Requested)
	require.Equal(t, 1, res.Skipped)
	require.Equal(t, 0, res.Succeeded)
	require.Equal(t, 0, res.Failed)
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
