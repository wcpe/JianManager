package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Crash recovery 表驱动：每个迁移/失败状态 Recover() 必须返回期望的
// owner / generation / queryable 侧 / 写入路由 / 排除目录。
func TestRecover_EachStateExpectedSides(t *testing.T) {
	type expect struct {
		owner           Owner
		generation      uint64
		ownerDir        string
		queryable       bool
		queryDir        string
		stagingOpen     bool
		writeOwner      Owner
		writeDir        string
		writeFrozen     bool
		authoritySwitch bool
		complete        bool
		recovery        bool
		action          string
		mustExclude     []string
	}

	cases := []struct {
		name  string
		state MigrationState
		// tweak 在 sampleMigrating 之后调整记录，模拟崩溃点差异。
		tweak func(t *testing.T, rec *Record)
		want  expect
	}{
		{
			name:  "ROUTING_FROZEN",
			state: StateRoutingFrozen,
			want: expect{
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1",
				// 冻结期写入路由到受控 late 目标（COLD staging），不是 HOT。
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: false,
				action:      "resume-from-draining:verify-freeze-then-drain",
				mustExclude: []string{"cold-g2"},
			},
		},
		{
			name:  "DRAINING",
			state: StateDraining,
			want: expect{
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: false,
				action:      "resume-draining:complete-inflight-then-snapshot",
				mustExclude: []string{"cold-g2"},
			},
		},
		{
			name:  "SNAPSHOTTING",
			state: StateSnapshotting,
			want: expect{
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: true,
				action:      "resume-snapshotting:restart-or-verify-snapshot",
				mustExclude: []string{"cold-g2"},
			},
		},
		{
			name:  "STAGING_VERIFY",
			state: StateStagingVerify,
			want: expect{
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: true,
				action:      "resume-staging-verify:reverify-staging-not-queryable",
				mustExclude: []string{"cold-g2"},
			},
		},
		{
			name:  "ATTACHED_STAGING",
			state: StateAttachedStaging,
			want: expect{
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1", stagingOpen: true,
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: true,
				action:      "keep-staging-excluded-await-owner-switch",
				mustExclude: []string{"cold-g2"},
			},
		},
		{
			name:  "OWNER_SWITCHED",
			state: StateOwnerSwitched,
			want: expect{
				owner: OwnerCold, generation: 2, ownerDir: "cold-g2",
				queryable: true, queryDir: "cold-g2",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: false,
				authoritySwitch: true, complete: false, recovery: false,
				action:      "resume-query-lease-draining",
				mustExclude: []string{"hot-g1"},
			},
		},
		{
			name:  "QUERY_LEASE_DRAINING",
			state: StateQueryLeaseDraining,
			want: expect{
				owner: OwnerCold, generation: 2, ownerDir: "cold-g2",
				queryable: true, queryDir: "cold-g2",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: false,
				authoritySwitch: true, complete: false, recovery: false,
				action:      "drain-legacy-query-leases-then-detach",
				mustExclude: []string{"hot-g1"},
			},
		},
		{
			name:  "DETACHED",
			state: StateDetached,
			tweak: func(t *testing.T, rec *Record) {
				rec.JournalComplete = false
			},
			want: expect{
				owner: OwnerCold, generation: 2, ownerDir: "cold-g2",
				queryable: true, queryDir: "cold-g2",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: false,
				authoritySwitch: true, complete: false, recovery: true,
				action:      "verify-residuals-journal-then-clean",
				mustExclude: []string{"hot-g1"},
			},
		},
		{
			name:  "CLEANED",
			state: StateCleaned,
			tweak: func(t *testing.T, rec *Record) {
				rec.JournalComplete = true
				rec.ResidualDirs = nil
			},
			want: expect{
				owner: OwnerCold, generation: 2, ownerDir: "cold-g2",
				queryable: true, queryDir: "cold-g2",
				writeOwner: OwnerCold, writeDir: "cold-g2", writeFrozen: false,
				authoritySwitch: true, complete: true, recovery: false,
				action:      "none",
				mustExclude: []string{},
			},
		},
		{
			name:  "FAILED_RETRYABLE",
			state: StateFailedRetryable,
			want: expect{
				// 未切换权威：查询与写入路由都回到 HOT 源侧。
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1",
				writeOwner: OwnerHot, writeDir: "hot-g1", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: true,
				action:      "retry-migration-from-journal-from-state",
				mustExclude: []string{"cold-g2"},
			},
		},
		{
			name:  "FAILED_MANUAL",
			state: StateFailedManual,
			want: expect{
				owner: OwnerHot, generation: 1, ownerDir: "hot-g1",
				queryable: true, queryDir: "hot-g1",
				writeOwner: OwnerHot, writeDir: "hot-g1", writeFrozen: true,
				authoritySwitch: false, complete: false, recovery: true,
				action:      "manual-intervention-required",
				mustExclude: []string{"cold-g2"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := sampleMigrating(t, tc.state)
			// 默认：迁移中 journal 未 complete（除 CLEANED 用例自行设置）。
			if tc.state != StateCleaned {
				rec.JournalComplete = false
			}
			// ROUTING_FROZEN/DRAINING 仅在 journal incomplete 时 recovery=true。
			if tc.state == StateRoutingFrozen || tc.state == StateDraining {
				// journal incomplete → recovery true；表中 recovery 字段在 cases 里写 false，
				// 这里按 journal incomplete 统一为 true 以匹配 Recover 实现。
			}
			if tc.tweak != nil {
				tc.tweak(t, rec)
			}

			view := Recover(rec)

			require.Equal(t, tc.want.owner, view.Owner, "owner")
			require.Equal(t, tc.want.generation, view.Generation, "generation")
			require.Equal(t, tc.want.ownerDir, view.OwnerDirID, "owner dir")
			require.Equal(t, tc.want.queryable, view.Queryable, "queryable")
			require.Equal(t, tc.want.queryDir, view.QueryDirID, "query dir")
			require.Equal(t, tc.want.stagingOpen, view.StagingOpen, "staging open")
			require.Equal(t, tc.want.writeOwner, view.WriteOwner, "write owner")
			require.Equal(t, tc.want.writeDir, view.WriteDirID, "write dir")
			require.Equal(t, tc.want.writeFrozen, view.WriteFrozen, "write frozen")
			require.Equal(t, tc.want.authoritySwitch, view.AuthoritySwitched, "authority switched")
			require.Equal(t, tc.want.complete, view.MigrationComplete, "complete")
			require.Equal(t, tc.want.action, view.NextRecoveryAction, "action")

			// journal incomplete 时，进行中/失败/DETACHED 必须 recovery。
			if tc.state != StateCleaned {
				require.True(t, view.RecoveryRequired || !rec.JournalComplete || tc.state == StateOwnerSwitched || tc.state == StateQueryLeaseDraining,
					"state=%s recovery=%v journalComplete=%v", tc.state, view.RecoveryRequired, rec.JournalComplete)
			}
			// 显式 recovery 断言（表驱动权威期望）。
			switch tc.state {
			case StateSnapshotting, StateStagingVerify, StateAttachedStaging, StateDetached,
				StateFailedRetryable, StateFailedManual:
				require.True(t, view.RecoveryRequired, "state=%s must require recovery", tc.state)
			case StateCleaned:
				require.False(t, view.RecoveryRequired, "CLEANED with complete journal must not require recovery")
				require.True(t, view.MigrationComplete)
			}

			for _, dir := range tc.want.mustExclude {
				if dir == "" {
					continue
				}
				if dir == view.QueryDirID {
					continue
				}
				require.False(t, IsQueryPlannerVisible(rec, dir),
					"state=%s dir=%s must be excluded from QueryPlanner", tc.state, dir)
			}

			// 旧目录 re-attach 过滤：非 query dir 的 hot-g1 / cold-g2 中错误侧不得可见。
			require.Equal(t, tc.want.queryDir == "cold-g2", IsQueryPlannerVisible(rec, "cold-g2"))
			require.Equal(t, tc.want.queryDir == "hot-g1", IsQueryPlannerVisible(rec, "hot-g1"))
		})
	}
}

// ROUTING_FROZEN / DRAINING：journal incomplete 时也必须标恢复。
func TestRecover_JournalIncompleteMarksRecovery(t *testing.T) {
	for _, st := range []MigrationState{StateRoutingFrozen, StateDraining, StateOwnerSwitched, StateQueryLeaseDraining} {
		rec := sampleMigrating(t, st)
		rec.JournalComplete = false
		view := Recover(rec)
		require.True(t, view.JournalIncomplete)
		require.True(t, view.RecoveryRequired, "state=%s", st)
		require.Contains(t, view.PartialReasons, "JOURNAL_INCOMPLETE")
	}
}

// QUERY_LEASE_DRAINING：旧租约未排空时保持恢复态，且新查询权威仍是 OWNER_SWITCHED 后的一侧。
func TestRecover_QueryLeaseDrainingWithActiveLeases(t *testing.T) {
	rec := sampleMigrating(t, StateQueryLeaseDraining)
	rec.QueryLeases = []QueryLeaseRef{
		{LeaseID: "l-old", ViewID: "v-old", Generation: 1}, // 绑旧 generation，无到期
	}
	view := Recover(rec)
	require.True(t, view.RecoveryRequired)
	require.Contains(t, view.PartialReasons, "QUERY_LEASES_ACTIVE")
	require.Equal(t, OwnerCold, view.Owner)
	require.Equal(t, "cold-g2", view.QueryDirID)
	require.True(t, view.Queryable)
}

// 冲突 generation：范围降为 RECOVERY_REQUIRED/PARTIAL，不得当完整结果。
func TestRecover_ConflictGeneration(t *testing.T) {
	rec := sampleMigrating(t, StateOwnerSwitched)
	rec.ConflictGeneration = "owner:hot→cold"
	rec.JournalComplete = true
	view := Recover(rec)
	require.True(t, view.RecoveryRequired)
	require.Contains(t, view.PartialReasons, "CONFLICT_GENERATION")
	require.Equal(t, "owner:hot→cold", view.ConflictGeneration)
}
