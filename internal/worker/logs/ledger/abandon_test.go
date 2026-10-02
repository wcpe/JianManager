package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// abandonFixture 造一个含未解决缺口的源。
func abandonFixture(t *testing.T, id string) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := keyOf(id, "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))
	require.NoError(t, led.RecordGap(key, 100, 150, "APPEND_REJECTED", "fixture gap"))
	return led, key
}

// 无操作人的放弃必须被**拒绝**，且不得改动任何缺口状态。
//
// 为什么这条是硬要求：放弃是唯一允许「回收链跨过一个永远补不上的空洞」的动作。
// 若允许无痕放弃，等于给「静默丢日志」开了一个合法后门——现场只会看到源恢复采集、
// 却再也查不到那一段数据是补齐了还是被丢弃了。
//
// 转红方式（实测）：把 AbandonGapsThrough 开头的 Operator 空值检查去掉，本用例第一条断言立即红。
func TestAbandonGapsThroughRequiresOperator(t *testing.T) {
	led, key := abandonFixture(t, "abandon-nobody")

	_, err := led.AbandonGapsThrough(key, 200, GapAbandonment{})
	require.Error(t, err, "无操作人的放弃必须被拒绝")
	require.Contains(t, err.Error(), "operator")

	// 空白字符不算操作人（避免用 "" 之外的空白绕过）。
	_, err = led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "   "})
	require.Error(t, err, "纯空白操作人同样必须被拒绝")

	entry := led.Get(key)
	require.False(t, entry.Gaps[0].Resolved, "被拒绝的放弃不得改动缺口状态")
	require.Empty(t, entry.Abandonments, "被拒绝的放弃不得留下凭据")
	require.Error(t, led.ResumeAcquire(key), "缺口仍未解决：恢复采集必须继续被拒")
}

// 放弃必须留下**结构化**凭据：原因码 + 时间 + 操作人 + 独立于缺口记录的裁定记录。
//
// 关键设计点：凭据（Abandonments）的生命周期必须长于缺口记录本身——缺口会被
// trimResolvedGapsLocked 按数量裁剪，而「回收链凭什么跨过这个空洞」的凭据要一直留存。
//
// 转红方式（实测）：把 AbandonGapsThrough 里写 entry.Abandonments 的那一段去掉，
// 本用例的凭据断言立即红。
func TestAbandonGapsThroughRecordsStructuredEvidence(t *testing.T) {
	led, key := abandonFixture(t, "abandon-structured")

	resolved, err := led.AbandonGapsThrough(key, 200, GapAbandonment{
		ReasonCode: GapReasonPermanentlyLost,
		Operator:   "ops@example",
		Detail:     "WAL 段物理损坏，校验永败",
	})
	require.NoError(t, err)
	require.Equal(t, 1, resolved)

	entry := led.Get(key)
	gap := entry.Gaps[0]
	require.True(t, gap.Resolved)
	require.Equal(t, GapReasonPermanentlyLost, gap.ReasonCode, "必须写下结构化原因码")
	require.Equal(t, "ops@example", gap.ResolvedBy, "必须记下操作人")
	require.NotEmpty(t, gap.ResolvedAtUTC, "必须记下裁定时间")
	require.NotContains(t, gap.Resolution, "manual resolve by admin",
		"不得再使用硬编码解算串（放弃与自动消解必须可区分）")

	// 独立凭据：区间取**实际被放弃的缺口并集**（不是调用方传入的 through）。
	require.Len(t, entry.Abandonments, 1)
	ab := entry.Abandonments[0]
	require.Equal(t, uint64(100), ab.From)
	require.Equal(t, uint64(150), ab.To, "凭据区间应是真实空洞，而不是 through=200")
	require.Equal(t, GapReasonPermanentlyLost, ab.ReasonCode)
	require.Equal(t, "ops@example", ab.Operator)
	require.NotEmpty(t, ab.AtUTC)
	require.Equal(t, uint64(200), ab.Through)

	// 覆盖查询：空洞内命中，空洞外不命中。
	_, ok := led.AbandonmentsCovering(key, 120)
	require.True(t, ok, "空洞内位置应命中放弃凭据")
	_, ok = led.AbandonmentsCovering(key, 180)
	require.False(t, ok, "未被放弃的位置不得命中")

	// 恢复采集可放行（缺口已解）。
	require.NoError(t, led.ResumeAcquire(key))
}

