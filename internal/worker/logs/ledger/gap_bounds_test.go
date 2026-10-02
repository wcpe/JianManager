package ledger

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 缺陷 A 回归（缺口风暴把采集焊死）。转红说明见各用例注释：
// 每条都对准一个「改动前必然失败」的可观测事实，而不是只断言实现细节。

// TestGapStormMergesAdjacentSameReasonFailures 是缺陷 A 的第一道转红闸门。
//
// 现场（2026-10-01/02）：一次持续数小时的投递故障在单源累积 50,863–50,864 条未解决缺口。
// 投递失败是时间连续的：相邻批次的区间首尾相接（最多隔一个行分隔符），因此它们必须被
// 合并成一条覆盖区间。
//
// 转红：改动前 RecordGap 是无条件 append，本用例会看到 1000 条（断言 1 条）→ 必红。
func TestGapStormMergesAdjacentSameReasonFailures(t *testing.T) {
	led := New()
	key := keyOf("storm", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "storm", SourceGeneration: "g1"})

	// 1000 次连续失败：每次区间紧接上一次（含分隔符导致的 1 字节间隙）。
	for i := 0; i < 1000; i++ {
		start := uint64(i) * 10
		end := start + 9
		require.NoError(t, led.RecordGap(key, start, end, "DELIVER_ERROR", fmt.Sprintf("attempt %d failed", i)))
	}

	entry := led.Get(key)
	require.Len(t, entry.Gaps, 1, "连续同因失败必须合并为一条覆盖区间")
	require.Equal(t, uint64(0), entry.Gaps[0].StartPos)
	require.Equal(t, uint64(9999), entry.Gaps[0].EndPos)
	require.Equal(t, 1, led.UnresolvedGapCount(key))
	// 失败次数仍是可观测的：ErrorCount 不受合并影响。
	require.Equal(t, 1000, entry.ErrorCount)
	stats := led.GapObservability(key)
	require.Equal(t, 1, stats.Unresolved)
	require.Equal(t, uint64(999), stats.MergedTotal, "被合并掉的上报次数必须可观测")
}

// TestGapStormStaysBoundedAcrossScatteredFailures 守住「数量级上限」：
// 即使失败区间互不相邻（无法合并），单源未解决缺口条数也必须停在常数级，
// 而不是随故障时长线性增长。
//
// 转红：改动前无任何上限，本用例会看到 1000 条（断言 ≤ 上限）→ 必红。
func TestGapStormStaysBoundedAcrossScatteredFailures(t *testing.T) {
	led := New()
	key := keyOf("scattered", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "scattered", SourceGeneration: "g1"})

	// 互不相邻（间隔 100 字节）的同因失败：无法走相邻合并，只能靠上限折叠。
	for i := 0; i < 1000; i++ {
		start := uint64(i) * 100
		require.NoError(t, led.RecordGap(key, start, start+9, "DELIVER_ERROR", "scattered failure"))
	}

	entry := led.Get(key)
	require.LessOrEqual(t, len(entry.Gaps), DefaultMaxUnresolvedGapsPerSource,
		"未解决缺口条数必须有界")
	require.LessOrEqual(t, led.UnresolvedGapCount(key), DefaultMaxUnresolvedGapsPerSource)
	// 折叠只能扩大区间（更保守），绝不能把未解决说成已解决。
	for _, gap := range entry.Gaps {
		require.False(t, gap.Resolved)
	}
	require.Equal(t, uint64(0), entry.Gaps[0].StartPos)
	require.Equal(t, uint64(99909), entry.Gaps[len(entry.Gaps)-1].EndPos)
	require.Greater(t, led.GapObservability(key).FoldedTotal, uint64(0), "折叠次数必须可观测")
}

// TestGapFoldKeepsReasonsSeparate 守住按原因折叠：不同原因的处置语义不同
// （例如 STDIO_RAW_WRITE_FAILED 明确不可由投影自动消解），折叠不得把它们混成一条。
//
// 转红：若实现改成「整体折叠成一条」，本用例会因为找不到 STDIO_RAW_WRITE_FAILED 原因而红。
func TestGapFoldKeepsReasonsSeparate(t *testing.T) {
	led := New()
	key := keyOf("mixed", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "mixed", SourceGeneration: "g1"})

	for i := 0; i < 200; i++ {
		start := uint64(i) * 10
		reason := "DELIVER_ERROR"
		if i%2 == 1 {
			reason = GapReasonStdioRawWriteFailed
		}
		require.NoError(t, led.RecordGap(key, start, start+5, reason, "mixed reason storm"))
	}

	entry := led.Get(key)
	byReason := map[string]int{}
	for _, gap := range entry.Gaps {
		byReason[gap.Reason]++
	}
	require.LessOrEqual(t, byReason["DELIVER_ERROR"], DefaultMaxUnresolvedGapsPerSource)
	require.LessOrEqual(t, byReason[GapReasonStdioRawWriteFailed], DefaultMaxUnresolvedGapsPerSource)
	require.Contains(t, byReason, GapReasonStdioRawWriteFailed, "不可自动消解的原因不得被折叠丢失")
}

