package logcoord

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 本文件是「日志查询路径 CP 侧 DB 占用」优化的回归测试（2026-10-09）。
//
// 优化前的实测口径（装饰 GORM 回调计数，见 TestResolverSQLCountDoesNotGrowWithInstances）：
//   - 通配 *：3 条 SQL（nodes / instances / log_target_holders 各 1），与实例数无关；
//   - 显式 inst:<id>：**N+2 条**（每个实例一次 GetByID + 1 条 holder + 1 条 nodes）——
//     25 个实例即 27 条，这是非平台管理员「收敛到可访问实例集合」的常规形态；
//   - 缓慢路径（Stats/Facets）每次解析目标 **2 遍**（CreateView 内一次 + resolveForQuery 一次）
//     ⇒ DB 开销翻倍（6 条），而 stats/facets 正是生产 p90 最高的两条端点。
//
// 优化后：任意形态、任意实例数均为常数条（通配 1 条冷 / 0 条暖；显式 2 条/次）。

// sqlCounterAll 统计全部 SQL 条数（Query + Row 两条回调路径都要挂：
// Find/First 走 Query 处理器，Scan/Raw 走 Row 处理器，只挂一个会漏计）。
type sqlCounterAll struct {
	// total 必须是原子量（本 PR 的测试夹具竞争修复）：
	// 本类型被 8 路并发的 Resolve 用例使用（见 TestResolverConcurrentResolveIsSingleFlight），
	// 而 gorm 的 After 回调会在**多个 goroutine** 里执行 `total++` —— 普通 int 的并发自增
	// 是数据竞争（-race 会报 DATA RACE，且计数可能丢更新使"单飞"断言失真）。
	total atomic.Int64
}

func newSQLCounterAll(t *testing.T, db *gorm.DB) *sqlCounterAll {
	t.Helper()
	c := &sqlCounterAll{}
	name := "sql_all_" + t.Name()
	record := func(tx *gorm.DB) {
		if tx.Statement.SQL.String() != "" {
			c.total.Add(1)
		}
	}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name+"_q", record))
	require.NoError(t, db.Callback().Row().After("gorm:row").Register(name+"_r", record))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name + "_q")
		_ = db.Callback().Row().Remove(name + "_r")
	})
	return c
}

// reset 归零，便于分段统计「冷查 / 暖查」。
func (c *sqlCounterAll) reset() { c.total.Store(0) }

