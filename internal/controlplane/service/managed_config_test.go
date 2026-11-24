package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 托管配置三层合并单测。
//
// 合并是纯函数（mergeManagedLayers），这里只断言输入输出，不涉及 gRPC / 探针。
// 必须覆盖的语义：对象递归合并、标量覆盖、**数组整体替换**、**显式 null 删键**、**逐键来源**。

// newManagedTestDB 建一个只含托管配置与实例/节点表的临时库。
func newManagedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 共享内存库在最后一个连接关闭后才释放；不关会占句柄导致后续用例迁移等锁。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.ManagedConfigLayer{},
	))
	return db
}

// mustParseLayer 解析一段 JSON 为点分键增量（测试辅助）。
func mustParseLayer(t *testing.T, raw string) map[string]any {
	t.Helper()
	doc, err := parseManagedConfigObject(raw)
	require.NoError(t, err)
	return doc
}

// buildChain 按 global → node → instance 顺序构造层链。
func buildChain(t *testing.T, global, node, instance string) []layerPayload {
	t.Helper()
	out := []layerPayload{
		{layer: model.ManagedConfigLayer{
			Scope: model.ManagedScopeGlobal, ScopeRefID: 0, ScopeName: "", Version: 3,
		}, doc: mustParseLayer(t, global)},
		{layer: model.ManagedConfigLayer{
			Scope: model.ManagedScopeNode, ScopeRefID: 7, ScopeName: "node-a", Version: 2,
		}, doc: mustParseLayer(t, node)},
		{layer: model.ManagedConfigLayer{
			Scope: model.ManagedScopeInstance, ScopeRefID: 42, ScopeName: "srv-42", Version: 5,
		}, doc: mustParseLayer(t, instance)},
	}
	return out
}

// TestMergeManagedLayers_ScalarOverrideAndObjectRecursion 验证标量覆盖与对象递归合并。
func TestMergeManagedLayers_ScalarOverrideAndObjectRecursion(t *testing.T) {
	res, err := mergeManagedLayers(buildChain(t,
		`{"collect-period-ticks": 20, "aggregation": {"window": 60, "other": 1}, "debug": false}`,
		`{"collect-period-ticks": 40, "aggregation": {"window": 120}}`,
		``,
	))
	require.NoError(t, err)

	// 标量：节点层覆盖全局层。
	assert.Equal(t, int64(40), res.Values["collect-period-ticks"])
	// 对象递归：节点层只覆盖 window，other 仍来自全局层。
	assert.Equal(t, int64(120), res.Values["aggregation.window"])
	assert.Equal(t, int64(1), res.Values["aggregation.other"])
	// 未被任何高层触碰的键保持全局值。
	assert.Equal(t, false, res.Values["debug"])
	assert.Empty(t, res.Removals)
	assert.Empty(t, res.ForbiddenKeys)
}

// TestMergeManagedLayers_ArrayReplacedWhole 验证数组整体替换（不是元素级合并）。
//
// 这条最容易实现错：把 [a,b,c] 与 [x] 合并成 [a,b,c,x] 或 [x,b,c] 都不是「配置里写的」。
func TestMergeManagedLayers_ArrayReplacedWhole(t *testing.T) {
	res, err := mergeManagedLayers(buildChain(t,
		`{"world.entity-types": ["zombie", "skeleton", "creeper"]}`,
		``,
		`{"world.entity-types": ["zombie"]}`,
	))
	require.NoError(t, err)

	got, ok := res.Values["world.entity-types"].([]any)
	require.True(t, ok, "数组键应保持数组形态")
	require.Len(t, got, 1, "数组必须整体替换，不能元素级合并")
	assert.Equal(t, "zombie", got[0])
}

