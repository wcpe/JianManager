package ledger

// FR-496 索引有界化：历史投递批次（delivery_batch）裁剪的判据回归。
//
// 背景（FR-498 实测 @60 源）：采集索引里唯一随总量线性增长的表是 delivery_batch（≈5 MB/天），
// 而它原先没有裁剪路径。判据与其证明见 delivery_batch_prune.go 的文件头注释：
// **batch.End <= Positions.Reclaim 的条目是 contiguousDeliveryEnd(…, Reclaim) 的恒等元**。
//
// 本文件守两件事：
//  1. 该判据的边界——「刚越过水位一字节」的条目绝不裁（它仍会把连续前缀推过水位）；
//  2. 裁剪的恒等元性质——裁剪前后按同一水位重算的连续前缀逐位相同，且守卫能拦下错误判据。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func batchOf(start, end uint64) DeliveryBatch {
	return DeliveryBatch{Start: start, End: end, State: logtypes.DeliveryRequestDone}
}

// TestPruneDeliveryBatchesCriterionBoundary 固定判据边界：末位 ≤ 水位即裁，末位 > 水位一条都不裁。
func TestPruneDeliveryBatchesCriterionBoundary(t *testing.T) {
	const watermark = 1000
	cases := []struct {
		name    string
		batch   DeliveryBatch
		dropped bool
	}{
		{"整体在水位之下", batchOf(0, 900), true},
		{"末位正好等于水位（恒等元：起点 ≤ 水位时推不动 pos）", batchOf(900, 1000), true},
		{"零长度且在水位之下", batchOf(500, 500), true},
		{"末位刚越过水位一字节（会把连续前缀从 1000 推到 1001，绝不裁）", batchOf(1000, 1001), false},
		{"末位越过水位且起点在水位之下", batchOf(999, 1500), false},
		// 零长度且起点/末位都等于水位：判据是「末位 ≤ 水位」，故裁掉；它本就被
		// contiguousDeliveryEnd 当退化跨度跳过（End <= Start），裁掉同样不改变任何派生值。
		{"零长度且末位等于水位", batchOf(1000, 1000), true},
		{"完全在水位之上", batchOf(1200, 1300), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := []DeliveryBatch{tc.batch}
			kept, dropped, err := PruneDeliveryBatches(before, watermark, 0)
			require.NoError(t, err)
			if tc.dropped {
				require.Equal(t, 1, dropped)
				require.Empty(t, kept)
				// 裁空必须是**非 nil 空切片**：索引迁移的逐字段比对区分 nil 与空
				// （见 ingest.normalizePersistedState），返回 nil 会让一次正常启动被判不一致。
				require.NotNil(t, kept)
			} else {
				require.Equal(t, 0, dropped)
				require.Equal(t, before, kept)
			}
		})
	}
}

