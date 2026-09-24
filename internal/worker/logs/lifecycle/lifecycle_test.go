package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func testKey() catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "ns/game-1", UTCDay: "2026-09-20"}
}

// recordingOps 记录调用顺序，可按步骤注入失败。
type recordingOps struct {
	calls     []string
	failAt    string
	checksum  string
	snapshotN int
}

func (r *recordingOps) ops() Ops {
	return Ops{
		Drain: func(key catalog.PartitionKey) error {
			r.calls = append(r.calls, "drain")
			return r.errIf("drain")
		},
		Snapshot: func(key catalog.PartitionKey, fromDir string) (string, error) {
			r.calls = append(r.calls, "snapshot")
			if err := r.errIf("snapshot"); err != nil {
				return "", err
			}
			r.snapshotN++
			return "snap-1", nil
		},
		Copy: func(key catalog.PartitionKey, snapshotID, toDir string) (string, error) {
			r.calls = append(r.calls, "copy")
			if err := r.errIf("copy"); err != nil {
				return "", err
			}
			return "copy-1", nil
		},
		Verify: func(key catalog.PartitionKey, copyID, toDir string) (string, error) {
			r.calls = append(r.calls, "verify")
			if err := r.errIf("verify"); err != nil {
				return "", err
			}
			if r.checksum == "" {
				r.checksum = "sha256:ok"
			}
			return r.checksum, nil
		},
		Attach: func(key catalog.PartitionKey, dirID string) error {
			r.calls = append(r.calls, "attach")
			return r.errIf("attach")
		},
		LeaseDrain: func(key catalog.PartitionKey, oldGen uint64) error {
			r.calls = append(r.calls, "lease-drain")
			return r.errIf("lease-drain")
		},
		Detach: func(key catalog.PartitionKey, dirID string) error {
			r.calls = append(r.calls, "detach")
			return r.errIf("detach")
		},
	}
}

func (r *recordingOps) errIf(op string) error {
	if r.failAt == op {
		return errors.New("simulated failure at " + op)
	}
	return nil
}

