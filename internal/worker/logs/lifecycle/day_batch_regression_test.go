package lifecycle

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

type failLeaseAdvanceOnceJournal struct {
	catalog.Journal
	failed bool
}

func (j *failLeaseAdvanceOnceJournal) Append(entry catalog.JournalEntry) error {
	if entry.State == catalog.StateQueryLeaseDraining && !j.failed {
		j.failed = true
		return errors.New("租约排空状态提交单次失败")
	}
	return j.Journal.Append(entry)
}

func TestDayManagerRetryRestoresAlreadyCommittedOwnerSwitchAcrossThreeStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner-switch.jsonl")
	open := func() *catalog.JournalWithStore {
		store, err := catalog.NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := catalog.NewJournalWithStore(store)
		require.NoError(t, err)
		return journal
	}
	journal := &failLeaseAdvanceOnceJournal{Journal: open()}
	cat := catalog.New(journal)
	key := catalog.PartitionKey{StorageNamespace: "namespace", UTCDay: "2026-09-20"}
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")
	record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "hot", QueryLocationDirID: "hot", QueryGeneration: 1, CoverageComplete: true}
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, Record: record}))
	require.Error(t, NewDayManager(cat, dayOps(), nil).Start(key.UTCDay, catalog.OwnerCold, "cold"))
	failed, _ := cat.Get(key)
	require.Equal(t, catalog.OwnerCold, failed.Owner)
	require.Equal(t, catalog.StateFailedRetryable, failed.MigrationState)
	for round := 1; round <= 3; round++ {
		cat = catalog.New(open())
		cat.StartingRecover()
		require.NoError(t, NewDayManager(cat, dayOps(), nil).Resume(key.UTCDay))
		restored, _ := cat.Get(key)
		require.Equal(t, catalog.OwnerCold, restored.Owner, "第%d启", round)
		require.EqualValues(t, 2, restored.Generation)
		require.Equal(t, catalog.StateCleaned, restored.MigrationState)
		require.True(t, restored.JournalComplete)
	}
}

func TestDayManagerLateNamespaceTransfersBeforePhysicalRetirement(t *testing.T) {
	cat, _, keys := dayFixture(t)
	late := catalog.PartitionKey{StorageNamespace: "late", UTCDay: keys[0].UTCDay}
	ops := dayOps()
	copies, detaches, cleanups := 0, 0, 0
	ops.Copy = func(catalog.PartitionKey, string, string) (string, error) {
		copies++
		if copies == 1 {
			require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: late, Record: catalog.NewStableRecord(late, catalog.OwnerHot, 1, "hot-day")}))
		}
		return "copy", nil
	}
	ops.Detach = func(catalog.PartitionKey, string) error {
		for _, rec := range cat.RecordsForDay(late.UTCDay) {
			require.NotEqual(t, catalog.OwnerHot, rec.Owner, "物理日任一 HOT 引用未转移时不得detach")
		}
		detaches++
		return nil
	}
	ops.Cleanup = func(catalog.PartitionKey, string) error { cleanups++; return nil }
	mgr := NewDayManager(cat, ops, nil)
	require.NoError(t, mgr.Start(late.UTCDay, catalog.OwnerCold, "cold"))
	for round := 0; round < 3; round++ {
		require.NoError(t, mgr.Resume(late.UTCDay))
		rec, _ := cat.Get(late)
		require.Equal(t, catalog.OwnerCold, rec.Owner, "Resume成功必须意味着残留已真实转移或明确pending，不能nil伪结账")
		require.Equal(t, catalog.StateCleaned, rec.MigrationState)
	}
	require.Equal(t, 2, copies)
	require.Equal(t, 1, detaches)
	require.Equal(t, 1, cleanups)
}