// TestPruneDeliveryBatchesKeepsContiguousPrefixIdentical 是判据的**性质回归**：
// 对一批形态各异的批次列表与水位，裁剪前后连续前缀必须逐位相同（恒等元性质），
// 且被裁条目一律满足 End <= 水位、未被裁的条目一律满足 End > 水位（判据不多裁一条）。
//
// 转红说明：把判据放宽为 `End <= watermark+1`（或改成「起点在水位之下就裁」）时，
// 下面的用例会立刻报出前缀变化。
func TestPruneDeliveryBatchesKeepsContiguousPrefixIdentical(t *testing.T) {
	lists := [][]DeliveryBatch{
		{batchOf(0, 100), batchOf(100, 500), batchOf(500, 1000)},
		{batchOf(0, 1000)},
		{batchOf(500, 1001), batchOf(1001, 2000)},
		{batchOf(1200, 1300), batchOf(0, 900)},                                 // 回填批次的 end 不单调
		{batchOf(0, 100), batchOf(200, 300)},                                   // 中间有空洞
		{batchOf(0, 100), batchOf(101, 200)},                                   // 单字节分隔符（+1 桥接规则）
		{DeliveryBatch{}, batchOf(300, 400), batchOf(0, 100), DeliveryBatch{}}, // 含零值条目
		{batchOf(1000, 1000), batchOf(1000, 1001)},                             // 退化零长度 + 刚越水位
		{batchOf(999, 1000), batchOf(1000, 1001)},                              // 边界相邻两条
		{batchOf(0, 100), batchOf(100, 101), batchOf(101, 1000), batchOf(1000, 1001)},
	}
	for _, watermark := range []uint64{0, 1, 100, 101, 500, 1000, 1001, 4000} {
		for index, batches := range lists {
			original := append([]DeliveryBatch(nil), batches...)
			want := ContiguousDeliveryEnd(original, watermark)
			kept, dropped, err := PruneDeliveryBatches(batches, watermark, 0)
			require.NoError(t, err, "list=%d watermark=%d", index, watermark)
			require.Equal(t, want, ContiguousDeliveryEnd(kept, watermark),
				"裁剪改变了连续前缀（list=%d watermark=%d）", index, watermark)
			if watermark == 0 {
				require.Equal(t, 0, dropped, "水位 0 不得裁任何条目")
				require.Equal(t, original, kept)
				continue
			}
			// 判据是「末位 ≤ 水位」：被裁条目的条数 = 原列表中满足该判据的条数，
			// 保留的条目一律是「末位 > 水位」的那些（keepRecent=0 无尾窗）。
			expectedDropped := 0
			for _, batch := range original {
				if batch.End <= watermark {
					expectedDropped++
				}
			}
			require.Equal(t, expectedDropped, dropped, "list=%d watermark=%d", index, watermark)
			for _, batch := range kept {
				require.Greater(t, batch.End, watermark,
					"保留了水位之下的条目：%+v（list=%d watermark=%d）", batch, index, watermark)
			}
		}
	}
}

// TestPruneDeliveryBatchesKeepRecentIsAuditTailOnly 验证审计尾窗只多留不少留。
func TestPruneDeliveryBatchesKeepRecentIsAuditTailOnly(t *testing.T) {
	batches := []DeliveryBatch{
		batchOf(0, 100), batchOf(100, 200), batchOf(200, 300), batchOf(300, 400), batchOf(400, 500),
		batchOf(900, 1001), // 水位之上：无论如何都保留
	}
	const watermark = 1000
	kept, dropped, err := PruneDeliveryBatches(batches, watermark, 2)
	require.NoError(t, err)
	// 被水位越过 5 条，尾窗保留最近 2 条 ⇒ 裁 3 条。
	require.Equal(t, 3, dropped)
	require.Equal(t, []DeliveryBatch{
		batchOf(300, 400), batchOf(400, 500), batchOf(900, 1001),
	}, kept)
	require.Equal(t, ContiguousDeliveryEnd(batches, watermark), ContiguousDeliveryEnd(kept, watermark))

	// 尾窗大于可裁条数时：一条都不裁（dropped=0 且返回同一列表）。
	keptAll, droppedAll, err := PruneDeliveryBatches(batches, watermark, 100)
	require.NoError(t, err)
	require.Equal(t, 0, droppedAll)
	require.Equal(t, batches, keptAll)

	// 非法尾窗（负）回退默认 0：严格按水位。
	keptStrict, droppedStrict, err := PruneDeliveryBatches(batches, watermark, -7)
	require.NoError(t, err)
	require.Equal(t, 5, droppedStrict)
	require.Equal(t, []DeliveryBatch{batchOf(900, 1001)}, keptStrict)
}

// TestPruneDeliveryBatchesIsIdempotent 验证裁剪幂等：再裁一次不再产生变更（稳定态零写入的前提）。
func TestPruneDeliveryBatchesIsIdempotent(t *testing.T) {
	batches := []DeliveryBatch{batchOf(0, 100), batchOf(100, 1001), batchOf(1001, 1500)}
	first, dropped, err := PruneDeliveryBatches(batches, 1000, 0)
	require.NoError(t, err)
	require.Equal(t, 1, dropped)
	second, droppedAgain, err := PruneDeliveryBatches(first, 1000, 0)
	require.NoError(t, err)
	require.Equal(t, 0, droppedAgain)
	require.Equal(t, first, second)
	// 水位前进后同样幂等（单调性：已裁条目在更晚的水位下仍是恒等元）。
	third, droppedThird, err := PruneDeliveryBatches(first, 1500, 0)
	require.NoError(t, err)
	require.Equal(t, 2, droppedThird, "水位推到 1500 后，两条（末位 ≤ 水位）都成为恒等元")
	require.Empty(t, third)
	require.NotNil(t, third)
}

