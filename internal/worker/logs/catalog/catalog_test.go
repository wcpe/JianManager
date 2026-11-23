package catalog

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey() PartitionKey {
	return PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
}

func TestCatalog_FullMigrationHappyPath(t *testing.T) {
	c := New(nil)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))

	rec, err := c.BeginMigration(testKey(), OwnerCold, "cold-g2")
	require.NoError(t, err)
	require.Equal(t, StateRoutingFrozen, rec.MigrationState)
	require.True(t, rec.WriteRoute.Frozen)

	for _, st := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		rec, err = c.Advance(testKey(), st)
		require.NoError(t, err)
		require.Equal(t, st, rec.MigrationState)
	}

	// staging attach 后仍不得查询 staging。
	require.False(t, c.IsQueryDirVisible(testKey(), "cold-g2"))
	require.True(t, c.IsQueryDirVisible(testKey(), "hot-g1"))

	// Advance 不能直接切 owner。
	_, err = c.Advance(testKey(), StateOwnerSwitched)
	require.ErrorIs(t, err, ErrIllegalTransition)

	proj := &PublishedProjection{
		ManifestVersion:    "pv-1",
		CoverageComplete:   true,
		ClosedVisibleSeq:   map[string]uint64{"src-a": 100},
		QueryLocationDirID: "cold-g2",
		QueryGeneration:    2,
	}
	rec, err = c.SwitchOwner(testKey(), OwnerCold, "cold-g2", 2, proj)
	require.NoError(t, err)
	require.Equal(t, OwnerCold, rec.Owner)
	require.EqualValues(t, 2, rec.Generation)
	require.NotNil(t, rec.PublishedProjection)
	require.True(t, rec.PublishedProjection.CoverageComplete)

	// 切换后旧目录被排除，新 owner 可见。
	require.False(t, c.IsQueryDirVisible(testKey(), "hot-g1"))
	require.True(t, c.IsQueryDirVisible(testKey(), "cold-g2"))

	ref, ok := c.OwnerForQuery(testKey())
	require.True(t, ok)
	require.Equal(t, OwnerCold, ref.Owner)
	require.Equal(t, "cold-g2", ref.DirID)

	// 迟到事件按 Catalog COLD 路由。
	wt, ok := c.RouteWrite(testKey(), "2026-09-01")
	require.True(t, ok)
	require.Equal(t, OwnerCold, wt.Owner)
	require.Equal(t, "cold-g2", wt.DirID)

	// 租约排空 → detach → journal complete → cleaned
	rec, err = c.Advance(testKey(), StateQueryLeaseDraining)
	require.NoError(t, err)
	require.Equal(t, OwnerCold, rec.Owner)

	rec, err = c.Advance(testKey(), StateDetached)
	require.NoError(t, err)

	// journal 未完成前完成度为 false。
	cur, _ := c.Get(testKey())
	require.False(t, IsMigrationComplete(cur))

	require.NoError(t, c.MarkJournalComplete(testKey(), "sha256:ok"))
	rec, err = c.Advance(testKey(), StateCleaned)
	require.NoError(t, err)
	// Advance 会把 state 写成 CLEANED；residual 仍在 → 仍非完成，直到 residual 清理。
	require.Equal(t, StateCleaned, rec.MigrationState)

	views := c.StartingRecover()
	v := views[testKey()]
	require.Equal(t, OwnerCold, v.Owner)
	require.Equal(t, "cold-g2", v.QueryDirID)
	require.Contains(t, v.ExcludedQueryDirs, "hot-g1")
}

func TestCatalog_StartingRecoverFromCrashPoint(t *testing.T) {
	// 模拟在 ATTACHED_STAGING 崩溃：journal 有到 staging 的条目。
	j := NewMemJournal()
	c := New(j)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	_, err := c.BeginMigration(testKey(), OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}

	views := c.StartingRecover()
	v := views[testKey()]
	require.Equal(t, StateAttachedStaging, v.MigrationState)
	require.Equal(t, OwnerHot, v.Owner)
	require.Equal(t, "hot-g1", v.QueryDirID)
	require.True(t, v.StagingOpen)
	require.True(t, v.RecoveryRequired)
	require.False(t, c.IsQueryDirVisible(testKey(), "cold-g2"))
	require.True(t, c.IsQueryDirVisible(testKey(), "hot-g1"))
}

