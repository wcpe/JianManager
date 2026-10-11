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
