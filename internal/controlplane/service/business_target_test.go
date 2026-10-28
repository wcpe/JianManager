package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newBatchTestDB 构造批量测试库（实例 + 节点 + 分组树 + 用户组关联）。
func newBatchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{},
		&model.InstanceGroupNode{}, &model.InstanceGroupMember{},
	))
	return db
}

// mkGroup 建分组节点（parentID 为 nil 即根）。
func mkGroup(t *testing.T, db *gorm.DB, name string, parentID *uint) *model.InstanceGroupNode {
	t.Helper()
	n := &model.InstanceGroupNode{Name: name, ParentID: parentID}
	require.NoError(t, db.Create(n).Error)
	return n
}

// mkInstance 建实例（状态默认 RUNNING）。
func mkBatchInstance(t *testing.T, db *gorm.DB, name string, nodeID uint, status model.InstanceStatus) *model.Instance {
	t.Helper()
	inst := &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: status,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

// linkGroup 把实例加入分组。
func linkGroup(t *testing.T, db *gorm.DB, groupID, instanceID uint) {
	t.Helper()
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: groupID, InstanceID: instanceID}).Error)
}

// TestBusinessTarget_Resolve_SingleGroupNoDescendants 单选分组 + includeDescendants=false 只含本组实例。
func TestBusinessTarget_Resolve_SingleGroupNoDescendants(t *testing.T) {
	db := newBatchTestDB(t)
	root := mkGroup(t, db, "cluster", nil)
	region := mkGroup(t, db, "region1", &root.ID)
	instRoot := mkBatchInstance(t, db, "a", 1, model.InstanceStatusRunning)
	instRegion := mkBatchInstance(t, db, "b", 1, model.InstanceStatusRunning)
	linkGroup(t, db, root.ID, instRoot.ID)
	linkGroup(t, db, region.ID, instRegion.ID)

	r := NewBusinessTargetResolver(db)
	got, err := r.Resolve(BusinessTarget{GroupIDs: []uint{root.ID}, IncludeDescendants: false})
	require.NoError(t, err)
	assert.Equal(t, []uint{instRoot.ID}, got.InstanceIDs, "includeDescendants=false 不应包含子分组实例")
	assert.Equal(t, 1, got.Count())
}

// TestBusinessTarget_Resolve_IncludeDescendants 含后代时递归展开整棵子树。
func TestBusinessTarget_Resolve_IncludeDescendants(t *testing.T) {
	db := newBatchTestDB(t)
	root := mkGroup(t, db, "cluster", nil)
	region := mkGroup(t, db, "region1", &root.ID)
	zone := mkGroup(t, db, "zone1", &region.ID)
	instRoot := mkBatchInstance(t, db, "a", 1, model.InstanceStatusRunning)
	instRegion := mkBatchInstance(t, db, "b", 1, model.InstanceStatusRunning)
	instZone := mkBatchInstance(t, db, "c", 1, model.InstanceStatusRunning)
	linkGroup(t, db, root.ID, instRoot.ID)
	linkGroup(t, db, region.ID, instRegion.ID)
	linkGroup(t, db, zone.ID, instZone.ID)

	r := NewBusinessTargetResolver(db)
	got, err := r.Resolve(BusinessTarget{GroupIDs: []uint{root.ID}, IncludeDescendants: true})
	require.NoError(t, err)
	assert.Equal(t, []uint{instRoot.ID, instRegion.ID, instZone.ID}, got.InstanceIDs,
		"含后代时应展开整棵子树并按 ID 升序")
	assert.Len(t, got.Groups, 1)
	assert.Equal(t, "cluster", got.Groups[0].Name)
	assert.Equal(t, 3, got.Groups[0].InstanceCount)
}