// TestMergeManagedLayers_NullDeletesKey 验证显式 null 删键（并进 Removals）。
func TestMergeManagedLayers_NullDeletesKey(t *testing.T) {
	res, err := mergeManagedLayers(buildChain(t,
		`{"server-name": "pool", "debug": true}`,
		`{"server-name": "region-1"}`,
		`{"server-name": null}`,
	))
	require.NoError(t, err)

	// 实例层把它删了：既不在 Values 里，也要在 Removals 里显式声明。
	_, exists := res.Values["server-name"]
	assert.False(t, exists, "被显式 null 删除的键不得出现在 values 里")
	assert.Contains(t, res.Removals, "server-name", "删键必须在 removals 里声明（否则探针侧旧值残留）")

	// 未被删除的键不受影响。
	assert.Equal(t, true, res.Values["debug"])

	// 删除也要留痕：能回答「是谁在哪一版删的」。
	var found *ManagedTracedKey
	for i := range res.Traced {
		if res.Traced[i].Key == "server-name" {
			found = &res.Traced[i]
			break
		}
	}
	require.NotNil(t, found, "被删除的键也必须出现在逐键来源里")
	assert.True(t, found.Deleted)
	assert.Equal(t, model.ManagedScopeInstance, found.Scope)
	assert.Equal(t, 5, found.Version)
	assert.Nil(t, found.Value)
}

// TestMergeManagedLayers_TracedSourcePerKey 验证逐键来源精确到层与版本。
func TestMergeManagedLayers_TracedSourcePerKey(t *testing.T) {
	res, err := mergeManagedLayers(buildChain(t,
		`{"server-name": "pool", "startup-top-n": 5}`,
		``,
		`{"startup-top-n": 10}`,
	))
	require.NoError(t, err)

	sources := map[string]ManagedTracedKey{}
	for _, entry := range res.Traced {
		sources[entry.Key] = entry
	}

	require.Contains(t, sources, "server-name")
	assert.Equal(t, model.ManagedScopeGlobal, sources["server-name"].Scope)
	assert.Equal(t, uint(0), sources["server-name"].ScopeRefID)
	assert.Equal(t, 3, sources["server-name"].Version, "server-name 只由全局层（v3）设定")
	assert.False(t, sources["server-name"].Deleted)

	require.Contains(t, sources, "startup-top-n")
	assert.Equal(t, model.ManagedScopeInstance, sources["startup-top-n"].Scope)
	assert.Equal(t, uint(42), sources["startup-top-n"].ScopeRefID)
	assert.Equal(t, "srv-42", sources["startup-top-n"].ScopeName)
	assert.Equal(t, 5, sources["startup-top-n"].Version, "startup-top-n 最终由实例层（v5）覆盖")
}

// TestMergeManagedLayers_ForbiddenAndGuarded 验证上游分级表被真正用上（而不是自己写一套）。
func TestMergeManagedLayers_ForbiddenAndGuarded(t *testing.T) {
	res, err := mergeManagedLayers(buildChain(t,
		`{"collect-period-ticks": 20, "debug": true, "web.port": 19999}`,
		``,
		``,
	))
	require.NoError(t, err)

	// 禁档键被上游 IsForbiddenConfigPath 命中（web.* 属监听面/凭证）。
	assert.Contains(t, res.ForbiddenKeys, "web.port")
	// GUARDED 档键被报出来（影响预估用）。
	assert.Contains(t, res.GuardedKeys, "collect-period-ticks")
	// SAFE 档键不该进 GUARDED 集合。
	assert.NotContains(t, res.GuardedKeys, "debug")

	// 档位名用的是上游 ManagedConfigTier 的中文名，没有另起一套文案。
	sources := map[string]ManagedTracedKey{}
	for _, entry := range res.Traced {
		sources[entry.Key] = entry
	}
	assert.Equal(t, "禁止", sources["web.port"].Tier)
	assert.True(t, sources["web.port"].TierForbid)
	assert.Equal(t, "危险", sources["collect-period-ticks"].Tier)
	assert.Equal(t, "安全", sources["debug"].Tier)
}

