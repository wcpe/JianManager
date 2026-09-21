package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newBeaconTestDB 建一份含分组树与实例的最小内存库。
func newBeaconTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{},
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
		&model.AuditLog{},
	))
	return db
}

// beaconTreeJSON 构造一份「bc-main → r1 → z1/z2」的 zone-tree 响应。
func beaconTreeJSON() BeaconZoneTree {
	return BeaconZoneTree{
		NamespaceID: 7,
		Clusters: []BeaconZoneTreeCluster{{
			ID: 1, Code: "bc-main", Name: "主集群",
			Regions: []BeaconZoneTreeRegion{{
				ID: 10, Code: "r1", Name: "一区",
				Zones: []BeaconZoneTreeZone{
					{ID: 100, Code: "z1", Name: "一区一小区", ServerCount: 2},
					{ID: 101, Code: "z2", Name: "一区二小区", ServerCount: 1},
				},
			}},
		}},
	}
}

// beaconServersJSON 构造与上述树配套的 server 列表：z1 两台（一台默认入口），z2 一台。
func beaconServersJSON() []BeaconServerView {
	bc, r1 := uint(1), "r1"
	z1, z2 := uint(100), uint(101)
	return []BeaconServerView{
		{ID: 1, ServerID: "r1-z1-g1", BCClusterID: &bc, ZoneID: &z1, RegionName: &r1, IsDefaultEntry: true, Assigned: true, LifecycleStatus: "active"},
		{ID: 2, ServerID: "r1-z1-g2", BCClusterID: &bc, ZoneID: &z1, RegionName: &r1, IsDefaultEntry: false, Assigned: true, LifecycleStatus: "active"},
		{ID: 3, ServerID: "r1-z2-g1", BCClusterID: &bc, ZoneID: &z2, RegionName: &r1, IsDefaultEntry: false, Assigned: true, LifecycleStatus: "active"},
	}
}

// newBeaconMock 起一个 httptest mock Beacon：
//   - /admin/v2/zone-tree  → zone-tree
//   - /admin/v2/servers    → 分页返回 servers（每页 pageSize 生效）
//
// 校验 Authorization 头（要求 Bearer <token>），不符合返回 401，用于验证鉴权头确实发出。
func newBeaconMock(t *testing.T, token string, tree BeaconZoneTree, servers []BeaconServerView) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(BeaconAPIPathZoneTree, func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tree)
	})
	mux.HandleFunc(BeaconAPIPathServers, func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		if size <= 0 {
			size = 20
		}
		start := (page - 1) * size
		if start > len(servers) {
			start = len(servers)
		}
		end := start + size
		if end > len(servers) {
			end = len(servers)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": servers[start:end],
			"total": len(servers),
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newBeaconSync 用 mock Beacon 构造拉取服务（不涉及真实网络）。
func newBeaconSync(t *testing.T, db *gorm.DB, endpoint string, token string) *BeaconSyncService {
	t.Helper()
	client := NewBeaconClient(BeaconClientConfig{Endpoint: endpoint, Token: token, PullEnabled: true})
	require.NotNil(t, client)
	svc := NewBeaconSyncService(db, client, BeaconSyncConfig{NamespaceID: 7})
	svc.SetAuditService(NewAuditService(db))
	return svc
}

// seedInstance 建一台实例，返回其 ID。
func seedInstance(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	inst := &model.Instance{NodeID: 1, Name: name, Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "./run.sh"}
	require.NoError(t, db.Create(inst).Error)
	return inst.ID
}

// groupsByName 返回「分组名 → 节点」映射（测试断言用）。
func groupsByName(t *testing.T, db *gorm.DB) map[string]model.InstanceGroupNode {
	t.Helper()
	var nodes []model.InstanceGroupNode
	require.NoError(t, db.Find(&nodes).Error)
	out := make(map[string]model.InstanceGroupNode, len(nodes))
	for _, n := range nodes {
		out[n.Name] = n
	}
	return out
}

// instanceTags 读回实例标签。
func instanceTags(t *testing.T, db *gorm.DB, id uint) []string {
	t.Helper()
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	return model.ParseTags(inst.Tags)
}

// TestBeaconPull_BuildsFullTree 验收 #6：拉取能生成 集群 → 大区 → 小区 三层分组树。
func TestBeaconPull_BuildsFullTree(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "secret", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "secret")

	result, err := svc.PullTopology(context.Background(), 1, "127.0.0.1")
	require.NoError(t, err)

	// bc_cluster 是根分组（FR-444 §4.1）。
	byName := groupsByName(t, db)
	bc := byName["bc-main"]
	require.Nil(t, bc.ParentID, "BC 集群应是根分组")
	r1 := byName["r1"]
	require.NotNil(t, r1.ParentID)
	require.Equal(t, bc.ID, *r1.ParentID, "大区应是集群的子分组")
	z1 := byName["z1"]
	require.NotNil(t, z1.ParentID)
	require.Equal(t, r1.ID, *z1.ParentID, "小区应是大区的子分组")

	require.Equal(t, 1, result.Clusters)
	require.Equal(t, 1, result.Regions)
	require.Equal(t, 2, result.Zones)
	require.Equal(t, 4, result.CreatedGroups, "首次拉取应新建 集群+大区+两个小区")
}

// TestBeaconPull_AddsRegionZoneRoleTags 验收 #7：拉取能补 region:/zone:/role: 标签。
func TestBeaconPull_AddsRegionZoneRoleTags(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	g1 := seedInstance(t, db, "r1-z1-g1") // 默认入口 → lobby
	g2 := seedInstance(t, db, "r1-z1-g2") // 非默认入口 → game
	g3 := seedInstance(t, db, "r1-z2-g1")

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)

	require.Equal(t, []string{"region:r1", "zone:z1", "role:lobby"}, instanceTags(t, db, g1))
	require.Equal(t, []string{"region:r1", "zone:z1", "role:game"}, instanceTags(t, db, g2))
	require.Equal(t, []string{"region:r1", "zone:z2", "role:game"}, instanceTags(t, db, g3))
}