// FR-476 happy path：状态走完、owner 切换、staging 期间不可查询、迟到事件路由 COLD。
func TestLifecycle_HappyPathOwnerSwitch(t *testing.T) {
	cat := catalog.New(nil)
	rec := &recordingOps{}
	m := New(cat, rec.ops())
	require.NoError(t, m.PutPartition(testKey(), catalog.OwnerHot, 1, "hot-g1"))

	// 观察：ATTACHED_STAGING 时 staging 不可查询、HOT 仍可查询。
	var sawStagingNotQueryable, sawHotQueryable, sawOwnerSwitched bool
	m.SetObserver(func(key catalog.PartitionKey, r *catalog.Record) {
		switch r.MigrationState {
		case catalog.StateAttachedStaging:
			sawStagingNotQueryable = !m.IsDirQueryable(key.StorageNamespace, key.UTCDay, "cold-g2")
			sawHotQueryable = m.IsDirQueryable(key.StorageNamespace, key.UTCDay, "hot-g1")
		case catalog.StateOwnerSwitched:
			sawOwnerSwitched = r.Owner == catalog.OwnerCold && r.Generation == 2
		}
	})

	rt := NewVLSupRuntime(nil)
	m.SetRuntime(rt)

	res, err := m.StartMigration(testKey().StorageNamespace, testKey().UTCDay, catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	require.True(t, res.OwnerSwitched)
	require.Equal(t, catalog.StateCleaned, res.Record.MigrationState)
	require.Equal(t, catalog.OwnerCold, res.Record.Owner)
	require.EqualValues(t, 2, res.Record.Generation)
	require.Equal(t, "cold-g2", res.Record.OwnerDirID)
	require.NotNil(t, res.Record.PublishedProjection)
	require.False(t, res.Record.PublishedProjection.CoverageComplete,
		"migration cannot invent complete coverage when the source had no published projection")

	require.True(t, sawStagingNotQueryable, "ATTACHED_STAGING must exclude staging from QueryPlanner")
	require.True(t, sawHotQueryable, "query authority stays on HOT until OWNER_SWITCHED")
	require.True(t, sawOwnerSwitched)

	// 切换后：旧 owner 目录被过滤，新 owner 可查询。
	require.False(t, m.IsDirQueryable(testKey().StorageNamespace, testKey().UTCDay, "hot-g1"))
	require.True(t, m.IsDirQueryable(testKey().StorageNamespace, testKey().UTCDay, "cold-g2"))
	ref, ok := m.QueryOwner(testKey().StorageNamespace, testKey().UTCDay)
	require.True(t, ok)
	require.Equal(t, catalog.OwnerCold, ref.Owner)
	require.Equal(t, "cold-g2", ref.DirID)

	// 迟到事件按 Catalog 路由 COLD，不按日历回写 HOT。
	late, ok := m.RouteLateEvent(testKey().StorageNamespace, testKey().UTCDay, "2026-09-01")
	require.True(t, ok)
	require.True(t, late.OK)
	require.Equal(t, catalog.OwnerCold, late.Owner)
	require.Equal(t, "cold-g2", late.DirID)

	// 操作顺序覆盖完整状态机。
	require.Equal(t, []string{"drain", "snapshot", "copy", "verify", "attach", "lease-drain", "detach"}, rec.calls)
	require.Equal(t, []catalog.MigrationState{
		catalog.StateRoutingFrozen,
		catalog.StateDraining,
		catalog.StateSnapshotting,
		catalog.StateStagingVerify,
		catalog.StateAttachedStaging,
		catalog.StateOwnerSwitched,
		catalog.StateQueryLeaseDraining,
		catalog.StateDetached,
		catalog.StateCleaned,
	}, res.StatesWalked)

	// residual 已清理 → CLEANED 真正完成。
	require.True(t, catalog.IsMigrationComplete(res.Record))
	require.Empty(t, res.Record.ResidualDirs)

	// Runtime 账面：staging 曾 attach，旧 dir 已 detach。
	require.NotContains(t, rt.AttachedDirs(), "hot-g1")
}

func TestResumeMigrationCompletesEveryPersistedCrashState(t *testing.T) {
	states := append([]catalog.MigrationState(nil), catalog.HappyPath...)
	for _, crashState := range states {
		t.Run(string(crashState), func(t *testing.T) {
			journal := catalog.NewMemJournal()
			cat := catalog.New(journal)
			rec := catalog.NewStableRecord(testKey(), catalog.OwnerHot, 1, "hot-g1")
			rec.PublishedProjection = &catalog.PublishedProjection{
				ManifestVersion: "hot-manifest", ProjectionGeneration: "projection-1",
				CoverageComplete: true, QueryLocationDirID: "hot-g1", QueryGeneration: 1,
				ClosedVisibleSeq: map[string]uint64{testKey().String(): 42},
			}
			require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: testKey(), State: catalog.StateCleaned,
				Owner: rec.Owner, Generation: rec.Generation, DirID: rec.OwnerDirID,
				AuthorityCommit: true, JournalComplete: true, Record: rec}))
			ops := Ops{
				Drain:      func(catalog.PartitionKey) error { return nil },
				Snapshot:   func(catalog.PartitionKey, string) (string, error) { return "snapshot", nil },
				Copy:       func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
				Verify:     func(catalog.PartitionKey, string, string) (string, error) { return "checksum", nil },
				Attach:     func(catalog.PartitionKey, string) error { return nil },
				LeaseDrain: func(catalog.PartitionKey, uint64) error { return nil },
				Detach:     func(catalog.PartitionKey, string) error { return nil },
				Cleanup:    func(catalog.PartitionKey, string) error { return nil },
			}
			manager := New(cat, ops)
			manager.SetRetentionGate(RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "ok" }))
			manager.SetObserver(func(_ catalog.PartitionKey, current *catalog.Record) {
				if current.MigrationState == crashState {
					panic("injected crash")
				}
			})
			crashed := false
			func() {
				defer func() {
					if recover() != nil {
						crashed = true
					}
				}()
				_, _ = manager.StartMigration(testKey().StorageNamespace, testKey().UTCDay, catalog.OwnerCold, "cold-g2")
			}()
			require.True(t, crashed)

			restartedCatalog := catalog.New(journal)
			restartedCatalog.StartingRecover()
			restarted := New(restartedCatalog, ops)
			restarted.SetRetentionGate(RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "ok" }))
			result, err := restarted.ResumeMigration(testKey().StorageNamespace, testKey().UTCDay)
			require.NoError(t, err)
			require.Equal(t, catalog.StateCleaned, result.Record.MigrationState)
			require.Equal(t, catalog.OwnerCold, result.Record.Owner)
			require.False(t, restarted.IsDirQueryable(testKey().StorageNamespace, testKey().UTCDay, "hot-g1"))
			require.True(t, restarted.IsDirQueryable(testKey().StorageNamespace, testKey().UTCDay, "cold-g2"))
			require.NotNil(t, result.Record.PublishedProjection)
			require.True(t, result.Record.PublishedProjection.CoverageComplete)
			require.EqualValues(t, 42, result.Record.PublishedProjection.ClosedVisibleSeq[testKey().String()])
		})
	}
}

