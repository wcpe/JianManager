package query

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 本文件验证「导出路径豁免投影清单版本漂移」的精确边界。
//
// 背景（2026-10-10 生产实证）：CP 导出自建视图后逐页 Search（耗时 30-37 秒），期间任一源
// 写入新投影都会让 ManifestVersion 变化 → 全部目标（实测 165 个）降级 partial → 导出永远
// 拿不到附件。而 ManifestVersion 只哈希内容清单，源在实时追加日志时**必然**变化。
//
// 三条断言必须同时成立，缺一即视为豁免过宽或未生效：
//  1. 带标记时，ManifestVersion 变化不再 VIEW_STALE（导出可用）；
//  2. 不带标记时，ManifestVersion 变化仍 VIEW_STALE（原设计意图 / 前端旧 view_id 复用防线）；
//  3. 带标记时，closed_visible_seq 缩水仍 VIEW_STALE（不放过数据变少）。
//
// 3 是关键负例：证明豁免只覆盖「内容追加」，没有把整个校验一起放宽。

// projectionDriftCatalog 构造一个带已发布投影的 HOT 权威分区，便于后续改写投影模拟漂移。
func projectionDriftCatalog(t *testing.T, manifest string, closedVisibleSeq uint64) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	key := testKey()
	c := catalog.New(nil)
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:    manifest,
		CoverageComplete:   true,
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
		ClosedVisibleSeq:   map[string]uint64{key.String(): closedVisibleSeq},
	}
	require.NoError(t, c.Put(rec))
	return c, key
}

// republish 模拟「源在导出期间又写了一批日志」：内容清单版本变化，闭可见水位前进。
func republish(t *testing.T, c *catalog.Catalog, key catalog.PartitionKey, manifest string, closedVisibleSeq uint64) {
	t.Helper()
	rec, ok := c.Get(key)
	require.True(t, ok)
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:    manifest,
		CoverageComplete:   true,
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
		ClosedVisibleSeq:   map[string]uint64{key.String(): closedVisibleSeq},
	}
	require.NoError(t, c.Put(rec))
}

// 正向：导出路径（带豁免标记）在投影清单版本变化后**仍可继续查询**，不再 VIEW_STALE。
func TestPlanner_AllowProjectionDriftExemptsManifestVersionChange(t *testing.T) {
	cat, key := projectionDriftCatalog(t, "manifest-v1", 100)
	planner := NewPlanner(cat, nil)

	first := planner.Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, first.Err)
	require.NotNil(t, first.View)
	require.Len(t, first.Ranges, 1)

	// 源在导出期间发布了新投影（内容追加，水位同时前进）。
	republish(t, cat, key, "manifest-v2", 200)

	// 不带标记：仍然 VIEW_STALE（防线未被破坏，见下一个用例的显式断言）。
	blocked := planner.Plan(PlanRequest{
		TargetIDs: []string{key.StorageNamespace},
		ViewRef:   &ViewRef{ViewID: first.View.ViewID},
	})
	require.NotNil(t, blocked.Err, "不带豁免标记时投影版本变化必须报 VIEW_STALE")
	require.Equal(t, ErrCodeViewStale, blocked.Err.Code)

	// 带标记（导出路径）：同一视图、同一漂移不再报错，且仍给出可读 range。
	allowed := planner.Plan(PlanRequest{
		TargetIDs:            []string{key.StorageNamespace},
		ViewRef:              &ViewRef{ViewID: first.View.ViewID},
		AllowProjectionDrift: true,
	})
	require.Nil(t, allowed.Err, "带豁免标记时投影版本变化不得报 VIEW_STALE")
	require.Len(t, allowed.Ranges, 1)
	require.Equal(t, CoverageSuccess, allowed.Coverage.Targets[0].State)
	require.True(t, allowed.Coverage.Complete, "导出必须拿到 complete 覆盖，否则闸门仍拦附件")

	// 关键安全性质：豁免不会扩大读取窗口。range 仍以**视图创建时冻结的水位**为上界，
	// 因此导出读到的仍是与首建视图一致的那段快照，不会把漂移期间新追加的数据一并读出。
	require.EqualValues(t, 100, allowed.Ranges[0].ClosedVisibleSeq,
		"豁免后读取上界必须仍是视图冻结水位，不得随漂移前进")
}