// TestBeaconPull_MergesTagsKeepingOtherDimensions 补标签不吞掉既有其它维度标签（如 env:）。
func TestBeaconPull_MergesTagsKeepingOtherDimensions(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	inst := &model.Instance{
		NodeID: 1, Name: "r1-z1-g1", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, StartCommand: "./run.sh",
		Tags: `["env:prod","region:old"]`,
	}
	require.NoError(t, db.Create(inst).Error)

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)

	tags := instanceTags(t, db, inst.ID)
	require.Contains(t, tags, "env:prod", "非 Beacon 维度标签应保留")
	require.Contains(t, tags, "region:r1", "同维度旧值应被覆盖")
	require.NotContains(t, tags, "region:old")
}

// TestBeaconPull_Idempotent 验收 #9：重复拉取幂等（不产生重复分组 / 不重复加入）。
func TestBeaconPull_Idempotent(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	g1 := seedInstance(t, db, "r1-z1-g1")
	seedInstance(t, db, "r1-z1-g2")
	seedInstance(t, db, "r1-z2-g1")

	first, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, 4, first.CreatedGroups)

	second, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)
	require.Zero(t, second.CreatedGroups, "重复拉取不应新建分组")
	require.Zero(t, second.MovedInstances, "重复拉取不应再移入")
	require.Zero(t, second.RetaggedInstances, "重复拉取不应再改标签")

	// 分组总数不膨胀；成员关系不重复。
	byName := groupsByName(t, db)
	require.Len(t, byName, 4, "应仍只有 集群+大区+两小区 四个分组")
	var members int64
	require.NoError(t, db.Model(&model.InstanceGroupMember{}).
		Where("group_id = ? AND instance_id = ?", byName["z1"].ID, g1).Count(&members).Error)
	require.EqualValues(t, 1, members)
}

// TestBeaconPull_ReusesExistingGroup 验收冲突处理：分组已存在同名节点 → 复用而非重复创建。
func TestBeaconPull_ReusesExistingGroup(t *testing.T) {
	db := newBeaconTestDB(t)
	groups := NewInstanceGroupService(db)

	// 人工已建同名根分组与子分组（承载非 Beacon 语义的归类）。
	existing, err := groups.Create("bc-main", nil)
	require.NoError(t, err)
	existingRegion, err := groups.Create("r1", &existing.ID)
	require.NoError(t, err)

	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")
	result, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)

	// 只新建两个小区；集群与大区复用既有节点。
	require.Equal(t, 2, result.CreatedGroups)

	byName := groupsByName(t, db)
	require.Len(t, byName, 4)
	require.Equal(t, existing.ID, byName["bc-main"].ID, "应复用既有根分组")
	require.Equal(t, existingRegion.ID, byName["r1"].ID, "应复用既有大区分组")
}