// TestPruneDedupesByWatermark 验证「同一水位只裁一次」（稳态 O(1)，不外溢成每轮扫描）。
//
// 取舍如实登记：水位未变时不再扫列表，故「整体落在水位之下的**回填**批次」（如
// ResolveDeliveryThroughRecovery 补登记的历史区间）会留到下一次水位前进才被清掉。它本就是
// 恒等元（不影响任何派生值），留着无害；换来的是每次投递/每轮采集都不再付 O(列表长度) 的扫描
// ——否则一个「水位被门禁长期挡住」的源会每轮扫描整张保留列表（积压场景可达十万级）。
func TestPruneDedupesByWatermark(t *testing.T) {
	led := New()
	key := keyOf("dedupe", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "dedupe", SourceGeneration: "g1"})
	require.NoError(t, led.RecordDelivery(key, 0, 800, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-1", Path: "/p", State: logtypes.RecoveryWALResponsibilityXfer,
		CoversFrom: 0, CoversTo: 800,
	}))
	_, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Empty(t, led.Get(key).DeliveryBatches, "水位推进时完成首次裁剪")

	// 回填一条整体在水位之下的批次：同一水位下不再重复扫描，故它会留下（判据不变、语义不变）。
	require.NoError(t, led.ResolveDeliveryThroughRecovery(key, 0, 100))
	require.Len(t, led.Get(key).DeliveryBatches, 1, "同一水位下不再重复扫描列表")
	require.Equal(t, uint64(800), led.Get(key).Positions.Delivery, "回填条目在水位之下，不改变派生值")

	// 水位再次前进：连同回填条目一起清掉，且按同一水位重算的连续前缀不变。
	//
	// 注意 Positions.Delivery 是派生值，只有 RecordDelivery / Restore 会重算它（TryReclaim 从不改它，
	// 与本次改动无关），故这里按同一水位显式重算两侧比较，而不是读那个可能滞后的落库值。
	beforePrefix := ContiguousDeliveryEnd(led.Get(key).DeliveryBatches, 900)
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-2", Path: "/p", State: logtypes.RecoveryWALResponsibilityXfer,
		CoversFrom: 800, CoversTo: 900,
	}))
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(900), pos)
	after := led.Get(key).DeliveryBatches
	require.Empty(t, after)
	require.Equal(t, beforePrefix, ContiguousDeliveryEnd(after, 900),
		"裁剪前后按同一水位重算的连续前缀必须相同（恒等元）")
}

// TestVerifyPruneIdentityRejectsUnsafePrune 直接校验守卫本身：把仍被需要的条目裁掉必须被拦下。
//
// 这是「裁剪失败只告警不阻断」的判据侧：一旦判据与派生计算不再自洽，守卫返回错误，
// 调用方（账本写路径 / 迁移比对）保留完整列表继续跑，绝不把「裁不掉」变成「采集出错」。
func TestVerifyPruneIdentityRejectsUnsafePrune(t *testing.T) {
	const watermark = 1000
	needed := []DeliveryBatch{batchOf(900, 1001)} // 末位越过水位一字节：仍会推进连续前缀
	require.Equal(t, uint64(1001), contiguousDeliveryEnd(needed, watermark))
	require.Error(t, verifyPruneIdentity(needed, nil, watermark),
		"裁掉仍被需要的条目必须被守卫拦下")
	require.NoError(t, verifyPruneIdentity(needed, needed, watermark))
}

