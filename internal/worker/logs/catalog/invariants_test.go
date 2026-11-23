package catalog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func sampleHot() *Record {
	key := PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
	rec := NewStableRecord(key, OwnerHot, 1, "hot-g1")
	rec.WriteRoute.LateOwner = OwnerHot
	rec.WriteRoute.LateDirID = "hot-g1"
	return rec
}

func sampleMigrating(t *testing.T, state MigrationState) *Record {
	t.Helper()

	// 失败态：从 STAGING_VERIFY 失败（未切换权威）。
	if state == StateFailedRetryable || state == StateFailedManual {
		rec := sampleMigrating(t, StateStagingVerify)
		require.NoError(t, Transition(rec, state))
		rec.RecoveryRequired = true
		return rec
	}

	rec := sampleHot()
	require.NoError(t, BeginMigration(rec, OwnerCold, "cold-g2"))
	if state == StateRoutingFrozen {
		return rec
	}

	path := []MigrationState{
		StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging,
	}
	reached := state == StateRoutingFrozen
	for _, s := range path {
		require.NoError(t, Transition(rec, s))
		if state == s {
			reached = true
			break
		}
	}
	if reached {
		return rec
	}

	// OWNER_SWITCHED 及之后
	require.Equal(t, StateAttachedStaging, rec.MigrationState)
	proj := &PublishedProjection{
		ManifestVersion:    "pv-1",
		CoverageComplete:   true,
		QueryLocationDirID: "cold-g2",
		QueryGeneration:    2,
	}
	require.NoError(t, ApplyOwnerSwitch(rec, OwnerCold, "cold-g2", 2, proj))
	if state == StateOwnerSwitched {
		return rec
	}

	after := []MigrationState{StateQueryLeaseDraining, StateDetached, StateCleaned}
	for _, s := range after {
		require.NoError(t, Transition(rec, s))
		if state == s {
			if s == StateCleaned {
				rec.JournalComplete = true
				rec.ResidualDirs = nil
				rec.LastVerifyCompleted = true
				rec.LastVerifyChecksum = "sum"
			}
			return rec
		}
	}
	t.Fatalf("unsupported target state %s", state)
	return nil
}

// ATTACHED_STAGING：副本可被 VL 挂载，但 QueryPlanner 必须排除；可查询侧仍是切换前权威。
func TestInvariant_AttachedStagingNotQueryable(t *testing.T) {
	rec := sampleMigrating(t, StateAttachedStaging)

	require.False(t, StagingQueryable(rec), "staging must never be query-planner visible")
	require.False(t, IsQueryPlannerVisible(rec, "cold-g2"), "attached staging dir must be excluded")
	require.True(t, IsQueryPlannerVisible(rec, "hot-g1"), "pre-switch owner remains queryable")

	ref := OwnerForQuery(rec)
	require.True(t, ref.OK)
	require.Equal(t, OwnerHot, ref.Owner)
	require.Equal(t, "hot-g1", ref.DirID)
	require.EqualValues(t, 1, ref.Generation)

	side := QueryableSideOf(rec)
	require.True(t, side.StagingOpen)
	require.False(t, IsQueryPlannerVisible(rec, rec.TargetDirID))
}

// OWNER_SWITCHED 是唯一原子权威切换：之后 owner/generation/写路由/projection 一起变，旧目录被排除。
func TestInvariant_OwnerSwitchedIsOnlyAtomicAuthoritySwitch(t *testing.T) {
	require.True(t, IsAtomicAuthoritySwitch(StateOwnerSwitched))
	for _, s := range HappyPath {
		if s == StateOwnerSwitched {
			continue
		}
		require.False(t, IsAtomicAuthoritySwitch(s), "state %s must not be authority switch", s)
	}
	require.False(t, IsAtomicAuthoritySwitch(StateFailedRetryable))
	require.False(t, IsAtomicAuthoritySwitch(StateFailedManual))

	rec := sampleMigrating(t, StateAttachedStaging)
	// 切换前写路由已冻结，迟到不进 HOT。
	wr := OwnerForWrite(rec)
	require.True(t, wr.Frozen)
	require.NotEqual(t, OwnerHot, wr.Owner, "frozen route must not write back to HOT authority")

	// 非 ATTACHED_STAGING 不允许切换。
	bad := sampleHot()
	proj := &PublishedProjection{ManifestVersion: "x"}
	require.ErrorIs(t, ApplyOwnerSwitch(bad, OwnerCold, "cold-g2", 2, proj), ErrIllegalTransition)

	require.ErrorIs(t, ApplyOwnerSwitch(rec, OwnerCold, "cold-g2", 2, nil), ErrMissingProjection)

	proj = &PublishedProjection{ManifestVersion: "pv-1", CoverageComplete: true}
	require.NoError(t, ApplyOwnerSwitch(rec, OwnerCold, "cold-g2", 2, proj))

	require.Equal(t, StateOwnerSwitched, rec.MigrationState)
	require.Equal(t, OwnerCold, rec.Owner)
	require.EqualValues(t, 2, rec.Generation)
	require.Equal(t, "cold-g2", rec.OwnerDirID)
	require.NotNil(t, rec.PublishedProjection)
	require.EqualValues(t, 2, rec.PublishedProjection.QueryGeneration)

	wr = OwnerForWrite(rec)
	require.Equal(t, OwnerCold, wr.Owner)
	require.Equal(t, "cold-g2", wr.DirID)
	require.False(t, wr.Frozen)

	// 旧目录 re-attach 仍被排除。
	require.False(t, IsQueryPlannerVisible(rec, "hot-g1"))
	require.True(t, IsQueryPlannerVisible(rec, "cold-g2"))
	require.False(t, IsQueryPlannerVisible(rec, rec.MigrationFromDirID))

	// residual 登记存在
	found := false
	for _, d := range rec.ResidualDirs {
		if d.ID == "hot-g1" && d.Role == DirResidual {
			found = true
		}
	}
	require.True(t, found, "old owner dir must be residual")
}

