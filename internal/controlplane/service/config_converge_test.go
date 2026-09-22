package service

import (
	"strconv"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newBaselineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{}, &model.InstanceConfigVersion{},
		&model.ConfigBaseline{}, &model.InstanceGroupNode{}, &model.InstanceGroupMember{},
		&model.GroupInstance{},
	))
	return db
}

func insertVersion(t *testing.T, db *gorm.DB, instanceID uint, content string) {
	t.Helper()
	require.NoError(t, db.Create(&model.InstanceConfigVersion{
		InstanceID: instanceID, FilePath: "server.properties",
		ContentHash: hashContent(content), Content: content,
	}).Error)
}

func TestConfigBaseline_DetectDrift(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	bl, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1)
	require.NoError(t, err)
	require.Equal(t, hashContent("A"), bl.ContentHash)

	insertVersion(t, db, ids[0], "A") // 与基线一致
	insertVersion(t, db, ids[1], "B") // 漂移

	items, err := svc.DetectDrift(bl.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	byID := map[uint]DriftItem{}
	for _, it := range items {
		byID[it.InstanceID] = it
	}
	require.False(t, byID[ids[0]].Drift)
	require.Equal(t, hashContent("A"), byID[ids[0]].CurrentHash)
	require.True(t, byID[ids[1]].Drift)
	require.Equal(t, hashContent("B"), byID[ids[1]].CurrentHash)
	require.Equal(t, bl.ContentHash, byID[ids[1]].BaselineHash)
}

func TestConfigBaseline_Converge(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	bl, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1)
	require.NoError(t, err)
	insertVersion(t, db, ids[0], "A")
	insertVersion(t, db, ids[1], "B")

	// 假写入：落一条与基线一致的版本，使收敛后复核无残余漂移。
	var written []uint
	svc.SetConvergeWriterForTest(func(instanceID uint, filePath, content, message string, authorID uint) (uint, error) {
		written = append(written, instanceID)
		v := &model.InstanceConfigVersion{InstanceID: instanceID, FilePath: filePath, ContentHash: hashContent(content), Content: content}
		require.NoError(t, db.Create(v).Error)
		return v.ID, nil
	})

	res, err := svc.Converge(bl.ID, ConvergeOptions{}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, res.Targeted, "仅漂移实例进入收敛")
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, []uint{ids[1]}, written)
	require.Empty(t, res.ResidualDrift, "收敛后复核应无残余漂移")
}

func TestConfigBaseline_ScopeGroup(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	group := &model.InstanceGroupNode{Name: "g1"}
	require.NoError(t, db.Create(group).Error)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: group.ID, InstanceID: ids[0]}).Error)

	bl, err := svc.UpsertBaseline(BaselineInput{
		ScopeKey: "group:" + strconv.FormatUint(uint64(group.ID), 10), FilePath: "server.properties", Content: "A",
	}, 1)
	require.NoError(t, err)

	insertVersion(t, db, ids[0], "X")
	insertVersion(t, db, ids[1], "Y")

	items, err := svc.DetectDrift(bl.ID)
	require.NoError(t, err)
	require.Len(t, items, 1, "group scope 仅覆盖成员实例")
	require.Equal(t, ids[0], items[0].InstanceID)
	require.True(t, items[0].Drift)
}

func TestConfigBaseline_UpsertRejectsBadScope(t *testing.T) {
	db := newBaselineTestDB(t)
	svc := NewConfigBaselineService(db, nil)
	_, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "bogus", FilePath: "server.properties", Content: "A"}, 1)
	require.Error(t, err)
}