// TestBeaconPull_KeepsLocalOnlyInstances 冲突处理：本地有而 Beacon 无 → 保留不动。
func TestBeaconPull_KeepsLocalOnlyInstances(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	localOnly := seedInstance(t, db, "local-only-server")
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", localOnly).
		Update("tags", `["env:dev"]`).Error)

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)

	// 标签未被 Beacon 维度覆盖，且不被加入任何分组。
	require.Equal(t, []string{"env:dev"}, instanceTags(t, db, localOnly))
	var members int64
	require.NoError(t, db.Model(&model.InstanceGroupMember{}).Where("instance_id = ?", localOnly).Count(&members).Error)
	require.Zero(t, members, "Beacon 未管理的实例不应被拉入分组")
}

// TestBeaconPull_SkipsServersWithoutLocalInstance 冲突处理：
// Beacon 有而本地无 → 跳过并记录警告（不同步创建实例）。
func TestBeaconPull_SkipsServersWithoutLocalInstance(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	// 只建一台，另两台本地缺失。
	seedInstance(t, db, "r1-z1-g1")

	result, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)

	require.Equal(t, []string{"r1-z1-g2", "r1-z2-g1"}, result.SkippedServers)
	require.Equal(t, 1, result.MatchedInstances)
	// 不同步创建实例。
	var count int64
	require.NoError(t, db.Model(&model.Instance{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

// TestBeaconPull_UnreachableLeavesLocalUntouched 验收 #8（核心硬要求）：
// Beacon 不可达时返回明确错误且本地零改动。
func TestBeaconPull_UnreachableLeavesLocalUntouched(t *testing.T) {
	db := newBeaconTestDB(t)
	groups := NewInstanceGroupService(db)

	// 前置本地状态：一个分组 + 一台带标签的实例。
	preexisting, err := groups.Create("人工分组", nil)
	require.NoError(t, err)
	instID := seedInstance(t, db, "r1-z1-g1")
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", instID).
		Update("tags", `["env:prod"]`).Error)

	// Beacon 立刻关闭：连接必失败。
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	endpoint := srv.URL
	srv.Close()

	svc := newBeaconSync(t, db, endpoint, "")
	_, err = svc.PullTopology(context.Background(), 1, "")
	require.Error(t, err, "不可达必须返回明确错误，不得静默成功")
	require.ErrorIs(t, err, ErrBeaconUnreachable)

	// 本地零改动：分组树与实例标签原样。
	byName := groupsByName(t, db)
	require.Len(t, byName, 1, "不应新建任何分组")
	require.Contains(t, byName, "人工分组")
	require.Equal(t, []string{"env:prod"}, instanceTags(t, db, instID))
	var members int64
	require.NoError(t, db.Model(&model.InstanceGroupMember{}).Where("instance_id = ?", instID).Count(&members).Error)
	require.Zero(t, members)
	_ = preexisting
}

// TestBeaconPull_ZoneTreeUnreachableDoesNotWrite 只 zone-tree 可达、servers 端点 500 时同样零改动。
// 覆盖「两个端点中任一失败」：拉取阶段未完成即不进入写入阶段。
func TestBeaconPull_ZoneTreeUnreachableDoesNotWrite(t *testing.T) {
	db := newBeaconTestDB(t)
	seedInstance(t, db, "r1-z1-g1")

	mux := http.NewServeMux()
	mux.HandleFunc(BeaconAPIPathZoneTree, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(beaconTreeJSON())
	})
	mux.HandleFunc(BeaconAPIPathServers, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := newBeaconSync(t, db, srv.URL, "")
	_, err := svc.PullTopology(context.Background(), 1, "")
	require.ErrorIs(t, err, ErrBeaconUnreachable)

	// 第一个端点已成功返回了树，但 servers 失败 → 整轮放弃，分组树保持为空。
	require.Empty(t, groupsByName(t, db), "第二个端点失败也不得写入半棵树")
}

// TestBeaconPull_EmptyTopologyRejected 拉取到的树为空 → 报错不改本地（不静默建空树）。
func TestBeaconPull_EmptyTopologyRejected(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", BeaconZoneTree{NamespaceID: 7}, nil)
	svc := newBeaconSync(t, db, srv.URL, "")

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.ErrorIs(t, err, ErrBeaconTopologyInvalid)
	require.Empty(t, groupsByName(t, db))
}

// TestBeaconPull_RollsBackOnPartialFailure 事务性：分组建到一半失败时整体回滚，不留半棵树。
//
// 用「先占用目标 ID，使后续建组撞唯一约束」构造确定性失败（SQLite 的 VARCHAR 不校验长度，
// 故不能用超长名触发；改成让第二轮建组与既有 UUID 冲突不可控，这里直接注入失败）。
func TestBeaconPull_RollsBackOnPartialFailure(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	// 让分组表在建第二个节点时失败：注册一个 before-create 钩子，
	// 在名为 "z1" 的节点创建前返回错误（此时 "bc-main" 与 "r1" 已建）。
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("beacon_test_fail_z1", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "instance_group_nodes" {
			return
		}
		node, ok := tx.Statement.Dest.(*model.InstanceGroupNode)
		if !ok || node.Name != "z1" {
			return
		}
		tx.AddError(errors.New("注入的建组失败（测试用）"))
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove("beacon_test_fail_z1") })

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.Error(t, err, "建组失败应报错")
	require.Empty(t, groupsByName(t, db), "失败的写入必须整体回滚，不留半棵树")

	// 解除注入后重跑应成功（证明前次确实回滚干净，无残留冲突）。
	require.NoError(t, db.Callback().Create().Remove("beacon_test_fail_z1"))
	result, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, 4, result.CreatedGroups, "回滚干净后重跑应能建全 4 个分组")
}