// seedResolverFixtures 造 count 个实例（同一节点）+ 每实例一条历史 holder，返回授权目标与解析器。
//
// 历史 holder 的 WorkerUUID 与当前节点不同，故它们会作为 HistoricalHolder 目标出现，
// 其 readiness 依赖 conn[h.WorkerUUID]（隧道活跃度）——这正是「不得缓存到失真」的对象。
func seedResolverFixtures(t *testing.T, db *gorm.DB, nodeUUID string, count int) (*ProductionTargetResolver, []uint, uint) {
	t.Helper()
	node := model.Node{UUID: nodeUUID, Name: "n-" + nodeUUID, Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	ids := make([]uint, 0, count)
	for i := 0; i < count; i++ {
		inst := model.Instance{
			UUID: fmt.Sprintf("inst-%s-%d", nodeUUID, i), Name: fmt.Sprintf("srv-%d", i), NodeID: node.ID,
			Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
		}
		require.NoError(t, db.Create(&inst).Error)
		ids = append(ids, inst.ID)
		require.NoError(t, db.Create(&model.LogTargetHolder{
			InstanceID: inst.ID, WorkerUUID: fmt.Sprintf("w-old-%s-%d", nodeUUID, i),
			SourceGeneration: "g", StorageNamespace: fmt.Sprintf("ns-%d", i), Current: false,
		}).Error)
	}
	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	r := NewProductionTargetResolver(service.NewNodeService(db), instSvc, fakeConnected{ids: []string{nodeUUID}}, service.NewLogTargetHolderService(db))
	return r, ids, node.ID
}

// TestResolverSQLCountDoesNotGrowWithInstances ① 优化后查询次数不随实例数增长。
//
// 优化前（实测）：显式 inst: 形态下 1 实例 = 3 条、5 实例 = 7 条、25 实例 = 27 条、50 实例 = 52 条
// （即 N+2）。本用例锁定「与实例数无关」这一不变量。
func TestResolverSQLCountDoesNotGrowWithInstances(t *testing.T) {
	for _, n := range []int{1, 5, 25, 50} {
		t.Run(fmt.Sprintf("instances=%d", n), func(t *testing.T) {
			db := setupAssembleDB(t)
			r, ids, _ := seedResolverFixtures(t, db, "w-scale", n)

			authorized := make([]string, 0, len(ids))
			for _, id := range ids {
				authorized = append(authorized, fmt.Sprintf("inst:%d", id))
			}

			c := newSQLCounterAll(t, db)
			targets, err := r.Resolve(context.Background(), authorized)
			require.NoError(t, err)
			// 冷查一次：nodes + instances(AllBriefs) + holders = 3 条；关键是**不随 n 增长**。
			assert.LessOrEqual(t, int(c.total.Load()), 3,
				"显式 inst: 形态下冷查 SQL 必须为常数条（优化前为 N+2=%d 条）", n+2)
			require.Len(t, targets, 2*n, "实例目标 + 历史 holder 目标都不得丢失")

			// 暖查（TTL 内）：完全不再打库。
			c.reset()
			_, err = r.Resolve(context.Background(), authorized)
			require.NoError(t, err)
			assert.Equal(t, int64(0), c.total.Load(), "TTL 内重复解析不得再产生 SQL")
		})
	}
}

// TestResolverTransientInstanceErrorNotCached 身份查询的瞬时错误不得被负缓存。
//
// 若把「查询报错」与「实例不存在」混为一谈并缓存，TTL 窗口内会持续谎称实例不存在
// （产出 not_ready 占位、丢掉历史 holder 目标），比不缓存更糟。本用例锁定：
// 表恢复后立刻能重新解析出实例目标与历史 holder。
func TestResolverTransientInstanceErrorNotCached(t *testing.T) {
	db := setupAssembleDB(t)
	_, ids, _ := seedResolverFixtures(t, db, "w-trans", 1)
	instID := ids[0]
	target := []string{fmt.Sprintf("inst:%d", instID)}

	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	holders := service.NewLogTargetHolderService(db)
	r := NewProductionTargetResolver(service.NewNodeService(db), instSvc, fakeConnected{ids: []string{"w-trans"}}, holders)

	// 首次：表齐全 ⇒ 正常解析（实例目标 + 历史 holder）。
	ok, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, ok, 2, "实例目标 + 1 条历史 holder 目标")

	// 换新解析器并把实例表打掉 ⇒ 身份查询报错（瞬时错误，而非「实例不存在」）。
	require.NoError(t, db.Migrator().DropTable(&model.Instance{}))
	fresh := NewProductionTargetResolver(service.NewNodeService(db), service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()), fakeConnected{ids: []string{"w-trans"}}, holders)
	broken, err := fresh.Resolve(context.Background(), target)
	require.NoError(t, err, "身份查询失败不应让整次解析报错")
	require.Len(t, broken, 1)
	assert.Equal(t, ReadyNotReady, broken[0].Readiness, "查不到身份时产出 not_ready 占位")

	// 立即恢复实例表（TTL 内）⇒ 必须重新解析成功，证明「报错」没有被负缓存。
	// 注意：这里刻意沿用**同一个** target（同一个 inst:<id>），因为若把「查询报错」
	// 负缓存为 found=false，恰好就是这条 target 会在 TTL 内一直拿到 not_ready 占位。
	require.NoError(t, db.AutoMigrate(&model.Instance{}))
	nodeID := brokenNodeID(t, db, "w-trans")
	require.NoError(t, db.Create(&model.Instance{
		ID: instID, UUID: "inst-trans-re", Name: "trans-re", NodeID: nodeID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}).Error)

	recovered, err := fresh.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, recovered, 2,
		"表恢复后必须立刻重新解析出实例目标与历史 holder（报错不得被负缓存）")
	assert.Equal(t, fmt.Sprintf("inst:%d", instID), recovered[0].ID)
	assert.NotEqual(t, ReadyNotReady, recovered[0].Readiness, "恢复后应是可用的节点身份")
}

