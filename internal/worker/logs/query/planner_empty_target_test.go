package query

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// coverageEntryFor 返回某目标 ID 的覆盖条目（不存在则 nil）。
func coverageEntryFor(cov Coverage, targetID string) *TargetCoverage {
	for i := range cov.Targets {
		if cov.Targets[i].TargetID == targetID {
			return &cov.Targets[i]
		}
	}
	return nil
}

// 回归（QRY-1）：授权目标在查询窗内**没有任何分区**时，必须按「空成功」计入覆盖，
// 而不是让 CP 侧判 missing_coverage（症状：一个静默源把整个平台判成未就绪；
// 2026-09-30 生产实证 inst:153 的最后分区早于查询窗）。
//
// 转红方式（实测）：把 coverEmptyAuthorizedTargets 的迭代源换回 view.TargetIDs（旧实现）⇒
// 本用例在「inst:153 必须有覆盖条目」处红——窗口内无分区的源根本不进 view.TargetIDs
// （selectKeys 按 dayOverlapsRange 过滤），迭代视图目标永远看不到它。
func TestPlanner_AuthorizedTargetWithoutPartitionInWindowIsEmptySuccess(t *testing.T) {
	cat := catalog.New(nil)
	// inst:153：最后分区早于窗口（静默源）；inst:160：窗口内有分区（对照）。
	require.NoError(t, cat.Put(catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:153", UTCDay: "2026-09-27"}, catalog.OwnerHot, 1, "hot-153")))
	require.NoError(t, cat.Put(catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:160", UTCDay: "2026-10-11"}, catalog.OwnerHot, 1, "hot-160")))

	req := PlanRequest{
		RequestID: "silent-source",
		TargetIDs: []string{"inst:153", "inst:160"},
		TimeRange: TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"},
	}
	p := NewPlanner(cat, nil)
	res := p.Plan(req)
	require.Nil(t, res.Err)

	// ① 只产出窗口内确实有分区的目标的 range；静默源**不得**产出 range（空 = 结果里 0 行）。
	require.Len(t, res.Ranges, 1)
	require.Equal(t, "inst:160/2026-10-11", res.Ranges[0].TargetID)

	// ② 静默源必须有「空成功」覆盖：这正是 CP 判 missing_coverage 的依据。
	silent := coverageEntryFor(res.Coverage, "inst:153")
	require.NotNil(t, silent, "窗口内无分区的授权目标必须出现在覆盖里（否则 CP 判 missing_coverage）")
	require.Equal(t, CoverageSuccess, silent.State)
	require.True(t, res.Coverage.Complete, "空成功覆盖不得把整体覆盖判成不完整")

	// ③ 与 CP 侧同口径：remapCoverageTargets 之后每个授权目标都要有条目。
	remapped := res.Coverage
	remapCoverageTargets(&remapped, req.TargetIDs)
	require.NotNil(t, coverageEntryFor(remapped, "inst:153"))
	require.NotNil(t, coverageEntryFor(remapped, "inst:160"))

	// ④ 固定视图复用路径必须给出同一条覆盖（否则翻页/复用时症状复现）。
	reused := p.Plan(PlanRequest{
		RequestID: "reuse",
		TargetIDs: req.TargetIDs,
		TimeRange: req.TimeRange,
		ViewRef:   &ViewRef{ViewID: res.View.ViewID, OrderVersion: SortVersion},
	})
	require.Nil(t, reused.Err)
	require.Len(t, reused.Ranges, 1)
	silentReuse := coverageEntryFor(reused.Coverage, "inst:153")
	require.NotNil(t, silentReuse, "复用固定视图时同样不得丢掉空成功覆盖")
	require.Equal(t, CoverageSuccess, silentReuse.State)
	require.True(t, reused.Coverage.Complete)
}

// 命名空间含 '/' 的授权目标（如 ns/game-1）同样要被识别：parseTargetKey 会把命名空间级授权
// 目标误拆成 ns/day，故判据必须「先按整串当命名空间找」。
func TestPlanner_EmptySuccessCoverageForSlashNamespace(t *testing.T) {
	cat := catalog.New(nil)
	require.NoError(t, cat.Put(catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}, catalog.OwnerHot, 1, "hot-old")))

	p := NewPlanner(cat, nil)
	res := p.Plan(PlanRequest{
		RequestID: "slash-ns",
		TargetIDs: []string{"ns/game-1"},
		TimeRange: TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"},
	})
	require.Nil(t, res.Err)
	require.Empty(t, res.Ranges, "窗口内无分区 ⇒ 不得产出 range")
	entry := coverageEntryFor(res.Coverage, "ns/game-1")
	require.NotNil(t, entry, "含 '/' 的命名空间级授权目标同样要补空成功覆盖")
	require.Equal(t, CoverageSuccess, entry.State)
}

// 对照（fail-closed）：Catalog 里**找不到任何可查记录**的授权目标不得被伪造成空成功——
// 宁可让 CP 判 missing_coverage，也不把「不知道」粉饰成「空」。
func TestPlanner_UnknownAuthorizedTargetNotFabricatedAsEmptySuccess(t *testing.T) {
	cat := catalog.New(nil)
	require.NoError(t, cat.Put(catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:160", UTCDay: "2026-10-11"}, catalog.OwnerHot, 1, "hot-160")))

	p := NewPlanner(cat, nil)
	res := p.Plan(PlanRequest{
		RequestID: "unknown-target",
		TargetIDs: []string{"inst:160", "inst:999"},
		TimeRange: TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"},
	})
	require.Nil(t, res.Err)
	require.Nil(t, coverageEntryFor(res.Coverage, "inst:999"),
		"无 Catalog 记录的授权目标不得被发明成空成功（未就绪必须保持可见）")

	// 同一命名空间「有记录但 OwnerForQuery 给不出权威」（Owner/OwnerDirID 为空）同样不得算空成功。
	require.NoError(t, cat.Put(&catalog.Record{
		Key: catalog.PartitionKey{StorageNamespace: "inst:500", UTCDay: "2026-09-27"},
	}))
	noAuth := p.Plan(PlanRequest{
		RequestID: "no-authority",
		TargetIDs: []string{"inst:500"},
		TimeRange: TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"},
	})
	require.Nil(t, noAuth.Err)
	require.Nil(t, coverageEntryFor(noAuth.Coverage, "inst:500"),
		"无权威副本的授权目标不得被发明成空成功")
}

// 回归（F1，2026-10-12 终审修复）：视图**复用**路径绝不得把「视图未覆盖、有数据却未读」的
// 授权目标补成**空成功覆盖**——那是把「有数据未读」粉饰成「空」，会让 complete 判 true，
// CP 的 BuildCoverage 据此放行导出附件，用户拿到「成功」附件却静默缺失该源。
//
// 现场形态：视图冻结（view.TargetIDs/TargetCoverage 在创建时定死）之后，某授权目标才
// 在 Catalog 里获得可查记录——例如 ① 视图创建后新源开始写日志；② 复用请求把授权集合**放宽**。
// 这两种情形下主循环**不会为它产出任何 range**（它不在 view.TargetIDs 里），本次查询从未读过
// 它的行，故必须**显式判未就绪**。
//
// 为什么"沉默"不够（2026-10-12 实测教训）：CP 侧 `absentTargetReasons` 在响应非空时返回 nil，
// `CoverageFromReadiness` 会给 ReadyOnline 的未上报目标判 **success**（targets_absent_test.go
// 固定的既有语义：「未出现 = 该目标无数据」）⇒ 沉默同样会被读成空成功。故本用例同时锁定
// 「必须有条目」与「必须是 not_ready」两件事。
//
// 转红方式（实测）：在 planRangesForView 末尾恢复 `p.coverEmptyAuthorizedTargets(req, view, res, false)`
// ⇒ 断言②（状态必须是 not_ready 而非 success）与断言③（complete 必须为 false）立即红；
// 改成什么都不做（仅删除该行）⇒ 断言②（必须有条目）立即红——两条路都被本用例封死。
func TestPlanner_ReusePathMustNotFabricateEmptySuccessForUnplannedTarget(t *testing.T) {
	cat := catalog.New(nil)
	require.NoError(t, cat.Put(catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:160", UTCDay: "2026-10-11"}, catalog.OwnerHot, 1, "hot-160")))

	tr := TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"}
	p := NewPlanner(cat, nil)

	// 创建视图：此刻 inst:170 在 Catalog 里**还没有**任何记录 ⇒ 它不进 view.TargetIDs，
	// 也不进 view.TargetCoverage（创建路径的补空判据②不成立）。
	created := p.Plan(PlanRequest{
		RequestID: "create-before-source-appears",
		TargetIDs: []string{"inst:160", "inst:170"},
		TimeRange: tr,
	})
	require.Nil(t, created.Err)
	require.Nil(t, coverageEntryFor(created.Coverage, "inst:170"),
		"前置：创建时无 Catalog 记录的目标不得被补空成功")

	// 视图冻结后 inst:170 才有可查记录（新源开始写日志），且它**仍在**本次授权集合内。
	require.NoError(t, cat.Put(catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:170", UTCDay: "2026-10-11"}, catalog.OwnerHot, 1, "hot-170")))

	reused := p.Plan(PlanRequest{
		RequestID: "reuse-after-source-appears",
		TargetIDs: []string{"inst:160", "inst:170"},
		TimeRange: tr,
		ViewRef:   &ViewRef{ViewID: created.View.ViewID, OrderVersion: SortVersion},
	})
	require.Nil(t, reused.Err)

	// ① 复用路径不得为该目标读行：它不在 view.TargetIDs 里，主循环不会产出它的 range。
	for _, rng := range reused.Ranges {
		require.NotEqual(t, "inst:170/2026-10-11", rng.TargetID,
			"视图未登记的目标不得被读出 range（视图冻结语义）")
	}
	// ② 核心断言：必须**显式**给出未就绪条目——既不能补空成功（粉饰），也不能沉默（CP 会判 success）。
	unplanned := coverageEntryFor(reused.Coverage, "inst:170")
	require.NotNil(t, unplanned,
		"复用路径必须显式上报未规划目标（沉默会被 CP 按 readiness 判 success，等同粉饰）")
	require.NotEqual(t, CoverageSuccess, unplanned.State,
		"视图未覆盖、本次未读取的目标绝不得上报空成功")
	require.Equal(t, CoverageNotReady, unplanned.State,
		"未规划目标必须判未就绪，让 CP 的 BuildCoverage 判 coverage_incomplete")
	// ③ 连带后果：整体覆盖不得判成完整（否则导出会放行一份静默缺失该源的附件）。
	require.False(t, reused.Coverage.Complete,
		"存在未覆盖的授权目标时 complete 必须为 false（否则导出会放行不完整附件）")
	require.Contains(t, reused.Coverage.PartialReasons, ReasonRangeUnavailable,
		"未就绪原因必须随覆盖上报，供 CP 归一化为可读的 partial reason")
}
