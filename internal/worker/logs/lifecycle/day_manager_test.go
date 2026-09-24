package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

func dayFixture(t *testing.T) (*catalog.Catalog, *catalog.MemJournal, []catalog.PartitionKey) {
	t.Helper()
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	keys := []catalog.PartitionKey{{StorageNamespace: "node:1", UTCDay: "2026-09-23"}, {StorageNamespace: "inst:9", UTCDay: "2026-09-23"}}
	for index, key := range keys {
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-day")
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "hot", ProjectionGeneration: "p-hot",
			CoverageComplete: true, QueryLocationDirID: "hot-day", QueryGeneration: 1,
			ClosedVisibleSeq: map[string]uint64{key.String(): uint64(index + 20)}}
		require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, State: catalog.StateCleaned,
			Owner: rec.Owner, Generation: rec.Generation, DirID: rec.OwnerDirID,
			AuthorityCommit: true, JournalComplete: true, Record: rec}))
	}
	return cat, journal, keys
}

func dayOps() Ops {
	return Ops{Drain: func(catalog.PartitionKey) error { return nil },
		Snapshot:   func(catalog.PartitionKey, string) (string, error) { return "snapshot", nil },
		Copy:       func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
		Verify:     func(catalog.PartitionKey, string, string) (string, error) { return "checksum", nil },
		Attach:     func(catalog.PartitionKey, string) error { return nil },
		LeaseDrain: func(catalog.PartitionKey, uint64) error { return nil },
		Detach:     func(catalog.PartitionKey, string) error { return nil },
		Cleanup:    func(catalog.PartitionKey, string) error { return nil }}
}

func stateRank(state catalog.MigrationState) int {
	rank, _ := catalog.HappyIndex(state)
	return rank
}

func TestDayManagerMigratesAllNamespacesTogether(t *testing.T) {
	cat, _, keys := dayFixture(t)
	manager := NewDayManager(cat, dayOps(), RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "ok" }))
	require.NoError(t, manager.Start("2026-09-23", catalog.OwnerCold, "20260923"))
	for index, key := range keys {
		rec, ok := cat.Get(key)
		require.True(t, ok)
		require.Equal(t, catalog.OwnerCold, rec.Owner)
		require.Equal(t, catalog.StateCleaned, rec.MigrationState)
		require.True(t, rec.JournalComplete)
		require.EqualValues(t, index+20, rec.PublishedProjection.ClosedVisibleSeq[key.String()])
		require.False(t, cat.IsQueryDirVisible(key, "hot-day"))
	}
}

func TestDayManagerResumesBatchFromEveryState(t *testing.T) {
	for _, stop := range catalog.HappyPath {
		t.Run(string(stop), func(t *testing.T) {
			cat, journal, keys := dayFixture(t)
			_, err := cat.BeginDayMigration("2026-09-23", catalog.OwnerCold, "20260923")
			require.NoError(t, err)
			if stop != catalog.StateRoutingFrozen {
				for _, state := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify} {
					_, err = cat.AdvanceDay("2026-09-23", state)
					require.NoError(t, err)
					if state == stop {
						break
					}
				}
			}
			if stateRank(stop) >= stateRank(catalog.StateAttachedStaging) {
				require.NoError(t, cat.RecordDayVerification("2026-09-23", "checksum"))
				if current := cat.RecordsForDay("2026-09-23")[0].MigrationState; current != catalog.StateAttachedStaging {
					_, err = cat.AdvanceDay("2026-09-23", catalog.StateAttachedStaging)
					require.NoError(t, err)
				}
			}
			if stateRank(stop) >= stateRank(catalog.StateOwnerSwitched) {
				projections := map[catalog.PartitionKey]*catalog.PublishedProjection{}
				for _, key := range keys {
					rec, _ := cat.Get(key)
					projections[key] = migratedProjection(rec.PublishedProjection, "20260923", 2)
				}
				_, err = cat.SwitchDayOwners("2026-09-23", catalog.OwnerCold, "20260923", projections)
				require.NoError(t, err)
			}
			for _, state := range []catalog.MigrationState{catalog.StateQueryLeaseDraining, catalog.StateDetached} {
				if stateRank(stop) >= stateRank(state) {
					_, err = cat.AdvanceDay("2026-09-23", state)
					require.NoError(t, err)
				}
			}
			if stop == catalog.StateCleaned {
				require.NoError(t, cat.ClearDayResiduals("2026-09-23"))
				require.NoError(t, cat.MarkDayComplete("2026-09-23", "checksum"))
				_, err = cat.AdvanceDay("2026-09-23", catalog.StateCleaned)
				require.NoError(t, err)
			}
			restarted := catalog.New(journal)
			restarted.StartingRecover()
			manager := NewDayManager(restarted, dayOps(), RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "ok" }))
			require.NoError(t, manager.Resume("2026-09-23"))
			for _, key := range keys {
				rec, _ := restarted.Get(key)
				require.Equal(t, catalog.StateCleaned, rec.MigrationState)
				require.Equal(t, catalog.OwnerCold, rec.Owner)
			}
		})
	}
}
