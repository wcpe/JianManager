package ingest

// 本文件是「清空冲突后对齐分区派生计数必须 CAS 写回」的回归（并发缺陷）。
//
// 缺陷（已确证）：resetNamespaceConflictCounts 早前是「Get 快照 → 克隆 → 基于旧代次自增 →
// AppendJournal 无条件覆盖」。它由 republishAfterConflictClear 在**不持 cycleMu** 的情况下
// 调用，而采集轮 pollOnce 正持着 cycleMu 在跑；catalog.AppendJournal 只加锁追加、从不比对
// 代次/序号。于是在「读」与「写回」之间只要发生过一次真实发布（甚至一次 OWNER_SWITCHED），
// 旧快照就会把它覆盖掉：代次/权威回退、真实 ConflictCount 被误清零（前端误报「无冲突」）。
//
// 本文件把「真实发布」做成**确定性事件**（不靠 sleep 抢窗口）：
//   - 用例 1：真实**权威切换**（OWNER_SWITCHED，代次前进）后，用陈旧快照对齐 ⇒ 必须被拒，
//     真实发布的 owner/目录/代次/计数一字不动；
//   - 用例 2：真实**投影发布**（代次不变、ConflictCount 被重算为 7）后，用陈旧快照对齐 ⇒
//     必须被拒，真实计数不得被清零；
//   - 用例 3：无并发时对齐照常生效，且**代次不前移也不回退**（派生计数对齐不是新权威代次），
//     其它 namespace 不牵连、重复调用幂等（不产生 journal 噪音）；
//   - 用例 4：某个分区不满足发布前置条件时只跳过它，其余分区仍被对齐（不 panic、不中断）。
//
// 为什么「代次不前移」而不是旧实现的 +1：catalog 契约里 Generation 是查询/写入权威代次，
// 只有 OWNER_SWITCHED 能变更，PublishedProjection.QueryGeneration 必须恒等于它；
// +1 却把 QueryGeneration 留在旧值，实测（可复现）会让该分区之后**任何一次真实发布都被
// PublishProjection 前置校验拒绝**，且查询侧把它的投影判成「未发布」。
// 用例 3 末尾把「重放后状态一致、QueryGeneration 仍等于代次」钉住。
//
// 变异验证（实测输出见报告）：把写回从 PublishProjection 改回无条件 AppendJournal
// ⇒ 用例 1/2 转红（真实发布被旧快照覆盖），用例 4 一并转红（前置校验被绕过）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// casTestRecord 造一条「已发布投影」的稳定 HOT 分区记录（ConflictCount=conflicts）。
// 形态与 publishSnapshot 产出的记录一致：QueryGeneration == Generation、
// QueryLocationDirID == OwnerDirID —— 这是 PublishProjection 的前置校验要求。
func casTestRecord(ns, day string, gen, conflicts uint64) *catalog.Record {
	dirID := "hot-projection-1"
	rec := catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: ns, UTCDay: day}, catalog.OwnerHot, gen, dirID)
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:      "manifest-" + day,
		ProjectionGeneration: "projection-1",
		CoverageComplete:     true,
		ConflictCount:        conflicts,
		QueryLocationDirID:   dirID,
		QueryGeneration:      gen,
	}
	return rec
}

