package ledger

// LEDGER-1 回归（2026-10-29 重写）：delivery_batch 裁剪的水位口径与「诊断面不失证」。
//
// ## 旧形态为何已不存在
//
// 旧缺陷的现场形态是「**Reclaim 越过 Delivery**」：tryReclaimLocked 的凭据放行分支
// 与责任已转移分支都把 target 取到 ref.CoversTo，而 TryReclaim 从不重算 Delivery，
// 于是暂停 / 空闲源上 (Delivery, Reclaim] 长期存在；若水位取 Reclaim，那段 UNKNOWN 证据
// 会被裁掉 ⇒ ingest.event_status 把「已发出未确认」报成「未发送」。
//
// 新语义下这个形态**不可能出现**：reclaim = min(REQUEST_DONE 连续覆盖末端, max(Read,Durable))，
// 而 REQUEST_DONE 区间必然也在 Positions.Delivery 的连续前缀里 ⇒ `Reclaim <= Delivery` 恒成立。
// 水位因此简化为 `Positions.Delivery`（与 advanceDeliveryEnd 的 floor 同一口径）。
//
// ## 本文件现在守什么
//
// 真正不能丢的关切是「**裁剪不得让诊断面失证**」，而它现在靠的不再是「水位夹取」，
// 而是 `DeliveryEvidence` / `RequestDoneRanges`（在批次被摘除之前就独立保存，
// 见 preserveDeliveryEvidence）。所以本文件同时钉两件事：
//
//  1. 水位口径本身 = Positions.Delivery（不再 min(Reclaim, Delivery)）；
//  2. 即使整段批次都被水位裁掉，`DeliveryEvidenceOf` 仍能给出每个区间的精确状态。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// abandonedFixture 造一个「已有人工裁定的永久丢失凭据」的源：
// read/durable = 1000，已确认投递 [0,500)，[500,600) 被裁定永久丢失。
//
// 保留它是因为 TryReclaimIf 的用例（durable_outbox_test.go）需要一个带凭据的源。
func abandonedFixture(t *testing.T, id string) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := keyOf(id, "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 1000))
	require.NoError(t, led.AdvanceDurable(key, 1000))
	// 已确认投递前缀 [0,500)：Delivery = 500。
	require.NoError(t, led.RecordDelivery(key, 0, 500, logtypes.DeliveryRequestDone))
	// 人工裁定 [500,600) 永久丢失。
	require.NoError(t, led.RecordGap(key, 500, 600, GapReasonWALBodyMissing, "正文缺失"))
	_, err := led.AbandonGapsThrough(key, 1000, GapAbandonment{
		ReasonCode: GapReasonPermanentlyLost, Operator: "ops@example", Reason: "段物理损坏",
	})
	require.NoError(t, err)
	return led, key
}