// 凭据必须比缺口记录活得久：缺口被裁剪后，放弃凭据仍在。
//
// 这是「凭据独立于 Gap 存一份」这个设计的**唯一理由**：Gap 会被 trimResolvedGapsLocked 按
// 数量裁剪（只留审计尾部），若裁定只挂在 Gap 上，「回收链凭什么跨过这个空洞」的依据会
// 随缺口一并被裁掉。
//
// 夹具要点：噪声缺口用 ResolveGapsThrough（不产生凭据）消解，从而把 Gap 挤出保留窗口，
// 而放弃凭据的条数始终为 1 —— 这样测到的才是「凭据长出缺口记录」，而不是「凭据自己也被裁了」。
//
// 转红方式（实测）：把 entry.Abandonments 换成「只在 Gap 上写字段」，本用例在缺口被裁剪后
// 找不到凭据，立即红。
func TestAbandonEvidenceOutlivesGapTrimming(t *testing.T) {
	led, key := abandonFixture(t, "abandon-outlives")

	resolved, err := led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)
	require.Equal(t, 1, resolved)

	// 制造大量**不产生凭据**的已解决缺口，把上面那条放弃过的缺口挤出保留窗口。
	for i := 0; i < DefaultMaxResolvedGapsPerSource+5; i++ {
		start := uint64(1000 + i*100)
		require.NoError(t, led.RecordGap(key, start, start+10, "APPEND_REJECTED", "noise"))
		_, err := led.ResolveGapsThrough(key, start+10, "verified published projection")
		require.NoError(t, err)
	}
	entry := led.Get(key)
	// 缺口记录已被裁剪（原始那条已不在窗口内）……
	require.LessOrEqual(t, len(entry.Gaps), DefaultMaxResolvedGapsPerSource+1,
		"已解决缺口必须被裁剪（保留窗口有界）")
	var stillHasOriginalGap bool
	for _, gap := range entry.Gaps {
		if gap.StartPos == 100 && gap.EndPos == 150 {
			stillHasOriginalGap = true
		}
	}
	require.False(t, stillHasOriginalGap, "夹具前提：原缺口记录必须已被裁剪出保留窗口")
	// ……但凭据仍在，且原始空洞依然被覆盖。
	_, ok := led.AbandonmentsCovering(key, 120)
	require.True(t, ok, "凭据必须长于缺口记录：否则回收链的放行依据会随裁剪一起消失")
	require.Len(t, entry.Abandonments, 1, "无用例噪声：凭据条数应仍为 1")
	require.LessOrEqual(t, len(entry.Abandonments), DefaultMaxAbandonmentsPerSource,
		"凭据本身也必须有界")
}

// 凭据本身有界：条数越界时按 From 从旧到新裁剪，且裁剪后条数不再增长。
//
// 取舍（如实记下）：被裁掉的是**最旧**的裁定——若某天真的有几十个互不相邻的永久空洞，
// 最旧的那个会失去凭据、从而重新变得不可跨越。这是「凭据不得随故障规模无界增长」
// （2026-09-28 状态文件 1.2GB 事故的同类风险）与「最旧空洞仍可跨」之间的取舍，
// 取前者：那时重新裁定一次即可，而状态文件爆掉是不可逆的。
func TestAbandonEvidenceIsBounded(t *testing.T) {
	led, key := abandonFixture(t, "abandon-bounded")
	for i := 0; i < DefaultMaxAbandonmentsPerSource+10; i++ {
		start := uint64(1000 + i*1000)
		require.NoError(t, led.RecordGap(key, start, start+10, "APPEND_REJECTED", "hole"))
		_, err := led.AbandonGapsThrough(key, start+10, GapAbandonment{Operator: "ops@example"})
		require.NoError(t, err)
	}
	entry := led.Get(key)
	require.LessOrEqual(t, len(entry.Abandonments), DefaultMaxAbandonmentsPerSource,
		"凭据条数必须被定界")
	// 最新的裁定必须留存（回收链当前要跨过的是它）。
	last := uint64(1000 + (DefaultMaxAbandonmentsPerSource+9)*1000)
	_, ok := led.AbandonmentsCovering(key, last+5)
	require.True(t, ok, "最新的放弃凭据必须留存")
}

// 相邻/重叠的放弃裁定必须就地合并，不得让凭据随裁定次数无界增长。
func TestAbandonMergesAdjacentEvidence(t *testing.T) {
	led, key := abandonFixture(t, "abandon-merge")

	_, err := led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)
	// 第二个空洞紧接第一个（相隔 1 字节，与区间合并同一口径）→ 应并入同一条凭据。
	require.NoError(t, led.RecordGap(key, 151, 300, "APPEND_REJECTED", "second"))
	_, err = led.AbandonGapsThrough(key, 300, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)

	entry := led.Get(key)
	require.Len(t, entry.Abandonments, 1, "相接的放弃区间必须合并为一条凭据")
	require.Equal(t, uint64(100), entry.Abandonments[0].From)
	require.Equal(t, uint64(300), entry.Abandonments[0].To)
	require.Equal(t, uint64(300), entry.Abandonments[0].Through, "Through 取最新（更大）值")
}

// 自动路径不得写出 PERMANENTLY_LOST：自动放弃 = 静默丢日志，明令禁止。
func TestAutoResolveNeverMarksPermanentlyLost(t *testing.T) {
	led := New()
	key := keyOf("abandon-auto", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "abandon-auto", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.RecordGap(key, 100, 150, GapReasonAppendRejected, "auto"))
	require.NoError(t, led.AdvanceDurable(key, 200))

	_, err := led.ResolveGapsCoveredByRanges(key, []PositionRange{{From: 0, To: 200}},
		"verified published projection", GapReasonAppendRejected)
	require.NoError(t, err)

	entry := led.Get(key)
	require.True(t, entry.Gaps[0].Resolved)
	require.NotEqual(t, GapReasonPermanentlyLost, entry.Gaps[0].ReasonCode,
		"自动消解不得伪装成放弃裁定")
	require.Empty(t, entry.Abandonments, "自动路径不得产生放弃凭据")
}
