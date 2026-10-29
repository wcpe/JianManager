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
		// 新语义下 Target = min(REQUEST_DONE 连续覆盖末端, max(Read, Durable))。
		// 夹具里已确认投递 [0,500)，被放弃的 [500,600) 没有 REQUEST_DONE ⇒ 认证覆盖停在 500。
		require.Equal(t, uint64(500), target, "必须看到由认证覆盖算出的实际目标，不是 read/durable=1000")
		require.Equal(t, before.Abandonments, proof.Abandonments)
		proof.Abandonments[0].Operator = "不能污染真实账本"
		return false
	})
	require.True(t, called)
	require.ErrorIs(t, err, ErrReclaimPrecondition)
	require.Zero(t, pos)
	after := led.Get(key)
	require.Equal(t, before.Positions, after.Positions)
	require.Equal(t, before.DeliveryBatches, after.DeliveryBatches)
	require.Equal(t, before.RequestDoneRanges, after.RequestDoneRanges)
	require.Equal(t, before.Abandonments, after.Abandonments)
	pos, err = led.TryReclaimIf(key, func(proof ReclaimTarget) bool {
		from, target := proof.From, proof.Target
		return from == 0 && target == 500
	})
	require.NoError(t, err)
	require.Equal(t, uint64(500), pos)
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

func TestDurableOutboxConditionalReclaimCannotBypassGate(t *testing.T) {
	// ① 认证覆盖为空（只有读入量）⇒ target = 0 <= Reclaim ⇒ 根本不会调 condition。
	led := New()
	key := keyOf("outbox-original-gate", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	require.NoError(t, led.AdvanceRead(key, 10))
	require.NoError(t, led.AdvanceDurable(key, 10))
	called := false
	pos, err := led.TryReclaimIf(key, func(ReclaimTarget) bool { called = true; return true })
	require.NoError(t, err, "无事可做时新语义返回 (当前Reclaim, nil)")
	require.False(t, called, "target <= Reclaim 时不得调用 condition（无事可做就直接返回）")
	require.Zero(t, pos)

	// ② 有可推进目标时，condition 是真正的闸门：恒 false ⇒ 拒绝且不推进（原子性）。
	led, key = abandonedFixture(t, "outbox-manual-hold")
	called = false
	pos, err = led.TryReclaimIf(key, func(ReclaimTarget) bool { called = true; return false })
	require.ErrorIs(t, err, ErrReclaimPrecondition)
	require.True(t, called)
	require.Zero(t, pos)
	require.Zero(t, led.Get(key).Positions.Reclaim, "被拒时不得推进")
}
