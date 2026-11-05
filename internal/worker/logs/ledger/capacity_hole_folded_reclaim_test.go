package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「折叠凸包不得被当作 reclaim 通行证」（2026-11-05 修复）。
//
// # 漏洞形态
//
// `capacityDroppedRangesLocked` 曾把每条 NonBlocking 缺口的 `StartPos/EndPos` 直接
// 并进 certified 覆盖。而这两个字段在**折叠**之后是**凸包**：
// `foldUnresolvedGapsLocked` 把同因的旧缺口并成一条时，StartPos 取最小、EndPos 取最大。
//
// 于是凸包跨越了夹在中间的**健康区间**——那些字节既未经投递确认、也**没被裁定丢弃**。
// reclaim 会一路推过凸包末端 ⇒ `pruneReclaimedLocked` 把中间那批**从未投递**的 WAL
// 条目剪掉 ⇒ 数据静默消失，且账本上读不出真实空洞（缺口区间已是凸包）。
//
// # 修法
//
// 与人工放弃路径同一口径（`AbandonGapsThrough` 对折叠过的缺口直接拒绝）：
// 折叠过的缺口**不并入** certified，reclaim 停在那里等人裁定。
//
// # 触发条件（生产上真实存在）
//
// `DefaultMaxUnresolvedGapsPerSource = 64` 条同因未解决缺口即触发折叠。生产上
// 「积压持续越界 → 每轮丢一小段」正是产生大量离散 WAL_BACKLOG_DROPPED 的典型形态。
//
// # 夹具的两个坑（都踩过，写在这里免得下一个人重犯）
//
//  1. **`Ledger.Get` 返回副本**（`ledger.go:1091` 的 `return e.clone()`）。
//     直接改返回值的 `Gaps` 不会进真实账本 —— 测试会假绿，什么都测不到。
//     所以必须通过真实 API（`RecordGap`）构造状态。
//  2. **投递证据必须覆盖凸包内部**。若投递证据只到第一个洞之前，
//     `contiguousCertifiedEnd` 会被投递证据本身挡住 —— 那样即使删掉修复，
//     主用例也是绿的。

// foldedLedger 用**真实 API** 造一个「已折叠」的容量丢弃缺口。
//
// # 形态（必须与生产同构，否则测不到漏洞）
//
// 关键认识：容量丢弃洞之所以存在，正是因为**那些字节没投递成功**。
// 所以投递证据必然是**断开的**，而不是覆盖凸包：
//
//	REQUEST_DONE: [0,100) 与 [400,800)     ← 中间那段从未投递成功
//	65 条离散容量丢弃缺口，折叠后凸包 [100,400)
//	  其中真实空洞只有 [100,101)、[108,109) … 与 [700,701)、[710,711)
//	  ⇒ [101,700) 中除那些小洞外都是**健康字节**（没投递、也没被裁定丢弃）
//
// 旧实现把凸包 [100,400) 并入 certified ⇒ 合并后变成 [0,800) ⇒ reclaim=800，
// 把中间那些从未投递的健康字节一起判成可回收，随后 prune 直接删掉。
// 修复后 certified 只到 100。
//
// # 两个夹具坑（都踩过）
//
//  1. **`Ledger.Get` 返回副本**（`ledger.go:1091` 的 `e.clone()`），直接改返回值
//     不会进真实账本 ⇒ 测试假绿。必须用真实 API 构造。
//  2. **缺口必须真的够 65 条且互不相邻**：`rangesTouch` 的容差是 `±1`，
//     `MergePositionRanges` 也是 `±1`，所以间隙要 ≥2 字节。
func foldedLedger(t *testing.T) (*Ledger, SourceKey) {
	t.Helper()
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 800))
	require.NoError(t, led.AdvanceDurable(key, 800))

	// 投递证据**在洞处断开**（这才是容量丢弃洞的真实形态）。
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 400, 800, logtypes.DeliveryRequestDone))

	// 63 条离散缺口（间隙 7 字节） + 2 条 → 共 65 条 > 64 上限 ⇒ 触发折叠。
	for i := 0; i < 63; i++ {
		from := uint64(100 + i*8)
		require.NoError(t, led.RecordGap(key, from, from+1, GapReasonWALBacklogDropped,
			"discrete capacity drop"))
	}
	for i := 0; i < 2; i++ {
		from := uint64(700 + i*10)
		require.NoError(t, led.RecordGap(key, from, from+1, GapReasonWALBacklogDropped,
			"discrete capacity drop"))
	}

	// 前置断言：折叠真的发生了。
	//
	// 判据用「出现了多条精确子区间的缺口」而不是 FoldSpanMerges>0：
	// 后者只在**子区间数超上限**时才 >0，只并几条受害者时它是 0
	// （探针实测：65 条→64 条时 `spans=2 merges=0`）。用它当判据会误判为“没折叠”。
	e := capacityHoleEntry(t, led, key)
	require.LessOrEqual(t, len(e.Gaps), DefaultMaxUnresolvedGapsPerSource,
		"夹具前提：缺口条数应已被折叠回上限内（实测 %d 条）", len(e.Gaps))
	folded := 0
	for i := range e.Gaps {
		if len(e.Gaps[i].FoldSpans) > 1 {
			folded++
		}
	}
	require.NotZero(t, folded,
		"夹具前提：65 条同因缺口应折叠出至少一条带多条子区间的缺口，实测 0 条")
	return led, key
}