// brokenNodeID 取指定 UUID 节点的 ID。
func brokenNodeID(t *testing.T, db *gorm.DB, uuid string) uint {
	t.Helper()
	var node model.Node
	require.NoError(t, db.Where("uuid = ?", uuid).First(&node).Error)
	return node.ID
}

// TestResolverHolderFailureRetriesWithinTTL holder 取数失败不得在 TTL 内被固化成逐实例回落。
//
// 若失败被写进快照，整个 TTL 窗口内每次解析都会走「逐实例回落」= N 条 SQL，
// 反而比不做缓存更糟（把一次瞬时失败放大成窗口内的高 DB 占用）。本用例锁定：
// 恢复后立刻补齐，且立刻回到「1 条批量 holder」而不是 N 条。
func TestResolverHolderFailureRetriesWithinTTL(t *testing.T) {
	db := setupAssembleDB(t)
	r, ids, _ := seedResolverFixtures(t, db, "w-hfail", 4)
	target := []string{"*"}

	// 首次解析正常，把快照预热（含 holder）。
	first, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	holderTargets := 0
	for _, tg := range first {
		if tg.HistoricalHolder {
			holderTargets++
		}
	}
	require.Equal(t, len(ids), holderTargets, "前置条件：每个实例应有一条历史 holder 目标")

	// 重建解析器（空快照），并让 holder 表在首次解析时不可用。
	require.NoError(t, db.Migrator().DropTable(&model.LogTargetHolder{}))
	fresh := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		fakeConnected{ids: []string{"w-hfail"}}, service.NewLogTargetHolderService(db),
	)
	degraded, err := fresh.Resolve(context.Background(), target)
	require.NoError(t, err, "holder 元数据不可用不得让整次解析失败")
	for _, tg := range degraded {
		assert.False(t, tg.HistoricalHolder, "holder 表不可用时不应凭空产出 holder 目标")
	}

	// 恢复 holder 表，并在 TTL 内再次解析：必须立刻补齐 holder（而不是把失败固化到 TTL 结束）。
	require.NoError(t, db.AutoMigrate(&model.LogTargetHolder{}))
	for i, id := range ids {
		require.NoError(t, db.Create(&model.LogTargetHolder{
			InstanceID: id, WorkerUUID: fmt.Sprintf("w-rec-%d", i),
			SourceGeneration: "g", StorageNamespace: "ns", Current: false,
		}).Error)
	}

	c := newSQLCounterAll(t, db)
	recovered, err := fresh.Resolve(context.Background(), target)
	require.NoError(t, err)
	assert.LessOrEqual(t, int(c.total.Load()), 3,
		"holder 恢复后必须回到批量取数（≤3 条），不得固化成逐实例回落（%d 条）", len(ids)+2)

	got := 0
	for _, tg := range recovered {
		if tg.HistoricalHolder {
			got++
		}
	}
	assert.Equal(t, len(ids), got, "holder 恢复后必须在同一 TTL 内补齐历史 holder 目标")
}

// TestResolverWildcardColdAndWarmCost 通配 *（平台管理员默认形态）的冷/暖开销。
func TestResolverWildcardColdAndWarmCost(t *testing.T) {
	db := setupAssembleDB(t)
	r, ids, _ := seedResolverFixtures(t, db, "w-wild", 25)

	c := newSQLCounterAll(t, db)
	targets, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)
	cold := c.total.Load()
	require.GreaterOrEqual(t, len(targets), 25)

	// 一次「页面加载」= 列表 + stats + facets 三次解析：后两次必须零 SQL。
	c.reset()
	for i := 0; i < 2; i++ {
		_, err = r.Resolve(context.Background(), []string{"*"})
		require.NoError(t, err)
	}
	assert.Equal(t, int64(0), c.total.Load(), "同一页面加载的后两次解析应命中快照，不再打库")
	assert.LessOrEqual(t, cold, int64(3), "通配冷查应为常数条（nodes + instances + holders）")
	_ = ids
}