// TestBusinessTarget_Resolve_MultiGroupsDedup 多选分组时同一实例只出现一次。
func TestBusinessTarget_Resolve_MultiGroupsDedup(t *testing.T) {
	db := newBatchTestDB(t)
	g1 := mkGroup(t, db, "g1", nil)
	g2 := mkGroup(t, db, "g2", nil)
	inst := mkBatchInstance(t, db, "shared", 1, model.InstanceStatusRunning)
	linkGroup(t, db, g1.ID, inst.ID)
	linkGroup(t, db, g2.ID, inst.ID)

	r := NewBusinessTargetResolver(db)
	got, err := r.Resolve(BusinessTarget{GroupIDs: []uint{g1.ID, g2.ID}})
	require.NoError(t, err)
	assert.Equal(t, []uint{inst.ID}, got.InstanceIDs, "多组命中同一实例应去重")
	assert.Len(t, got.Groups, 2, "两个请求分组都应出现在 groups 里")
}

// TestBusinessTarget_Resolve_IntersectWithInstanceIDs 分组与实例列表同时给出时取交集。
func TestBusinessTarget_Resolve_IntersectWithInstanceIDs(t *testing.T) {
	db := newBatchTestDB(t)
	g := mkGroup(t, db, "g1", nil)
	inGroup := mkBatchInstance(t, db, "in", 1, model.InstanceStatusRunning)
	outGroup := mkBatchInstance(t, db, "out", 1, model.InstanceStatusRunning)
	linkGroup(t, db, g.ID, inGroup.ID)

	r := NewBusinessTargetResolver(db)
	got, err := r.Resolve(BusinessTarget{
		GroupIDs:    []uint{g.ID},
		InstanceIDs: []uint{inGroup.ID, outGroup.ID},
	})
	require.NoError(t, err)
	assert.Equal(t, []uint{inGroup.ID}, got.InstanceIDs, "交集应只剩同时在分组与实例列表里的那个")
}

// TestBusinessTarget_Resolve_InstanceIDsOnly 只给实例列表时不再受分组约束。
func TestBusinessTarget_Resolve_InstanceIDsOnly(t *testing.T) {
	db := newBatchTestDB(t)
	a := mkBatchInstance(t, db, "a", 1, model.InstanceStatusRunning)
	b := mkBatchInstance(t, db, "b", 1, model.InstanceStatusRunning)

	r := NewBusinessTargetResolver(db)
	got, err := r.Resolve(BusinessTarget{InstanceIDs: []uint{b.ID, a.ID, b.ID}})
	require.NoError(t, err)
	assert.Equal(t, []uint{a.ID, b.ID}, got.InstanceIDs, "应去重并按 ID 升序")
}

// TestBusinessTarget_Resolve_Filters 三个 filter 维度分别收敛。
func TestBusinessTarget_Resolve_Filters(t *testing.T) {
	db := newBatchTestDB(t)
	g := mkGroup(t, db, "g1", nil)
	running := mkBatchInstance(t, db, "running", 7, model.InstanceStatusRunning)
	stopped := mkBatchInstance(t, db, "stopped", 7, model.InstanceStatusStopped)
	otherRole := &model.Instance{
		Name: "proxy", NodeID: 7, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(otherRole).Error)
	for _, id := range []uint{running.ID, stopped.ID, otherRole.ID} {
		linkGroup(t, db, g.ID, id)
	}
	r := NewBusinessTargetResolver(db)

	t.Run("status", func(t *testing.T) {
		status := string(model.InstanceStatusRunning)
		got, err := r.Resolve(BusinessTarget{
			GroupIDs: []uint{g.ID},
			Filters:  &BusinessBatchFilter{Status: &status},
		})
		require.NoError(t, err)
		assert.Equal(t, []uint{running.ID, otherRole.ID}, got.InstanceIDs)
	})
	t.Run("role", func(t *testing.T) {
		role := string(model.InstanceRoleProxy)
		got, err := r.Resolve(BusinessTarget{
			GroupIDs: []uint{g.ID},
			Filters:  &BusinessBatchFilter{Role: &role},
		})
		require.NoError(t, err)
		assert.Equal(t, []uint{otherRole.ID}, got.InstanceIDs)
	})
	t.Run("nodeId", func(t *testing.T) {
		nodeID := uint(99) // 无实例属于该节点
		got, err := r.Resolve(BusinessTarget{
			GroupIDs: []uint{g.ID},
			Filters:  &BusinessBatchFilter{NodeID: &nodeID},
		})
		require.NoError(t, err)
		assert.Empty(t, got.InstanceIDs)
	})
}