// foldedHullEnd 返回折叠凸包的末端（reclaim 不得越过的界）。
func foldedHullEnd(t *testing.T, e *Entry) uint64 {
	t.Helper()
	var end uint64
	for i := range e.Gaps {
		g := e.Gaps[i]
		if len(g.FoldSpans) > 1 && g.EndPos > end {
			end = g.EndPos
		}
	}
	return end
}

// TestContiguousCertifiedEnd_DoesNotCrossFoldedCapacityHull 是本修复的主用例。
//
// 判据：reclaim 必须**小于**折叠凸包末端。修复前它会一路推到凸包末端，
// 把中间那段健康字节一起判成「可回收」。
//
// 转红方式（实测）：删掉 `capacityDroppedRangesLocked` 里
// `if gap.FoldSpansUnknown || gap.FoldSpanMerges > 0 { continue }`，
// 本用例以「reclaim 跨过折叠凸包末端」变红。
func TestContiguousCertifiedEnd_DoesNotCrossFoldedCapacityHull(t *testing.T) {
	led, key := foldedLedger(t)
	e := capacityHoleEntry(t, led, key)
	hullEnd := foldedHullEnd(t, e)
	require.NotZero(t, hullEnd)

	end := contiguousCertifiedEnd(e)
	require.Less(t, end, hullEnd,
		"reclaim 不得跨过折叠凸包末端（%d）：凸包内部夹着从未投递的健康字节，"+
			"跨过去再 prune 就是静默丢数据（实测 end=%d）", hullEnd, end)

	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Less(t, pos, hullEnd, "TryReclaim 同样不得跨过折叠凸包（实测 pos=%d）", pos)
}

// TestCapacityDroppedRanges_ExpandsFoldedGapsExactly 折叠缺口必须**逐段精确展开**，
// 而不是整条拒绝、也不是用凸包。
//
// # 为何是展开而不是拒绝（这一条我改过一次期望值）
//
// 第一版写的是「折叠过就不产出任何洞」（`require.Empty`），那会把 65 段真实空洞
// 全部藏起来 ⇒ reclaim 挺在第一个洞前不动 ⇒ 变成新的永久停采。
//
// 正确语义：折叠缺口的 `FoldSpans` 里存的仍是**精确**区间（`foldUnresolvedGapsLocked`
// 显式保留了它们），逐段并入既安全、又保住了回收能力。
// 只有 `FoldSpansUnknown`（证据真的丢了）才整条拒绝。
func TestCapacityDroppedRanges_ExpandsFoldedGapsExactly(t *testing.T) {
	led, key := foldedLedger(t)
	holes := capacityDroppedRangesLocked(capacityHoleEntry(t, led, key))
	require.NotEmpty(t, holes, "折叠缺口的精确子区间必须被展开（否则 reclaim 永久停住）")

	// 每一段都必须是**窄**区间（真实空洞各占 1 字节），不得出现跨越式凸包。
	for _, h := range holes {
		require.LessOrEqual(t, h.To-h.From, uint64(1),
			"不得把夹间健康字节当可穿洞：实测有宽达 %d 字节的洞 %v", h.To-h.From, h)
	}
	// 关键：不得覆盖那段夹间健康字节（如 [101,700) 内的任意点）。
	for _, h := range holes {
		require.False(t, h.From <= 101 && h.To >= 700,
			"不得产出跨越大段健康区的凸包洞：%v", h)
	}
}

// TestContiguousCertifiedEnd_StillCrossesUnfoldedHole 对照：未折叠的单洞仍可穿，
// 2026-10-29 那轮生产修复不得被本次回退。
func TestContiguousCertifiedEnd_StillCrossesUnfoldedHole(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))
	require.NoError(t, led.RecordDelivery(key, 0, 300, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordGap(key, 100, 200, GapReasonWALBacklogDropped, "single hole"))

	require.EqualValues(t, 300, contiguousCertifiedEnd(capacityHoleEntry(t, led, key)),
		"未折叠的单洞必须仍可被穿过")
}

// TestCapacityDroppedRanges_UnknownSpansAreConservative FoldSpansUnknown 也不得穿过。
//
// 这条是唯一允许直接改账本内部的地方：`FoldSpansUnknown` 没有公开写入入口
// （只在折叠时由代码置位），而它恰恰是「证据已丢」这个形态的唯一表示。
func TestCapacityDroppedRanges_UnknownSpansAreConservative(t *testing.T) {
	led, key := capacityHoleFixture(t)
	require.NoError(t, led.RecordGap(key, 50, 60, GapReasonWALBacklogDropped, "probe"))

	led.mu.Lock()
	e := led.entries[key]
	require.NotNil(t, e)
	require.NotEmpty(t, e.Gaps)
	e.Gaps[len(e.Gaps)-1].FoldSpansUnknown = true
	led.mu.Unlock()

	got := capacityDroppedRangesLocked(capacityHoleEntry(t, led, key))
	require.Empty(t, got,
		"FoldSpansUnknown 表示证据已丢失，不得据此放行 reclaim（实测 %v）", got)
}
