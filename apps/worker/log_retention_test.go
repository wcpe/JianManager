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
	for _, state := range []catalog.MigrationState{catalog.StateRoutingFrozen, catalog.StateFailedManual, catalog.MigrationState("UNKNOWN")} {
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