// FR-476：staging attach 成功也不得进入 QueryPlanner（显式夹具）。
func TestLifecycle_StagingNotQueryable(t *testing.T) {
	cat := catalog.New(nil)
	m := New(cat, Ops{})
	require.NoError(t, m.PutPartition(testKey(), catalog.OwnerHot, 1, "hot-g1"))

	// 手动推进到 ATTACHED_STAGING。
	_, err := cat.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	for _, st := range []catalog.MigrationState{
		catalog.StateDraining,
		catalog.StateSnapshotting,
		catalog.StateStagingVerify,
		catalog.StateAttachedStaging,
	} {
		_, err = cat.Advance(testKey(), st)
		require.NoError(t, err)
	}

	require.False(t, m.IsDirQueryable("ns/game-1", "2026-09-20", "cold-g2"), "staging must not be queryable")
	require.True(t, m.IsDirQueryable("ns/game-1", "2026-09-20", "hot-g1"))
	ref, ok := m.QueryOwner("ns/game-1", "2026-09-20")
	require.True(t, ok)
	require.Equal(t, "hot-g1", ref.DirID)

	// catalog.StagingQueryable 契约恒 false。
	require.False(t, catalog.StagingQueryable(mustRec(t, m)))
}

func mustRec(t *testing.T, m *Manager) *catalog.Record {
	t.Helper()
	rec, ok := m.GetPartition("ns/game-1", "2026-09-20")
	require.True(t, ok)
	return rec
}

