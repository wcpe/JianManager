package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func keyOf(id, gen string) SourceKey {
	return SourceKey{LogSourceID: id, SourceGeneration: gen}
}

// F-004：非法 release_reason / self-reference / hold / CLEANED 路径必须拒绝。
func TestTransitionRecovery_ReleaseValidation(t *testing.T) {
	setup := func() (*Ledger, SourceKey) {
		led := New()
		key := keyOf("rel", "g1")
		led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "rel", SourceGeneration: "g1"})
		_ = led.AdvanceDurable(key, 100)
		_ = led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone)
		_ = led.RegisterRecovery(key, RecoveryRef{
			SegmentID: "seg1", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 100,
		})
		if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryDurableVerified, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryWALResponsibilityXfer, "", "receiver-A"); err != nil {
			t.Fatal(err)
		}
		return led, key
	}

	// 非法 reason
	led, key := setup()
	if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryReleased, "NOT_A_REASON", "receiver-B"); err == nil {
		t.Fatal("invalid release_reason must fail")
	}
	// self-reference
	led, key = setup()
	if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryReleased, logtypes.ReleaseNextCopyVerified, "seg1"); err == nil {
		t.Fatal("self-reference receiver must fail")
	}
	// CLEANED 只能从 RELEASED
	led, key = setup()
	if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryCleaned, logtypes.ReleaseNextCopyVerified, "receiver-B"); err == nil {
		t.Fatal("CLEANED from non-RELEASED must fail")
	}
	// hold 阻止释放
	led, key = setup()
	if err := led.SetRecoveryHold(key, "seg1", true); err != nil {
		t.Fatal(err)
	}
	if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryReleased, logtypes.ReleaseNextCopyVerified, "receiver-B"); err == nil {
		t.Fatal("release under hold must fail")
	}
	// 合法路径：TRANSFERRED → RELEASED → CLEANED
	led, key = setup()
	if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryReleased, logtypes.ReleaseProjectionBacked, "receiver-B"); err != nil {
		t.Fatal(err)
	}
	if err := led.TransitionRecovery(key, "seg1", logtypes.RecoveryCleaned, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRestartBindingPreservesAdvancedStateAndRejectsBackwardsTransition(t *testing.T) {
	led := New()
	key := SourceKey{LogSourceID: "s", SourceGeneration: "g"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "s", SourceGeneration: "g"})
	ref := RecoveryRef{SegmentID: "seg", Path: "projection://g1", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 10}
	require.NoError(t, led.RegisterRecovery(key, ref))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryWALResponsibilityXfer, "", "projection:g1"))
	require.NoError(t, led.SetRecoveryHold(key, "seg", true))
	require.NoError(t, led.RegisterRecovery(key, ref), "restart binding must be idempotent")
	got := led.Get(key).RecoveryRefs[0]
	require.Equal(t, logtypes.RecoveryWALResponsibilityXfer, got.State)
	require.True(t, got.HasHold)
	require.Equal(t, "projection:g1", got.ResponsibilityReceiver)
	require.ErrorContains(t, led.TransitionRecovery(key, "seg", logtypes.RecoveryDurableVerified, "", ""), "cannot move backward")
	require.ErrorContains(t, led.RegisterRecovery(key, RecoveryRef{SegmentID: "seg", Path: "other", State: logtypes.RecoveryStaged, CoversTo: 10}), "identity conflict")
}

func TestResolveGapsKeepsAuditAndRequiresFullCoverageBeforeResume(t *testing.T) {
	ledger := New()
	key := SourceKey{LogSourceID: "source", SourceGeneration: "g1"}
	ledger.Ensure(key, logtypes.SourceIdentity{LogSourceID: "source", SourceGeneration: "g1"})
	require.NoError(t, ledger.PauseAcquire(key, "disk full"))
	require.NoError(t, ledger.RecordGap(key, 0, 10, "PAUSED", "disk full"))
	require.NoError(t, ledger.RecordGap(key, 10, 20, "DELIVERY_ERROR", "unverified"))
	resolved, err := ledger.ResolveGapsThrough(key, 10, "projection verified")
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.Equal(t, 1, ledger.UnresolvedGapCount(key))
	require.Error(t, ledger.ResumeAcquire(key))
	resolved, err = ledger.ResolveGapsThrough(key, 20, "projection verified")
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.NoError(t, ledger.ResumeAcquire(key))
	entry := ledger.Get(key)
	require.False(t, entry.AcquirePaused)
	require.Len(t, entry.Gaps, 2)
	for _, gap := range entry.Gaps {
		require.True(t, gap.Resolved)
		require.Equal(t, "projection verified", gap.Resolution)
	}
}

func TestLedgerEnsureAndWatermarks(t *testing.T) {
	led := New()
	key := keyOf("s1", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "s1", SourceGeneration: "g1", ParserVersion: "p1"})

	if err := led.AdvanceRead(key, 10); err != nil {
		t.Fatal(err)
	}
	// read 可回退。
	if err := led.AdvanceRead(key, 4); err != nil {
		t.Fatal(err)
	}
	if err := led.AdvanceDurable(key, 20); err != nil {
		t.Fatal(err)
	}
	if err := led.AdvanceDurable(key, 10); err == nil {
		t.Fatal("durable must be monotonic")
	}
	ent := led.Get(key)
	if ent.Positions.Read != 4 || ent.Positions.Durable != 20 {
		t.Fatalf("positions=%+v", ent.Positions)
	}
}

