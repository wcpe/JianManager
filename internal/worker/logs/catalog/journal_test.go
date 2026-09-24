package catalog

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type rejectingJournal struct {
	Journal
	reject bool
}

func (j *rejectingJournal) Append(entry JournalEntry) error {
	if j.reject {
		return errors.New("journal write rejected")
	}
	return j.Journal.Append(entry)
}

func TestMigrationJournalRestoresIntentAndAuthorityAtEveryCrashPoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	store, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	j, err := NewJournalWithStore(store)
	require.NoError(t, err)
	cat := New(j)
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22"}
	require.NoError(t, cat.Put(NewStableRecord(key, OwnerHot, 1, "hot-g1")))
	reload := func(expected MigrationState, owner Owner, dir string) {
		t.Helper()
		store, err := NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := NewJournalWithStore(store)
		require.NoError(t, err)
		cat = New(journal)
		views := cat.StartingRecover()
		rec, ok := cat.Get(key)
		require.True(t, ok)
		require.Equal(t, expected, rec.MigrationState)
		require.Equal(t, owner, rec.Owner)
		require.Equal(t, dir, rec.OwnerDirID)
		require.Equal(t, OwnerCold, rec.TargetOwner)
		require.Equal(t, "cold-g2", rec.TargetDirID)
		require.EqualValues(t, 2, rec.TargetGeneration)
		require.Equal(t, "hot-g1", rec.MigrationFromDirID)
		require.Equal(t, dir, views[key].QueryDirID)
		require.False(t, cat.IsQueryDirVisible(key, "hot-g1") && owner == OwnerCold,
			"re-attached old owner must stay outside the query plan")
	}

	_, err = cat.BeginMigration(key, OwnerCold, "cold-g2")
	require.NoError(t, err)
	reload(StateRoutingFrozen, OwnerHot, "hot-g1")
	rec, _ := cat.Get(key)
	require.True(t, rec.WriteRoute.Frozen)
	for _, state := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		_, err = cat.Advance(key, state)
		require.NoError(t, err)
		reload(state, OwnerHot, "hot-g1")
	}
	proj := &PublishedProjection{ManifestVersion: "pv-2", CoverageComplete: true, QueryLocationDirID: "cold-g2", QueryGeneration: 2}
	_, err = cat.SwitchOwner(key, OwnerCold, "cold-g2", 2, proj)
	require.NoError(t, err)
	reload(StateOwnerSwitched, OwnerCold, "cold-g2")
	rec, _ = cat.Get(key)
	require.Equal(t, "pv-2", rec.PublishedProjection.ManifestVersion)
	require.NotEmpty(t, rec.ResidualDirs)
	for _, state := range []MigrationState{StateQueryLeaseDraining, StateDetached} {
		_, err = cat.Advance(key, state)
		require.NoError(t, err)
		reload(state, OwnerCold, "cold-g2")
	}
	rec, _ = cat.Get(key)
	rec.ResidualDirs = nil
	require.NoError(t, cat.Put(rec))
	require.NoError(t, cat.MarkJournalComplete(key, "verified-checksum"))
	_, err = cat.Advance(key, StateCleaned)
	require.NoError(t, err)
	reload(StateCleaned, OwnerCold, "cold-g2")
	rec, _ = cat.Get(key)
	require.True(t, rec.JournalComplete)
	require.Empty(t, rec.ResidualDirs)
	require.Equal(t, "verified-checksum", rec.LastVerifyChecksum)
}

