package ingest

// 本文件是「清空身份冲突」缺陷 B7（🟠 用户可见的假成功）的回归。
//
// 缺陷（只读复审确证）：`resetNamespaceConflictCounts` / `republishAfterConflictClear` 对
// 「派生计数（conflict_count）没能刷新」只打一行 Warn 就 continue，`ClearIdentityConflicts`
// 依旧 `return result, nil`，而结果结构里**没有**记录失败的字段。于是：
//
//	某分区处于 `QueryGeneration != Generation` 的不一致态（生产实测 inst:320 有 28 个）⇒
//	`alignPartitionConflictCount` 走 `PublishProjection` 一进门就被
//	`projection and authority do not match` 拒 ⇒ 只 Warn ⇒ API 返回 clearedCount=3、无 error
//	⇒ 运维以为已生效，但该 namespace 下历史分区的 conflict_count 仍是旧值，
//	查询覆盖历史日期时仍报 duplicate_quality=conflict、导出继续被拦。
//
// 更糟的是**历史分区不会再有任何投递**去触发 `publishSnapshot`，所以 Warn 里那句
// 「待下次投递刷新」对它们是**永久性**的，没有自发重试路径。
//
// 修法（本文件的判据）：失败项必须进入结果结构 `DerivedRefreshFailures`，使
// 「冲突已清空」与「派生计数已对齐」在对外结果上可区分；冲突本身仍然算清空成功
// （不因此返回 error，那部分是**真成功**），既有逐条 Warn 日志保留。
//
// 变异验证（实测输出见报告）：把 `republishAfterConflictClear` 里的失败项从结果去掉
// （改回「只 Warn、不返回」）⇒ 本文件用例转红。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// TestClearIdentityConflictsReportsDerivedRefreshFailures 覆盖「一个坏分区 + 一个正常分区」。
//
// 夹具形态（与生产逐字同构）：
//   - `inst:145`/2026-09-20：正常分区，conflict_count=5 ⇒ 必须被对齐成 0；
//   - `inst:145`/2026-09-25：`QueryGeneration(2) != Generation(3)` ⇒
//     `PublishProjection` 前置校验必拒 ⇒ 必须出现在 `DerivedRefreshFailures` 里。
//
// 断言两条，缺一不可：
//  1. 正常分区被对齐（证明刷新流程本身在跑，不是整体失效）；
//  2. 结果 `DerivedRefreshFailures` 非空且包含坏分区（证明失败**对调用方可观测**）。
//
// 同时钉住语义边界：`err` 必须是 nil、`ClearedCount` 必须照旧——冲突记录确实清空了，
// 这是真成功，不能因为派生字段没刷上就把整个操作报成失败（那会让运维不知该不该重试）。
func TestClearIdentityConflictsReportsDerivedRefreshFailures(t *testing.T) {
	fixture := newConflictClearFixture(t, 3)
	m, key, source := fixture.manager, fixture.key, fixture.source
	require.Equal(t, "inst:145", source.StorageNamespace, "夹具前提：源必须落在 inst:145")

	// 先真发布一次，把当前分区写进 catalog（形态与现场一致：投影里 cc=3）。
	events := ingestTestEvents(source.LogSourceID, source.SourceGeneration, 0, 2, "body")
	require.NoError(t, m.publish(source, "g-proj", events, false))

	// 历史分区 1：投影自洽，只是派生计数陈旧 ⇒ 必须被对齐。
	goodDay := "2026-09-20"
	require.NoError(t, m.cat.Put(casTestRecord("inst:145", goodDay, 3, 5)))

	// 历史分区 2：投影不自洽（QueryGeneration != Generation）⇒ 对齐必被拒。
	// 这正是生产 28 个分区的形态：任何一次真实发布都会在门槛上被挡回去。
	brokenDay := "2026-09-25"
	broken := casTestRecord("inst:145", brokenDay, 3, 4)
	broken.PublishedProjection.QueryGeneration = 2
	require.NoError(t, m.cat.Put(broken))

	result, err := m.ClearIdentityConflicts(context.Background(), key, "ops-alice",
		"已核对 VL 保留版本正确，同意清空")
	require.NoError(t, err, "冲突记录确实清空了：派生字段刷新失败不得让整个操作变成失败")
	require.Equal(t, 3, result.ClearedCount, "冲突条数照实返回（这部分是真成功）")

	// ① 正常分区必须被对齐。
	aligned, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: "inst:145", UTCDay: goodDay})
	require.True(t, ok)
	require.Zero(t, aligned.PublishedProjection.ConflictCount,
		"投影自洽的历史分区必须被对齐成 0（这是「清空后查询不再报 conflict」的正路）")

	// ② 坏分区必须出现在结果里，且指明是哪个分区。
	require.NotEmpty(t, result.DerivedRefreshFailures,
		"派生计数刷新失败必须记入结果（否则 API 只回报 clearedCount=3、无 error ⇒ 假成功）")
	require.Contains(t, joinFailures(result.DerivedRefreshFailures), brokenDay,
		"失败项必须指明是哪个分区没能刷新（运维据此定位需人工处理的分区）")

	// 坏分区本身不得被硬改（它的投影本就不自洽，硬改会掩盖真实损坏）。
	stillBroken, ok := m.cat.Get(broken.Key)
	require.True(t, ok)
	require.EqualValues(t, 4, stillBroken.PublishedProjection.ConflictCount,
		"过不了前置校验的分区不得被硬改")
	require.EqualValues(t, 2, stillBroken.PublishedProjection.QueryGeneration,
		"坏分区的投影字段必须保持原样")
}

// TestResetNamespaceConflictCountsReportsFailures 直接钉住底层返回值的契约（成功数 + 失败清单）。
//
// 为什么单独测这一层：`republishAfterConflictClear` 还要做「取权威事件 / 重发布」等动作，
// 那些在桩环境下也会产生失败项，会把「坏分区是否被报出来」这条判据搅混。这里只看对齐本身。
func TestResetNamespaceConflictCountsReportsFailures(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())
	const ns = "inst:b7"
	m := &Manager{cat: cat}

	require.NoError(t, cat.Put(casTestRecord(ns, "2026-09-20", 2, 5)))
	broken := casTestRecord(ns, "2026-09-25", 3, 4)
	broken.PublishedProjection.QueryGeneration = 2
	require.NoError(t, cat.Put(broken))

	changed, failures := m.resetNamespaceConflictCounts(ns, 0)
	require.Equal(t, 1, changed, "只有投影自洽的分区能被对齐")
	require.Len(t, failures, 1, "失败清单必须只含那个坏分区")
	require.Contains(t, failures[0], "2026-09-25", "失败项必须带分区日期")
	require.Contains(t, failures[0], "projection and authority do not match",
		"失败项必须带真实原因（运维据此判断是「待自愈」还是「需人工」）")
}

// joinFailures 把失败清单拼成一段可读文本，便于用包含关系断言分区被点名。
func joinFailures(failures []string) string {
	out := ""
	for _, f := range failures {
		out += f + "\n"
	}
	return out
}
