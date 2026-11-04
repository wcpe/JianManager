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
	require.NoError(t, cat.Put(queryableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:153", UTCDay: "2026-09-27"}, catalog.OwnerHot, 1, "hot-153")))
	require.NoError(t, cat.Put(queryableRecord(
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
	require.NoError(t, cat.Put(queryableRecord(
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
	require.NoError(t, cat.Put(queryableRecord(
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
	require.NoError(t, cat.Put(queryableRecord(
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
	require.NoError(t, cat.Put(queryableRecord(
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

// 回归（2026-11-05 生产真机）：查询窗口**整体早于**该源最早分区时，不得判「空成功」。
//
// # 故障形态
//
// VL 直查 inst:318 的 2026-11-02 有 1746 条，但接口响应是
//
//	items=0  complete=true  enumeration_state=EXHAUSTED  reasons=None
//
// 也就是一个**静默的错误空**——用户看到「这天没日志」，而数据就在那里。
//
// # 根因
//
// `coverEmptyAuthorizedTargets` 的判据 2 只问「该命名空间在**任意**一天有没有可查记录」。
// 重启后 Catalog 纯内存（2026-10-30 裁定不落盘），只有重启后补建的天（11-04）有记录；
// 查 11-02 时窗口内无分区 → 进补空逻辑 → 按命名空间找到 11-04 的记录 → 命中
// ⇒ 给 11-02 也补了「空成功」⇒ `complete=true`。
//
// # 为什么不能简单改成「窗口内必须有记录」
//
// 那会杀掉 `TestPlanner_AuthorizedTargetWithoutPartitionInWindowIsEmptySuccess`（#60）：
// 静默源 inst:153 的最后分区早于窗口，本应判空；严要求会让停产源把平台判成
// missing_coverage。所以判据取**方向**：窗口晚于源最早分区 → 空成功；
// 窗口整体早于源最早分区 → 未就绪（fail-closed）。
//
// 转红方式（实测）：删掉 `namespaceWindowPrecedesEarliestDay` 那道闸，本用例在
// 「必须是 CoverageNotReady」处红（会得到 CoverageSuccess + complete=true）。
func TestPlanner_WindowBeforeEarliestPartitionIsNotReady(t *testing.T) {
	cat := catalog.New(nil)
	// 该源只有 11-04 的记录（真机重启后的形态）。
	require.NoError(t, cat.Put(queryableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"}, catalog.OwnerHot, 1, "hot-318")))

	p := NewPlanner(cat, nil)
	// 查 11-02：窗口整体早于该源最早分区。
	res := p.Plan(PlanRequest{
		RequestID: "silent-empty-before-earliest",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-11-02T00:00:00Z", ToUTC: "2026-11-03T00:00:00Z"},
	})
	require.Nil(t, res.Err)
	require.Empty(t, res.Ranges, "该窗口内无分区 ⇒ 不产出 range")

	got := coverageEntryFor(res.Coverage, "inst:318")
	require.NotNil(t, got, "授权目标必须出现在覆盖里")
	require.Equal(t, CoverageNotReady, got.State,
		"窗口早于最早分区时本地记录无法证明「那天没有数据」⇒ 必须 fail-closed，"+
			"不得粉饰成空成功（否则用户看到「这天没日志」而 VL 里确有数据）")
	require.Contains(t, got.Reasons, ReasonNoCatalogRecord)
	require.False(t, res.Coverage.Complete,
		"静默空不得报成完整覆盖")

	// 对照：同一份 Catalog，查 11-04（窗口 == 最早分区）仍须给出 range。
	ok := p.Plan(PlanRequest{
		RequestID: "in-window",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-11-04T00:00:00Z", ToUTC: "2026-11-05T00:00:00Z"},
	})
	require.Nil(t, ok.Err)
	require.Len(t, ok.Ranges, 1, "窗口内的分区必须产出 range")
	require.Equal(t, "inst:318/2026-11-04", ok.Ranges[0].TargetID)

	// 对照 #60：窗口**晚于**最早分区（静默源）仍按空成功计入。
	silent := p.Plan(PlanRequest{
		RequestID: "silent-after",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-12-01T00:00:00Z", ToUTC: "2026-12-02T00:00:00Z"},
	})
	require.Nil(t, silent.Err)
	require.Empty(t, silent.Ranges)
	entry := coverageEntryFor(silent.Coverage, "inst:318")
	require.NotNil(t, entry)
	require.Equal(t, CoverageSuccess, entry.State,
		"窗口晚于该源最后数据时，#60 的空成功裁定不变（否则停产源会把平台判成未就绪）")
	require.True(t, silent.Coverage.Complete)
}

// 回归（2026-11-05，跨窗口场景）：窗口**横跨**该源的最早分区时，方向判据分不开
// 「该源那时还没上线」与「那天记录丢了」，必须靠「该源是否仍在采集」来判。
//
// # 为什么方向判据不够（子代理实测指出）
//
// `namespaceWindowPrecedesEarliestDay` 只看窗口终点与最早分区的先后。于是：
//
//	窗口 [11-01, 11-05)，最早分区 11-04  →  to(11-05) > earliest(11-04) → 判空成功 ✗
//
// 而窗口里的 11-01～11-03 恰恰是「没有记录的天」，其中 11-02 在 VL 里有 1746 条。
// 用户点「最近 7 天」就会踩中。
//
// # 修法
//
// 用 `EmptyTargetResolver` 问「这个源还在采集吗」：
//   - 还在采集 ⇒ 本进程可能还没重建它的分区记录（journal 纯内存、重启归零）
//     ⇒ 查不到不构成「没有数据」的证据 ⇒ fail-closed；
//   - 已停产（如 inst:153）⇒ 历史已封闭，窗口在其数据之后仍按空计入（#60）。
//
// 转红方式（实测）：把 `isActivelyCollecting` 那半条件去掉，本用例在
// 「跨窗也必须 not_ready」处红。
func TestPlanner_CrossWindowOverActivelyCollectedSourceIsNotReady(t *testing.T) {
	cat := catalog.New(nil)
	// 只有 11-04 的记录（重启后的形态）。
	require.NoError(t, cat.Put(queryableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"}, catalog.OwnerHot, 1, "hot-318")))

	p := NewPlanner(cat, nil)
	p.SetEmptyTargetResolver(func() []string { return []string{"inst:318"} })

	// 窗口横跨最早分区：11-01 ~ 11-05（含 11-04，也含无记录的 11-01~11-03）。
	res := p.Plan(PlanRequest{
		RequestID: "cross-window-collecting",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-11-01T00:00:00Z", ToUTC: "2026-11-05T00:00:00Z"},
	})
	require.Nil(t, res.Err)
	// 11-04 在窗口内 ⇒ 有 range；但这不豁免「其余天无记录」的判断。
	require.NotEmpty(t, res.Ranges, "窗口内的 11-04 必须产出 range")

	// 关键：孤立查一段没有记录、且落在「采集中的源」历史里的窗口 ⇒ 不得判空。
	only := p.Plan(PlanRequest{
		RequestID: "cross-window-gap-day",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-11-02T00:00:00Z", ToUTC: "2026-11-03T00:00:00Z"},
	})
	require.Nil(t, only.Err)
	entry := coverageEntryFor(only.Coverage, "inst:318")
	require.NotNil(t, entry)
	require.Equal(t, CoverageNotReady, entry.State,
		"源仍在采集 ⇒ 查不到不等于没有数据（journal 纯内存，重启后尚未重建该天记录）")
	require.False(t, only.Coverage.Complete, "不得把「记录未重建」粉饰成完整空结果")
}

// 反向：同一个源**已经停产**（resolver 不再返回它）时，窗口在其数据之后仍按空成功计入。
// 这条守住 #60 的裁定不被上面的 fail-closed 顺手推翻。
func TestPlanner_StoppedSourceAfterDataStillCountsEmptySuccess(t *testing.T) {
	cat := catalog.New(nil)
	require.NoError(t, cat.Put(queryableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:153", UTCDay: "2026-09-27"}, catalog.OwnerHot, 1, "hot-153")))

	p := NewPlanner(cat, nil)
	// resolver 不返回 inst:153 ⇒ 该源已停产（不在 pipes 里）。
	p.SetEmptyTargetResolver(func() []string { return []string{"inst:318"} })

	res := p.Plan(PlanRequest{
		RequestID: "stopped-source",
		TargetIDs: []string{"inst:153"},
		TimeRange: TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"},
	})
	require.Nil(t, res.Err)
	entry := coverageEntryFor(res.Coverage, "inst:153")
	require.NotNil(t, entry)
	require.Equal(t, CoverageSuccess, entry.State,
		"停产源在窗口之后没有新数据 ⇒ 空成功（#60 裁定，不得因 fail-closed 被推翻）")
	require.True(t, res.Coverage.Complete)
}

// 回归（2026-11-05，真·跨窗口）：该源有**两个**分区（10-30 与 11-04），用户查 11-02。
//
// # 为什么前一条用例守不住这个场景
//
// 前一条里该源只有 11-04 一个分区，于是「窗口 [11-02,11-03)」整体早于最早分区（11-04）
// ⇒ 方向判据自己就判了 not_ready ⇒ 去掉「仍在采集」守卫也照样绿（变异实测确认）。
//
// 真正需要采集判据的是**两分区夹击**的情形：
//
//	分区：10-30, 11-04
//	最早分区 = 10-30 ⇒ 窗口 [11-02,11-03) 并不早于它 ⇒ 方向判据判空成功 ✗
//	但 11-02 那天既无记录、又落在「采集中的源」的数据区间里
//	⇒ 只有「该源仍在采集」这条判据能拦住（VL 里 11-02 有 1746 条，正是本次事故）
//
// 这也是子代理实测指出的跨窗漏洞的**最小复现**：点「最近 7 天」就会踩中。
//
// 转红方式（实测）：把 `isActivelyCollecting` 的返回值固定为 false，本用例在
// 「必须是 CoverageNotReady」处红——它是这条守卫的唯一守卫。
func TestPlanner_MiddleGapBetweenPartitionsIsNotProvablyEmpty(t *testing.T) {
	cat := catalog.New(nil)
	// 该源有 10-30 与 11-04 两个分区，中间 11-02 那天没有记录。
	require.NoError(t, cat.Put(queryableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-10-30"}, catalog.OwnerHot, 1, "hot-318-a")))
	require.NoError(t, cat.Put(queryableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"}, catalog.OwnerHot, 2, "hot-318-b")))

	p := NewPlanner(cat, nil)
	p.SetEmptyTargetResolver(func() []string { return []string{"inst:318"} })

	res := p.Plan(PlanRequest{
		RequestID: "middle-gap",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-11-02T00:00:00Z", ToUTC: "2026-11-03T00:00:00Z"},
	})
	require.Nil(t, res.Err)
	require.Empty(t, res.Ranges, "11-02 无分区 ⇒ 无 range")

	entry := coverageEntryFor(res.Coverage, "inst:318")
	require.NotNil(t, entry)
	require.Equal(t, CoverageNotReady, entry.State,
		"窗口夹在该源两个分区之间、该天无记录、且源仍在采集 ⇒ 查不到不构成「没有数据」的证据，"+
			"必须 fail-closed（VL 里那天实有 1746 条，报空成功就是静默丢数据）")
	require.False(t, res.Coverage.Complete)

	// 对照：同一个源、同一天，若**已停产**则历史封闭，按空成功计入（#60）。
	stopped := NewPlanner(cat, nil)
	stopped.SetEmptyTargetResolver(func() []string { return nil })
	viaStopped := stopped.Plan(PlanRequest{
		RequestID: "middle-gap-stopped",
		TargetIDs: []string{"inst:318"},
		TimeRange: TimeRange{FromUTC: "2026-11-02T00:00:00Z", ToUTC: "2026-11-03T00:00:00Z"},
	})
	require.Nil(t, viaStopped.Err)
	stoppedEntry := coverageEntryFor(viaStopped.Coverage, "inst:318")
	require.NotNil(t, stoppedEntry)
	require.Equal(t, CoverageSuccess, stoppedEntry.State,
		"停产源的历史已封闭 ⇒ 那一天确实没有数据，按空成功计入")
}