// DETACHED ≠ 完成：journal 未完成 / residual 残留时仍是恢复态。
func TestInvariant_DetachedNotCompleteWhenJournalIncomplete(t *testing.T) {
	rec := sampleMigrating(t, StateDetached)
	rec.JournalComplete = false
	require.True(t, JournalIncomplete(rec))
	require.False(t, IsMigrationComplete(rec))
	require.False(t, DetachIsComplete(rec))

	view := Recover(rec)
	require.False(t, view.MigrationComplete)
	require.True(t, view.RecoveryRequired)
	require.Contains(t, view.PartialReasons, "DETACHED_NOT_COMPLETE")
	require.Contains(t, view.PartialReasons, "JOURNAL_INCOMPLETE")

	// 即使 journal complete，DETACHED 本身也不等于 CLEANED 完成。
	rec.JournalComplete = true
	require.False(t, IsMigrationComplete(rec), "DETACHED alone is not migration complete")
	require.False(t, DetachIsComplete(rec))

	// CLEANED + journal complete + 无 residual 才完成。
	clean := sampleMigrating(t, StateCleaned)
	clean.JournalComplete = true
	clean.ResidualDirs = nil
	require.True(t, IsMigrationComplete(clean))
}

// 迟到事件按 Catalog owner/generation 路由，不按 now-7d 日历猜测。
func TestInvariant_LateEventsRouteByCatalogNotCalendar(t *testing.T) {
	// 稳定 HOT 分区。
	hot := sampleHot()
	late := RouteLateEvent(hot, "2026-09-01") // 远早于 now-7d
	require.True(t, late.OK)
	require.Equal(t, OwnerHot, late.Owner)
	require.Equal(t, "hot-g1", late.DirID)
	require.EqualValues(t, 1, late.Generation)

	// 迁移冻结中：即便事件 utc_day 很“老”，也不得回写 HOT 权威。
	frozen := sampleMigrating(t, StateRoutingFrozen)
	for _, day := range []string{"2026-09-20", "2026-09-01", "2020-01-01", ""} {
		lt := RouteLateEvent(frozen, day)
		require.True(t, lt.OK)
		require.NotEqual(t, OwnerHot, lt.Owner, "day=%q must not invent HOT authority", day)
		require.Equal(t, OwnerCold, lt.Owner)
		require.Equal(t, "cold-g2", lt.DirID)
		require.True(t, lt.Frozen)
	}

	// 切换后：按 Catalog owner=COLD 路由。
	sw := sampleMigrating(t, StateOwnerSwitched)
	lt := RouteLateEvent(sw, "2026-09-19")
	require.Equal(t, OwnerCold, lt.Owner)
	require.Equal(t, "cold-g2", lt.DirID)
	require.EqualValues(t, 2, lt.Generation)

	// 无 Catalog 记录：不路由。
	miss := RouteLateEvent(nil, "2026-09-20")
	require.False(t, miss.OK)
}

// re-attach 的旧目录必须持续排除出 QueryPlanner（OwnerForQuery 只认 Catalog）。
func TestInvariant_ReattachedOldDirExcludedFromQueryPlanner(t *testing.T) {
	rec := sampleMigrating(t, StateOwnerSwitched)

	// 模拟 VL 自动 re-attach 旧目录：扫描结果写入 Dirs，但角色不是 owner。
	rec.Dirs = append(rec.Dirs, PhysicalDir{ID: "hot-g1", Path: "/var/vl/hot/2026-09-20", Role: DirResidual})

	// 物理存在 ≠ 查询权威。
	require.False(t, IsQueryPlannerVisible(rec, "hot-g1"))
	require.True(t, IsQueryPlannerVisible(rec, "cold-g2"))

	ref := OwnerForQuery(rec)
	require.Equal(t, "cold-g2", ref.DirID)
	require.NotEqual(t, "hot-g1", ref.DirID)

	// 崩溃恢复后同样排除。
	view := Recover(rec)
	require.Contains(t, view.ExcludedQueryDirs, "hot-g1")
	require.Equal(t, "cold-g2", view.QueryDirID)
	require.True(t, view.Queryable)
}

