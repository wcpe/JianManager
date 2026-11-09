package archive_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 本文件是「已归档（ARCHIVED）」在**查询面**上的验收。
//
// 为什么落在 `logs/archive` 包而不是 `logs/query` 包：本文件验收的是「归档分区
// 如何影响查询规划」，天然要同时握住 archive 的分区键与 query 的规划器；放在本包
// 可以只依赖 query 的**导出面**，不必进 query 的内部测试包。
//
// 历史备注：曾经存在 `query(内部测试) → lifecycle → archive → query` 的测试期导入环，
// 根因是 `lifecycle` 的导出器直接依赖 archive。该依赖已按「消费者声明窄接口、
// 装配层注入」的口径反转（导出器迁入本包，冻结快照由 `lifecycle` 注入），环已消失。
//
// 验收点（对应任务书「验收测试」栏）：
//  1. 归档后查询规划器把该分区标成归档缺失（LOG_COVERAGE_ARCHIVE_MISSING），
//     而不是「查得到 0 行」；
//  2. 归档不可达时 Search 返回**明确错误** LOG_ARCHIVE_MISSING（不是空成功）；
//  3. 热层的旧目录（已退役）永不进入 QueryPlanner —— 即便 VL 重新 attach；
//  4. 已恢复（有对齐投影）的归档分区仍按归档层可查（owner=archive ⇒ tier=archive）。

const archivedDay = "2026-09-01"

// completeProjection 构造一个**证据完整**的投影（含覆盖源身份与对应水位）。
//
// 必须补齐 CoveredSourceGenerations：查询侧对「只有分区 max 而无来源身份」的投影
// 判为 PROJECTION_INCOMPLETE 并拒绝出 range——那是刻意的 fail-closed，
// 不能为了让测试通过而放宽（否则「有数据未读」会被粉饰成「空」）。
func completeProjection(manifest, dirID string, generation uint64, seq uint64) *catalog.PublishedProjection {
	return &catalog.PublishedProjection{
		ManifestVersion:  manifest,
		CoverageComplete: true,
		CoveredSourceGenerations: []catalog.SourceGenerationRef{
			{LogSourceID: "source", SourceGeneration: "s1"},
		},
		ClosedVisibleSeq:      map[string]uint64{"source/s1": seq, "default": seq},
		QueryLocationDirID:    dirID,
		QueryGeneration:       generation,
		ProjectionGeneration:  "p-" + manifest,
		ProjectionGenerations: []string{"p-" + manifest},
	}
}

func archivedKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "inst:147", UTCDay: archivedDay}
}

func archiveEvidenceFixture() catalog.ArchiveEvidence {
	return catalog.ArchiveEvidence{
		ProviderKind:     "local",
		ObjectPrefix:     "inst:147/2026-09-01/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage: catalog.ArchiveCoverage{
			ObjectCount: 3, SourceBytes: 4096, StoredBytes: 1024, Complete: true,
		},
	}
}

// archivedCatalog 构造一个「已归档且热层已回收」的分区（无本地可查副本）。
func archivedCatalog(t *testing.T) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	cat := catalog.New(nil)
	key := archivedKey()
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = completeProjection("manifest-hot-1", "hot-g1", 1, 42)
	require.NoError(t, cat.Put(rec))
	now := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	_, _, err := cat.MarkArchived(key, archiveEvidenceFixture(), now)
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(key, now.Add(time.Minute))
	require.NoError(t, err)
	return cat, key
}

func newPlanner(cat *catalog.Catalog) *query.Planner {
	return query.NewPlanner(cat, nil)
}

// TestPlannerRoutesArchivedPartitionToArchive 归档分区必须走归档语义，而不是「空成功」。
func TestPlannerRoutesArchivedPartitionToArchive(t *testing.T) {
	cat, key := archivedCatalog(t)
	planner := newPlanner(cat)

	res := planner.Plan(query.PlanRequest{TimeRange: query.TimeRange{}})
	require.Nil(t, res.Err, "规划本身不应失败（失败发生在读取阶段）")

	// 关键：不得产出任何指向热层的 range（热层目录已被回收）。
	require.Empty(t, res.Ranges, "已归档且未恢复的分区不得产出可读 range")

	target := findTarget(t, res.Coverage, key.String())
	require.Equal(t, query.CoverageArchiveMiss, target.State,
		"归档区间必须报 LOG_COVERAGE_ARCHIVE_MISSING，而不是 NOT_READY/成功")
	require.Contains(t, target.Reasons, query.ReasonArchiveNotRestored)
	require.NotContains(t, target.Reasons, query.ReasonNoCatalogAuthority,
		"归档权威不能报成「没有 Catalog 权威」（那是配置/记录缺失的措辞）")
	// 审计可见性：位置必须是归档标识，且绝不等于已退役的热层目录。
	require.Equal(t, "archive", target.Owner)
	require.NotEqual(t, "hot-g1", target.DirID, "覆盖里绝不能出现已回收的热层目录")
	require.Contains(t, target.DirID, "archive:")
	require.False(t, res.Coverage.Complete, "归档缺失必须让覆盖不完整")
}

// TestPlannerHidesRetiredHotDir 已退役热层目录永久排除出 QueryPlanner。
func TestPlannerHidesRetiredHotDir(t *testing.T) {
	cat, key := archivedCatalog(t)
	require.False(t, cat.IsQueryDirVisible(key, "hot-g1"),
		"VL 重新 attach 一个已回收的旧目录，也不得让它进入 QueryPlanner")
}