// TestBeaconPull_DisabledWhenNotConfigured 核心约束：未配置 endpoint 时拉取报「未启用」，
// 且不触碰本地数据（可选协同，绝非依赖）。
func TestBeaconPull_DisabledWhenNotConfigured(t *testing.T) {
	db := newBeaconTestDB(t)
	require.Nil(t, NewBeaconClient(BeaconClientConfig{}), "未配置 endpoint 时客户端应为 nil")

	svc := NewBeaconSyncService(db, NewBeaconClient(BeaconClientConfig{}), BeaconSyncConfig{})
	require.False(t, svc.Configured())
	require.False(t, svc.Enabled())

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.ErrorIs(t, err, ErrBeaconSyncDisabled)
	require.Empty(t, groupsByName(t, db))
}

// TestBeaconPull_DisabledWhenPullNotEnabled 配了端点但未开 pull-enabled → 同样拒绝且零改动。
func TestBeaconPull_DisabledWhenPullNotEnabled(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())

	client := NewBeaconClient(BeaconClientConfig{Endpoint: srv.URL, PullEnabled: false})
	require.NotNil(t, client)
	svc := NewBeaconSyncService(db, client, BeaconSyncConfig{})
	require.True(t, svc.Configured(), "配了端点即 configured")
	require.False(t, svc.Enabled(), "未开 pull-enabled 即 not enabled")

	_, err := svc.PullTopology(context.Background(), 1, "")
	require.ErrorIs(t, err, ErrBeaconSyncDisabled)
	require.Empty(t, groupsByName(t, db))
}

// TestBeaconPull_SendsBearerToken 鉴权头确实按配置发出（错误 token 被 401 拒绝）。
func TestBeaconPull_SendsBearerToken(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "right-token", beaconTreeJSON(), beaconServersJSON())

	wrong := newBeaconSync(t, db, srv.URL, "wrong-token")
	_, err := wrong.PullTopology(context.Background(), 1, "")
	require.ErrorIs(t, err, ErrBeaconUnreachable, "鉴权失败应报不可达类错误")
	require.Empty(t, groupsByName(t, db))

	right := newBeaconSync(t, db, srv.URL, "right-token")
	_, err = right.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)
	require.NotEmpty(t, groupsByName(t, db))
}

