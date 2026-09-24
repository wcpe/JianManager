package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDayMigrationBatchAtomicallySwitchesAllNamespaces(t *testing.T) {
	journal := NewMemJournal()
	cat := New(journal)
	day := "2026-09-23"
	keys := []PartitionKey{{StorageNamespace: "node:1", UTCDay: day}, {StorageNamespace: "inst:9", UTCDay: day}}
	for index, key := range keys {
		rec := NewStableRecord(key, OwnerHot, 1, "hot-20260923")
		rec.PublishedProjection = &PublishedProjection{ManifestVersion: "manifest-hot", ProjectionGeneration: "p-hot",
			CoverageComplete: true, QueryLocationDirID: rec.OwnerDirID, QueryGeneration: 1,
			ClosedVisibleSeq: map[string]uint64{key.String(): uint64(index + 10)}}
		require.NoError(t, cat.Put(rec))
	}
	records, err := cat.BeginDayMigration(day, OwnerCold, "20260923")
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.EqualValues(t, 1, journal.LastSeq(), "one JSONL entry freezes the whole physical day")
	for _, state := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify} {
		_, err = cat.AdvanceDay(day, state)
		require.NoError(t, err)
	}
	require.NoError(t, cat.RecordDayVerification(day, "checksum"))
	_, err = cat.AdvanceDay(day, StateAttachedStaging)
	require.NoError(t, err)
	projections := map[PartitionKey]*PublishedProjection{}
	for _, key := range keys {
		current, _ := cat.Get(key)
		projection := *current.PublishedProjection
		projection.ManifestVersion = "manifest-cold"
		projection.QueryLocationDirID = "20260923"
		projection.QueryGeneration = 2
		projections[key] = &projection
	}
	_, err = cat.SwitchDayOwners(day, OwnerCold, "20260923", projections)
	require.NoError(t, err)

	replayed := New(journal)
	replayed.StartingRecover()
	for index, key := range keys {
		rec, ok := replayed.Get(key)
		require.True(t, ok)
		require.Equal(t, OwnerCold, rec.Owner)
		require.EqualValues(t, 2, rec.Generation)
		require.Equal(t, "20260923", rec.OwnerDirID)
		require.Empty(t, rec.ConflictGeneration)
		require.True(t, rec.WriteRoute.Owner == OwnerCold && !rec.WriteRoute.Frozen)
		require.EqualValues(t, index+10, rec.PublishedProjection.ClosedVisibleSeq[key.String()])
		require.True(t, replayed.IsQueryDirVisible(key, "20260923"))
		require.False(t, replayed.IsQueryDirVisible(key, "hot-20260923"))
		require.NotEmpty(t, journal.Entries(key), "batch entry must be indexed under every logical key")
	}
}