func TestLedgerRotationKeepsGeneration(t *testing.T) {
	led := New()
	key := keyOf("src", "g7")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g7"})
	_ = led.RegisterSegment(key, Segment{Path: "latest.log", Kind: SegmentLive})
	if err := led.LinkRotation(key, "latest.log", "latest.log.1.gz", 100); err != nil {
		t.Fatal(err)
	}
	link, ok := led.RotationLinked(key, "latest.log.1.gz")
	if !ok {
		t.Fatal("expected rotation linked")
	}
	if link.Generation != "g7" {
		t.Fatalf("generation must carry over, got %s", link.Generation)
	}
	if link.EndPosFrom != 100 {
		t.Fatalf("end pos from want 100 got %d", link.EndPosFrom)
	}
}

func TestLedgerGapAndPause(t *testing.T) {
	led := New()
	key := keyOf("g", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "g", SourceGeneration: "g1"})
	if err := led.RecordGap(key, 0, 50, "CAPACITY", "wal full"); err != nil {
		t.Fatal(err)
	}
	if err := led.PauseAcquire(key, "budget"); err != nil {
		t.Fatal(err)
	}
	ent := led.Get(key)
	if !ent.AcquirePaused || len(ent.Gaps) != 1 || ent.ErrorCount != 1 {
		t.Fatalf("gap/pause not recorded: %+v", ent)
	}
	if err := led.ResumeAcquire(key); err == nil {
		t.Fatal("unresolved gap must block resume")
	}
	if _, err := led.ResolveGapsThrough(key, 50, "operator verified projection"); err != nil {
		t.Fatal(err)
	}
	if err := led.ResumeAcquire(key); err != nil {
		t.Fatal(err)
	}
	if led.Get(key).AcquirePaused {
		t.Fatal("should resume")
	}
}

func TestLedgerReclaimGateTable(t *testing.T) {
	tests := []struct {
		name     string
		state    logtypes.RecoverySegmentState
		reason   logtypes.ReleaseReason
		hold     bool
		delivery uint64
		wantOK   bool
	}{
		{"staged blocks", logtypes.RecoveryStaged, logtypes.ReleaseProjectionBacked, false, 100, false},
		{"durable verified blocks", logtypes.RecoveryDurableVerified, logtypes.ReleaseNextCopyVerified, false, 100, false},
		{"xfer allows", logtypes.RecoveryWALResponsibilityXfer, logtypes.ReleaseNextCopyVerified, false, 100, true},
		{"xfer hold blocks", logtypes.RecoveryWALResponsibilityXfer, logtypes.ReleaseNextCopyVerified, true, 100, false},
		// 契约 §4.3：责任转移即可 reclaim，无需 release_reason（reason 仅用于 RELEASED）
		{"xfer no reason allows", logtypes.RecoveryWALResponsibilityXfer, "", false, 100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			led := New()
			key := keyOf("r", "g1")
			led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "r", SourceGeneration: "g1"})
			if err := led.AdvanceDurable(key, 100); err != nil {
				t.Fatal(err)
			}
			if err := led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone); err != nil {
				t.Fatal(err)
			}
			_ = tt.delivery
			ref := RecoveryRef{
				SegmentID:  "seg",
				State:      logtypes.RecoveryStaged,
				CoversFrom: 0,
				CoversTo:   100,
				HasHold:    tt.hold,
			}
			if err := led.RegisterRecovery(key, ref); err != nil {
				t.Fatal(err)
			}
			// 推进到目标状态。
			if tt.state != logtypes.RecoveryStaged {
				next := logtypes.RecoveryDurableVerified
				if err := led.TransitionRecovery(key, "seg", next, "", ""); err != nil {
					t.Fatal(err)
				}
				if tt.state == logtypes.RecoveryWALResponsibilityXfer {
					reason := tt.reason
					if reason == "" {
						reason = logtypes.ReleaseProjectionBacked
					}
					if err := led.TransitionRecovery(key, "seg", logtypes.RecoveryWALResponsibilityXfer, reason, "receiver"); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := led.TryReclaim(key)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("want reclaim ok: %v", err)
				}
				if led.Get(key).Positions.Reclaim != 100 {
					t.Fatalf("reclaim pos=%d", led.Get(key).Positions.Reclaim)
				}
			} else if err == nil {
				t.Fatalf("want reclaim blocked")
			}
		})
	}
}

func TestReleaseRequiresReason(t *testing.T) {
	led := New()
	key := keyOf("rel", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "rel", SourceGeneration: "g1"})
	_ = led.RegisterRecovery(key, RecoveryRef{
		SegmentID:  "s",
		State:      logtypes.RecoveryStaged,
		CoversFrom: 0,
		CoversTo:   10,
	})
	if err := led.TransitionRecovery(key, "s", logtypes.RecoveryDurableVerified, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := led.TransitionRecovery(key, "s", logtypes.RecoveryWALResponsibilityXfer, logtypes.ReleaseNextCopyVerified, "next"); err != nil {
		t.Fatal(err)
	}
	// RELEASED 必须有 reason + receiver。
	if err := led.TransitionRecovery(key, "s", logtypes.RecoveryReleased, "", ""); err == nil {
		t.Fatal("RELEASED must require reason and receiver")
	}
	if err := led.TransitionRecovery(key, "s", logtypes.RecoveryReleased, logtypes.ReleaseNextCopyVerified, "next"); err != nil {
		t.Fatal(err)
	}
}
