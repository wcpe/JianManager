package query

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// testNow 是归档/清除迁移使用的时间戳。
//
// 刻意**不用** 1970 基准：Catalog 的 ActiveLeaseCount 与 eligibility 判定都用真实
// 挂钟比较，一个远古时间戳会让「有没有活跃租约」的语义变成偶然——而这里要测的是
// 清除标记对查询面的影响，不是时间轴本身。
func testNow() time.Time { return time.Now().UTC() }

// 本文件守「归档对象已按保留期清除」在查询面上的**诚实性**（FR-501）。
//
// 缺陷形态只有一种，但它极其危险：删掉归档对象之后仍按 ARCHIVED 未恢复处理。
// 那会让查询面返回 LOG_ARCHIVE_MISSING，也就是「归档还在、只是本地没恢复，
// 去 rehydrate 一下」。用户点恢复 ⇒ 必然失败 ⇒ 收到第二条错误 ⇒ 最后要人来解释
// 「其实平台自己按保留期删掉了」。**平台对自身动作撒谎**是最伤信任的一类缺陷，
// 所以这里既不能返回空成功（0 行会被读成「那天没有日志」），也不能复用 MISSING。

// purgedArchiveRecord 构造一条「已归档、热层已回收、归档对象已按保留期清除」的记录。
//
// 刻意**不带** PublishedProjection：已清除的分区按设计不会有本地已恢复投影
// （那正是清除的前置条件之一），带一个只会造出违规形态。
func purgedArchiveRecord(t *testing.T) (*catalog.Catalog, catalog.PartitionKey) {
	t.Helper()
	key := catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-01"}
	cat := catalog.New(nil)
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20260901")
	require.NoError(t, cat.Put(rec))
	_, _, err := cat.MarkArchived(key, catalog.ArchiveEvidence{
		ProviderKind:     "local",
		ObjectPrefix:     "ns/game-1/2026-09-01/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage:         catalog.ArchiveCoverage{ObjectCount: 2, Complete: true},
	}, testNow())
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(key, testNow())
	require.NoError(t, err)
	_, _, err = cat.MarkArchiveObjectPurgeRequested(key, "已过保留期", testNow())
	require.NoError(t, err)
	_, err = cat.MarkArchiveObjectsPurged(key, "已过保留期", testNow())
	require.NoError(t, err)

	rec, ok := cat.Get(key)
	require.True(t, ok, "前置条件：记录必须仍在")
	require.True(t, rec.ArchiveEvidence.Purged, "前置条件：必须是已清除形态")
	return cat, key
}

// 契约：已清除的分区在 planning 层**不再**产出 ARCHIVE_NOT_RESTORED，而是 ARCHIVE_PURGED。
//
// 反例（本用例咬的缺陷）：若 planner 仍走「ARCHIVED 且未恢复」分支追加
// ARCHIVE_NOT_RESTORED，noRangeError 就会返回 LOG_ARCHIVE_MISSING——平台会劝
// 用户去点一个必然失败的恢复按钮。本用例同时断言「不得返回空成功」的反面：
// 覆盖必须显式不完整（Complete=false），因为这段日志确实不在了。
func TestPlanner_PurgedArchiveReportsPurgedNotMissing(t *testing.T) {
	cat, key := purgedArchiveRecord(t)
	planner := NewPlanner(cat, nil)

	res := planner.Plan(PlanRequest{RequestID: "purged", TargetIDs: []string{key.StorageNamespace}})
	require.Nil(t, res.Err)
	require.Empty(t, res.Ranges, "已清除的分区绝不能产出可读 range")
	require.False(t, res.Coverage.Complete, "数据确实不在，覆盖不得报告为完整（那就是空成功）")
	require.Contains(t, res.Coverage.PartialReasons, ReasonArchivePurged)
	require.NotContains(t, res.Coverage.PartialReasons, ReasonArchiveNotRestored,
		"已清除的分区不得再声称「未恢复」——那条建议是死路")

	require.Len(t, res.Coverage.Targets, 1)
	target := res.Coverage.Targets[0]
	require.Equal(t, CoverageArchiveMiss, target.State)
	require.Contains(t, target.Reasons, ReasonArchivePurged)
	require.NotContains(t, target.Reasons, ReasonArchiveNotRestored)
}

// 契约（关键）：Search 无可读 range 时的错误码必须是 LOG_ARCHIVE_PURGED。
//
// 这一条断言的是**用户真正看到的东西**：错误码 + 文案。
// 复用 LOG_ARCHIVE_MISSING 会让前端把它映射成「历史日志尚未取回」，
// 于是用户按提示去恢复、恢复失败、再报错——而真相是这段日志已经被平台
// 按保留期删掉了，任何操作都取不回来。
func TestNoRangeError_PurgedArchiveIsDistinctFromMissing(t *testing.T) {
	purged := noRangeError(Coverage{PartialReasons: []string{ReasonArchivePurged}})
	require.Equal(t, ErrCodeArchivePurged, purged.Code)
	require.Contains(t, purged.Message, "保留期")
	require.NotContains(t, purged.Message, "rehydrate", "已清除的文案不得再引导用户去恢复")

	missing := noRangeError(Coverage{PartialReasons: []string{ReasonArchiveNotRestored}})
	require.Equal(t, ErrCodeArchiveMissing, missing.Code, "未恢复的语义不得被顺带改掉")

	// 两者混在一起时以「已清除」为准：那是更精确、且处置动作相反的那个。
	mixed := noRangeError(Coverage{PartialReasons: []string{ReasonArchiveNotRestored, ReasonArchivePurged}})
	require.Equal(t, ErrCodeArchivePurged, mixed.Code,
		"同时出现两种原因时必须报已清除（否则用户会被引向必然失败的恢复）")

	// 其余情况保持既有语义（不把预算截断误报成归档问题）。
	other := noRangeError(Coverage{PartialReasons: []string{ReasonBudgetExceeded}})
	require.Equal(t, ErrCodePartial, other.Code)
}