func TestRejectedJournalAppendLeavesCatalogRouteUnchanged(t *testing.T) {
	journal := &rejectingJournal{Journal: NewMemJournal(), reject: true}
	cat := New(journal)
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22"}
	require.NoError(t, cat.Put(NewStableRecord(key, OwnerHot, 1, "hot-g1")))
	before, _ := cat.Get(key)
	_, err := cat.BeginMigration(key, OwnerCold, "cold-g2")
	require.Error(t, err)
	after, _ := cat.Get(key)
	require.Equal(t, before, after)

	journal.reject = false
	_, err = cat.BeginMigration(key, OwnerCold, "cold-g2")
	require.NoError(t, err)
	before, _ = cat.Get(key)
	journal.reject = true
	_, err = cat.Advance(key, StateDraining)
	require.Error(t, err)
	after, _ = cat.Get(key)
	require.Equal(t, before, after)

	journal.reject = false
	for _, state := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		_, err = cat.Advance(key, state)
		require.NoError(t, err)
	}
	before, _ = cat.Get(key)
	journal.reject = true
	_, err = cat.SwitchOwner(key, OwnerCold, "cold-g2", 2, &PublishedProjection{
		ManifestVersion: "pv-2", QueryLocationDirID: "cold-g2", QueryGeneration: 2,
	})
	require.Error(t, err)
	after, _ = cat.Get(key)
	require.Equal(t, before, after)
	require.True(t, cat.IsQueryDirVisible(key, "hot-g1"))
	require.False(t, cat.IsQueryDirVisible(key, "cold-g2"))
}

func TestJournalWithStoreConcurrentAppendReplaysEveryEntry(t *testing.T) {
	store, err := NewJSONLFileStore(filepath.Join(t.TempDir(), "catalog.jsonl"))
	require.NoError(t, err)
	journal, err := NewJournalWithStore(store)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- journal.Append(JournalEntry{
				Key:   PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22"},
				State: StateCleaned, Owner: OwnerHot, Generation: 1, DirID: "hot-g1",
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	replayed, err := NewJournalWithStore(store)
	require.NoError(t, err)
	require.Len(t, replayed.Snapshot(), 32)
	for index, entry := range replayed.Snapshot() {
		require.EqualValues(t, index+1, entry.Seq)
	}
}

func TestMemJournalAppendOnlyAndOrder(t *testing.T) {
	j := NewMemJournal()
	key := PartitionKey{StorageNamespace: "ns", UTCDay: "2026-09-20"}

	require.NoError(t, j.Append(JournalEntry{Key: key, State: StateRoutingFrozen}))
	require.NoError(t, j.Append(JournalEntry{Key: key, State: StateDraining}))
	require.ErrorIs(t, j.Append(JournalEntry{Seq: 1, Key: key, State: StateSnapshotting}), ErrJournalOrder)

	entries := j.Entries(key)
	require.Len(t, entries, 2)
	require.EqualValues(t, 1, entries[0].Seq)
	require.EqualValues(t, 2, entries[1].Seq)
	require.Equal(t, uint64(2), j.LastSeq())

	other := PartitionKey{StorageNamespace: "ns", UTCDay: "2026-09-21"}
	require.NoError(t, j.Append(JournalEntry{Key: other, State: StateCleaned, JournalComplete: true}))
	require.Len(t, j.Entries(other), 1)
	require.Len(t, j.Entries(key), 2)
	require.Len(t, j.Snapshot(), 3)
}

func TestJSONLFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog-journal.jsonl")
	store, err := NewJSONLFileStore(path)
	require.NoError(t, err)

	key := PartitionKey{StorageNamespace: "ns/game", UTCDay: "2026-09-20"}
	e1 := JournalEntry{Seq: 1, Key: key, State: StateRoutingFrozen, Owner: OwnerHot, Generation: 1, DirID: "hot-g1"}
	e2 := JournalEntry{Seq: 2, Key: key, State: StateOwnerSwitched, Owner: OwnerCold, Generation: 2, DirID: "cold-g2", AuthorityCommit: true}
	require.NoError(t, store.AppendEntry(e1))
	require.NoError(t, store.AppendEntry(e2))

	got, err := store.LoadEntries()
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, e1.State, got[0].State)
	require.True(t, got[1].AuthorityCommit)
	require.Equal(t, OwnerCold, got[1].Owner)

	// JournalWithStore 启动时从文件重放。
	jw, err := NewJournalWithStore(store)
	require.NoError(t, err)
	require.EqualValues(t, 2, jw.LastSeq())
	require.Len(t, jw.Entries(key), 2)

	// 追加会同步落盘。
	require.NoError(t, jw.Append(JournalEntry{Key: key, State: StateQueryLeaseDraining, JournalComplete: true}))
	reloaded, err := store.LoadEntries()
	require.NoError(t, err)
	require.Len(t, reloaded, 3)
	require.True(t, reloaded[2].JournalComplete)
}

