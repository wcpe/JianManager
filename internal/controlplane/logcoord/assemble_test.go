package logcoord

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

type fakeConnected struct{ ids []string }

func (f fakeConnected) ConnectedNodes() []string { return f.ids }

// P1：inst:<id> 必须解析到所属 Worker UUID。
func TestProductionTargetResolver_InstanceToWorker(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "worker-uuid-1", Name: "n1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-uuid-1", Name: "srv", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)

	nodeSvc := service.NewNodeService(db)
	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	r := NewProductionTargetResolver(nodeSvc, instSvc, fakeConnected{ids: []string{"worker-uuid-1"}}, nil)

	targets, err := r.Resolve(context.Background(), []string{"inst:" + itoa(inst.ID)})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "inst:"+itoa(inst.ID), targets[0].ID)
	assert.Equal(t, "worker-uuid-1", targets[0].WorkerID)
	assert.Equal(t, ReadyOnline, targets[0].Readiness)
}

// P1：无活跃隧道/节点离线不得标 ReadyOnline。
func TestProductionTargetResolver_OfflineNotReadyOnline(t *testing.T) {
	db := setupAssembleDB(t)
	on := model.Node{UUID: "w-on", Name: "on", Status: model.NodeStatusOnline}
	off := model.Node{UUID: "w-off", Name: "off", Status: model.NodeStatusOffline}
	// 在线但无隧道
	nolink := model.Node{UUID: "w-nolink", Name: "nolink", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&on).Error)
	require.NoError(t, db.Create(&off).Error)
	require.NoError(t, db.Create(&nolink).Error)

	r := NewProductionTargetResolver(service.NewNodeService(db), nil, fakeConnected{ids: []string{"w-on"}}, nil)
	targets, err := r.Resolve(context.Background(), []string{
		"node:" + itoa(on.ID),
		"node:" + itoa(off.ID),
		"node:" + itoa(nolink.ID),
	})
	require.NoError(t, err)
	by := map[string]Readiness{}
	for _, tg := range targets {
		by[tg.ID] = tg.Readiness
	}
	assert.Equal(t, ReadyOnline, by["node:"+itoa(on.ID)])
	assert.Equal(t, ReadyOffline, by["node:"+itoa(off.ID)])
	assert.Equal(t, ReadyNotReady, by["node:"+itoa(nolink.ID)], "online heartbeat without tunnel must not be ReadyOnline")
}

func TestProductionTargetResolver_AllIncludesNodeWithoutInstances(t *testing.T) {
	db := setupAssembleDB(t)
	n := model.Node{UUID: "worker-node-only", Name: "node-only", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&n).Error)
	r := NewProductionTargetResolver(service.NewNodeService(db), service.NewInstanceService(db, nil, nil), fakeConnected{ids: []string{"worker-node-only"}}, nil)
	targets, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "node:"+itoa(n.ID), targets[0].ID)
}