// 契约：DefaultPartitionStatus 对已清除分区仍判「不可查」，且原因不是 MISSING。
//
// 这条钉的是「默认状态推导」这条独立路径：planner 的归档分支之外，
// status 函数也会给出一份 NotReadyReasons，两处措辞必须一致（同一事实不应有两种说法）。
// 断言的正是「那份 NotReadyReasons 里不得出现 ARCHIVE_NOT_RESTORED」——
// 只要它漏进去，classifyStatus 就会把这个分区同时说成「已清除」和「请去恢复」。
func TestDefaultPartitionStatus_PurgedArchiveIsNotQueryable(t *testing.T) {
	cat, key := purgedArchiveRecord(t)
	rec, ok := cat.Get(key)
	require.True(t, ok)
	ref, _ := cat.OwnerForQuery(key)

	st := DefaultPartitionStatus(key, rec, ref)
	require.False(t, st.Queryable, "已清除的分区不可查（数据不在了）")
	require.False(t, st.ArchiveRestored)
	require.Contains(t, st.NotReadyReasons, ReasonArchivePurged,
		"状态推导必须给出「已按保留期清除」这个原因")
	require.NotContains(t, st.NotReadyReasons, ReasonArchiveNotRestored,
		"状态推导不得把已清除的分区说成「未恢复」")
}

// 契约：清除标记**同时**在两条路径上生效（planner 分支 + status 函数）。
//
// 为什么需要这条「双路径」断言：planner 的归档分支用 lead 换了措辞，
// 但 classifyStatus 与 status 函数是另外两条独立的追加点——只要其中一处漏判，
// 最终 PartialReasons 里就会出现「已清除 + 请去恢复」并存，
// 而 noRangeError 是按 ARCHIVE_NOT_RESTORED 决定错误码的那一侧。
// 本用例把最终 Coverage 当作唯一断言对象（用户看到的就是它）。
func TestPlanner_PurgedArchiveNeverCoexistsWithNotRestored(t *testing.T) {
	for _, mode := range []struct {
		name string
		req  PlanRequest
	}{
		{name: "默认请求", req: PlanRequest{RequestID: "p1", TargetIDs: []string{"ns/game-1"}}},
		{name: "显式要求归档层", req: PlanRequest{RequestID: "p2", TargetIDs: []string{"ns/game-1"}, RequireArchive: true}},
		{name: "同时要求冷层与归档层", req: PlanRequest{RequestID: "p3", TargetIDs: []string{"ns/game-1"}, RequireCold: true, RequireArchive: true}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			cat, _ := purgedArchiveRecord(t)
			planner := NewPlanner(cat, nil)
			res := planner.Plan(mode.req)
			require.Contains(t, res.Coverage.PartialReasons, ReasonArchivePurged)
			require.NotContains(t, res.Coverage.PartialReasons, ReasonArchiveNotRestored,
				"已清除与「请去恢复」不能并存（那会让用户看到两条互斥的引导）")
			for _, target := range res.Coverage.Targets {
				require.NotContains(t, target.Reasons, ReasonArchiveNotRestored)
			}
			// 用户最终看到的错误码必须可区分。
			require.Equal(t, ErrCodeArchivePurged, noRangeError(res.Coverage).Code)
		})
	}
}

// 契约：**没有**清除标记的归档分区仍按「未恢复」处理（不能顺手改掉既有语义）。
//
// 反向守门：如果哪天有人把「ARCHIVED 一律报 ARCHIVE_PURGED」，这个用例会立刻转红——
// 那种改动会把「其实还能救」的分区谎报成「救不回来了」，比原缺陷更糟。
func TestPlanner_ArchivedWithoutPurgeStillReportsNotRestored(t *testing.T) {
	key := catalog.PartitionKey{StorageNamespace: "ns/game-2", UTCDay: "2026-09-02"}
	cat := catalog.New(nil)
	require.NoError(t, cat.Put(queryableRecord(key, catalog.OwnerArchive, 3, "arch-2")))

	planner := NewPlanner(cat, nil)
	res := planner.Plan(PlanRequest{RequestID: "arch", TargetIDs: []string{key.StorageNamespace}})
	require.Empty(t, res.Ranges)
	require.Contains(t, res.Coverage.PartialReasons, ReasonArchiveNotRestored)
	require.NotContains(t, res.Coverage.PartialReasons, ReasonArchivePurged,
		"没有清除标记的分区不得被报成已清除")

	require.Equal(t, ErrCodeArchiveMissing,
		noRangeError(res.Coverage).Code)
}
