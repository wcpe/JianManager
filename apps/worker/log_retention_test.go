package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
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
func TestRetentionDriverResumesRetryableWithoutNewBegin(t *testing.T) {
	for _, failure := range []string{"copy", "lease", "detach", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			cat, keys := retentionCatalogFixture(t)
			blocked := true
			copies, detaches, cleanups, leases := 0, 0, 0, 0
			ops := lifecycle.Ops{
				Snapshot: func(catalog.PartitionKey, string) (string, error) { return "snapshot", nil },
				Copy: func(catalog.PartitionKey, string, string) (string, error) {
					copies++
					if blocked && failure == "copy" {
						return "", errors.New("冷层不可达")
					}
					return "copy", nil
				},
				Verify: func(catalog.PartitionKey, string, string) (string, error) { return "checksum", nil },
				LeaseDrain: func(catalog.PartitionKey, uint64) error {
					leases++
					if blocked && failure == "lease" {
						return errors.New("旧查询租约未排空")
					}
					return nil
				},
				Detach: func(catalog.PartitionKey, string) error {
					detaches++
					if blocked && failure == "detach" {
						return errors.New("卸载失败")
					}
					return nil
				},
				Cleanup: func(catalog.PartitionKey, string) error {
					cleanups++
					if blocked && failure == "cleanup" {
						return errors.New("清理失败")
					}
					return nil
				},
			}
			mgr := lifecycle.NewDayManager(cat, ops, lifecycle.RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "已有校验副本" }))
			policy := retention.DefaultPolicy()
			policy.Enabled = true
			d := retention.NewDriver(policy, newCatalogPartitionLister(cat), newDayManagerMover(mgr, cat), nil)
			now := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
			first, err := d.Step(context.Background(), now)
			require.NoError(t, err)
			require.Zero(t, first.Moved)
			require.Zero(t, first.Discarded)
			require.Greater(t, first.KeptOriginal, 0)
			for _, key := range keys {
				r, _ := cat.Get(key)
				require.Equal(t, catalog.StateFailedRetryable, r.MigrationState)
				if failure == "copy" || failure == "lease" {
					require.Zero(t, detaches, "失败时不得先卸载")
				}
				if failure == "copy" {
					require.Equal(t, catalog.OwnerHot, r.Owner)
				} else {
					require.Equal(t, catalog.OwnerCold, r.Owner)
					require.NotEmpty(t, r.ResidualDirs)
				}
			}
			beforeCopy, beforeLease, beforeDetach := copies, leases, detaches
			blocked = false
			second, err := d.Step(context.Background(), now.Add(time.Hour))
			require.NoError(t, err)
			require.Greater(t, second.Moved, 0, "可重试失败必须由自动驱动器恢复，包含已切OwnerCold的清理失败")
			require.Zero(t, second.Discarded)
			for _, key := range keys {
				r, _ := cat.Get(key)
				require.Equal(t, catalog.StateCleaned, r.MigrationState)
				require.EqualValues(t, 2, r.Generation)
				require.Empty(t, r.ResidualDirs)
				begin := 0
				for _, entry := range cat.Journal().Entries(key) {
					if entry.State == catalog.StateRoutingFrozen {
						begin++
					}
				}
				require.Equal(t, 1, begin, "只能Resume，不得重新Begin")
			}
			if failure != "copy" {
				require.Equal(t, beforeCopy, copies, "owner切换后恢复不重复复制")
			}
			if failure == "cleanup" {
				require.Equal(t, beforeLease, leases)
				require.Equal(t, beforeDetach, detaches)
				require.Greater(t, cleanups, 1)
			}
			third, err := d.Step(context.Background(), now.Add(2*time.Hour))
			require.NoError(t, err)
			require.Zero(t, third.Moved)
			require.Equal(t, 2, third.Skipped)
		})
	}
}

func TestRetentionAdapterSkipsActiveManualAndUnknown(t *testing.T) {
	for _, state := range []catalog.MigrationState{catalog.StateFailedManual, catalog.MigrationState("UNKNOWN")} {
		t.Run(string(state), func(t *testing.T) {
			cat, keys := retentionCatalogFixture(t)
			_, err := cat.BeginDayMigration(keys[0].UTCDay, catalog.OwnerCold, "cold")
			require.NoError(t, err)
			if state == catalog.StateFailedManual {
				_, err = cat.FailDay(keys[0].UTCDay, state, "人工处理")
				require.NoError(t, err)
			}
			if state == "UNKNOWN" {
				for _, key := range keys {
					r, _ := cat.Get(key)
					r.MigrationState = state
					require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, State: state, Record: r}))
				}
			}
			calls := 0
			p := retention.DefaultPolicy()
			p.Enabled = true
			d := retention.NewDriver(p, newCatalogPartitionLister(cat), retention.MoverFunc(func(context.Context, string, string) (string, error) { calls++; return "", nil }), nil)
			r, err := d.Step(context.Background(), time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
			require.NoError(t, err)
			require.Zero(t, calls)
			require.Equal(t, 2, r.Skipped)
			if state == catalog.StateFailedManual || state == "UNKNOWN" {
				_, err = newDayManagerMover(lifecycle.NewDayManager(cat, lifecycle.Ops{}, nil), cat).MoveToCold(context.Background(), keys[0].StorageNamespace, keys[0].UTCDay)
				require.Error(t, err)
			}
		})
	}
}