// TestResolverCacheIsolatesAuthorizedSets ② 最关键的安全断言：
// 不同 authorized 集合**绝不允许**互相命中缓存。
//
// 本条把「缓存只能存授权无关的输入」这一设计变成可执行断言：
// 先让「全平台通配」调用把缓存彻底预热，再立刻以「仅 inst:A」的窄授权调用同一解析器实例。
// 若实现把授权过滤结果（而非原始元数据）入了缓存，窄授权方就会拿到全平台目标 —— 越权。
func TestResolverCacheIsolatesAuthorizedSets(t *testing.T) {
	db := setupAssembleDB(t)
	r, ids, _ := seedResolverFixtures(t, db, "w-iso", 6)
	require.GreaterOrEqual(t, len(ids), 3)
	instA, instB, instC := ids[0], ids[1], ids[2]

	// 1) 先用平台级通配把缓存预热到「满」。
	wide, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(wide), 12, "通配应展开出全部实例与历史 holder")

	// 2) 立刻用窄授权（只含 inst:A）解析 —— 缓存必然处于新鲜期。
	narrow, err := r.Resolve(context.Background(), []string{fmt.Sprintf("inst:%d", instA)})
	require.NoError(t, err)

	// 3) 窄授权的结果只允许含 A 自身（实例目标 + 其历史 holder），绝不允许出现 B/C 的任何目标。
	allowedInst := fmt.Sprintf("inst:%d", instA)
	var holderIDsOfA []string
	for _, tg := range wide {
		if tg.AuthorizationID == allowedInst && tg.HistoricalHolder {
			holderIDsOfA = append(holderIDsOfA, tg.ID)
		}
	}
	require.NotEmpty(t, holderIDsOfA, "前置条件：A 应有一条历史 holder 目标")

	got := map[string]bool{}
	for _, tg := range narrow {
		got[tg.ID] = true
	}
	assert.True(t, got[allowedInst], "A 自身的实例目标必须可见")
	assert.NotContains(t, got, fmt.Sprintf("inst:%d", instB), "预热后的缓存不得让 B 的实例目标泄漏给 A")
	assert.NotContains(t, got, fmt.Sprintf("inst:%d", instC), "预热后的缓存不得让 C 的实例目标泄漏给 A")
	for id := range got {
		// 每一项都必须是 A 自身或 A 的历史 holder —— 用 wide 的结果做白名单交叉校验。
		ok := id == allowedInst
		for _, h := range holderIDsOfA {
			if id == h {
				ok = true
			}
		}
		assert.True(t, ok, "窄授权结果出现集合外目标 %q（缓存跨集合命中 = 越权）", id)
	}
	assert.Len(t, narrow, 1+len(holderIDsOfA), "窄授权目标数应恰为 A + 其历史 holder")

	// 4) 反向：先窄后宽也必须各自正确（防止缓存按「首位调用方」固定住结果）。
	reverse := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		fakeConnected{ids: []string{"w-iso"}},
		service.NewLogTargetHolderService(db),
	)
	n1, err := reverse.Resolve(context.Background(), []string{fmt.Sprintf("inst:%d", instB)})
	require.NoError(t, err)
	w1, err := reverse.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)
	for _, tg := range n1 {
		assert.NotEqual(t, fmt.Sprintf("inst:%d", instA), tg.ID, "先窄后宽：窄调用不得被宽结果污染")
	}
	assert.GreaterOrEqual(t, len(w1), 12, "先窄后宽：宽调用仍须拿到全量目标")
}