// TestDeliveryBatchWatermarkIsDelivery 固定水位口径：`watermark = Positions.Delivery`。
//
// 旧口径是 min(Reclaim, Delivery)，保留 (Reclaim, Delivery] 的批次。新语义下 Reclaim <= Delivery
// 恒成立，那段在每轮采集都会被重新扫一遍却一条也裁不掉（水位永远追不上）——
// 所以必须改成 Delivery。
//
// 转红方式（实测）：把 deliveryBatchWatermark 改回 min(Reclaim, Delivery)
// ⇒ 「Reclaim 低于 Delivery 时水位仍取 Delivery」一行立即红。
func TestDeliveryBatchWatermarkIsDelivery(t *testing.T) {
	cases := []struct {
		name              string
		reclaim, delivery uint64
		want              uint64
	}{
		{"Delivery 追平 Reclaim：水位 = Delivery", 800, 800, 800},
		{"Delivery 越过 Reclaim：水位 = Delivery（新口径，不再夹到 Reclaim）", 800, 900, 900},
		// 新语义下不可达（Reclaim = min(认证覆盖, 读入量) <= 认证覆盖 <= Delivery），
		// 但函数本身仍需对任意输入给出确定结果：取 Delivery 是保守方向（少裁不多裁）。
		{"Reclaim 高于 Delivery（新语义下不可达）：仍取 Delivery", 1000, 500, 500},
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

// TestPrunePreservesDiagnosticEvidenceAfterBatchesDropped 是 LEDGER-1 关切的**新形态**：
// 整段批次被水位裁掉之后，诊断面（event_status 用的 DeliveryEvidenceOf）必须仍然精确。
//
// 为什么这才是真正的关切：裁剪是空间优化，但「已发出未确认」这个状态必须可查。
// 旧实现靠「水位夹取」间接保护它；新实现靠**独立证据**（DeliveryEvidence / RequestDoneRanges
// 在批次被摘除前就已保存）。所以本用例不靠「保留批次」，而是直接验证据面。
//
// 转红方式（实测）：把 RecordDelivery 里的 preserveDeliveryEvidence 调用去掉
// ⇒ 裁剪后 DeliveryEvidenceOf 只剩 REQUEST_DONE，UNKNOWN 区间失去证据，本用例红。
func TestPrunePreservesDiagnosticEvidenceAfterBatchesDropped(t *testing.T) {
	led := New()
	key := keyOf("ledger1-evidence", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "ledger1-evidence", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 1000))
	require.NoError(t, led.AdvanceDurable(key, 1000))
	require.NoError(t, led.RecordDelivery(key, 0, 500, logtypes.DeliveryRequestDone))
	// 已发出未确认：诊断面必须能把它与「未发送」区分开。
	require.NoError(t, led.RecordDelivery(key, 500, 600, logtypes.DeliveryUnknown))

	before := led.Get(key)
	require.Equal(t, uint64(600), before.Positions.Delivery, "Delivery 含 UNKNOWN ⇒ 连续前缀 = 600")
	require.Equal(t, uint64(500), func() uint64 {
		pos, _ := led.TryReclaim(key)
		return pos
	}(), "reclaim 只认 REQUEST_DONE ⇒ 停在 UNKNOWN 之前（500）")

	// 按当前水位（= Delivery = 600）裁一次：两条批次都落在水位之内。
	after := led.Get(key)
	require.Empty(t, after.DeliveryBatches, "水位 = Delivery = 600 ⇒ 两条批次均按恒等元裁掉")
	require.Equal(t, uint64(600), after.Positions.Delivery, "裁剪不得改变 delivery_position")
	// 同一水位重算的连续前缀逐位相同（恒等元，守卫也守着这条）。
	require.Equal(t, uint64(600), ContiguousDeliveryEnd(after.DeliveryBatches, after.Positions.Delivery))

	// 关键断言：批次没了，但证据面完整。
	evidence := DeliveryEvidenceOf(after)
	require.Equal(t, []DeliveryBatch{
		{Start: 0, End: 500, State: logtypes.DeliveryRequestDone},
		{Start: 500, End: 600, State: logtypes.DeliveryUnknown},
	}, evidence, "裁剪不得丢掉 UNKNOWN 的精确区间证据（否则 SENT_UNACKED 会退化成 UNSENT）")
	require.Equal(t, []PositionRange{{From: 0, To: 500}}, RequestDoneRangesOf(after),
		"只有真实 REQUEST_DONE 可生成 ACK 证据，UNKNOWN 不得被升格")

	// 持久化往返后证据仍在（快照 → Restore 是真实的恢复路径）。
	restored := New()
	require.NoError(t, restored.Restore(led.Snapshot()))
	rEntry := restored.Get(key)
	require.NotNil(t, rEntry)
	require.Equal(t, []DeliveryBatch{
		{Start: 0, End: 500, State: logtypes.DeliveryRequestDone},
		{Start: 500, End: 600, State: logtypes.DeliveryUnknown},
	}, DeliveryEvidenceOf(rEntry), "重启后诊断证据必须逐字保留")
	require.Equal(t, uint64(600), rEntry.Positions.Delivery, "Restore 按新口径重算 Delivery（floor = 自身）")
}

// TestPruneKeepsUnackedEvidenceWhenDeliveryLagsReclaim 在纯函数层固定同一条性质：
// 对「水位 = Delivery」这个入参，越过水位的条目一条都不裁。
func TestPruneKeepsUnackedEvidenceWhenDeliveryLagsReclaim(t *testing.T) {
	// 覆盖形态：[0,400) 已确认（连续前缀末端 = Delivery = 400），(400,1000] 上有一条
	// UNKNOWN —— **刻意留空洞**（400→500 之间没有任何批次），否则连续前缀会一路推到 600。
	batches := []DeliveryBatch{
		batchOf(0, 200),
		batchOf(200, 400),
		{Start: 500, End: 600, State: logtypes.DeliveryUnknown}, // 水位之上的证据
	}
	require.Equal(t, uint64(400), ContiguousDeliveryEnd(batches, 0),
		"连续前缀必须停在 [400,500) 空洞之前（= Delivery）")

	// 水位 = Delivery = 400：只裁水位内的两条，UNKNOWN 原样留下。
	kept, dropped, err := PruneDeliveryBatches(batches, 400, 0)
	require.NoError(t, err)
	require.Equal(t, 2, dropped, "水位内的两条应按恒等元裁掉")
	require.Len(t, kept, 1, "越过水位的 UNKNOWN 必须留下")
	require.Equal(t, uint64(500), kept[0].Start)
	require.Equal(t, uint64(600), kept[0].End)
	require.Equal(t, logtypes.DeliveryUnknown, kept[0].State)
	// 同一水位重算的连续前缀不变（恒等元）：裁前裁后都停在同一处。
	require.Equal(t, uint64(400), ContiguousDeliveryEnd(kept, 400))

	// 对照（旧判据的反例）：水位取 1000 会连 UNKNOWN 段一起裁掉。
	// 新语义下 Reclaim 永远 <= Delivery，所以这种水位不会被选中——本断言只是把「为何不能
	// 拿 Reclaim 当水位」留在用例里，它不依赖生产代码的任何当前行为。
	oldKept, oldDropped, oldErr := PruneDeliveryBatches(batches, 1000, 0)
	require.NoError(t, oldErr)
	require.Equal(t, 3, oldDropped, "水位 1000 会把诊断证据一起裁掉 ⇒ 这正是不能拿 Reclaim 当水位的原因")
	require.Empty(t, oldKept)
}