func TestLegacyMixedDayJournalResumesEachRecordBatchAcrossThreeStarts(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "query-lease-draining", true: "failed-retryable"}[failed], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.jsonl")
			open := func() *catalog.Catalog {
				store, err := catalog.NewJSONLFileStore(path)
				require.NoError(t, err)
				journal, err := catalog.NewJournalWithStore(store)
				require.NoError(t, err)
				c := catalog.New(journal)
				c.StartingRecover()
				return c
			}
			cat := open()
			a := catalog.PartitionKey{StorageNamespace: "a-settled", UTCDay: "2026-09-20"}
			b := catalog.PartitionKey{StorageNamespace: "b-residual", UTCDay: a.UTCDay}
			for _, key := range []catalog.PartitionKey{a, b} {
				record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")
				record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: key.StorageNamespace, QueryLocationDirID: "hot", QueryGeneration: 1, CoverageComplete: true}
				require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, Record: record}))
			}
			_, err := cat.BeginMigration(a, catalog.OwnerCold, "cold")
			require.NoError(t, err)
			for _, state := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify} {
				_, err = cat.Advance(a, state)
				require.NoError(t, err)
			}
			require.NoError(t, cat.RecordVerification(a, "verified-a"))
			_, err = cat.Advance(a, catalog.StateAttachedStaging)
			require.NoError(t, err)
			record, _ := cat.Get(a)
			_, err = cat.SwitchOwner(a, catalog.OwnerCold, "cold", 2, migratedProjection(record.PublishedProjection, "cold", 2))
			require.NoError(t, err)
			_, err = cat.Advance(a, catalog.StateQueryLeaseDraining)
			require.NoError(t, err)
			if failed {
				_, err = cat.Fail(a, catalog.StateFailedRetryable, "旧版本租约未结束")
				require.NoError(t, err)
			}
			_, err = cat.BeginMigration(b, catalog.OwnerCold, "cold")
			require.NoError(t, err)
			copies := 0
			ops := dayOps()
			ops.Copy = func(key catalog.PartitionKey, _, _ string) (string, error) {
				require.Equal(t, b, key, "旧冷层成员不得被纳入残留HOT的复制起点")
				copies++
				return "copy", nil
			}
			for round := 1; round <= 3; round++ {
				cat = open()
				require.NoError(t, NewDayManager(cat, ops, nil).Resume(a.UTCDay))
				for _, key := range []catalog.PartitionKey{a, b} {
					restored, _ := cat.Get(key)
					require.Equal(t, catalog.OwnerCold, restored.Owner, "第%d启", round)
					require.Equal(t, catalog.StateCleaned, restored.MigrationState)
					require.True(t, restored.LastVerifyCompleted)
					require.NotEmpty(t, restored.LastVerifyChecksum)
				}
			}
			require.Equal(t, 1, copies)
		})
	}
}

func TestDayManagerFileJournalResidualThreeStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	open := func() *catalog.Catalog {
		store, err := catalog.NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := catalog.NewJournalWithStore(store)
		require.NoError(t, err)
		c := catalog.New(journal)
		c.StartingRecover()
		return c
	}
	cat := open()
	day := "2026-09-20"
	key := catalog.PartitionKey{StorageNamespace: "initial", UTCDay: day}
	late := catalog.PartitionKey{StorageNamespace: "late", UTCDay: day}
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, Record: catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")}))
	copies, detaches := 0, 0
	blocked := true
	ops := dayOps()
	ops.Copy = func(catalog.PartitionKey, string, string) (string, error) {
		copies++
		if _, exists := cat.Get(late); !exists {
			require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: late, Record: catalog.NewStableRecord(late, catalog.OwnerHot, 1, "hot")}))
		}
		return "copy", nil
	}
	ops.Detach = func(catalog.PartitionKey, string) error {
		if blocked {
			return errors.New("显式暂停以重启")
		}
		detaches++
		return nil
	}
	require.Error(t, NewDayManager(cat, ops, nil).Start(day, catalog.OwnerCold, "cold"))
	blocked = false
	for round := 1; round <= 3; round++ {
		cat = open()
		require.NoError(t, NewDayManager(cat, ops, nil).Resume(day))
		for _, k := range []catalog.PartitionKey{key, late} {
			rec, _ := cat.Get(k)
			require.Equal(t, catalog.OwnerCold, rec.Owner, "第%d启", round)
			require.Equal(t, catalog.StateCleaned, rec.MigrationState)
			require.True(t, rec.LastVerifyCompleted)
			require.NotEmpty(t, rec.LastVerifyChecksum)
			require.True(t, rec.JournalComplete)
			require.Equal(t, "cold", rec.PublishedProjection.QueryLocationDirID)
			require.Equal(t, rec.Generation, rec.PublishedProjection.QueryGeneration)
		}
	}
	require.Equal(t, 2, copies, "重启不得重造已验证物理副本")
	require.Equal(t, 1, detaches)
}