// TestMergeManagedLayers_ParentChildConflict 验证「同一个键不能既是叶子又有子键」。
func TestMergeManagedLayers_ParentChildConflict(t *testing.T) {
	// 低层写标量 alert，高层写 alert.enabled：高层胜出，低层的标量必须消失。
	res, err := mergeManagedLayers(buildChain(t,
		`{"alert": false}`,
		`{"alert.enabled": true}`,
		``,
	))
	require.NoError(t, err)
	_, exists := res.Values["alert"]
	assert.False(t, exists, "子键存在时，同名的标量祖先必须被清掉")
	assert.Equal(t, true, res.Values["alert.enabled"])

	// 反向：低层写子键，高层写标量，子键必须被清掉。
	res2, err := mergeManagedLayers(buildChain(t,
		`{"alert.enabled": true}`,
		``,
		`{"alert": false}`,
	))
	require.NoError(t, err)
	_, childExists := res2.Values["alert.enabled"]
	assert.False(t, childExists, "标量覆盖时子键必须被清掉")
	assert.Equal(t, false, res2.Values["alert"])
}

// TestSaveLayer_RejectsForbiddenKey 验证保存前的预检走的是上游分级表。
func TestSaveLayer_RejectsForbiddenKey(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	_, err := svc.SaveLayer(ManagedLayerInput{
		Scope:  model.ManagedScopeGlobal,
		Config: `{"web.port": 19999}`,
	}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedConfigForbidden)
	// 错误文案必须点名具体的键（探针侧只会笼统地说「整包拒绝」）。
	assert.Contains(t, err.Error(), "web.port")

	// 未登记档位的键同样拒（fail-close），不是放行。
	_, err = svc.SaveLayer(ManagedLayerInput{
		Scope:  model.ManagedScopeGlobal,
		Config: `{"totally-unknown-key": 1}`,
	}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedConfigForbidden)
	assert.Contains(t, err.Error(), "totally-unknown-key")
}

// TestSaveLayer_ForbiddenRemoval 验证「删一个禁档键」与「写它」同属越界。
func TestSaveLayer_ForbiddenRemoval(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	_, err := svc.SaveLayer(ManagedLayerInput{
		Scope:  model.ManagedScopeGlobal,
		Config: `{"mcp.enabled": null}`,
	}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedConfigForbidden)
}

// TestSaveLayer_RejectsNonObject 验证根节点不是对象时拒绝。
func TestSaveLayer_RejectsNonObject(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	for _, raw := range []string{`[1,2,3]`, `"text"`, `123`, `{bad json`} {
		_, err := svc.SaveLayer(ManagedLayerInput{Scope: model.ManagedScopeGlobal, Config: raw}, 1)
		require.Error(t, err, "输入 %q 应被拒绝", raw)
	}
}

// TestSaveLayer_VersionBumpAndConflict 验证版本自增与乐观并发。
func TestSaveLayer_VersionBumpAndConflict(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	first, err := svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeGlobal, Config: `{"server-name": "pool"}`,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, first.Version)

	second, err := svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeGlobal, Config: `{"server-name": "pool-2"}`, ExpectedVersion: 1,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)

	// 基于过期版本保存必须被拒（否则会静默覆盖别人的修改）。
	_, err = svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeGlobal, Config: `{"server-name": "pool-3"}`, ExpectedVersion: 1,
	}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedConfigConflict)

	// 层不存在时期望版本必须为 0。
	_, err = svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeNode, ScopeRefID: 1, Config: `{}`, ExpectedVersion: 3,
	}, 1)
	require.Error(t, err)
}

// TestSaveLayer_ScopeRefValidation 验证作用域与引用的搭配校验。
func TestSaveLayer_ScopeRefValidation(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)
	node := model.Node{Name: "n1", UUID: "node-uuid-1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-uuid-1", NodeID: node.ID, Name: "srv-1",
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDirect, Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(&inst).Error)

	// global 不能带 scopeRefId。
	_, err := svc.SaveLayer(ManagedLayerInput{Scope: model.ManagedScopeGlobal, ScopeRefID: 9, Config: `{}`}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedScopeRefInvalid)

	// node/instance 必须带 scopeRefId。
	_, err = svc.SaveLayer(ManagedLayerInput{Scope: model.ManagedScopeNode, Config: `{}`}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedScopeRefInvalid)

	// 引用不存在时明确报错（而不是建一条指向空气的层）。
	_, err = svc.SaveLayer(ManagedLayerInput{Scope: model.ManagedScopeNode, ScopeRefID: 999, Config: `{}`}, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManagedConfigScopeRefNotFound)

	// 合法引用可以保存，并回填可读名。
	saved, err := svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeNode, ScopeRefID: node.ID, Config: `{"collect-period-ticks": 30}`,
	}, 1)
	require.NoError(t, err)
	assert.Equal(t, "n1", saved.ScopeName)
}