// TestResolveGapsCoveredByRangeRequiresFullCoverage 守住语义红线：
// 只有**完全落在**已确认落库区间内的缺口才可消解；部分重叠不算证据。
//
// 转红：若实现改用「末端 ≤ position」的判据，第二条缺口会被误判为已覆盖 → 必红。
func TestResolveGapsCoveredByRangeRequiresFullCoverage(t *testing.T) {
	led := New()
	key := keyOf("coverage", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "coverage", SourceGeneration: "g1"})
	require.NoError(t, led.RecordGap(key, 100, 200, "DELIVER_ERROR", "inside"))
	require.NoError(t, led.RecordGap(key, 900, 1000, "DELIVER_ERROR", "partially overlapping"))

	// 成功重投的区间是 [100,900)：第一条完全落在内，第二条只重叠一部分（越过 900）。
	resolved, err := led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 100, To: 900}},
		"delivery confirmed for this range", GapReasonDeliverError)
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.Equal(t, 1, led.UnresolvedGapCount(key))
	entry := led.Get(key)
	require.True(t, entry.Gaps[0].Resolved)
	require.False(t, entry.Gaps[1].Resolved, "部分重叠不构成“已落库”证据")

	// 完整的第二次重投覆盖 [900,1000)：第二条随之消解。
	resolved, err = led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 900, To: 1000}},
		"delivery confirmed for this range", GapReasonDeliverError)
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.Zero(t, led.UnresolvedGapCount(key))
	// 边界：起点早于证据区间的缺口同样不构成覆盖（只做包含判定）。
	require.NoError(t, led.RecordGap(key, 50, 60, "DELIVER_ERROR", "before evidence range"))
	resolved, err = led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 55, To: 60}},
		"delivery confirmed for this range", GapReasonDeliverError)
	require.NoError(t, err)
	require.Zero(t, resolved, "起点不在证据区间内不得消解")
	require.Equal(t, 1, led.UnresolvedGapCount(key))
}

// TestResolveGapsCoveredByRangesRejectsConvexHull 守住「连续覆盖 ≠ 凸包」（复审 P1-1）：
// 落在两段已确认区间**之间空洞**里的缺口绝不能被消解。
//
// 现场形态：一次 replay=true 的整窗重放把 evidence 扩为全量 canonical 集，
// [min(Start), max(End)] 凸包覆盖整段历史——落在凸包内部、却从未进入 WAL 的批次
// （APPEND_REJECTED / 容量门禁暂停）会被标成「delivery confirmed for this range」，
// 空洞被永久掩盖并放行 ResumeAcquire。
//
// 转红：把判据退回「缺口落在 min..max 凸包内」（或退回单区间 from/to 写法）即红——
// 空洞中的缺口会被误判为已覆盖。
func TestResolveGapsCoveredByRangesRejectsConvexHull(t *testing.T) {
	led := New()
	key := keyOf("hull", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "hull", SourceGeneration: "g1"})
	// 空洞（从未进 WAL 的批次）落在两段已确认区间 [0,100] 与 [200,300] 之间。
	require.NoError(t, led.RecordGap(key, 120, 180, GapReasonAppendRejected, "append rejected while paused"))

	resolved, err := led.ResolveGapsCoveredByRanges(key,
		[]PositionRange{{From: 0, To: 100}, {From: 200, To: 300}},
		"delivery confirmed for this range", GapReasonDeliverError, GapReasonAppendRejected)
	require.NoError(t, err)
	require.Zero(t, resolved, "缺口落在两段证据之间的空洞里，不得被消解")
	require.Equal(t, 1, led.UnresolvedGapCount(key))

	// 反向对照：同一区间被一整段连续证据覆盖时必须消解（证明上面不是因为「什么都不消解」而通过）。
	resolved, err = led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 0, To: 300}},
		"delivery confirmed for this range", GapReasonDeliverError, GapReasonAppendRejected)
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.Zero(t, led.UnresolvedGapCount(key))
}