// TestBeaconPull_ServerPagination 分页遍历：超过一页的 server 列表应全部拉到。
func TestBeaconPull_ServerPagination(t *testing.T) {
	db := newBeaconTestDB(t)
	z1 := uint(100)
	servers := make([]BeaconServerView, 0, beaconServersPageSize+5)
	for i := 0; i < beaconServersPageSize+5; i++ {
		name := "server-" + strconv.Itoa(i)
		servers = append(servers, BeaconServerView{ID: uint(i + 1), ServerID: name, ZoneID: &z1, LifecycleStatus: "active"})
		seedInstance(t, db, name)
	}
	srv := newBeaconMock(t, "", beaconTreeJSON(), servers)
	svc := newBeaconSync(t, db, srv.URL, "")

	result, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, beaconServersPageSize+5, result.MatchedInstances, "跨页 server 应全部匹配")
}

// TestBeaconPull_SkipsUnassignedAndTombstoned server 未分派 / 已墓碑 → 跳过，不建归组。
func TestBeaconPull_SkipsUnassignedAndTombstoned(t *testing.T) {
	db := newBeaconTestDB(t)
	z1 := uint(100)
	seedInstance(t, db, "unassigned")
	seedInstance(t, db, "tombstoned")
	seedInstance(t, db, "normal")
	servers := []BeaconServerView{
		{ID: 1, ServerID: "unassigned", LifecycleStatus: "active"},
		{ID: 2, ServerID: "tombstoned", ZoneID: &z1, LifecycleStatus: "active", Tombstone: &struct {
			At     string `json:"at"`
			Reason string `json:"reason"`
		}{At: "2026-01-01T00:00:00Z", Reason: "decommissioned"}},
		{ID: 3, ServerID: "normal", ZoneID: &z1, LifecycleStatus: "active"},
	}
	srv := newBeaconMock(t, "", beaconTreeJSON(), servers)
	svc := newBeaconSync(t, db, srv.URL, "")

	result, err := svc.PullTopology(context.Background(), 1, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.MatchedInstances, "只有 normal 应被归组")

	// 墓碑与被跳过的实例都不进分组。
	var members int64
	require.NoError(t, db.Model(&model.InstanceGroupMember{}).Count(&members).Error)
	require.EqualValues(t, 1, members)
}

// TestBeaconPull_WritesAudit 成功与失败各写一条审计（动作名可区分）。
func TestBeaconPull_WritesAudit(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())
	svc := newBeaconSync(t, db, srv.URL, "")

	_, err := svc.PullTopology(context.Background(), 42, "10.0.0.1")
	require.NoError(t, err)

	var okLogs []model.AuditLog
	require.NoError(t, db.Where("action = ?", AuditActionBeaconPullOK).Find(&okLogs).Error)
	require.Len(t, okLogs, 1)
	require.False(t, okLogs[0].Failed)
	require.Equal(t, uint(42), okLogs[0].UserID)

	// 失败：换成不可达端点。
	bad := newBeaconSync(t, db, "http://127.0.0.1:1", "")
	_, err = bad.PullTopology(context.Background(), 42, "10.0.0.1")
	require.Error(t, err)

	var failLogs []model.AuditLog
	require.NoError(t, db.Where("action = ?", AuditActionBeaconPullFail).Find(&failLogs).Error)
	require.Len(t, failLogs, 1)
	require.True(t, failLogs[0].Failed)
	require.NotEmpty(t, failLogs[0].Error, "失败审计应带错误详情")
}

// TestBeaconPull_NotTriggeredByOtherOperations 保证拉取恒为手动：
// 服务不注册任何定时器，构造后本地库不变。
func TestBeaconPull_NoAutomaticTreeBuilding(t *testing.T) {
	db := newBeaconTestDB(t)
	srv := newBeaconMock(t, "", beaconTreeJSON(), beaconServersJSON())

	calls := int32(0)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	_ = srv

	_ = NewBeaconSyncService(db, NewBeaconClient(BeaconClientConfig{Endpoint: "http://127.0.0.1:1", PullEnabled: true}), BeaconSyncConfig{})
	_ = httptest.NewServer(mux)

	// 构造服务本身不发任何请求、不建任何分组（不自动建树）。
	require.Zero(t, atomic.LoadInt32(&calls))
	require.Empty(t, groupsByName(t, db), "构造服务不得自动建树")
}

