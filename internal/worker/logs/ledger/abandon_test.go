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

// 永久空洞的出口：放弃前回收链必须**持续卡住**，放弃后必须能推进。
//
// 场景（用户原案）：区间 [0,200] 内位置 100–150 的 WAL 记录物理永久损坏/被删/校验永败。
// 恢复分段因此永远到不了 WAL_RESPONSIBILITY_TRANSFERRED，`CanReclaim` 永不放行 →
// reclaim 不动 → WAL 积压永不回落 → 滞回永不满足 → 源永久 PAUSED（"到天荒地老"）。
//
// 两条断言缺一不可：
//
//	① 放弃**之前**必须卡住（变体：能推进即红 —— 那说明 default-deny 门禁被放宽了）；
//	② 放弃**之后**必须能推进（变体：仍卡住即红 —— 那说明出口没接上）。
//
// 转红方式（实测）：把 tryReclaimLocked 里的凭据放行段去掉，②立即红；
// 把 `!allowed` 判定改成恒放行，①立即红。
func TestAbandonedHoleReleasesReclaimGate(t *testing.T) {
	led := New()
	key := keyOf("hole-exit", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "hole-exit", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))
	require.NoError(t, led.RecordDelivery(key, 0, 200, logtypes.DeliveryRequestDone))
	// 恢复分段只到 STAGED：责任未转移 ⇒ CanReclaim 拒绝（这就是"永久卡住"的机器）。
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-hole", Path: "project://seg-hole", State: logtypes.RecoveryStaged,
		CoversFrom: 0, CoversTo: 200,
	}))
	// 位置 100–150 永久丢失。
	require.NoError(t, led.RecordGap(key, 100, 150, GapReasonAppendRejected, "WAL 段物理损坏"))

	// ① 放弃前：回收必须持续失败（且不得静默推进）。
	for i := 0; i < 3; i++ {
		pos, err := led.TryReclaim(key)
		require.Error(t, err, "放弃前回收链必须持续被门禁挡住")
		require.Zero(t, pos, "被挡住时 reclaim 不得推进")
	}
	require.Error(t, led.ResumeAcquire(key), "存在未解决缺口时不得恢复采集")

	// ② 人工裁定放弃（带操作人）。
	resolved, err := led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)
	require.Equal(t, 1, resolved)

	// 放弃后：回收链必须能跨过这个空洞。
	pos, err := led.TryReclaim(key)
	require.NoError(t, err, "带凭据的回收链必须能跨过已放弃的空洞")
	require.Equal(t, uint64(200), pos, "应推进到该恢复分段覆盖的末端")
	require.NoError(t, led.ResumeAcquire(key), "缺口已裁定放弃，恢复采集应放行")
}

// hold 优先于凭据：运维显式钉住的分段不得因"已放弃空洞"而被回收跨过。
//
// 为什么：hold 表达"这段先别动"，凭据表达"这段数据没了"。两者冲突时以"不许动"为准 ——
// 凭据是**丢失**的裁定，不是**放行**的授权；把 hold 让位给凭据等于让放弃动作顺带解除钉住，
// 那是运维没同意的副作用。
//
// 转红方式（实测）：把凭据放行段的条件 `!ref.HasHold` 去掉，本用例立即红。
func TestAbandonedHoleDoesNotOverrideHold(t *testing.T) {
	led := New()
	key := keyOf("hole-hold", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "hole-hold", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))
	require.NoError(t, led.RecordDelivery(key, 0, 200, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-hold", Path: "project://seg-hold", State: logtypes.RecoveryWALResponsibilityXfer,
		ReleaseReason: logtypes.ReleaseProjectionBacked, CoversFrom: 0, CoversTo: 200, HasHold: true,
	}))
	require.NoError(t, led.RecordGap(key, 100, 150, GapReasonAppendRejected, "damaged"))
	_, err := led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)

	pos, err := led.TryReclaim(key)
	require.Error(t, err, "有 hold 时即便存在放弃凭据也必须拒绝回收")
	require.Zero(t, pos)
}

// 放弃凭据**不得**成为通用放行：凭据区间与本分段覆盖区间不相交时，门禁照旧拒绝。
//
// 这条守的是"凭据放行是定向的、不是全局开关"。
func TestAbandonmentOutsideSegmentDoesNotReleaseReclaim(t *testing.T) {
	led := New()
	key := keyOf("hole-outside", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "hole-outside", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))
	require.NoError(t, led.RecordDelivery(key, 0, 200, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg-out", Path: "project://seg-out", State: logtypes.RecoveryStaged,
		CoversFrom: 0, CoversTo: 200,
	}))
	// 空洞在 50000–60000：远在本分段覆盖区间之外。
	require.NoError(t, led.RecordGap(key, 50000, 60000, GapReasonAppendRejected, "elsewhere"))
	_, err := led.AbandonGapsThrough(key, 60000, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)

	pos, err := led.TryReclaim(key)
	require.Error(t, err, "凭据与分段覆盖区间不相交时不得放行回收")
	require.Zero(t, pos)
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