// TestResolveGapsCoveredByRangesIsAllowlist 守住「原因判据是允许名单（默认拒绝）」：
// 未登记的原因一律不自动消解；空名单是装配错误，必须响亮报错而不是静默什么都不做。
func TestResolveGapsCoveredByRangesIsAllowlist(t *testing.T) {
	led := New()
	key := keyOf("allowlist", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "allowlist", SourceGeneration: "g1"})
	require.NoError(t, led.RecordGap(key, 0, 100, GapReasonStdioRawWriteFailed, "raw bytes may be lost"))
	require.NoError(t, led.RecordGap(key, 0, 100, GapReasonAppendRejected, "append rejected while paused"))

	// 未登记的原因（哪怕区间完全被证据覆盖）不得被消解。
	resolved, err := led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 0, To: 100}},
		"verified published projection", GapReasonDeliverError)
	require.NoError(t, err)
	require.Zero(t, resolved, "允许名单之外的原因不得被自动消解")
	require.Equal(t, 2, led.UnresolvedGapCount(key))

	// 空允许名单 = 装配错误：必须报错（否则「名单写空」会表现成「永远解不开」的无声失效）。
	_, err = led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 0, To: 100}}, "delivery confirmed for this range")
	require.Error(t, err, "空允许名单必须报错，不得静默什么都不做")

	// 登记进名单的原因在同一区间上可以消解（证明上面不是因为判据整体失效）。
	resolved, err = led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 0, To: 100}},
		"verified published projection", GapReasonAppendRejected)
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	entry := led.Get(key)
	require.False(t, entry.Gaps[0].Resolved, "未登记的原因（原始字节写入失败）必须保持未解决，等待人工确认")
	require.True(t, entry.Gaps[1].Resolved)
	// 自动路径之外，人工解算接口（ResolveGapsThrough，即 ResolveCoveredGapsForSource 的内核）
	// 语义不变：它仍按末端消解，可显式确认放弃不可证明的原因。
	require.Error(t, led.ResumeAcquire(key), "存在未解决缺口时仍必须拒绝恢复采集")
	resolved, err = led.ResolveGapsThrough(key, 100, "manual resolve by admin")
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.NoError(t, led.ResumeAcquire(key))
}

// TestMergePositionRangesBridgesSingleSeparator 守住区间合并的口径：相邻区间之间只隔一个
// 分隔字节（规范事件区间不含行分隔符）时必须合并为一段连续覆盖。
func TestMergePositionRangesBridgesSingleSeparator(t *testing.T) {
	merged := MergePositionRanges([]PositionRange{
		{From: 10, To: 20}, {From: 21, To: 30}, {From: 40, To: 50},
	})
	require.Len(t, merged, 2, "隔 1 个字节（分隔符）必须合并，隔 10 个字节（真实空洞）必须保留")
	require.Equal(t, PositionRange{From: 10, To: 30}, merged[0])
	require.Equal(t, PositionRange{From: 40, To: 50}, merged[1])
	require.True(t, CoveredByPositionRanges(merged, 12, 29))
	require.False(t, CoveredByPositionRanges(merged, 25, 45), "跨空洞的区间不得判为已覆盖")
}

// TestResolvedGapsAreTrimmedButAuditTailKept 守住「已解决缺口有界」：
// 缺口被反复消解时，审计尾部保留最近若干条，而不是无界累积。
func TestResolvedGapsAreTrimmedButAuditTailKept(t *testing.T) {
	led := New()
	key := keyOf("trim", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "trim", SourceGeneration: "g1"})

	total := DefaultMaxResolvedGapsPerSource + 20
	for i := 0; i < total; i++ {
		start := uint64(i) * 10
		require.NoError(t, led.RecordGap(key, start, start+5, "DELIVER_ERROR", "resolvable"))
		require.NoError(t, ledgerResolveOne(led, key, start+5))
	}

	entry := led.Get(key)
	require.LessOrEqual(t, len(entry.Gaps), DefaultMaxResolvedGapsPerSource)
	require.Equal(t, DefaultMaxResolvedGapsPerSource, led.GapObservability(key).Resolved)
	// 保留的是**最新**的若干条（审计尾部），最旧的位置必须已被裁掉。
	require.Greater(t, entry.Gaps[0].StartPos, uint64(0))
	require.Equal(t, uint64((total-1)*10), entry.Gaps[len(entry.Gaps)-1].StartPos)
}

func ledgerResolveOne(led *Ledger, key SourceKey, through uint64) error {
	_, err := led.ResolveGapsThrough(key, through, "delivery confirmed for this range")
	return err
}