// TestConfigBaseline_GroupScopeFailClosed 覆盖修复（NEW-ISSUE）：group:<id> 不再静默解析为空集。
// 组织树分组（ADR-033）与用户组（ADR-004）id 空间正交，误填用户组 id 必须报错而不是「创建成功但谁都不覆盖」。
func TestConfigBaseline_GroupScopeFailClosed(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 1)
	svc := NewConfigBaselineService(db, nil)

	// 1) 组织树中不存在的 id → ErrBaselineScopeGroupNotFound（且不落库）。
	_, err := svc.UpsertBaseline(BaselineInput{
		ScopeKey: "group:999999", FilePath: "server.properties", Content: "A",
	}, 1)
	require.ErrorIs(t, err, ErrBaselineScopeGroupNotFound)
	require.Contains(t, err.Error(), "组织树分组")
	var cnt int64
	require.NoError(t, db.Model(&model.ConfigBaseline{}).Count(&cnt).Error)
	require.Zero(t, cnt, "非法 group scope 不得落库")

	// 2) 用户组 id（ADR-004）被填进 group: → 同样被拒（组织树里没有这个节点）。
	//    这里直接建一条用户组关联，证明它与 group: 的解析空间无关。
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 42, InstanceID: ids[0]}).Error)
	_, err = svc.resolveScopeInstances("group:42")
	require.ErrorIs(t, err, ErrBaselineScopeGroupNotFound)

	// 3) 分组存在但子树内无成员实例 → ErrBaselineScopeGroupEmpty（与「不存在」区分开）。
	empty := &model.InstanceGroupNode{Name: "空分组"}
	require.NoError(t, db.Create(empty).Error)
	_, err = svc.UpsertBaseline(BaselineInput{
		ScopeKey: "group:" + strconv.FormatUint(uint64(empty.ID), 10), FilePath: "server.properties", Content: "A",
	}, 1)
	require.ErrorIs(t, err, ErrBaselineScopeGroupEmpty)
	require.Contains(t, err.Error(), "没有成员实例")

	// 4) 分组有成员实例 → 正常解析（含子树）。
	root := &model.InstanceGroupNode{Name: "根"}
	require.NoError(t, db.Create(root).Error)
	child := &model.InstanceGroupNode{Name: "子", ParentID: &root.ID}
	require.NoError(t, db.Create(child).Error)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: child.ID, InstanceID: ids[0]}).Error)
	bl, err := svc.UpsertBaseline(BaselineInput{
		ScopeKey: "group:" + strconv.FormatUint(uint64(root.ID), 10), FilePath: "server.properties", Content: "A",
	}, 1)
	require.NoError(t, err)
	items, err := svc.DetectDrift(bl.ID)
	require.NoError(t, err)
	require.Len(t, items, 1, "根分组 scope 应含子树成员实例")
	require.Equal(t, ids[0], items[0].InstanceID)

	// 5) 脏数据（分组被软删）时：非管理员读取按 NOT_FOUND 隐藏存在性（不泄露、不放行）。
	require.NoError(t, db.Delete(&model.InstanceGroupNode{}, root.ID).Error)
	_, err = svc.GetBaselineScoped(bl.ID, []uint{ids[0]}, true)
	require.ErrorIs(t, err, ErrRollingOpNotFound)
	// 平台管理员读取该基线本体不受 scope 解析影响；但漂移检测会给出明确原因。
	got, err := svc.GetBaselineScoped(bl.ID, nil, false)
	require.NoError(t, err)
	require.Equal(t, bl.ID, got.ID)
	_, err = svc.DetectDriftScoped(bl.ID, nil, false)
	require.ErrorIs(t, err, ErrBaselineScopeGroupNotFound)
}

// TestConfigBaseline_ScopeFiltering 覆盖 FR-458 越权修复：非管理员只能看到/收敛「scope ∩ 可访问集合」。
func TestConfigBaseline_ScopeFiltering(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 3)
	svc := NewConfigBaselineService(db, nil)

	bl, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1)
	require.NoError(t, err)
	for _, id := range ids {
		insertVersion(t, db, id, "B") // 全部漂移
	}

	// 仅可见 ids[0] → 漂移检测收敛到 1 台。
	items, err := svc.DetectDriftScoped(bl.ID, []uint{ids[0]}, true)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, ids[0], items[0].InstanceID)

	// 平台管理员（scoped=false）看到全部。
	all, err := svc.DetectDriftScoped(bl.ID, nil, false)
	require.NoError(t, err)
	require.Len(t, all, 3)

	// 收敛只写可访问集合内的实例。
	//
	// 写入回调由 convergeBatch 的**并发 worker** 调用，累加必须加锁：无锁 `append` 在
	// `-race` 下是真实 DATA RACE（既是测试缺陷，也会淹没本包的竞态结论）。
	var (
		writtenMu sync.Mutex
		written   []uint
	)
	svc.SetConvergeWriterForTest(func(instanceID uint, filePath, content, message string, authorID uint) (uint, error) {
		writtenMu.Lock()
		written = append(written, instanceID)
		writtenMu.Unlock()
		v := &model.InstanceConfigVersion{InstanceID: instanceID, FilePath: filePath, ContentHash: hashContent(content), Content: content}
		require.NoError(t, db.Create(v).Error)
		return v.ID, nil
	})
	res, err := svc.ConvergeScoped(bl.ID, ConvergeOptions{}, 1, []uint{ids[0], ids[1]}, true)
	require.NoError(t, err)
	require.Equal(t, 2, res.Targeted)
	require.ElementsMatch(t, []uint{ids[0], ids[1]}, written, "收敛绝不触达 scope 外实例")
}