// TestProductionTargetResolver_BatchesHolderQueries 平台级解析必须批量取 holder。
//
// 回归目标（2026-10-09 生产实证）：原实现对每个实例各发一条 holder 查询，
// 25 个实例 ⇒ 25 条串行 SQL；CP 的 SQLite 连接池被刻意收敛为单连接，
// 这些查询全部排队并与其它请求互相阻塞（生产 SLOW SQL：同形态 holder 查询 p90≈2.7s、
// max≈10s）。本用例以「SQL 语句计数」为断言口径，直接证明 N 条降为 1 条。
func TestProductionTargetResolver_BatchesHolderQueries(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-multi", Name: "multi", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)

	const instCount = 12
	instIDs := make([]uint, 0, instCount)
	for i := 0; i < instCount; i++ {
		inst := model.Instance{
			UUID: "inst-batch-" + itoa(uint(i)), Name: "srv-" + itoa(uint(i)), NodeID: node.ID,
			Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
		}
		require.NoError(t, db.Create(&inst).Error)
		instIDs = append(instIDs, inst.ID)
	}
	holders := service.NewLogTargetHolderService(db)
	// 每实例一条历史 holder（不得被静默丢弃：必须仍出现在解析结果中）。
	for i, id := range instIDs {
		require.NoError(t, db.Create(&model.LogTargetHolder{
			InstanceID: id, WorkerUUID: "w-old-" + itoa(uint(i)),
			SourceGeneration: "gen-" + itoa(uint(i)), StorageNamespace: "ns-" + itoa(uint(i)),
			Current: false,
		}).Error)
	}

	counter := newSQLCounter()
	counter.attach(t, db)
	defer counter.detach(db)

	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	r := NewProductionTargetResolver(service.NewNodeService(db), instSvc, fakeConnected{ids: []string{"w-multi"}}, holders)

	targets, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err)

	holderQueries := counter.countHolderQueries()
	assert.Equal(t, 1, holderQueries,
		"全部实例的 holder 必须由 1 条 SQL 覆盖（批量前为 %d 条，即每实例一条）", instCount)

	// 覆盖完整性不变：每个实例的实例目标 + 历史 holder 目标都必须在结果里。
	got := map[string]bool{}
	for _, tg := range targets {
		got[tg.ID] = true
	}
	for i, id := range instIDs {
		assert.True(t, got["inst:"+itoa(id)], "实例目标 inst:%d 不得丢失", id)
		assert.True(t, got["holder:"+itoa(uint(i+1))], "历史 holder 目标不得丢失（实例 %d）", id)
	}
	// 历史 holder 的 readiness 按隧道活跃度判定，不得无条件 ReadyOnline。
	for _, tg := range targets {
		if tg.HistoricalHolder {
			assert.NotEqual(t, ReadyOnline, tg.Readiness,
				"隧道未连接的旧 holder 不得标 ReadyOnline（conn 只含 w-multi）")
		}
	}
}

// TestProductionTargetResolver_MultiInstanceTargetsBatchHolders 显式多 inst: 目标同样批量取 holder。
func TestProductionTargetResolver_MultiInstanceTargetsBatchHolders(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-explicit", Name: "explicit", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	const instCount = 6
	authorized := make([]string, 0, instCount)
	for i := 0; i < instCount; i++ {
		inst := model.Instance{
			UUID: "inst-exp-" + itoa(uint(i)), Name: "exp-" + itoa(uint(i)), NodeID: node.ID,
			Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
		}
		require.NoError(t, db.Create(&inst).Error)
		authorized = append(authorized, "inst:"+itoa(inst.ID))
	}
	holders := service.NewLogTargetHolderService(db)

	counter := newSQLCounter()
	counter.attach(t, db)
	defer counter.detach(db)

	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	r := NewProductionTargetResolver(service.NewNodeService(db), instSvc, fakeConnected{ids: []string{"w-explicit"}}, holders)
	targets, err := r.Resolve(context.Background(), authorized)
	require.NoError(t, err)
	require.Len(t, targets, instCount)
	assert.Equal(t, 1, counter.countHolderQueries(), "多个 inst: 目标应共用一次批量 holder 查询")
}