// TestMergeBeaconTags 表驱动覆盖标签合并的各维度语义。
func TestMergeBeaconTags(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		desired  []string
		want     []string
	}{
		{"空既有", nil, []string{"region:r1"}, []string{"region:r1"}},
		{"保留其它维度", []string{"env:prod"}, []string{"region:r1"}, []string{"env:prod", "region:r1"}},
		{"覆盖同维度", []string{"region:old"}, []string{"region:r1"}, []string{"region:r1"}},
		// 只覆盖 desired 中出现的维度：本次未涉及 zone: 时，旧 zone 值原样保留。
		{"只覆盖出现的维度", []string{"region:old", "zone:old", "env:dev"}, []string{"region:r1"}, []string{"zone:old", "env:dev", "region:r1"}},
		{"多维度同时覆盖", []string{"region:old", "zone:old", "env:dev"}, []string{"region:r1", "zone:z1"}, []string{"env:dev", "region:r1", "zone:z1"}},
		{"role 维度覆盖", []string{"role:game"}, []string{"role:lobby"}, []string{"role:lobby"}},
		{"desired 为空时仅规范化", []string{" env:a ", "", "env:a"}, nil, []string{"env:a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, mergeBeaconTags(tc.existing, tc.desired))
		})
	}
}

// TestBeaconTags 表驱动覆盖 role 推断（isDefaultEntry → lobby/game）。
func TestBeaconTags(t *testing.T) {
	require.Equal(t, []string{"region:r1", "zone:z1", "role:lobby"}, beaconTags("r1", "z1", true))
	require.Equal(t, []string{"region:r1", "zone:z1", "role:game"}, beaconTags("r1", "z1", false))
	require.Equal(t, []string{"role:game"}, beaconTags("", "", false), "code 缺失时不产生空标签")
}

// TestBeaconClient_NilWhenEndpointEmpty 未配置端点即不构造客户端（调用方整体跳过）。
func TestBeaconClient_NilWhenEndpointEmpty(t *testing.T) {
	require.Nil(t, NewBeaconClient(BeaconClientConfig{Endpoint: "   "}))
	require.Nil(t, NewBeaconClient(BeaconClientConfig{Endpoint: ""}))
	client := NewBeaconClient(BeaconClientConfig{Endpoint: "http://b.example.com/"})
	require.NotNil(t, client)
	require.Equal(t, "http://b.example.com", client.Endpoint(), "尾斜杠应归一化")
	require.True(t, client.Enabled())
}

// TestBeaconClient_PullDisabled 客户端层面也拦未启用（双保险）。
func TestBeaconClient_PullDisabled(t *testing.T) {
	client := NewBeaconClient(BeaconClientConfig{Endpoint: "http://b.example.com"})
	require.NotNil(t, client)
	_, err := client.PullTopology(context.Background(), 0)
	require.ErrorIs(t, err, ErrBeaconPullDisabled)
}

// TestBeaconClient_Non2xxIsUnreachable 非 2xx 一律归为「不可达」类错误。
func TestBeaconClient_Non2xxIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"FORBIDDEN"}`))
	}))
	defer srv.Close()

	client := NewBeaconClient(BeaconClientConfig{Endpoint: srv.URL, PullEnabled: true})
	_, err := client.PullTopology(context.Background(), 0)
	require.ErrorIs(t, err, ErrBeaconUnreachable)
	require.Contains(t, err.Error(), "403")
}

// TestBeaconClient_InvalidJSONIsUnreachable 响应不可解析同样报错（不静默当空树）。
func TestBeaconClient_InvalidJSONIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	client := NewBeaconClient(BeaconClientConfig{Endpoint: srv.URL, PullEnabled: true})
	_, err := client.PullTopology(context.Background(), 0)
	require.ErrorIs(t, err, ErrBeaconUnreachable)
	require.False(t, errors.Is(err, ErrBeaconTopologyInvalid), "解析失败属不可达类，不是拓扑不合法")
}
