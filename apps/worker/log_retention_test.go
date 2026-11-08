package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
)

func retentionCatalogFixture(t *testing.T) (*catalog.Catalog, []catalog.PartitionKey) {
	t.Helper()
	cat := catalog.New(catalog.NewMemJournal())
	keys := []catalog.PartitionKey{{StorageNamespace: "inst:1", UTCDay: "2026-01-01"}, {StorageNamespace: "node:1", UTCDay: "2026-01-01"}}
	for _, key := range keys {
		r := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-20260101")
		r.PublishedProjection = &catalog.PublishedProjection{CoverageComplete: true, QueryLocationDirID: r.OwnerDirID, QueryGeneration: 1}
		require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, State: catalog.StateCleaned, Owner: r.Owner, Generation: 1, DirID: r.OwnerDirID, AuthorityCommit: true, JournalComplete: true, Record: r}))
	}
	return cat, keys
}

// 走真实 Catalog、DayManager、生产适配器，不能用假 Mover 的成功掩盖续跑断线。
// 正常 pending 不能因 Owner 或 InFlight 被驱动器永久跳过。
// 列举与执行之间人工接管，Mover 必须重新读真源，不得按旧候选覆盖手工失败。
// —— 归档权威态适配器（newCatalogArchiveAuthority）的固定点 ——
//
// 这一层只做「retention 抽象 → catalog 真源」的字段搬运，但它决定了两件
// 安全相关的事：日期归一（否则会冻结一天、归档另一天）与冻结/解冻的成对性
// （只冻结不解冻会让该物理日永久拒绝写入）。

// TestArchiveAuthorityNormalizesDayFormat Catalog 用 `2006-01-02`，retention 传 `20060102`。
func TestArchiveAuthorityNormalizesDayFormat(t *testing.T) {
	cat, _ := retentionCatalogFixture(t)
	auth := newCatalogArchiveAuthority(cat)
	require.NotNil(t, auth)

	// 用 retention 的紧凑写法调用，验证落到 Catalog 的规范化日期上。
	require.NoError(t, auth.(interface {
		FreezeForArchive(context.Context, string, string) error
	}).FreezeForArchive(context.Background(), "inst:1", "20260101"))

	// 冻结的是 `2026-01-01` 这一天（若写法未归一，这里会是 false）。
	require.True(t, cat.ArchivedDay("2026-01-01"), "冻结必须落在 Catalog 的规范日期写法上")
	require.False(t, cat.ArchivedDay("20260101"), "不得另造一个紧凑写法的日记录")
}

// TestArchiveAuthorityFreezeReleaseIsPair 冻结与解冻必须成对（否则该日永久拒写）。
func TestArchiveAuthorityFreezeReleaseIsPair(t *testing.T) {
	cat, keys := retentionCatalogFixture(t)
	auth := newCatalogArchiveAuthority(cat).(interface {
		FreezeForArchive(context.Context, string, string) error
		ReleaseArchiveFreeze(context.Context, string, string, string) error
	})
	ctx := context.Background()
	require.NoError(t, auth.FreezeForArchive(ctx, keys[0].StorageNamespace, "20260101"))
	_, err := cat.WriteAdmission(keys[0])
	require.Error(t, err, "冻结期该日必须拒绝写入")

	require.NoError(t, auth.ReleaseArchiveFreeze(ctx, keys[0].StorageNamespace, "20260101", "导出失败"))
	require.False(t, cat.ArchivedDay("2026-01-01"), "解冻后不得仍处于归档冻结")
	_, err = cat.WriteAdmission(keys[0])
	require.NoError(t, err, "解冻后该日必须重新接受写入")
}

// TestArchiveAuthorityKeepsFreezeWhileDayHasArchivedMember 该日仍有归档成员时保持冻结。
//
// 为什么不能解冻：一天里有命名空间已归档、另一个还是 HOT，此时任何写入都会
// 落进「正准备被删的热层目录」，随后删目录就把这批新数据一起删掉。
func TestArchiveAuthorityKeepsFreezeWhileDayHasArchivedMember(t *testing.T) {
	cat, keys := retentionCatalogFixture(t)
	auth := newCatalogArchiveAuthority(cat).(interface {
		FreezeForArchive(context.Context, string, string) error
		MarkArchived(context.Context, string, string, retention.ArchiveEvidence) error
		ReleaseArchiveFreeze(context.Context, string, string, string) error
	})
	ctx := context.Background()
	require.NoError(t, auth.FreezeForArchive(ctx, keys[0].StorageNamespace, "20260101"))
	require.NoError(t, auth.MarkArchived(ctx, keys[0].StorageNamespace, "20260101", retention.ArchiveEvidence{
		ProviderKind: "local", ObjectPrefix: "inst:1/2026-01-01/g1", ManifestChecksum: "sha256:m",
		ObjectCount: 1, SourceBytes: 8, StoredBytes: 4, CoverageComplete: true,
	}))

	require.NoError(t, auth.ReleaseArchiveFreeze(ctx, keys[1].StorageNamespace, "20260101", "另一个命名空间导出失败"))
	require.True(t, cat.ArchivedDay("2026-01-01"), "同日仍有归档成员时必须保持冻结")
}

// TestArchiveReadyForReclaimRequiresAllThree 三闸/权威态/删除出口缺一即不就绪。
func TestArchiveReadyForReclaimRequiresAllThree(t *testing.T) {
	p := retention.DefaultPolicy()
	p.Enabled = true
	p.ArchiveCopy.Enabled = true
	ex := retention.NewArchiveExecutor(p, nil)

	ok, reason := ex.ArchiveReadyForReclaim()
	require.False(t, ok)
	require.Contains(t, reason, "discard")

	p.Discard = true
	p.Sweep.VLSweep = true
	p.Sweep.VLDeleteEnabled = true
	ex.SetPolicy(p)
	ok, reason = ex.ArchiveReadyForReclaim()
	require.False(t, ok, "三闸开了但没有权威态出口仍不就绪")
	require.Contains(t, reason, "权威态")

	cat, _ := retentionCatalogFixture(t)
	ex.SetArchiveAuthority(newCatalogArchiveAuthority(cat))
	ok, reason = ex.ArchiveReadyForReclaim()
	require.False(t, ok, "还缺删除出口")
	require.Contains(t, reason, "删除出口")
}
