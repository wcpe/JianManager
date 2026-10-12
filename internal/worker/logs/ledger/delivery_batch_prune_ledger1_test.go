package ledger

// LEDGER-1 回归：delivery_batch 裁剪的水位必须夹住 Positions.Delivery，否则诊断面
// （ingest.event_status 按批次判态）会把已确认落库的区间报成「未发送」。
//
// 现场形态（PR #61 审查实测推演）：tryReclaimLocked 的**凭据放行**分支让 Reclaim 越过
// Delivery——target 无条件取 ref.CoversTo，而 Positions.Delivery 只在 RecordDelivery /
// Restore 重算，TryReclaim 从不重算它。暂停 / 空闲源上 (Delivery, Reclaim] 这段长期存在，
// 投递不再发生 ⇒ Delivery 长期不回补 ⇒ 退化状态长期存在。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// abandonedFixture 造一个「已有人工裁定的永久丢失凭据、且恢复分段尚未转移责任」的源。
//
// 这正是凭据放行分支的输入：CanReclaim 默认拒绝（状态是 STAGED），但区间内已有永久丢失
// 凭据 ⇒ tryReclaimLocked 放行并把 target 取到 ref.CoversTo。
func abandonedFixture(t *testing.T, id string) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := keyOf(id, "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 1000))
	require.NoError(t, led.AdvanceDurable(key, 1000))
	// 已确认投递前缀 [0,500)：Delivery = 500。
	require.NoError(t, led.RecordDelivery(key, 0, 500, logtypes.DeliveryRequestDone))
	// 人工裁定 [500,600) 永久丢失（凭据放行的前提）。
	require.NoError(t, led.RecordGap(key, 500, 600, GapReasonWALBodyMissing, "正文缺失"))
	_, err := led.AbandonGapsThrough(key, 1000, GapAbandonment{
		ReasonCode: GapReasonPermanentlyLost, Operator: "ops@example", Reason: "段物理损坏",
	})
	require.NoError(t, err)
	// 恢复分段覆盖 [0,1000)，状态仍为 STAGED（责任未转移 ⇒ 只有凭据能放行）。
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-1", Path: "/data/recovery/seg-1", State: logtypes.RecoveryStaged,
		CoversFrom: 0, CoversTo: 1000,
	}))
	return led, key
}

// TestTryReclaimCredentialReleaseCrossesDeliveryKeepsBatchEvidence 是 LEDGER-1 的转红用例。
//
// 转红方式（实测）：把 deliveryBatchWatermark 的下界去掉（水位改回 Positions.Reclaim），
// 本用例最后两条断言立即红——(Delivery, Reclaim] 上的 UNKNOWN 批次被裁掉，event_status
// 对该区间再无证据可用。
func TestTryReclaimCredentialReleaseCrossesDeliveryKeepsBatchEvidence(t *testing.T) {
	led, key := abandonedFixture(t, "reclaim-cross-delivery")

	// 登记一段 (Delivery, Reclaim] 上的「已发出未确认」批次：这是诊断面区分
	// 「已确认 / 已发出未确认」的唯一证据。它与已确认前缀之间隔着被放弃的 [500,600)
	// ⇒ 连续前缀停在那里，Delivery 保持 500 而 Reclaim 即将被推到 1000。
	require.NoError(t, led.RecordDelivery(key, 600, 700, logtypes.DeliveryUnknown))
	before := led.Get(key)
	require.Equal(t, uint64(500), before.Positions.Delivery, "Delivery 是连续前缀末端，被放弃段之后不再前进")
	require.Equal(t, uint64(0), before.Positions.Reclaim, "夹具尚未回收")

	pos, err := led.TryReclaim(key)
	require.NoError(t, err, "凭据放行必须放行（没有它回收链会永久卡住）")
	require.Equal(t, uint64(1000), pos)
	entry := led.Get(key)
	require.Equal(t, uint64(1000), entry.Positions.Reclaim, "凭据放行把 Reclaim 推到分段末端")
	require.Equal(t, uint64(500), entry.Positions.Delivery,
		"TryReclaim 从不重算 Delivery ⇒ Reclaim 越过了 Delivery（本用例要守的形态）")
	require.Greater(t, entry.Positions.Reclaim, entry.Positions.Delivery)

	// 断言语义：位置 ≤ 水位的条目被裁掉（它们对任何派生值都是恒等元），而 (Delivery, Reclaim]
	// 上的 UNKNOWN 批次必须**留下**——否则该区间会从 SENT_UNACKED 退化成 UNSENT/no_evidence。
	//
	// 注意不能写成「剩余条目全部 ≤ Delivery」：本用例要守的恰是**越过** Delivery 的那条必须留下。
	require.Len(t, entry.DeliveryBatches, 1,
		"只剩越过 Delivery 的那条 UNKNOWN（水位内的两条已按恒等元裁掉）")
	require.Equal(t, uint64(600), entry.DeliveryBatches[0].Start)
	require.Equal(t, uint64(700), entry.DeliveryBatches[0].End)
	require.Equal(t, logtypes.DeliveryUnknown, entry.DeliveryBatches[0].State,
		"留下的必须是诊断证据（已发出未确认），而不是被裁成空")
	require.Equal(t, uint64(500), entry.Positions.Delivery,
		"裁剪不得把 Delivery 前移兜底（它会掩盖 (Delivery, Reclaim] 的未确认区间）")

	// 同一水位再裁一次仍是恒等元（幂等 + 守卫自洽）。
	kept, dropped, err := PruneDeliveryBatches(entry.DeliveryBatches, deliveryBatchWatermark(entry), 0)
	require.NoError(t, err)
	require.Zero(t, dropped)
	require.Equal(t, ContiguousDeliveryEnd(entry.DeliveryBatches, deliveryBatchWatermark(entry)),
		ContiguousDeliveryEnd(kept, deliveryBatchWatermark(entry)))
}