// TestBusinessTarget_Resolve_Empty 解析后无实例不算错误，返回空结果（由上层映射 TARGET_EMPTY）。
func TestBusinessTarget_Resolve_Empty(t *testing.T) {
	db := newBatchTestDB(t)
	g := mkGroup(t, db, "empty-group", nil)
	r := NewBusinessTargetResolver(db)

	got, err := r.Resolve(BusinessTarget{GroupIDs: []uint{g.ID}, IncludeDescendants: true})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Count())

	// 不存在的分组同样解析为空，不报错。
	got, err = r.Resolve(BusinessTarget{GroupIDs: []uint{9999}})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Count())

	// 不存在的实例 ID 同样被丢弃。
	got, err = r.Resolve(BusinessTarget{InstanceIDs: []uint{9998}})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Count())
}

// TestBusinessTarget_Resolve_Unspecified 两个维度都不给时拒绝（避免退化成全量）。
func TestBusinessTarget_Resolve_Unspecified(t *testing.T) {
	db := newBatchTestDB(t)
	_, err := NewBusinessTargetResolver(db).Resolve(BusinessTarget{})
	require.ErrorIs(t, err, ErrBusinessTargetUnspecified)
}

// TestBusinessTarget_Resolve_CycleGuard 分组树成环时不无限递归（脏数据保护）。
func TestBusinessTarget_Resolve_CycleGuard(t *testing.T) {
	db := newBatchTestDB(t)
	a := mkGroup(t, db, "a", nil)
	b := mkGroup(t, db, "b", &a.ID)
	// 人为制造环：a.ParentID = b
	require.NoError(t, db.Model(&model.InstanceGroupNode{}).Where("id = ?", a.ID).Update("parent_id", b.ID).Error)
	inst := mkBatchInstance(t, db, "x", 1, model.InstanceStatusRunning)
	linkGroup(t, db, a.ID, inst.ID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := NewBusinessTargetResolver(db).Resolve(BusinessTarget{GroupIDs: []uint{a.ID}, IncludeDescendants: true})
		assert.NoError(t, err)
		assert.Equal(t, []uint{inst.ID}, got.InstanceIDs)
	}()
	<-done // 若递归无保护会挂死，测试超时即暴露
}

// TestBusinessTarget_Scope_Convergence 越权实例被剔除且不报错（存在性隐藏）。
func TestBusinessTarget_Scope_Convergence(t *testing.T) {
	db := newBatchTestDB(t)
	g := mkGroup(t, db, "g1", nil)
	mine := mkBatchInstance(t, db, "mine", 1, model.InstanceStatusRunning)
	other := mkBatchInstance(t, db, "other", 1, model.InstanceStatusRunning)
	linkGroup(t, db, g.ID, mine.ID)
	linkGroup(t, db, g.ID, other.ID)

	r := NewBusinessTargetResolver(db)
	got, err := r.ResolveScoped(BusinessTarget{GroupIDs: []uint{g.ID}},
		BusinessTargetScope{IDs: []uint{mine.ID}, Scoped: true})
	require.NoError(t, err)
	assert.Equal(t, []uint{mine.ID}, got.InstanceIDs)

	// Scoped 但集合为空 → 直接空结果（不打库也不报错）。
	got, err = r.ResolveScoped(BusinessTarget{GroupIDs: []uint{g.ID}},
		BusinessTargetScope{Scoped: true})
	require.NoError(t, err)
	assert.Equal(t, 0, got.Count())
}

// TestBusinessTarget_Hash 指纹对顺序不敏感、对内容敏感。
func TestBusinessTarget_Hash(t *testing.T) {
	a := BusinessTargetHash([]uint{3, 1, 2})
	b := BusinessTargetHash([]uint{1, 2, 3})
	c := BusinessTargetHash([]uint{1, 2, 3})
	d := BusinessTargetHash([]uint{1, 2})
	assert.Equal(t, a, b, "排序不同但内容相同应得到同一指纹")
	assert.Equal(t, b, c)
	assert.NotEqual(t, c, d, "内容不同必须得到不同指纹")

	dup := BusinessTargetHash([]uint{1, 1, 2, 3})
	assert.Equal(t, c, dup, "重复 ID 应当被归一")
}