func TestCatalog_StartingRecoverAfterOwnerSwitch(t *testing.T) {
	j := NewMemJournal()
	c := New(j)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	_, err := c.BeginMigration(testKey(), OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}
	_, err = c.SwitchOwner(testKey(), OwnerCold, "cold-g2", 2, &PublishedProjection{
		ManifestVersion: "pv-1",
		QueryGeneration: 2,
	})
	require.NoError(t, err)

	// 新 Catalog 实例，同 journal —— 模拟进程重启。
	c2 := New(j)
	require.NoError(t, c2.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	// 手动把记录置成切换前样子，依赖 journal 重放权威。
	views := c2.StartingRecover()
	v := views[testKey()]
	require.Equal(t, OwnerCold, v.Owner)
	require.EqualValues(t, 2, v.Generation)
	require.Equal(t, "cold-g2", v.OwnerDirID)
	require.True(t, v.AuthoritySwitched)

	// 残留旧目录即使“VL re-attach”写进 Dirs，也不得查询。
	rec, _ := c2.Get(testKey())
	require.NoError(t, c2.Put(func() *Record {
		r := rec.Clone()
		r.Dirs = append(r.Dirs, PhysicalDir{ID: "hot-g1", Role: DirResidual})
		return r
	}()))
	// 再 recover 一次（journal 仍在）
	_ = c2.StartingRecover()
	require.False(t, c2.IsQueryDirVisible(testKey(), "hot-g1"))
	require.True(t, c2.IsQueryDirVisible(testKey(), "cold-g2"))
}

func TestCatalog_FileBackedJournalRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	store, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	j, err := NewJournalWithStore(store)
	require.NoError(t, err)

	c := New(j)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	_, err = c.BeginMigration(testKey(), OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
		_, err = c.Advance(testKey(), st)
		require.NoError(t, err)
	}
	_, err = c.SwitchOwner(testKey(), OwnerCold, "cold-g2", 2, &PublishedProjection{ManifestVersion: "pv-1"})
	require.NoError(t, err)

	// 重启：新 store + new journal + empty catalog put pre-switch snapshot.
	store2, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	j2, err := NewJournalWithStore(store2)
	require.NoError(t, err)
	c2 := New(j2)
	require.NoError(t, c2.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	views := c2.StartingRecover()
	v := views[testKey()]
	require.Equal(t, OwnerCold, v.Owner)
	require.Equal(t, "cold-g2", v.QueryDirID)
	require.True(t, v.AuthoritySwitched)
}

func TestCatalog_FailAndRetryableRecovery(t *testing.T) {
	c := New(nil)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	_, err := c.BeginMigration(testKey(), OwnerCold, "cold-g2")
	require.NoError(t, err)
	_, err = c.Advance(testKey(), StateDraining)
	require.NoError(t, err)
	rec, err := c.Fail(testKey(), StateFailedRetryable, "drain timeout")
	require.NoError(t, err)
	require.Equal(t, StateFailedRetryable, rec.MigrationState)
	require.True(t, rec.RecoveryRequired)

	views := c.StartingRecover()
	v := views[testKey()]
	require.True(t, v.RecoveryRequired)
	require.Equal(t, OwnerHot, v.Owner)
	require.Equal(t, "hot-g1", v.QueryDirID)
	require.False(t, c.IsQueryDirVisible(testKey(), "cold-g2"))

	// FAILED_RETRYABLE 可回到正常路径继续。
	rec, err = c.Advance(testKey(), StateDraining)
	require.NoError(t, err)
	require.Equal(t, StateDraining, rec.MigrationState)
}

func TestCatalog_MissingRecord(t *testing.T) {
	c := New(nil)
	_, err := c.BeginMigration(testKey(), OwnerCold, "cold-g2")
	require.ErrorIs(t, err, ErrNotFound)
	_, ok := c.Get(testKey())
	require.False(t, ok)
	wt, ok := c.RouteWrite(testKey(), "2026-09-20")
	require.False(t, ok)
	require.False(t, wt.OK)
}

func TestOwnerValid(t *testing.T) {
	require.True(t, OwnerHot.Valid())
	require.True(t, OwnerCold.Valid())
	require.True(t, OwnerArchive.Valid())
	require.False(t, Owner("warm").Valid())
}