// TestMergeForInstance_ThreeLayersFromDB 验证从库里取三层并按优先级合并。
func TestMergeForInstance_ThreeLayersFromDB(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	node := model.Node{Name: "n1", UUID: "node-uuid-1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{
		UUID: "inst-uuid-1", NodeID: node.ID, Name: "srv-1",
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDirect, Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(&inst).Error)
	// 另一节点上的实例：不该被节点层或实例层影响。
	otherNode := model.Node{Name: "n2", UUID: "node-uuid-2", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&otherNode).Error)
	other := model.Instance{
		UUID: "inst-uuid-2", NodeID: otherNode.ID, Name: "srv-2",
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDirect, Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(&other).Error)

	_, err := svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeGlobal, Config: `{"collect-period-ticks": 20, "server-name": "pool"}`,
	}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeNode, ScopeRefID: node.ID, Config: `{"collect-period-ticks": 10}`,
	}, 1)
	require.NoError(t, err)
	_, err = svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeInstance, ScopeRefID: inst.ID, Config: `{"server-name": "srv-1"}`,
	}, 1)
	require.NoError(t, err)

	res, err := svc.MergeForInstance(inst)
	require.NoError(t, err)
	assert.Equal(t, int64(10), res.Values["collect-period-ticks"], "节点层覆盖全局层")
	assert.Equal(t, "srv-1", res.Values["server-name"], "实例层覆盖全局层")

	// 另一台实例只受全局层影响：节点/实例层的键不能泄漏过去。
	resOther, err := svc.MergeForInstance(other)
	require.NoError(t, err)
	assert.Equal(t, int64(20), resOther.Values["collect-period-ticks"])
	assert.Equal(t, "pool", resOther.Values["server-name"])
}

// TestEstimateImpact_MetricMultiplier 验证指标量倍率与采集周期下限。
func TestEstimateImpact_MetricMultiplier(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	node := model.Node{Name: "n1", UUID: "node-uuid-1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	for i := 0; i < 3; i++ {
		inst := model.Instance{
			UUID: "inst-" + string(rune('a'+i)), NodeID: node.ID, Name: "srv",
			Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
			ProcessType: model.ProcessTypeDirect, Status: model.InstanceStatusRunning,
		}
		require.NoError(t, db.Create(&inst).Error)
	}
	// 一台不适用的（代理）：计入总数但不计入受影响数。
	proxy := model.Instance{
		UUID: "inst-proxy", NodeID: node.ID, Name: "proxy",
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleProxy,
		ProcessType: model.ProcessTypeDirect, Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(&proxy).Error)

	cases := []struct {
		name       string
		values     map[string]any
		wantTicks  int
		wantFactor float64
	}{
		{"默认周期", map[string]any{}, 100, 1},
		{"周期提速十倍→十倍", map[string]any{"collect-period-ticks": 10}, 10, 10},
		{"周期降速四成→二点五倍", map[string]any{"collect-period-ticks": 40}, 40, 2.5},
		{"零按默认（不除零）", map[string]any{"collect-period-ticks": 0}, 100, 1},
		{"负数按默认", map[string]any{"collect-period-ticks": -5}, 100, 1},
		{"字符串形式可识别", map[string]any{"collect-period-ticks": "5"}, 5, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			impact, err := svc.EstimateImpact(model.ManagedScopeNode, node.ID, tc.values, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTicks, impact.EffectiveCollectPeriodTicks)
			assert.Equal(t, tc.wantFactor, impact.MetricMultiplier)
			assert.Equal(t, 4, impact.TotalInstances)
			assert.Equal(t, 3, impact.AffectedInstances, "代理不适用探针，不计入受影响数")
		})
	}

	// 极激进周期必须被下限钳住，不能出现 Inf/NaN。
	impact, err := svc.EstimateImpact(model.ManagedScopeNode, node.ID, map[string]any{"collect-period-ticks": 0.001}, nil)
	require.NoError(t, err)
	assert.Equal(t, minCollectPeriodTicks, impact.EffectiveCollectPeriodTicks)
	assert.Equal(t, float64(defaultCollectPeriodTicks), impact.MetricMultiplier, "下限 1 tick → 倍率封顶 100")
}