// TestDeliveryBatchWatermarkIsMinOfReclaimAndDelivery 固定水位口径本身。
func TestDeliveryBatchWatermarkIsMinOfReclaimAndDelivery(t *testing.T) {
	cases := []struct {
		name              string
		reclaim, delivery uint64
		want              uint64
	}{
		{"Delivery 追平 Reclaim：水位 = Reclaim（稳态零差异）", 800, 800, 800},
		{"Delivery 越过 Reclaim：水位 = Reclaim（不因派生值高而多裁）", 800, 900, 800},
		{"Reclaim 越过 Delivery：水位 = Delivery（LEDGER-1 的夹取）", 1000, 500, 500},
		{"从未投递：水位 0 ⇒ 一条都不裁", 1000, 0, 0},
		{"两者皆 0", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Entry{}
			e.Positions.Reclaim, e.Positions.Delivery = tc.reclaim, tc.delivery
			require.Equal(t, tc.want, deliveryBatchWatermark(e))
			// 水位为 0 时纯函数层也必须一条不裁（与 acquire.WAL.pruneReclaimed 同口径）。
			if tc.want == 0 {
				_, dropped, err := PruneDeliveryBatches([]DeliveryBatch{batchOf(0, 10)}, tc.want, 0)
				require.NoError(t, err)
				require.Zero(t, dropped)
			}
		})
	}
	require.Zero(t, deliveryBatchWatermark(nil), "nil 条目水位为 0（不裁）")
}

// TestPruneKeepsUnackedEvidenceWhenDeliveryLagsReclaim 在纯函数层固定同一条性质：
// 对「水位 = min(Reclaim, Delivery)」这个入参，越过 Delivery 的条目一条都不裁。
func TestPruneKeepsUnackedEvidenceWhenDeliveryLagsReclaim(t *testing.T) {
	// 覆盖形态：[0,400) 已确认（连续前缀末端 = Delivery = 400），(400,1000] 上有一条
	// UNKNOWN —— **刻意留空洞**（400→500 之间没有任何批次），否则连续前缀会一路推到 600，
	// 就造不出「Delivery 滞后于 Reclaim」这个本用例要守的形态。
	batches := []DeliveryBatch{
		batchOf(0, 200),
		batchOf(200, 400),
		{Start: 500, End: 600, State: logtypes.DeliveryUnknown}, // (Delivery, Reclaim] 上的证据
	}
	require.Equal(t, uint64(400), ContiguousDeliveryEnd(batches, 0),
		"连续前缀必须停在被放弃的 [400,500) 空洞之前（= Delivery）")

	// 水位 = min(Reclaim=1000, Delivery=400) = 400：只裁水位内的两条，UNKNOWN 原样留下。
	kept, dropped, err := PruneDeliveryBatches(batches, 400, 0)
	require.NoError(t, err)
	require.Equal(t, 2, dropped, "水位内的两条应按恒等元裁掉")
	require.Len(t, kept, 1, "越过水位的 UNKNOWN 必须留下")
	require.Equal(t, uint64(500), kept[0].Start)
	require.Equal(t, uint64(600), kept[0].End)
	require.Equal(t, logtypes.DeliveryUnknown, kept[0].State)
	// 同一水位重算的连续前缀不变（恒等元）：裁前裁后都停在同一处。
	require.Equal(t, uint64(400), ContiguousDeliveryEnd(kept, 400))

	// 旧判据（水位 = Reclaim = 1000）会连 UNKNOWN 段一起裁掉：这正是要转红的形态。
	oldKept, oldDropped, oldErr := PruneDeliveryBatches(batches, 1000, 0)
	require.NoError(t, oldErr)
	require.Equal(t, 3, oldDropped, "旧判据把诊断证据一起裁掉 ⇒ event_status 对该区间再无证据")
	require.Empty(t, oldKept)
}
