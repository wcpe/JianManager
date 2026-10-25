package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestDurableOutboxConditionalReclaimRejectsBeforeAnyAdvanceOrPrune(t *testing.T) {
	led, key := abandonedFixture(t, "outbox-conditional")
	before := led.Get(key)
	called := false
	pos, err := led.TryReclaimIf(key, func(proof ReclaimTarget) bool {
		from, target := proof.From, proof.Target
		called = true
		require.Equal(t, uint64(0), from)
		require.Equal(t, uint64(1000), target, "必须看到CanReclaim和人工出口已算出的实际目标，不是delivery=500")
		require.Equal(t, before.Abandonments, proof.Abandonments)
		proof.Abandonments[0].Operator = "不能污染真实账本"
		return false
	})
	require.True(t, called)
	require.ErrorIs(t, err, ErrReclaimPrecondition)
	require.NotErrorIs(t, err, ErrReclaimNoSegment)
	require.Zero(t, pos)
	after := led.Get(key)
	require.Equal(t, before.Positions, after.Positions)
	require.Equal(t, before.DeliveryBatches, after.DeliveryBatches)
	require.Equal(t, before.RequestDoneRanges, after.RequestDoneRanges)
	require.Equal(t, before.Abandonments, after.Abandonments)
	pos, err = led.TryReclaimIf(key, func(proof ReclaimTarget) bool {
		from, target := proof.From, proof.Target
		return from == 0 && target == 1000
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1000), pos)
}

func TestDurableOutboxArchiveCursorCASPreservesAcceptedPrefixAndOtherObjects(t *testing.T) {
	for _, kind := range []string{"accepted", "object", "advanced", "mapping", "prefix"} {
		t.Run(kind, func(t *testing.T) {
			led := New()
			key := keyOf("archive-cursor", "g1")
			led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
			expected := Segment{Path: "old.log.1", Kind: SegmentRotatedText, StartPos: 0, EndPos: 20, PhysicalMappingKnown: true, ArchiveObjectID: "object-a"}
			current := expected
			switch kind {
			case "object":
				current.ArchiveObjectID = "replacement"
			case "advanced":
				current.EndPos = 30
			case "mapping":
				current.PhysicalStart = 5
			}
			require.NoError(t, led.RegisterSegment(key, current))
			from := uint64(10)
			if kind == "prefix" {
				from = 0
			}
			err := led.RewindArchiveCursor(key, expected, from, 10, true)
			actual := led.Get(key).Segments[0]
			if kind == "accepted" {
				require.NoError(t, err)
				require.Equal(t, uint64(10), actual.EndPos)
				require.False(t, actual.Imported)
			} else {
				require.ErrorIs(t, err, ErrArchiveCursorPrecondition)
				require.Equal(t, current.EndPos, actual.EndPos)
				require.Equal(t, current.ArchiveObjectID, actual.ArchiveObjectID)
				require.Equal(t, current.PhysicalStart, actual.PhysicalStart)
			}
		})
	}
}

func TestDurableOutboxConditionalReclaimCannotBypassHoldOrOriginalGate(t *testing.T) {
	for _, held := range []bool{false, true} {
		led := New()
		key := keyOf("outbox-original-gate", "g1")
		led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
		require.NoError(t, led.AdvanceRead(key, 10))
		require.NoError(t, led.AdvanceDurable(key, 10))
		require.NoError(t, led.RegisterRecovery(key, RecoveryRef{SegmentID: "proof", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 10, HasHold: held}))
		called := false
		pos, err := led.TryReclaimIf(key, func(ReclaimTarget) bool { called = true; return true })
		require.Error(t, err)
		require.False(t, called, "原门禁拒绝时不能用额外condition放行")
		require.Zero(t, pos)
	}
	led, key := abandonedFixture(t, "outbox-manual-hold")
	require.NoError(t, led.SetRecoveryHold(key, "seg-1", true))
	called := false
	_, err := led.TryReclaimIf(key, func(ReclaimTarget) bool { called = true; return true })
	require.Error(t, err)
	require.False(t, called, "hold必须优先人工永久丢失和新回调")
}