// TestResolverCacheDoesNotCarryExternalAuthorization 授权收紧必须立即生效。
//
// 场景：调用方在上一次（宽）授权后被降权为只有 A。快照仍在 TTL 内，
// 此时解析必须只返回 A —— 授权边界由入参现算，与缓存无关。
func TestResolverCacheDoesNotCarryExternalAuthorization(t *testing.T) {
	db := setupAssembleDB(t)
	r, ids, _ := seedResolverFixtures(t, db, "w-revoke", 4)

	_, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)

	after := fmt.Sprintf("inst:%d", ids[3])
	got, err := r.Resolve(context.Background(), []string{after})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	for _, tg := range got {
		assert.Equal(t, after, tg.AuthorizationID,
			"降权后只允许看到被授权的那个实例（含其历史 holder），缓存不得保留旧可见范围")
	}
}

// TestResolverReadinessTracksTunnelChanges ③ readiness/隧道变化必须在预期时延内反映。
//
// Readiness 依赖 conn[WorkerUUID]（隧道活跃度），这是易变状态，**绝不入缓存**：
// 即便节点/实例/holder 快照仍在 TTL 内，只要隧道集合变化，readiness 必须立刻跟上。
// 这里用可变隧道实现证明「快照命中期间 readiness 仍实时」。
func TestResolverReadinessTracksTunnelChanges(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-ready", Name: "ready", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-ready", Name: "ready-inst", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)

	connected := &mutableConnected{}
	r := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		connected, nil,
	)
	target := []string{fmt.Sprintf("inst:%d", inst.ID)}

	// 无隧道：不得标 ReadyOnline（节点心跳在线也不行）。
	connected.set(nil)
	first, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, ReadyNotReady, first[0].Readiness)

	// 隧道建立（快照仍在 TTL 内）：必须立刻变为 ReadyOnline。
	connected.set([]string{"w-ready"})
	second, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, ReadyOnline, second[0].Readiness,
		"快照命中期间隧道状态变化必须实时反映，readiness 不得被缓存冻结")

	// 隧道断开：必须立刻回落（不得把 ReadyOnline 缓存住）。
	connected.set(nil)
	third, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, third, 1)
	assert.Equal(t, ReadyNotReady, third[0].Readiness,
		"隧道断开必须立刻反映，禁止缓存出假 ReadyOnline")
}

// TestResolverNodeStatusChangeVisibleAfterTTL 节点状态（快照内）必须在 TTL 后反映。
//
// 节点状态是本设计中唯一被短 TTL 延后的字段，故显式锁定其陈旧上界：
// TTL 内可旧，TTL 后必须新。这也说明为什么 TTL 必须远小于生产 90s 判离线阈值。
func TestResolverNodeStatusChangeVisibleAfterTTL(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-ttl", Name: "ttl", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-ttl", Name: "ttl-inst", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)

	r := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		fakeConnected{ids: []string{"w-ttl"}}, nil,
	)
	r.snapshotTTL = 40 * time.Millisecond

	target := []string{fmt.Sprintf("inst:%d", inst.ID)}
	before, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, ReadyOnline, before[0].Readiness)

	// 节点转为离线（心跳超时由外部扫描写入）。
	require.NoError(t, db.Model(&model.Node{}).Where("id = ?", node.ID).
		Update("status", model.NodeStatusOffline).Error)

	// 快照仍在 TTL 内：允许短暂沿用旧状态（这是换取减负的代价，且 3s ≪ 生产 90s 判离线阈值）。
	within, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, within, 1)

	// TTL 过后必须取到新状态。
	time.Sleep(60 * time.Millisecond)
	after, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, ReadyOffline, after[0].Readiness,
		"TTL 过期后必须回源取到节点已离线")
}

// TestResolverSnapshotFailureNotCached 回源失败不得被缓存成 TTL 窗口内的持续失败。
func TestResolverSnapshotFailureNotCached(t *testing.T) {
	db := setupAssembleDB(t)
	r, ids, _ := seedResolverFixtures(t, db, "w-fail", 2)
	target := []string{fmt.Sprintf("inst:%d", ids[0])}

	_, err := r.Resolve(context.Background(), target)
	require.NoError(t, err)

	// 打掉 nodes 表 ⇒ 下次回源必然报错。
	require.NoError(t, db.Migrator().DropTable(&model.Node{}))
	require.NoError(t, db.Migrator().DropTable(&model.Instance{}))
	require.NoError(t, db.Migrator().DropTable(&model.LogTargetHolder{}))

	// 直接构造新解析器（快照必空）验证失败路径：错误必须上抛，而不是被缓存吞掉。
	fresh := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		fakeConnected{ids: []string{"w-fail"}}, nil,
	)
	_, err = fresh.Resolve(context.Background(), target)
	require.Error(t, err, "节点表不可用时解析必须报错（与缓存引入前一致）")

	// 失败不落缓存：即使刚失败过，仍会再次尝试回源（而不是命中一份「失败快照」）。
	_, err = fresh.Resolve(context.Background(), target)
	require.Error(t, err, "失败不得被缓存为成功或静默空结果")
}