// TestSearchArchivedUnreachableReturnsExplicitError 归档不可达必须报错，不得静默返回空结果。
//
// 这条是本文件最重要的断言：静默的空结果会让运维以为「这一天没产生日志」，
// 而真相是「正本在归档、需要先恢复」。前者永远没人去查，后者有明确处置动作。
func TestSearchArchivedUnreachableReturnsExplicitError(t *testing.T) {
	cat, _ := archivedCatalog(t)
	svc := query.NewService(newPlanner(cat), nil, "test-build")

	resp := svc.Search(context.Background(), query.QueryRequest{
		RequestID:         "req-archived",
		AuthorizedTargets: []string{"inst:147"},
	})
	require.NotNil(t, resp.Err, "归档不可达时**必须**返回错误，不得伪装成空成功")
	require.Equal(t, query.ErrCodeArchiveMissing, resp.Err.Code,
		"错误码应为 LOG_ARCHIVE_MISSING（可直接引导 rehydrate），得到 %s/%s",
		resp.Err.Code, resp.Err.Message)
	require.Empty(t, resp.Items)
	require.Contains(t, resp.Coverage.PartialReasons, query.ReasonArchiveNotRestored)
	// 明确性：消息必须指出「归档未恢复」这一处置动作。
	require.Contains(t, resp.Err.Message, "恢复")
}

// TestOpenViewArchivedReportsArchiveMissing 打开视图同样显式降级（不宣称可查）。
func TestOpenViewArchivedReportsArchiveMissing(t *testing.T) {
	cat, key := archivedCatalog(t)
	svc := query.NewService(newPlanner(cat), nil, "test-build")

	resp := svc.OpenView(query.QueryRequest{RequestID: "req-view", AuthorizedTargets: []string{"inst:147"}})
	require.Nil(t, resp.Err, "打开视图只做规划与租约，读取降级由覆盖表达")
	// OpenView 的覆盖按**授权目标**聚合（CP 授权粒度是命名空间），故这里找命名空间条目；
	// 分区级细节由 Search/Planner 的覆盖给出（见上一个用例）。
	_ = key
	target := findTarget(t, resp.Coverage, "inst:147")
	require.Equal(t, query.CoverageArchiveMiss, target.State)
}

// TestPlannerServesRehydratedArchiveFromArchiveTier 已恢复的归档分区按归档层可查。
func TestPlannerServesRehydratedArchiveFromArchiveTier(t *testing.T) {
	cat := catalog.New(nil)
	key := archivedKey()
	// 模拟 rehydrate 已完成：记录为 archive 权威，且投影与目录/代次对齐。
	rec := catalog.NewStableRecord(key, catalog.OwnerArchive, 1, "rehydrate-20260901")
	rec.PublishedProjection = completeProjection("manifest-rehydrate-1", rec.OwnerDirID, rec.Generation, 7)
	require.NoError(t, cat.Put(rec))

	planner := newPlanner(cat)
	res := planner.Plan(query.PlanRequest{TimeRange: query.TimeRange{}})
	require.Len(t, res.Ranges, 1, "已恢复的归档分区必须可查")
	require.Equal(t, catalog.OwnerArchive, res.Ranges[0].Owner)
	require.Equal(t, query.TierArchive, res.Ranges[0].Tier,
		"归档权威必须路由到 archive tier（查询后端据此选 Rehydrate 客户端）")
	require.Equal(t, "rehydrate-20260901", res.Ranges[0].DirID)
}

// TestPlannerMixedHotAndArchivedIsPartial 混合场景：HOT 可查、归档显式 partial。
func TestPlannerMixedHotAndArchivedIsPartial(t *testing.T) {
	cat, archived := archivedCatalog(t)
	hotKey := catalog.PartitionKey{StorageNamespace: "inst:147", UTCDay: "2026-09-02"}
	hot := catalog.NewStableRecord(hotKey, catalog.OwnerHot, 1, "hot-g2")
	hot.PublishedProjection = completeProjection("manifest-hot-2", "hot-g2", 1, 9)
	require.NoError(t, cat.Put(hot))

	res := newPlanner(cat).Plan(query.PlanRequest{TimeRange: query.TimeRange{}})
	require.Len(t, res.Ranges, 1, "只有 HOT 分区可读")
	require.Equal(t, catalog.OwnerHot, res.Ranges[0].Owner)
	require.False(t, res.Coverage.Complete, "归档缺失必须让整体覆盖不完整")
	require.Contains(t, res.Coverage.PartialReasons, query.ReasonArchiveNotRestored)

	archivedTarget := findTarget(t, res.Coverage, archived.String())
	require.Equal(t, query.CoverageArchiveMiss, archivedTarget.State)
	hotTarget := findTarget(t, res.Coverage, hotKey.String())
	require.Equal(t, query.CoverageSuccess, hotTarget.State)
}

func findTarget(t *testing.T, cov query.Coverage, targetID string) query.TargetCoverage {
	t.Helper()
	for _, target := range cov.Targets {
		if target.TargetID == targetID {
			return target
		}
	}
	t.Fatalf("覆盖里没有目标 %q（实际 %v）", targetID, cov.Targets)
	return query.TargetCoverage{}
}