// FR-476 Runbook B：每个迁移状态崩溃后 Recover 必须给出期望的 owner/query 侧。
func TestLifecycle_SimulatedCrashRecoverEachState(t *testing.T) {
	type expect struct {
		queryable   bool
		queryDir    string
		owner       catalog.Owner
		needRecover bool
		stagingOpen bool
		actionSub   string
		exclude     []string
	}
	cases := []struct {
		state catalog.MigrationState
		want  expect
	}{
		{
			state: catalog.StateRoutingFrozen,
			want: expect{
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				// journal incomplete → RecoveryRequired（契约：迁移中崩溃须标恢复）。
				needRecover: true, actionSub: "resume-from-draining",
				exclude: []string{"cold-g2"},
			},
		},
		{
			state: catalog.StateDraining,
			want: expect{
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				needRecover: true, actionSub: "resume-draining",
				exclude: []string{"cold-g2"},
			},
		},
		{
			state: catalog.StateSnapshotting,
			want: expect{
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				needRecover: true, actionSub: "resume-snapshotting",
				exclude: []string{"cold-g2"},
			},
		},
		{
			state: catalog.StateStagingVerify,
			want: expect{
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				needRecover: true, actionSub: "resume-staging-verify",
				exclude: []string{"cold-g2"},
			},
		},
		{
			state: catalog.StateAttachedStaging,
			want: expect{
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				needRecover: true, stagingOpen: true, actionSub: "keep-staging-excluded",
				exclude: []string{"cold-g2"},
			},
		},
		{
			state: catalog.StateOwnerSwitched,
			want: expect{
				queryable: true, queryDir: "cold-g2", owner: catalog.OwnerCold,
				needRecover: true, actionSub: "resume-query-lease-draining",
				exclude: []string{"hot-g1"},
			},
		},
		{
			state: catalog.StateQueryLeaseDraining,
			want: expect{
				queryable: true, queryDir: "cold-g2", owner: catalog.OwnerCold,
				needRecover: true, actionSub: "drain-legacy-query-leases",
				exclude: []string{"hot-g1"},
			},
		},
		{
			state: catalog.StateDetached,
			want: expect{
				queryable: true, queryDir: "cold-g2", owner: catalog.OwnerCold,
				needRecover: true, actionSub: "verify-residuals",
				exclude: []string{"hot-g1"},
			},
		},
		{
			state: catalog.StateCleaned,
			want: expect{
				// CLEANED + journal complete + residual 清理 → 真正完成。
				queryable: true, queryDir: "cold-g2", owner: catalog.OwnerCold,
				needRecover: false, actionSub: "none",
				exclude: nil,
			},
		},
		{
			state: catalog.StateFailedRetryable,
			want: expect{
				// 未切换权威的失败：回到 HOT。
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				needRecover: true, actionSub: "retry-migration",
				exclude: []string{"cold-g2"},
			},
		},
		{
			state: catalog.StateFailedManual,
			want: expect{
				queryable: true, queryDir: "hot-g1", owner: catalog.OwnerHot,
				needRecover: true, actionSub: "manual-intervention",
				exclude: []string{"cold-g2"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			cat := catalog.New(nil)
			require.NoError(t, cat.Put(catalog.NewStableRecord(testKey(), catalog.OwnerHot, 1, "hot-g1")))
			advanceCrash(cat, t, tc.state)

			m := New(cat, Ops{})
			views := m.Recover()
			view, ok := views[testKey()]
			require.True(t, ok, "recover must project partition")
			require.Equal(t, tc.state, view.MigrationState)
			require.Equal(t, tc.want.owner, view.Owner, "owner")
			require.Equal(t, tc.want.queryable, view.Queryable, "queryable")
			require.Equal(t, tc.want.queryDir, view.QueryDirID, "query dir")
			require.Equal(t, tc.want.stagingOpen, view.StagingOpen, "staging open")
			require.Equal(t, tc.want.needRecover, view.RecoveryRequired, "recovery required")
			require.Contains(t, view.NextRecoveryAction, tc.want.actionSub, "next action")

			for _, dir := range tc.want.exclude {
				require.False(t, m.IsDirQueryable(testKey().StorageNamespace, testKey().UTCDay, dir),
					"state=%s dir=%s must be excluded after recover", tc.state, dir)
			}
			// 旧 owner re-attach 过滤：非 query dir 的一侧不可见。
			require.Equal(t, tc.want.queryDir == "cold-g2", m.IsDirQueryable("ns/game-1", "2026-09-20", "cold-g2"))
			require.Equal(t, tc.want.queryDir == "hot-g1", m.IsDirQueryable("ns/game-1", "2026-09-20", "hot-g1"))
		})
	}
}

// advanceCrash 把稳定记录推到目标状态，模拟“进程在该状态崩溃”。
func advanceCrash(cat *catalog.Catalog, t *testing.T, target catalog.MigrationState) {
	t.Helper()
	if catalog.IsFailure(target) {
		// 失败态：从 STAGING_VERIFY 失败（权威未切换）。
		advanceCrash(cat, t, catalog.StateStagingVerify)
		_, err := cat.Fail(testKey(), target, "simulated crash-fail")
		require.NoError(t, err)
		return
	}
	if target == "" {
		return
	}
	_, err := cat.BeginMigration(testKey(), catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	if target == catalog.StateRoutingFrozen {
		return
	}
	for _, st := range catalog.HappyPath {
		if st == catalog.StateRoutingFrozen {
			continue
		}
		if st == catalog.StateOwnerSwitched {
			proj := &catalog.PublishedProjection{
				ManifestVersion:    "pv-cold-g2",
				CoverageComplete:   true,
				QueryLocationDirID: "cold-g2",
				QueryGeneration:    2,
			}
			_, err = cat.SwitchOwner(testKey(), catalog.OwnerCold, "cold-g2", 2, proj)
			require.NoError(t, err)
		} else {
			_, err = cat.Advance(testKey(), st)
			require.NoError(t, err)
		}
		if st == target {
			if st == catalog.StateCleaned {
				// CLEANED 完成态：journal complete + residual 清理。
				if rec, ok := cat.Get(testKey()); ok {
					rec.ResidualDirs = nil
					rec.LastVerifyCompleted = true
					rec.LastVerifyChecksum = "sha256:ok"
					require.NoError(t, cat.Put(rec))
				}
				require.NoError(t, cat.MarkJournalComplete(testKey(), "sha256:ok"))
			}
			return
		}
	}
}

// StartMigration 中途操作失败 → FAILED_RETRYABLE，Recover 仍给出权威侧。
func TestLifecycle_OpsFailureThenRecover(t *testing.T) {
	cat := catalog.New(nil)
	rec := &recordingOps{failAt: "verify"}
	m := New(cat, rec.ops())
	require.NoError(t, m.PutPartition(testKey(), catalog.OwnerHot, 1, "hot-g1"))

	res, err := m.StartMigration("ns/game-1", "2026-09-20", catalog.OwnerCold, "cold-g2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "verify")
	require.Equal(t, catalog.StateFailedRetryable, res.Record.MigrationState)
	require.Equal(t, catalog.OwnerHot, res.Record.Owner, "authority stays on last successful commit")
	require.True(t, res.Record.RecoveryRequired)

	views := m.Recover()
	view := views[testKey()]
	require.True(t, view.RecoveryRequired)
	require.Equal(t, catalog.OwnerHot, view.Owner)
	require.Equal(t, "hot-g1", view.QueryDirID)
	require.False(t, m.IsDirQueryable("ns/game-1", "2026-09-20", "cold-g2"))
}

// retention last-copy：未确认接收前拒绝 detach（owner 已切换但迁移未完成）。
func TestLifecycle_RetentionLastCopyProtection(t *testing.T) {
	cat := catalog.New(nil)
	rec := &recordingOps{}
	m := New(cat, rec.ops())
	require.NoError(t, m.PutPartition(testKey(), catalog.OwnerHot, 1, "hot-g1"))

	m.SetRetentionGate(RetentionGateFunc(func(key catalog.PartitionKey, fromDir, toDir string) (bool, string) {
		return false, "next-tier copy not confirmed for " + toDir
	}))

	res, err := m.StartMigration("ns/game-1", "2026-09-20", catalog.OwnerCold, "cold-g2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "last-copy")
	require.True(t, res.OwnerSwitched)
	require.NotContains(t, res.StatesWalked, catalog.StateDetached)
	require.NotContains(t, res.StatesWalked, catalog.StateCleaned)
	require.Equal(t, catalog.StateQueryLeaseDraining, res.Record.MigrationState)

	// 旧目录仍在，迁移未完成。
	require.Contains(t, res.BlockedReason, "last-copy")
	require.False(t, catalog.IsMigrationComplete(res.Record))
	require.False(t, m.IsDirQueryable("ns/game-1", "2026-09-20", "hot-g1"), "old owner still filtered after switch")
	require.True(t, m.IsDirQueryable("ns/game-1", "2026-09-20", "cold-g2"))
}

// retention 放行时 detach 发生且 CLEANED。
func TestLifecycle_RetentionAllowsDetachWhenConfirmed(t *testing.T) {
	cat := catalog.New(nil)
	m := New(cat, Ops{})
	require.NoError(t, m.PutPartition(testKey(), catalog.OwnerHot, 1, "hot-g1"))
	m.SetRetentionGate(RetentionGateFunc(func(key catalog.PartitionKey, fromDir, toDir string) (bool, string) {
		require.Equal(t, "hot-g1", fromDir)
		require.Equal(t, "cold-g2", toDir)
		return true, ""
	}))
	res, err := m.StartMigration("ns/game-1", "2026-09-20", catalog.OwnerCold, "cold-g2")
	require.NoError(t, err)
	require.Equal(t, catalog.StateCleaned, res.Record.MigrationState)
}

// VLSupRuntime 薄适配：Status 缺 supervisor 时报错；Attach/Detach 账面可用。
func TestVLSupRuntime_Adapter(t *testing.T) {
	rt := NewVLSupRuntime(nil)
	_, err := rt.Status(vlsup.NamespaceHot)
	require.Error(t, err)

	require.NoError(t, rt.AttachStorage(vlsup.NamespaceCold, "cold-g2", "/data/cold-g2"))
	require.Contains(t, rt.AttachedDirs(), "cold-g2")
	require.NoError(t, rt.DetachStorage(vlsup.NamespaceCold, "cold-g2"))
	require.NotContains(t, rt.AttachedDirs(), "cold-g2")

	require.Equal(t, vlsup.NamespaceHot, NamespaceForOwner(catalog.OwnerHot))
	require.Equal(t, vlsup.NamespaceCold, NamespaceForOwner(catalog.OwnerCold))
	require.Equal(t, vlsup.NamespaceRehydrate, NamespaceForOwner(catalog.OwnerArchive))

	_ = time.Now()
}
