package ledger

// 「容量丢弃洞可被 reclaim 穿过」回归（2026-10-29 生产死锁）。
//
// ## 现场形态（`ingest.index.db` 实读，非虚构）
//
// 用户裁定「WAL 内存满就直接丢最旧、不暂停」后，丢弃会登记一条真实位置区间的
// `WAL_BACKLOG_DROPPED` 缺口（并进 `gapReasonsNonBlocking` ⇒ 不挡 ResumeAcquire）。
// 但 `contiguousCertifiedEnd` 只认 `RequestDoneRangesOf`（真投递证据），于是：
//
//	inst:322  Reclaim=1784470
//	          RequestDone = [(0,1784470), (1787011,2443892)]
//	          未解决缺口  = (1784470,1787011)  ← WAL_BACKLOG_DROPPED，2541 字节
//
// 洞正好卡在两个已投递区间之间 ⇒ 连续覆盖断在那里 ⇒ reclaim 永久钉死。
// 后续 `[1787011,2443892]` 明明早就 REQUEST_DONE（真的投出去了），却被反复重投 +
// 反复重建 ⇒ `pending_reconfirm` 循环、WAL 不回收、持续丢数据。
//
// 19 个源的天然对照（无例外）：
//
//	有该洞的 8 个源：reclaim 钉在 ~1784xxx，read 已到 310-445 万（差 1.3-4.1MB）
//	零洞的 11 个源：reclaim == read（差 0）
//
// ## 本文件钉住的不变量
//
//	① 容量丢弃洞**可被穿过**（否则源永久停采）；
//	② 但**绝不**把洞写进 RequestDoneRanges / DeliveryEvidence（谎报已投递 = 数据诚实性红线）；
//	③ **只有** NonBlocking 名单的洞可穿：归档类（ARCHIVE_CONTENT_UNKNOWN）不可穿；
//	④ 未知名仍阻断（default-deny 不得放宽）。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// capacityHoleFixture 建一个「reclaim 被容量丢弃洞钉住」的真实形态账本。
//
// 水位以 inst:322 的生产值为蓝本。
func capacityHoleFixture(t *testing.T) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := SourceKey{LogSourceID: "inst:322/file", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "inst:322/file", SourceGeneration: "g1", ParserVersion: "v1"})
	return led, key
}

func capacityHoleEntry(t *testing.T, led *Ledger, key SourceKey) *Entry {
	t.Helper()
	e := led.Get(key)
	require.NotNil(t, e, "夹具应已建源")
	return e
}

// TestContiguousCertifiedEndCrossesCapacityDroppedHole 是本修复的**主正例**。
//
// 转红方式（实测）：去掉 `contiguousCertifiedEnd` 里 capacityDroppedRangesLocked 那段合并
// ⇒ reclaim 恒停在洞前 1784470，必红。
func TestContiguousCertifiedEndCrossesCapacityDroppedHole(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 2443892))
	require.NoError(t, led.AdvanceDurable(key, 2443892))

	// 两段真实投递证据，中间夹一个容量丢弃洞（与 inst:322 完全同构）。
	require.NoError(t, led.RecordDelivery(key, 0, 1784470, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 1787011, 2443892, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordGap(key, 1784470, 1787011, GapReasonWALBacklogDropped,
		"WAL backlog exceeded (entries=5024/5000); dropped 24 oldest entries to stay in memory"))

	end := contiguousCertifiedEnd(capacityHoleEntry(t, led, key))
	require.EqualValues(t, 2443892, end,
		"容量丢弃洞必须可被穿过：否则该源 reclaim 永久钉在 1784470、WAL 永不回收（13.6MB 不释放）")

	// 端到端：TryReclaim 必须真的推过去。
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.EqualValues(t, 2443892, pos, "TryReclaim 必须穿过洞推进到已投递末端")
}

// TestCapacityDroppedHoleNotClaimedAsDelivered 钉住红线 ②：洞绝不能被当成「已投递」。
//
// 这是本修复最容易犯的错：为了让 reclaim 走通，就把洞塞进 RequestDoneRanges。
// 那样下游会以为那些字节真在 VL 里 ⇒ 用户查不到却被告知“已投递”⇒ 谎报。
//
// 转红方式（实测）：把 `certified = MergePositionRanges(...)` 改成写回
// `e.RequestDoneRanges = ...` ⇒ 必红。
func TestCapacityDroppedHoleNotClaimedAsDelivered(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 200, 300, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordGap(key, 100, 200, GapReasonWALBacklogDropped, "容量丢弃"))

	// 让 reclaim 真的跑一遍（正是它最容易把洞当证扚的时机）。
	_, err := led.TryReclaim(key)
	require.NoError(t, err)

	done := RequestDoneRangesOf(capacityHoleEntry(t, led, key))
	require.NotEmpty(t, done, "正向对照：真投递的那两段必须在")
	for _, r := range done {
		require.False(t, r.From <= 100 && r.To >= 200,
			"投递证据里不得包含被丢弃的区间 [100,200]（那不是已投递，是永远没了）")
	}

	found := false
	for _, g := range capacityHoleEntry(t, led, key).Gaps {
		if g.StartPos == 100 && g.EndPos == 200 {
			require.False(t, g.Resolved, "容量丢弃缺口必须保持未解决=可见，不得被静默消解")
			found = true
		}
	}
	require.True(t, found, "缺口必须仍在账本里可查")
}

// TestContiguousCertifiedEndDoesNotCrossArchiveGaps 钉住红线 ③。
//
// `ARCHIVE_CONTENT_UNKNOWN` 同样「不阻塞恢复」（属 permanentlyLost，过宽限期即放行），
// 但它的语义是「历史归档归属证明不了」，那些位置**从未进过投递流**。
// 把它们也当可穿过，会让 reclaim 越过真正该重读的区间。
//
// 转红方式（实测）：把 `IsGapReasonNonBlocking` 换成 `IsGapReasonPermanentlyLost` ⇒ 必红。
func TestContiguousCertifiedEndDoesNotCrossArchiveGaps(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 200, 300, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordGap(key, 100, 200, "ARCHIVE_CONTENT_UNKNOWN", "no content anchor"))

	require.EqualValues(t, 100, contiguousCertifiedEnd(capacityHoleEntry(t, led, key)),
		"归档归属类缺口不得被穿过：它们从未进过投递流，越过就是跳过该重读的区间")
}

// TestContiguousCertifiedEndStopsAtUnknownGap 钉住默认拒绝方向。
//
// 未知名（不在任何名单）必须仍然阻断连续性——default-deny 是既有契约，本修复不得放宽。
func TestContiguousCertifiedEndStopsAtUnknownGap(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 200, 300, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordGap(key, 100, 200, "FUTURE_UNKNOWN_REASON", "未知"))

	require.EqualValues(t, 100, contiguousCertifiedEnd(capacityHoleEntry(t, led, key)),
		"未知原因必须默认阻断，不得被本修复放宽")
}

// TestContiguousCertifiedEndUnaffectedWhenEvidenceAlreadyContiguous 反向对照。
//
// 无洞时行为必须与修改前**逐字相同**（不能因为新逻辑把相邻区间误处理）。
func TestContiguousCertifiedEndUnaffectedWhenEvidenceAlreadyContiguous(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))
	require.NoError(t, led.RecordDelivery(key, 0, 300, logtypes.DeliveryRequestDone))

	require.EqualValues(t, 300, contiguousCertifiedEnd(capacityHoleEntry(t, led, key)))
}