// TestPruneDeliveryBatchesOnLedgerWritePaths 验证账本写路径确实执行裁剪、且不影响派生值。
func TestPruneDeliveryBatchesOnLedgerWritePaths(t *testing.T) {
	led := New()
	key := keyOf("prune", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "prune", SourceGeneration: "g1"})

	// 水位为 0（尚无任何字节被裁定安全）⇒ 一条都不裁。
	require.NoError(t, led.RecordDelivery(key, 0, 400, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 400, 800, logtypes.DeliveryUnknown))
	require.Len(t, led.Get(key).DeliveryBatches, 2, "水位 0 时不得裁剪")
	require.Equal(t, uint64(800), led.Get(key).Positions.Delivery)

	// 推进水位到 800（走完责任转移链，CanReclaim 放行）：旧批次应被裁，派生值不变。
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-1", Path: "/data/recovery/seg-1", State: logtypes.RecoveryStaged,
		CoversFrom: 0, CoversTo: 800,
	}))
	require.NoError(t, led.TransitionRecovery(key, "seg-1", logtypes.RecoveryWALResponsibilityXfer, "", "receiver"))
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(800), pos)
	entry := led.Get(key)
	require.Empty(t, entry.DeliveryBatches, "水位之下的批次应在 TryReclaim 时被裁掉")
	require.Equal(t, uint64(800), entry.Positions.Delivery, "裁剪不得改变 delivery_position")
	// 最近状态与错误计数另存，不随裁剪丢失。
	require.Equal(t, logtypes.DeliveryUnknown, entry.DeliveryState)

	// 水位之上的一条必须留下（刚越过水位一字节）。
	require.NoError(t, led.RecordDelivery(key, 800, 801, logtypes.DeliveryRequestDone))
	entry = led.Get(key)
	require.Equal(t, []DeliveryBatch{batchOf(800, 801)}, entry.DeliveryBatches)
	require.Equal(t, uint64(801), entry.Positions.Delivery)

	// 快照（持久化读路径）同样只看到裁剪后的列表：索引行数因此不随总量增长。
	snapshot := led.Snapshot()
	require.Len(t, snapshot, 1)
	require.Len(t, snapshot[0].DeliveryBatches, 1)
	require.Equal(t, uint64(801), snapshot[0].Positions.Delivery)
}

// TestDeliveryBatchPruneConfigEscapeHatchAndFallback 验证开关与非法值回退（配置误写不放宽判据）。
func TestDeliveryBatchPruneConfigEscapeHatchAndFallback(t *testing.T) {
	key := keyOf("cfg", "g1")
	newLedger := func() *Ledger {
		led := New()
		led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "cfg", SourceGeneration: "g1"})
		require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
		require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
			SegmentID: "seg-1", Path: "/p", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 100,
		}))
		require.NoError(t, led.TransitionRecovery(key, "seg-1", logtypes.RecoveryWALResponsibilityXfer, "", "receiver"))
		return led
	}

	// 默认：开启（水位推进即裁）。
	led := newLedger()
	require.True(t, led.DeliveryBatchPrune().Enabled, "默认必须开启裁剪")
	_, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Empty(t, led.Get(key).DeliveryBatches)

	// 逃生口：显式关闭后不再裁（应急排查用）。
	off := newLedger()
	off.SetDeliveryBatchPrune(DeliveryBatchPruneConfig{Enabled: false})
	require.False(t, off.DeliveryBatchPrune().Enabled)
	_, err = off.TryReclaim(key)
	require.NoError(t, err)
	require.Len(t, off.Get(key).DeliveryBatches, 1, "关闭裁剪后水位之下的条目必须保留")
	require.Equal(t, uint64(100), off.Get(key).Positions.Delivery)

	// 非法值回退：负尾窗回退默认 0（不放宽判据）；Enabled 原样尊重（零值 false = 显式关闭，
	// 默认开启由装载侧 indexPruneConfigOf(nil) 提供，见 DeliveryBatchPruneConfig 的零值语义注释）。
	invalid := DeliveryBatchPruneConfig{Enabled: true, KeepRecent: -3}
	require.Equal(t, DeliveryBatchPruneConfig{Enabled: true, KeepRecent: 0}, invalid.Normalized())
	require.Equal(t, DeliveryBatchPruneConfig{Enabled: false, KeepRecent: 0},
		DeliveryBatchPruneConfig{KeepRecent: -1}.Normalized(),
		"归一化只修尾窗，不擅自打开被显式关闭的开关")
	require.Equal(t, DeliveryBatchPruneConfig{Enabled: true, KeepRecent: 0},
		DeliveryBatchPruneConfig{Enabled: true}.Normalized(),
		"零值尾窗就是默认 0 条（严格按水位），不被改成别的魔数")
}