// 正常 pending 不能因 Owner 或 InFlight 被驱动器永久跳过。
func TestRetentionAdapterResumesNormalPendingPhysicalDay(t *testing.T) {
	cat, keys := retentionCatalogFixture(t)
	_, err := cat.BeginDayMigration(keys[0].UTCDay, catalog.OwnerCold, "20260101")
	require.NoError(t, err)
	calls := 0
	ops := lifecycle.Ops{Snapshot: func(catalog.PartitionKey, string) (string, error) { calls++; return "snapshot", nil }, Copy: func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil }, Verify: func(catalog.PartitionKey, string, string) (string, error) { return "proof", nil }}
	manager := lifecycle.NewDayManager(cat, ops, nil)
	policy := retention.DefaultPolicy()
	policy.Enabled = true
	driver := retention.NewDriver(policy, newCatalogPartitionLister(cat), newDayManagerMover(manager, cat), nil)
	result, err := driver.Step(context.Background(), time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Greater(t, result.Moved, 0)
	require.Equal(t, 1, calls)
	for _, key := range keys {
		record, _ := cat.Get(key)
		require.Equal(t, catalog.OwnerCold, record.Owner)
		require.Equal(t, catalog.StateCleaned, record.MigrationState)
	}
}

// 列举与执行之间人工接管，Mover 必须重新读真源，不得按旧候选覆盖手工失败。
func TestRetentionMoverRechecksManualAfterListing(t *testing.T) {
	cat, keys := retentionCatalogFixture(t)
	_, err := cat.BeginDayMigration(keys[0].UTCDay, catalog.OwnerCold, "cold")
	require.NoError(t, err)
	_, err = cat.FailDay(keys[0].UTCDay, catalog.StateFailedRetryable, "可重试")
	require.NoError(t, err)
	base := newCatalogPartitionLister(cat)
	lister := retention.ListerFunc(func() ([]retention.PartitionRef, error) {
		parts, err := base.ListPartitions()
		if err != nil {
			return nil, err
		}
		_, err = cat.FailDay(keys[0].UTCDay, catalog.StateFailedManual, "人工接管")
		return parts, err
	})
	calls := 0
	ops := lifecycle.Ops{Drain: func(catalog.PartitionKey) error { calls++; return nil }}
	mgr := lifecycle.NewDayManager(cat, ops, nil)
	p := retention.DefaultPolicy()
	p.Enabled = true
	d := retention.NewDriver(p, lister, newDayManagerMover(mgr, cat), nil)
	r, err := d.Step(context.Background(), time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Zero(t, r.Moved)
	require.Zero(t, r.Discarded)
	require.Equal(t, 2, r.KeptOriginal)
	require.Zero(t, calls)
	for _, key := range keys {
		rec, _ := cat.Get(key)
		require.Equal(t, catalog.StateFailedManual, rec.MigrationState)
		require.EqualValues(t, 1, rec.Generation)
	}
}

func TestRetentionMoverPreservesLastCopyGate(t *testing.T) {
	cat, keys := retentionCatalogFixture(t)
	detaches := 0
	ops := lifecycle.Ops{
		Snapshot: func(catalog.PartitionKey, string) (string, error) { return "snapshot", nil },
		Copy:     func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
		Verify:   func(catalog.PartitionKey, string, string) (string, error) { return "checksum", nil },
		Detach:   func(catalog.PartitionKey, string) error { detaches++; return nil },
	}
	mgr := lifecycle.NewDayManager(cat, ops, lifecycle.RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return false, "最后副本保护" }))
	_, err := newDayManagerMover(mgr, cat).MoveToCold(context.Background(), keys[0].StorageNamespace, "20260101")
	require.ErrorContains(t, err, "last-copy")
	require.Zero(t, detaches)
	for _, key := range keys {
		r, _ := cat.Get(key)
		require.Equal(t, catalog.StateQueryLeaseDraining, r.MigrationState)
		require.NotEmpty(t, r.ResidualDirs)
	}
}

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