func TestReplayJournalIdempotentAuthority(t *testing.T) {
	key := PartitionKey{StorageNamespace: "ns", UTCDay: "2026-09-20"}
	rec := NewStableRecord(key, OwnerHot, 1, "hot-g1")
	BeginMigration(rec, OwnerCold, "cold-g2")
	rec.MigrationState = StateAttachedStaging

	entries := []JournalEntry{
		{Seq: 1, Key: key, State: StateRoutingFrozen, FromState: StateCleaned},
		{Seq: 2, Key: key, State: StateDraining},
		{Seq: 3, Key: key, State: StateSnapshotting},
		{Seq: 4, Key: key, State: StateStagingVerify},
		{Seq: 5, Key: key, State: StateAttachedStaging},
		{Seq: 6, Key: key, State: StateOwnerSwitched, Owner: OwnerCold, Generation: 2, DirID: "cold-g2", AuthorityCommit: true},
		{Seq: 7, Key: key, State: StateQueryLeaseDraining},
		{Seq: 8, Key: key, State: StateDetached},
		{Seq: 9, Key: key, State: StateCleaned, JournalComplete: true},
	}
	conflict := ReplayJournal(rec, entries)
	require.Empty(t, conflict)
	require.Equal(t, OwnerCold, rec.Owner)
	require.EqualValues(t, 2, rec.Generation)
	require.Equal(t, "cold-g2", rec.OwnerDirID)
	require.Equal(t, StateCleaned, rec.MigrationState)
	require.True(t, rec.JournalComplete)

	// 幂等重放
	conflict = ReplayJournal(rec, entries)
	require.Empty(t, conflict)
	require.Equal(t, OwnerCold, rec.Owner)
}

func TestReplayJournalDetectsConflict(t *testing.T) {
	key := PartitionKey{StorageNamespace: "ns", UTCDay: "2026-09-20"}
	rec := NewStableRecord(key, OwnerHot, 1, "hot-g1")
	// 同一 journal 流出现互相矛盾的两次权威提交 → 冲突，仍以最后一次为准并标记。
	entries := []JournalEntry{
		{Seq: 1, Key: key, State: StateOwnerSwitched, Owner: OwnerCold, Generation: 2, DirID: "cold-g2", AuthorityCommit: true},
		{Seq: 2, Key: key, State: StateOwnerSwitched, Owner: OwnerArchive, Generation: 3, DirID: "arc-g3", AuthorityCommit: true},
	}
	conflict := ReplayJournal(rec, entries)
	require.NotEmpty(t, conflict)
	require.Equal(t, "arc-g3", rec.OwnerDirID)
	require.Equal(t, OwnerArchive, rec.Owner)
	require.EqualValues(t, 3, rec.Generation)
	require.Equal(t, conflict, rec.ConflictGeneration)
}

func TestReplayJournalFullRecordAuthorityDoesNotConflictWithOmittedLegacyFields(t *testing.T) {
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-08"}
	rec := NewStableRecord(key, OwnerArchive, 1, "deep-g1")
	failed := rec.Clone()
	failed.RecoveryRequired = true
	failed.PartialReasons = []string{"REHYDRATE_FAILED"}
	recovered := rec.Clone()
	recovered.PublishedProjection = &PublishedProjection{ManifestVersion: "manifest-rh", ProjectionGeneration: "rh-1",
		ProjectionGenerations: []string{"rh-1"}, CoverageComplete: true, QueryLocationDirID: "deep-g1", QueryGeneration: 1}
	entries := []JournalEntry{
		{Seq: 1, Key: key, Record: failed},
		// 与生产 rehydrate 发布相同：完整 Record + AuthorityCommit，不重复写顶层兼容字段。
		{Seq: 2, Key: key, Record: recovered, AuthorityCommit: true},
	}
	conflict := ReplayJournal(rec, entries)
	require.Empty(t, conflict)
	require.Empty(t, rec.ConflictGeneration)
	require.Equal(t, OwnerArchive, rec.Owner)
	require.EqualValues(t, 1, rec.Generation)
	require.Equal(t, "deep-g1", rec.OwnerDirID)
	require.False(t, rec.RecoveryRequired)
}