// TestEstimateImpact_ForbiddenKeyReported 验证影响预估会把越界键报出来。
func TestEstimateImpact_ForbiddenKeyReported(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	impact, err := svc.EstimateImpact(model.ManagedScopeGlobal, 0, map[string]any{"metrics.port": 9090}, nil)
	require.NoError(t, err)
	assert.Contains(t, impact.ForbiddenKeys, "metrics.port")
	assert.NotEmpty(t, impact.Notes)
}

// TestEstimateImpact_AccessibleFilter 验证预演只统计调用方可见的实例：
// 计数与代表实例的合并周期都必须来自可见集合，空集合则不能泄露任何信息。
func TestEstimateImpact_AccessibleFilter(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	node := model.Node{Name: "n1", UUID: "node-uuid-1", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	mkInst := func(uuid string, ticks int) model.Instance {
		inst := model.Instance{
			UUID: uuid, NodeID: node.ID, Name: "srv-" + uuid,
			Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
			ProcessType: model.ProcessTypeDirect, Status: model.InstanceStatusRunning,
		}
		require.NoError(t, db.Create(&inst).Error)
		require.NoError(t, db.Create(&model.ManagedConfigLayer{
			Scope: model.ManagedScopeInstance, ScopeRefID: inst.ID, ScopeName: inst.Name,
			Config:  fmt.Sprintf(`{"collect-period-ticks": %d}`, ticks),
			Version: 1,
		}).Error)
		return inst
	}
	mkInst("a", 50)
	b := mkInst("b", 25)

	// 不受限：两台都算，代表实例取第一台（a，周期 50）。
	impact, err := svc.EstimateImpact(model.ManagedScopeNode, node.ID, map[string]any{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, impact.TotalInstances)
	assert.Equal(t, 2, impact.AffectedInstances)
	assert.Equal(t, 50, impact.EffectiveCollectPeriodTicks)

	// 只见 b：计数收敛到 1，代表周期也必须取 b 的 25，而不是借 a 的 50。
	impact, err = svc.EstimateImpact(model.ManagedScopeNode, node.ID, map[string]any{}, []uint{b.ID})
	require.NoError(t, err)
	assert.Equal(t, 1, impact.TotalInstances)
	assert.Equal(t, 1, impact.AffectedInstances)
	assert.Equal(t, 25, impact.EffectiveCollectPeriodTicks)

	// 空集合（一个都不可见）：计数为 0，周期回落默认，不透露任何实例信息。
	impact, err = svc.EstimateImpact(model.ManagedScopeNode, node.ID, map[string]any{}, []uint{})
	require.NoError(t, err)
	assert.Equal(t, 0, impact.TotalInstances)
	assert.Equal(t, defaultCollectPeriodTicks, impact.EffectiveCollectPeriodTicks)
}

// TestFlattenManagedConfig 验证嵌套对象展平为点分键、空对象不产出键。
func TestFlattenManagedConfig(t *testing.T) {
	doc, err := parseManagedConfigObject(`{
		"collect-period-ticks": 20,
		"aggregation": {"window": 60, "empty": {}},
		"world": {"entity-types": ["zombie"]}
	}`)
	require.NoError(t, err)

	assert.Equal(t, int64(20), doc["collect-period-ticks"])
	assert.Equal(t, int64(60), doc["aggregation.window"])
	assert.Equal(t, []any{"zombie"}, doc["world.entity-types"])
	_, hasEmpty := doc["aggregation.empty"]
	assert.False(t, hasEmpty, "空对象不产出可下发键")
	_, hasMid := doc["aggregation"]
	assert.False(t, hasMid, "中间层对象不单独成键（它的子键才是可下发项）")
}

// TestSaveLayer_KeepsExplicitNull 验证显式 null 被原样保留（它不是「没有值」）。
func TestSaveLayer_KeepsExplicitNull(t *testing.T) {
	db := newManagedTestDB(t)
	svc := NewManagedConfigService(db)

	saved, err := svc.SaveLayer(ManagedLayerInput{
		Scope: model.ManagedScopeGlobal, Config: `{"server-name": null, "debug": true}`,
	}, 1)
	require.NoError(t, err)

	var stored map[string]any
	require.NoError(t, json.Unmarshal([]byte(saved.Config), &stored))
	value, exists := stored["server-name"]
	assert.True(t, exists, "显式 null 必须落库，不能当成缺省丢弃")
	assert.Nil(t, value)
}

// TestParseManagedConfigObject_AcceptsYAML 验证服务端能吃下 YAML。
//
// 前端「高级编辑」产出的是带注释的 YAML（托管配置本身就是探针 config.yml 的增量，
// 运维日常读写的也是 YAML）。若只认 JSON，界面写着「改 YAML」、后端却回
// `invalid character 'c'`，用户在两端之间无法自洽——真机点击保存当场暴露过这个缺口。
func TestParseManagedConfigObject_AcceptsYAML(t *testing.T) {
	const text = `
# 指标采集周期（危险档）
# 多久采集一次指标。
collect-period-ticks: 40
aggregation:
  window: 60
world:
  entity-types:
    - zombie
    - skeleton
debug: true
server-name: "1s"
`
	doc, err := parseManagedConfigObject(text)
	require.NoError(t, err)

	assert.Equal(t, int64(40), doc["collect-period-ticks"])
	assert.Equal(t, int64(60), doc["aggregation.window"])
	assert.Equal(t, []any{"zombie", "skeleton"}, doc["world.entity-types"])
	assert.Equal(t, true, doc["debug"])
	// 加引号的 "1s" 必须仍是字符串：不加引号时 YAML 会把它当成数字/时间量。
	assert.Equal(t, "1s", doc["server-name"])
}

// TestParseManagedConfigObject_AcceptsJSONStill 确认 YAML 解析没有破坏既有 JSON 形态。
func TestParseManagedConfigObject_AcceptsJSONStill(t *testing.T) {
	doc, err := parseManagedConfigObject(`{"collect-period-ticks": 20, "debug": false}`)
	require.NoError(t, err)
	assert.Equal(t, int64(20), doc["collect-period-ticks"])
	assert.Equal(t, false, doc["debug"])
}

// TestParseManagedConfigObject_RejectsNonObject 确认两种格式下的根节点校验一致。
func TestParseManagedConfigObject_RejectsNonObject(t *testing.T) {
	for _, text := range []string{"- a\n- b\n", "[1, 2]", "42"} {
		_, err := parseManagedConfigObject(text)
		require.Error(t, err, "根节点不是对象时必须拒绝：%q", text)
		assert.ErrorIs(t, err, ErrManagedConfigInvalid)
	}
}

// TestParseManagedConfigObject_ExplicitNullFromYAML 确认 YAML 的 null 也是「删键」指令。
func TestParseManagedConfigObject_ExplicitNullFromYAML(t *testing.T) {
	doc, err := parseManagedConfigObject("server-name: null\ndebug: true\n")
	require.NoError(t, err)
	value, exists := doc["server-name"]
	assert.True(t, exists, "显式 null 必须保留为删键指令")
	assert.Nil(t, value)
	assert.Equal(t, true, doc["debug"])
}
