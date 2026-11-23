package catalog

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPhysicalDayRepeatedBeginKeepsOriginalBatch(t *testing.T) {
	for _, state := range []MigrationState{StateRoutingFrozen, StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		t.Run(string(state), func(t *testing.T) {
			cat := New(nil)
			key := PartitionKey{StorageNamespace: "original", UTCDay: "2026-09-20"}
			require.NoError(t, cat.Put(NewStableRecord(key, OwnerHot, 1, "hot")))
			_, err := cat.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
			require.NoError(t, err)
			for _, next := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
				cur, _ := cat.Get(key)
				if cur.MigrationState == state {
					break
				}
				_, err = cat.AdvanceDay(key.UTCDay, next)
				require.NoError(t, err)
			}
			late := PartitionKey{StorageNamespace: "late", UTCDay: key.UTCDay}
			require.NoError(t, cat.AppendJournal(JournalEntry{Key: late, Record: NewStableRecord(late, OwnerHot, 1, "hot")}))
			_, err = cat.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
			require.NoError(t, err)
			got, _ := cat.Get(late)
			require.Equal(t, StateCleaned, got.MigrationState, "重复 Begin 不得向原批加入异步起点的新成员")
			orig, _ := cat.Get(key)
			require.Equal(t, state, orig.MigrationState)
		})
	}
}

func TestPhysicalDayFileJournalThreeStartsPreserveMemberPostState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	open := func() *Catalog {
		store, err := NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := NewJournalWithStore(store)
		require.NoError(t, err)
		return New(journal)
	}
	cat := open()
	old := PartitionKey{StorageNamespace: "original", UTCDay: "2026-09-20"}
	late := PartitionKey{StorageNamespace: "late", UTCDay: old.UTCDay}
	require.NoError(t, cat.AppendJournal(JournalEntry{Key: old, Record: NewStableRecord(old, OwnerHot, 1, "hot")}))
	_, err := cat.BeginDayMigration(old.UTCDay, OwnerCold, "cold")
	require.NoError(t, err)
	for _, state := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify} {
		_, err = cat.AdvanceDay(old.UTCDay, state)
		require.NoError(t, err)
	}
	require.NoError(t, cat.AppendJournal(JournalEntry{Key: late, Record: NewStableRecord(late, OwnerHot, 1, "late-hot")}))
	require.NoError(t, cat.RecordDayVerification(old.UTCDay, "verified-original"))
	for round := 1; round <= 3; round++ {
		cat = open()
		cat.StartingRecover()
		got, ok := cat.Get(late)
		require.True(t, ok)
		require.Equal(t, StateCleaned, got.MigrationState, "第%d启不得被批顶层状态污染", round)
		require.Equal(t, OwnerHot, got.Owner)
		require.Equal(t, "late-hot", got.OwnerDirID)
		require.Equal(t, "late-hot", got.WriteRoute.DirID)
		require.False(t, got.LastVerifyCompleted, "未验证成员不得借用另一成员凭据")
		require.Empty(t, got.TargetDirID)
	}
}