// TestResolverConcurrentResolveIsSingleFlight 并发冷启动只回源一次（单飞），
// 且并发下授权隔离仍然成立（race 检测下运行）。
func TestResolverConcurrentResolveIsSingleFlight(t *testing.T) {
	db := setupAssembleDB(t)
	r, ids, _ := seedResolverFixtures(t, db, "w-conc", 5)

	c := newSQLCounterAll(t, db)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	results := make([][]TargetInfo, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			auth := []string{"*"}
			if i%2 == 1 {
				auth = []string{fmt.Sprintf("inst:%d", ids[1])}
			}
			<-start
			results[i], errs[i] = r.Resolve(context.Background(), auth)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "并发解析第 %d 路不得报错", i)
	}
	// 单飞：8 路并发冷启动共享同一份快照，回源 SQL 数有界（远小于 8×3）。
	assert.LessOrEqual(t, int(c.total.Load()), 9,
		"并发冷启动应由单飞收敛到少量回源，而不是 8 路各查一遍（实测并发共享后 ≤ 9 条）")
	// 关键：并发下授权隔离不破 —— 窄授权路不得出现集合外目标。
	narrowID := fmt.Sprintf("inst:%d", ids[1])
	for i, res := range results {
		if i%2 == 0 {
			continue
		}
		for _, tg := range res {
			assert.Equal(t, narrowID, tg.AuthorizationID,
				"并发窄授权第 %d 路出现集合外目标 %q", i, tg.ID)
		}
	}
}

// TestSlowPathResolvesTargetsOnce Stats/Facets 等慢路径每请求只解析一次目标。
//
// 优化前实测：CreateView 内解析一次 + resolveForQuery 再解析一次 = 2 次（6 条 SQL）；
// stats/facets 恰是生产 p90 最高的两条联邦端点（6.20s / 9.99s）。
func TestSlowPathResolvesTargetsOnce(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-once", Name: "once", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-once", Name: "once-inst", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)

	real := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		fakeConnected{ids: []string{"w-once"}}, nil,
	)
	cr := &countingResolver{inner: real}
	worker := &fakeWorker{workerID: "w-once"}
	dialer := newFakeDialer()
	dialer.clients["w-once"] = worker
	coord := New(cr, dialer)

	q := Query{
		AuthorizedTargetIDs: []string{"*"}, PrincipalKey: "p1", PermissionScope: "s1",
		FromUTC: "2026-10-09T00:00:00Z", ToUTC: "2026-10-09T01:00:00Z",
	}

	c := newSQLCounterAll(t, db)
	_, err := coord.Stats(context.Background(), StatsQuery{Query: q})
	require.NoError(t, err)
	assert.Equal(t, int64(1), cr.count(), "Stats 每次请求只应解析一次目标（优化前为 2 次）")
	statsSQL := c.total.Load()

	c.reset()
	_, err = coord.Facets(context.Background(), FacetsQuery{
		Query: q, Dimensions: []string{"level"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), cr.count(), "Facets 每次请求只应多解析一次（累计 2）")
	assert.LessOrEqual(t, c.total.Load(), statsSQL, "Facets 的目标解析开销不得高于 Stats")

	// createView 仍保持既有对外契约：单独调用只解析一次。
	before := cr.count()
	_, err = coord.CreateView(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, before+1, cr.count(), "CreateView 单独调用应只解析一次")
}