// 反向：不带标记时，投影清单版本变化必须仍然 VIEW_STALE（保护未被放宽）。
func TestPlanner_ProjectionDriftStillStaleWithoutFlag(t *testing.T) {
	cat, key := projectionDriftCatalog(t, "manifest-v1", 100)
	planner := NewPlanner(cat, nil)

	first := planner.Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, first.Err)

	// 只改内容清单版本，水位不动（纯内容清单变化的纯净场景）。
	republish(t, cat, key, "manifest-changed", 100)

	stale := planner.Plan(PlanRequest{
		TargetIDs: []string{key.StorageNamespace},
		ViewRef:   &ViewRef{ViewID: first.View.ViewID},
	})
	require.NotNil(t, stale.Err, "无豁免标记时投影版本变化仍必须拦截")
	require.Equal(t, ErrCodeViewStale, stale.Err.Code)
	require.Contains(t, stale.Err.Message, "projection changed")
	require.Equal(t, EnumStale, stale.Coverage.EnumerationState)
	// 原实现语义保留：该目标被标记为 stale。
	require.Equal(t, CoverageStale, stale.Coverage.Targets[0].State)
}

// 关键负例：带豁免标记时，closed_visible_seq 缩水（数据变少）仍必须 VIEW_STALE。
// 这是「豁免没有过宽」的判据——若误把整段校验一起放宽，本用例即转红。
func TestPlanner_AllowProjectionDriftStillRejectsClosedVisibleSeqShrink(t *testing.T) {
	cat, key := projectionDriftCatalog(t, "manifest-v1", 300)
	planner := NewPlanner(cat, nil)

	first := planner.Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, first.Err)
	require.Len(t, first.Ranges, 1)
	require.EqualValues(t, 300, first.Ranges[0].ClosedVisibleSeq)

	// 投影前缀缩水：水位从 300 退回 100（数据变少，必须拦）。
	republish(t, cat, key, "manifest-v2", 100)

	shrunk := planner.Plan(PlanRequest{
		TargetIDs:            []string{key.StorageNamespace},
		ViewRef:              &ViewRef{ViewID: first.View.ViewID},
		AllowProjectionDrift: true,
	})
	require.NotNil(t, shrunk.Err, "带豁免标记时闭可见水位缩水仍必须 VIEW_STALE")
	require.Equal(t, ErrCodeViewStale, shrunk.Err.Code)
	require.Contains(t, shrunk.Err.Message, "closed_visible_seq shrunk")
	require.Equal(t, EnumStale, shrunk.Coverage.EnumerationState)
}

// droppingCatalog 在视图建立后丢弃指定分区键，模拟「分区记录消失」分支。
// 只用于验证豁免没有把该分支一起放过（Catalog 生产实现无删除入口，故用测试替身）。
type droppingCatalog struct {
	inner   *catalog.Catalog
	drop    catalog.PartitionKey
	dropped bool
}

func (d *droppingCatalog) Keys() []catalog.PartitionKey {
	if d.dropped {
		out := make([]catalog.PartitionKey, 0, 1)
		for _, k := range d.inner.Keys() {
			if k != d.drop {
				out = append(out, k)
			}
		}
		return out
	}
	return d.inner.Keys()
}

func (d *droppingCatalog) Get(key catalog.PartitionKey) (*catalog.Record, bool) {
	if d.dropped && key == d.drop {
		return nil, false
	}
	return d.inner.Get(key)
}

func (d *droppingCatalog) OwnerForQuery(key catalog.PartitionKey) (catalog.QueryRef, bool) {
	if d.dropped && key == d.drop {
		return catalog.QueryRef{}, false
	}
	return d.inner.OwnerForQuery(key)
}