// TestLogTargetHolderService_ListForInstancesMatchesSingle 批量与逐个查询必须逐行等价。
func TestLogTargetHolderService_ListForInstancesMatchesSingle(t *testing.T) {
	db := setupAssembleDB(t)
	holders := service.NewLogTargetHolderService(db)
	node := model.Node{UUID: "w-eq", Name: "eq", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	const instCount = 4
	ids := make([]uint, 0, instCount)
	for i := 0; i < instCount; i++ {
		inst := model.Instance{
			UUID: "inst-eq-" + itoa(uint(i)), Name: "eq-" + itoa(uint(i)), NodeID: node.ID,
			Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
		}
		require.NoError(t, db.Create(&inst).Error)
		ids = append(ids, inst.ID)
	}
	// 每实例两条：一条 current、一条历史（顺序约定 current DESC, id ASC）。
	for i, id := range ids {
		require.NoError(t, db.Create(&model.LogTargetHolder{
			InstanceID: id, WorkerUUID: "w-hist-" + itoa(uint(i)),
			SourceGeneration: "g-h-" + itoa(uint(i)), StorageNamespace: "ns-h-" + itoa(uint(i)), Current: false,
		}).Error)
		require.NoError(t, db.Create(&model.LogTargetHolder{
			InstanceID: id, WorkerUUID: "w-cur-" + itoa(uint(i)),
			SourceGeneration: "g-c-" + itoa(uint(i)), StorageNamespace: "ns-c-" + itoa(uint(i)), Current: true,
		}).Error)
	}

	batched, err := holders.ListForInstances(ids)
	require.NoError(t, err)
	require.Len(t, batched, instCount)
	for _, id := range ids {
		single, err := holders.ListForInstance(id)
		require.NoError(t, err)
		require.Len(t, batched[id], len(single), "实例 %d 行数必须一致", id)
		for i := range single {
			assert.Equal(t, single[i].ID, batched[id][i].ID,
				"实例 %d 第 %d 行顺序必须与 ListForInstance 一致", id, i)
		}
	}

	// 未知实例：批量返回空分组，不得凭空造行。
	unknown, err := holders.ListForInstances([]uint{999999})
	require.NoError(t, err)
	assert.Empty(t, unknown[999999])
	// 空输入不查询、不报错。
	empty, err := holders.ListForInstances(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestProductionTargetResolver_BatchHolderFailureFallsBack 批量查询失败不得把整次解析打成失败。
//
// 语义不变式（与批量优化前逐字一致）：holder 元数据取不到只是该实例不产出 holder 目标，
// 实例目标本身照常产出、解析照常成功——绝不能因此让日志页 500。
func TestProductionTargetResolver_BatchHolderFailureFallsBack(t *testing.T) {
	db := setupAssembleDB(t)
	node := model.Node{UUID: "w-fallback", Name: "fb", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-fb-1", Name: "fb-1", NodeID: node.ID,
		Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "true",
	}
	require.NoError(t, db.Create(&inst).Error)
	require.NoError(t, db.Create(&model.LogTargetHolder{
		InstanceID: inst.ID, WorkerUUID: "w-old", SourceGeneration: "g", StorageNamespace: "ns", Current: false,
	}).Error)

	// 删除 holder 表：批量查询与逐实例回落都会报错。
	require.NoError(t, db.Migrator().DropTable(&model.LogTargetHolder{}))

	holders := service.NewLogTargetHolderService(db)
	instSvc := service.NewInstanceService(db, service.NewGroupService(db), cpgrpc.NewClientPool())
	r := NewProductionTargetResolver(service.NewNodeService(db), instSvc, fakeConnected{ids: []string{"w-fallback"}}, holders)

	targets, err := r.Resolve(context.Background(), []string{"*"})
	require.NoError(t, err, "holder 元数据不可用不得让整次解析失败")
	got := map[string]bool{}
	for _, tg := range targets {
		got[tg.ID] = true
	}
	assert.True(t, got["inst:"+itoa(inst.ID)], "实例目标必须照常产出")
}

// sqlCounter 统计 holder 表查询次数，把「N 条降为 1 条」变成可断言的事实。
type sqlCounter struct {
	holderQueries int
	name          string
}

func newSQLCounter() *sqlCounter { return &sqlCounter{} }

// attach 注册 GORM 查询回调（After 阶段取最终 SQL），并把计数器挂到该 DB 实例，
// 避免用例间共享状态。
func (c *sqlCounter) attach(t *testing.T, db *gorm.DB) {
	t.Helper()
	c.name = "sql_counter_" + t.Name()
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(c.name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "log_target_holders") {
			c.holderQueries++
		}
	}))
}

func (c *sqlCounter) detach(db *gorm.DB) {
	if c.name != "" {
		_ = db.Callback().Query().Remove(c.name)
	}
}

func (c *sqlCounter) countHolderQueries() int { return c.holderQueries }

func setupAssembleDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.LogTargetHolder{}))
	return db
}

func itoa(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