// TestCreateViewTargetIDsMatchFanoutSet view 声明的目标集合与实际扇出集合必须同源。
//
// 优化前 view.TargetIDs 取自解析 #1、扇出集合取自解析 #2，两次快照可能不一致；
// 现在二者共用同一次解析结果，本用例锁定该不变量。
func TestCreateViewTargetIDsMatchFanoutSet(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-same", Name: "same", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-same", Name: "same-inst", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)
	require.NoError(t, db.Create(&model.LogTargetHolder{
		InstanceID: inst.ID, WorkerUUID: "w-old-same", SourceGeneration: "g", StorageNamespace: "ns", Current: false,
	}).Error)

	real := NewProductionTargetResolver(
		service.NewNodeService(db),
		service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool()),
		fakeConnected{ids: []string{"w-same"}}, service.NewLogTargetHolderService(db),
	)
	cr := &countingResolver{inner: real}
	dialer := newFakeDialer()
	dialer.clients["w-same"] = &fakeWorker{workerID: "w-same"}
	coord := New(cr, dialer)

	view, resolved, err := coord.createView(context.Background(), Query{
		AuthorizedTargetIDs: []string{"*"}, PrincipalKey: "p1", PermissionScope: "s1",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), cr.count(), "createView 只解析一次")
	assert.Equal(t, resolved.TargetIDs(), view.TargetIDs,
		"视图目标集合必须与实际扇出集合来自同一次解析")
	assert.Contains(t, view.TargetIDs, fmt.Sprintf("inst:%d", inst.ID))
	assert.Contains(t, view.TargetIDs, "node:"+itoa(node.ID))
}

// countingResolver 记录 Resolve 调用次数（内层为真实解析器，保留完整行为）。
type countingResolver struct {
	inner TargetResolver
	mu    sync.Mutex
	calls int64
}

func (c *countingResolver) Resolve(ctx context.Context, authorized []string) ([]TargetInfo, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Resolve(ctx, authorized)
}

func (c *countingResolver) count() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// mutableConnected 可变的隧道集合（模拟隧道建立/断开），用于验证 readiness 实时性。
type mutableConnected struct {
	mu  sync.Mutex
	ids []string
}

func (m *mutableConnected) ConnectedNodes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ids...)
}

func (m *mutableConnected) set(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ids = ids
}

// readSourceFile 读取本包源文件，用于结构级不变量断言（如「快照类型不得含隧道字段」）。
// 这类断言用源码结构而非运行行为表达，才能在字段被误加时立刻失败。
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "无法定位测试文件路径")
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), name))
	require.NoError(t, err, "读取源文件 %s 失败", name)
	return string(raw)
}

// TestResolverColdSnapshotTTLIsShortEnough 锁定默认 TTL 必须显著小于生产判离线阈值（90s）。
//
// 这是「快照只延后节点状态」这一取舍的安全边界：若将来有人把 TTL 调大，
// 本用例会立刻失败，避免 readiness 长时间失真。
func TestResolverColdSnapshotTTLIsShortEnough(t *testing.T) {
	assert.LessOrEqual(t, resolverSnapshotTTL, 10*time.Second,
		"快照 TTL 必须远小于生产 90s 判离线阈值（service.CheckOfflineNodes）")
	assert.Greater(t, resolverSnapshotTTL, time.Duration(0))

	// 反向断言：readiness 依赖的隧道集合绝不能进入快照类型（结构上不可缓存）。
	src := readSourceFile(t, "assemble.go")
	nodesSnapBlock := src[strings.Index(src, "type nodesSnapshot struct"):strings.Index(src, "// NewProductionTargetResolver")]
	assert.NotContains(t, nodesSnapBlock, "conn",
		"nodesSnapshot 不得含隧道/connected 字段（隧道活跃度必须每次现算）")
	globalBlock := src[strings.Index(src, "type globalSnapshot struct"):strings.Index(src, "// NewProductionTargetResolver")]
	assert.NotContains(t, globalBlock, "conn",
		"globalSnapshot 不得含隧道/connected 字段（隧道活跃度必须每次现算）")
}