// TestConflictCountResetRejectsStaleSnapshotAfterOwnerSwitch 覆盖「真实权威切换赢在中间」。
//
// 这是最坏形态：陈旧快照里还写着 owner=hot/代次 3，而无条件覆盖会连 owner 与目录一起写回，
// 等于把一次已提交的 OWNER_SWITCHED 抹掉。
func TestConflictCountResetRejectsStaleSnapshotAfterOwnerSwitch(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())
	const ns = "inst:cas-switch"
	key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: "2026-10-05"}
	require.NoError(t, cat.Put(casTestRecord(ns, key.UTCDay, 3, 3)))
	m := &Manager{cat: cat}

	// 对齐前读到的快照：这正是「读」与「写回」之间会被真实发布超越的那一份。
	stale, ok := cat.Get(key)
	require.True(t, ok)
	require.EqualValues(t, 3, stale.Generation)
	require.EqualValues(t, 3, stale.PublishedProjection.ConflictCount)

	// 模拟中间发生了一次真实发布并推进代次：现网形态只能是 OWNER_SWITCHED（hot → cold）。
	_, err := cat.BeginMigration(key, catalog.OwnerCold, "cold-projection-4")
	require.NoError(t, err)
	for _, state := range []catalog.MigrationState{
		catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify, catalog.StateAttachedStaging,
	} {
		_, err := cat.Advance(key, state)
		require.NoError(t, err)
	}
	_, err = cat.SwitchOwner(key, catalog.OwnerCold, "cold-projection-4", 4, &catalog.PublishedProjection{
		ManifestVersion:      "manifest-real-publish",
		ProjectionGeneration: "cold-projection-4",
		CoverageComplete:     true,
		ConflictCount:        7, // 那次真实发布算出的计数：不得被陈旧快照清零
		QueryLocationDirID:   "cold-projection-4",
		QueryGeneration:      4,
	})
	require.NoError(t, err)

	// 陈旧快照的写回必须被 CAS 拒绝（catalog.ErrInvalidState = 并发冲突）。
	before, err := m.alignPartitionConflictCount(stale, 0)
	require.ErrorIs(t, err, catalog.ErrInvalidState, "陈旧快照不得覆盖更晚的真实发布")
	require.EqualValues(t, 3, before, "失败时也要如实返回对齐前的值，供日志记录 from")

	current, ok := cat.Get(key)
	require.True(t, ok)
	require.EqualValues(t, 4, current.Generation, "真实发布的代次不得回退")
	require.Equal(t, catalog.OwnerCold, current.Owner, "真实发布的 owner 不得被旧快照写回")
	require.Equal(t, "cold-projection-4", current.OwnerDirID, "真实发布的目录不得被旧快照写回")
	require.EqualValues(t, 7, current.PublishedProjection.ConflictCount, "真实发布的冲突计数不得被误清零")
	require.Equal(t, "manifest-real-publish", current.PublishedProjection.ManifestVersion)
}

// TestConflictCountResetRejectsStaleSnapshotAfterConcurrentPublish 覆盖「同代次的真实发布赢在中间」。
//
// 这是采集轮最常见的中断形态：代次不变（投影发布不得改权威），但 ConflictCount 被重算成别的值。
// 判据只能落在 journal 序号上——PublishProjection 的 CAS 正是这么比的。
func TestConflictCountResetRejectsStaleSnapshotAfterConcurrentPublish(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())
	const ns = "inst:cas-publish"
	key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: "2026-10-06"}
	require.NoError(t, cat.Put(casTestRecord(ns, key.UTCDay, 3, 3)))
	m := &Manager{cat: cat}

	stale, ok := cat.Get(key)
	require.True(t, ok)

	// 中间的一次真实投影发布：代次不变、清单与计数被重算（这里写成 7）。
	realNext := stale.Clone()
	realNext.PublishedProjection.ConflictCount = 7
	realNext.PublishedProjection.ManifestVersion = "manifest-real-publish"
	require.NoError(t, cat.PublishProjection(stale, realNext))

	if _, err := m.alignPartitionConflictCount(stale, 0); err == nil {
		t.Fatalf("陈旧快照必须被 CAS 拒绝，实际 err=nil")
	}

	current, ok := cat.Get(key)
	require.True(t, ok)
	require.EqualValues(t, 3, current.Generation, "派生计数对齐/被拒绝都不得改动代次")
	require.EqualValues(t, 7, current.PublishedProjection.ConflictCount, "并发发布的计数不得被陈旧快照清零")
	require.Equal(t, "manifest-real-publish", current.PublishedProjection.ManifestVersion)
	require.EqualValues(t, current.Generation, current.PublishedProjection.QueryGeneration,
		"QueryGeneration 必须与代次保持一致（否则查询侧判「投影未发布」）")
}