// TestConfigBaseline_ScopeHidesInvisible 非管理员读取 scope 不相交的基线应以 NOT_FOUND 隐藏存在性。
func TestConfigBaseline_ScopeHidesInvisible(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	group := &model.InstanceGroupNode{Name: "hidden"}
	require.NoError(t, db.Create(group).Error)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: group.ID, InstanceID: ids[0]}).Error)

	bl, err := svc.UpsertBaseline(BaselineInput{
		ScopeKey: "group:" + strconv.FormatUint(uint64(group.ID), 10), FilePath: "server.properties", Content: "A",
	}, 1)
	require.NoError(t, err)

	// 可访问集合仅含 ids[1]（不在基线 scope）→ 不可见。
	_, err = svc.GetBaselineScoped(bl.ID, []uint{ids[1]}, true)
	require.ErrorIs(t, err, ErrRollingOpNotFound)

	// 可访问集合含 ids[0] → 可见。
	got, err := svc.GetBaselineScoped(bl.ID, []uint{ids[0]}, true)
	require.NoError(t, err)
	require.Equal(t, bl.ID, got.ID)

	// List 过滤：ids[1] 不可见该基线。
	rows, err := svc.ListBaselinesScoped([]uint{ids[1]}, true)
	require.NoError(t, err)
	require.Empty(t, rows)
	rows, err = svc.ListBaselinesScoped([]uint{ids[0]}, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

// TestConfigBaseline_DetectDriftReadErrorNotDrift 读取错误单独分类，不计入漂移（FR-458 nit）。
func TestConfigBaseline_DetectDriftReadErrorNotDrift(t *testing.T) {
	db := newBaselineTestDB(t)
	seedPlainInstances(t, db, 1) // 该实例无版本；config 未注入 → Read 报错
	svc := NewConfigBaselineService(db, nil)

	bl, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1)
	require.NoError(t, err)
	// 实例无版本且 config 未注入 → currentHash 报错。
	items, err := svc.DetectDrift(bl.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotEmpty(t, items[0].Error, "读取错误应记入 Error")
	require.False(t, items[0].Drift, "读取错误不应计为漂移")
	// 因此不进入收敛目标。
	svc.SetConvergeWriterForTest(func(instanceID uint, filePath, content, message string, authorID uint) (uint, error) {
		t.Fatalf("读取错误的实例不应被收敛: instance=%d", instanceID)
		return 0, nil
	})
	res, err := svc.Converge(bl.ID, ConvergeOptions{}, 1)
	require.NoError(t, err)
	require.Equal(t, 0, res.Targeted)
}

// TestConfigBaseline_UpsertScopedGuard 覆盖 FR-458 越权修复：非管理员不能创建/覆盖 scope 超出可访问范围的基线。
func TestConfigBaseline_UpsertScopedGuard(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 3)
	svc := NewConfigBaselineService(db, nil)

	// 非管理员（scoped=true，可访问 ids[0]）以 scope=all 建基线 → 拒绝。
	_, err := svc.UpsertBaselineScoped(
		BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1, []uint{ids[0]}, true)
	require.ErrorIs(t, err, ErrBaselineScopeForbidden)

	// scope=instance:<ids[0]>（全部可访问）→ 允许。
	bl, err := svc.UpsertBaselineScoped(BaselineInput{
		ScopeKey: "instance:" + strconv.FormatUint(uint64(ids[0]), 10),
		FilePath: "server.properties", Content: "A",
	}, 1, []uint{ids[0]}, true)
	require.NoError(t, err)
	require.NotZero(t, bl.ID)

	// scope=instance:<ids[1]>（不可访问）→ 拒绝。
	_, err = svc.UpsertBaselineScoped(BaselineInput{
		ScopeKey: "instance:" + strconv.FormatUint(uint64(ids[1]), 10),
		FilePath: "server.properties", Content: "B",
	}, 1, []uint{ids[0]}, true)
	require.ErrorIs(t, err, ErrBaselineScopeForbidden)

	// 平台管理员（scoped=false）不受限。
	adminBL, err := svc.UpsertBaselineScoped(
		BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "C"}, 1, nil, false)
	require.NoError(t, err)
	require.NotZero(t, adminBL.ID)
}