// 负例：带豁免标记时，catalog 分区记录消失仍必须拦（分支 1）。
func TestPlanner_AllowProjectionDriftStillRejectsMissingCatalogRecord(t *testing.T) {
	inner, key := projectionDriftCatalog(t, "manifest-v1", 100)
	cat := &droppingCatalog{inner: inner, drop: key}
	planner := NewPlanner(cat, nil)

	first := planner.Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, first.Err)

	cat.dropped = true

	gone := planner.Plan(PlanRequest{
		TargetIDs:            []string{key.StorageNamespace},
		ViewRef:              &ViewRef{ViewID: first.View.ViewID},
		AllowProjectionDrift: true,
	})
	require.NotNil(t, gone.Err, "带豁免标记时分区记录消失仍必须 VIEW_STALE")
	require.Equal(t, ErrCodeViewStale, gone.Err.Code)
	require.Contains(t, gone.Err.Message, "catalog record missing")
}

// 负例：带豁免标记时，generation 变化（数据源身份变了）仍必须拦（分支 3）。
func TestPlanner_AllowProjectionDriftStillRejectsGenerationChange(t *testing.T) {
	cat, key := projectionDriftCatalog(t, "manifest-v1", 100)
	planner := NewPlanner(cat, nil)

	first := planner.Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, first.Err)
	require.EqualValues(t, 1, first.Ranges[0].Generation)

	// 代次推进（写入另一份隔离 generation）：身份变了，必须拦。
	rec, ok := cat.Get(key)
	require.True(t, ok)
	rec.Generation = 2
	rec.PublishedProjection.QueryGeneration = 2
	require.NoError(t, cat.Put(rec))

	switched := planner.Plan(PlanRequest{
		TargetIDs:            []string{key.StorageNamespace},
		ViewRef:              &ViewRef{ViewID: first.View.ViewID},
		AllowProjectionDrift: true,
	})
	require.NotNil(t, switched.Err, "带豁免标记时代次变化仍必须 VIEW_STALE")
	require.Equal(t, ErrCodeViewStale, switched.Err.Code)
	require.Contains(t, switched.Err.Message, "generation mismatch")
}

// 负例：带豁免标记时，owner 目录切换（dir mismatch）仍必须拦（分支 3 的另一半）。
func TestPlanner_AllowProjectionDriftStillRejectsDirChange(t *testing.T) {
	cat, key := projectionDriftCatalog(t, "manifest-v1", 100)
	planner := NewPlanner(cat, nil)

	first := planner.Plan(PlanRequest{TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, first.Err)

	rec, ok := cat.Get(key)
	require.True(t, ok)
	rec.OwnerDirID = "hot-g2"
	rec.Dirs = append(rec.Dirs, catalog.PhysicalDir{ID: "hot-g2", Role: catalog.DirOwner})
	rec.PublishedProjection.QueryLocationDirID = "hot-g2"
	require.NoError(t, cat.Put(rec))

	moved := planner.Plan(PlanRequest{
		TargetIDs:            []string{key.StorageNamespace},
		ViewRef:              &ViewRef{ViewID: first.View.ViewID},
		AllowProjectionDrift: true,
	})
	require.NotNil(t, moved.Err, "带豁免标记时权威目录变化仍必须 VIEW_STALE")
	require.Equal(t, ErrCodeViewStale, moved.Err.Code)
	require.Contains(t, moved.Err.Message, "dir mismatch")
}

// 豁免不得让「不带 view_id 的新建视图」改变行为：标记在首建路径上无副作用。
func TestPlanner_AllowProjectionDriftDoesNotAffectViewCreation(t *testing.T) {
	cat, key := projectionDriftCatalog(t, "manifest-v1", 100)
	planner := NewPlanner(cat, nil)

	withFlag := planner.Plan(PlanRequest{
		TargetIDs:            []string{key.StorageNamespace},
		AllowProjectionDrift: true,
	})
	require.Nil(t, withFlag.Err)
	require.Len(t, withFlag.Ranges, 1)
	require.True(t, withFlag.Coverage.Complete)
	// 视图仍被登记（可被后续页复用），豁免不改变视图注册语义。
	_, ok := planner.GetView(withFlag.View.ViewID)
	require.True(t, ok)
}