// TestResetNamespaceConflictCountsAlignsAllPartitionsWithoutConcurrency 覆盖无并发时的正常对齐。
//
// 断言：计数被置为 want、代次不变（不自增也不回退）、QueryGeneration 保持一致、
// 其它 namespace 不牵连、重复调用幂等、journal 重放仍是同一状态。
func TestResetNamespaceConflictCountsAlignsAllPartitionsWithoutConcurrency(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())
	const ns = "inst:cas-align"
	type partition struct {
		day    string
		gen    uint64
		before uint64
	}
	partitions := []partition{
		{day: "2026-09-20", gen: 3, before: 3},
		{day: "2026-10-05", gen: 2, before: 5},
	}
	for _, p := range partitions {
		require.NoError(t, cat.Put(casTestRecord(ns, p.day, p.gen, p.before)))
	}
	// 另一个 namespace 的分区不得被牵连。
	otherKey := catalog.PartitionKey{StorageNamespace: "inst:other", UTCDay: "2026-09-20"}
	require.NoError(t, cat.Put(casTestRecord(otherKey.StorageNamespace, otherKey.UTCDay, 1, 4)))
	m := &Manager{cat: cat}

	changed, failures := m.resetNamespaceConflictCounts(ns, 0)
	require.Equal(t, len(partitions), changed,
		"该 namespace 下所有未对齐分区都应被改写")
	require.Empty(t, failures, "无并发、投影自洽 ⇒ 不应有刷新失败项")

	for _, p := range partitions {
		rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: ns, UTCDay: p.day})
		require.True(t, ok)
		require.Zero(t, rec.PublishedProjection.ConflictCount, "派生计数必须被置为 want")
		require.EqualValues(t, p.gen, rec.Generation, "派生计数对齐不得推进或回退权威代次")
		require.EqualValues(t, rec.Generation, rec.PublishedProjection.QueryGeneration,
			"QueryGeneration 必须与代次保持一致")
		require.Equal(t, "manifest-"+p.day, rec.PublishedProjection.ManifestVersion, "投影清单不得被改写")
	}

	other, ok := cat.Get(otherKey)
	require.True(t, ok)
	require.EqualValues(t, 4, other.PublishedProjection.ConflictCount, "其它 namespace 的分区不得被牵连")

	seqAfterFirst := cat.Journal().LastSeq()
	require.EqualValues(t, len(partitions), seqAfterFirst, "每个被对齐的分区各落一条 journal")
	changedAgain, failuresAgain := m.resetNamespaceConflictCounts(ns, 0)
	require.Zero(t, changedAgain, "幂等：已对齐的分区不应再产生 journal 项")
	require.Empty(t, failuresAgain, "幂等调用不应产生失败项（已对齐 ≠ 刷新失败）")
	require.EqualValues(t, seqAfterFirst, cat.Journal().LastSeq(), "幂等调用不得产生 journal 噪音")

	// 重启重放后仍是同一状态：派生计数为 want、代次与 QueryGeneration 一致。
	// （若像旧实现那样把 Generation +1 却把 QueryGeneration 留在旧值，重放会把这份不自洽
	// 一起带进内存，之后任何一次真实发布都会被 PublishProjection 前置校验拒绝。）
	for _, p := range partitions {
		key := catalog.PartitionKey{StorageNamespace: ns, UTCDay: p.day}
		replayed := &catalog.Record{Key: key}
		require.Empty(t, catalog.ReplayJournal(replayed, cat.Journal().Entries(key)))
		require.EqualValues(t, p.gen, replayed.Generation)
		require.Zero(t, replayed.PublishedProjection.ConflictCount)
		require.EqualValues(t, replayed.Generation, replayed.PublishedProjection.QueryGeneration)
	}
}

// TestResetNamespaceConflictCountsSkipsInconsistentPartition 覆盖「个别分区过不了前置校验」。
//
// 形态：该分区的已发布投影本身不自洽（QueryGeneration != Generation，历史遗留），
// PublishProjection 的前置校验会拒绝——不是并发冲突，同样只告警跳过。
// 断言：其余分区照常对齐，坏分区保持原样（它的投影本就不自洽，查询侧已按「未发布」处理），
// 且整轮不 panic、不中断；**并且坏分区必须出现在失败清单里**（缺陷 B7：这些分区若属历史
// 日期，不会再有任何投递来触发发布，只告警等于永久沉默，运维无从知道要人工介入）。
func TestResetNamespaceConflictCountsSkipsInconsistentPartition(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())
	const ns = "inst:cas-skip"

	broken := casTestRecord(ns, "2026-09-20", 3, 3)
	broken.PublishedProjection.QueryGeneration = 2 // 与代次失配：PublishProjection 前置校验必拒
	require.NoError(t, cat.Put(broken))

	goodDay := "2026-10-05"
	require.NoError(t, cat.Put(casTestRecord(ns, goodDay, 2, 5)))
	m := &Manager{cat: cat}

	changed, failures := m.resetNamespaceConflictCounts(ns, 0)
	require.Equal(t, 1, changed,
		"只有能通过校验的分区被对齐，其余跳过且不中断整轮")
	require.Len(t, failures, 1, "坏分区必须如实出现在失败清单里，不能被静默跳过")
	require.Contains(t, failures[0], broken.Key.UTCDay, "失败项必须指明是哪个分区")

	stillBroken, ok := cat.Get(broken.Key)
	require.True(t, ok)
	require.EqualValues(t, 3, stillBroken.PublishedProjection.ConflictCount, "坏分区不得被硬改")
	require.EqualValues(t, 2, stillBroken.PublishedProjection.QueryGeneration, "坏分区字段保持原样")

	aligned, ok := cat.Get(catalog.PartitionKey{StorageNamespace: ns, UTCDay: goodDay})
	require.True(t, ok)
	require.Zero(t, aligned.PublishedProjection.ConflictCount, "其余分区仍必须被正常对齐")
}