func TestMigrationTransitionsExactHappyPath(t *testing.T) {
	require.Equal(t, []MigrationState{
		StateRoutingFrozen, StateDraining, StateSnapshotting, StateStagingVerify,
		StateAttachedStaging, StateOwnerSwitched, StateQueryLeaseDraining,
		StateDetached, StateCleaned,
	}, HappyPath)

	for i := 0; i < len(HappyPath)-1; i++ {
		require.True(t, CanTransition(HappyPath[i], HappyPath[i+1]), "%s→%s", HappyPath[i], HappyPath[i+1])
	}
	// 禁止跳步与回退。
	require.False(t, CanTransition(StateRoutingFrozen, StateAttachedStaging))
	require.False(t, CanTransition(StateOwnerSwitched, StateAttachedStaging))
	require.False(t, CanTransition(StateCleaned, StateDraining))
	// 允许进入失败态。
	require.True(t, CanTransition(StateStagingVerify, StateFailedRetryable))
	require.True(t, CanTransition(StateStagingVerify, StateFailedManual))
	require.True(t, CanTransition(StateFailedRetryable, StateAttachedStaging))
}

func TestBeginMigrationFreezesWriteRoute(t *testing.T) {
	rec := sampleHot()
	require.NoError(t, BeginMigration(rec, OwnerCold, "cold-g2"))
	require.Equal(t, StateRoutingFrozen, rec.MigrationState)
	require.True(t, rec.WriteRoute.Frozen)
	require.Equal(t, OwnerCold, rec.WriteRoute.LateOwner)
	require.EqualValues(t, 2, rec.TargetGeneration)
	require.Equal(t, OwnerHot, rec.MigrationFromOwner)
	require.False(t, rec.JournalComplete)

	// 已有在途迁移时禁止再次 begin。
	require.ErrorIs(t, BeginMigration(rec, OwnerArchive, "arc-g3"), ErrIllegalTransition)
}

func TestQueryableSideDuringMigration(t *testing.T) {
	cases := []struct {
		state       MigrationState
		wantOwner   Owner
		wantDir     string
		wantQuery   bool
		wantStaging bool
	}{
		{StateRoutingFrozen, OwnerHot, "hot-g1", true, false},
		{StateDraining, OwnerHot, "hot-g1", true, false},
		{StateSnapshotting, OwnerHot, "hot-g1", true, false},
		{StateStagingVerify, OwnerHot, "hot-g1", true, false},
		{StateAttachedStaging, OwnerHot, "hot-g1", true, true},
		{StateOwnerSwitched, OwnerCold, "cold-g2", true, false},
		{StateQueryLeaseDraining, OwnerCold, "cold-g2", true, false},
		{StateDetached, OwnerCold, "cold-g2", true, false},
		{StateCleaned, OwnerCold, "cold-g2", true, false},
	}
	for _, tc := range cases {
		rec := sampleMigrating(t, tc.state)
		view := Recover(rec)
		require.Equal(t, tc.wantOwner, view.Owner, "state=%s owner", tc.state)
		require.Equal(t, tc.wantDir, view.OwnerDirID, "state=%s dir", tc.state)
		require.Equal(t, tc.wantQuery, view.Queryable, "state=%s queryable", tc.state)
		require.Equal(t, tc.wantStaging, view.StagingOpen, "state=%s staging", tc.state)
		require.Equal(t, tc.wantDir, view.QueryDirID, "state=%s query dir", tc.state)
	}
}

func TestQueryLeaseRefActive(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	live := QueryLeaseRef{LeaseID: "l1", ViewID: "v1", Generation: 2, ExpiresAt: now.Add(time.Hour)}
	require.True(t, live.Active(now, 2))
	require.False(t, live.Active(now, 1), "lease bound to generation")

	expired := QueryLeaseRef{LeaseID: "l2", ViewID: "v2", Generation: 2, ExpiresAt: now.Add(-time.Minute)}
	require.False(t, expired.Active(now, 2))

	invalid := QueryLeaseRef{LeaseID: "l3", ViewID: "v3", Generation: 2, InvalidReason: "VIEW_STALE"}
	require.False(t, invalid.Active(now, 2))

	liveGen1 := QueryLeaseRef{LeaseID: "l1b", ViewID: "v1b", Generation: 1, ExpiresAt: now.Add(time.Hour)}
	rec := sampleHot()
	rec.AddLease(live)
	rec.AddLease(liveGen1)
	require.Equal(t, 2, rec.LeaseCount())
	require.Equal(t, 1, rec.ActiveLeaseCount(now), "only generation-1 lease is active on current gen")
}
