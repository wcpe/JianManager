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

// 永久空洞的出口：放弃前采集恢复必须被挡，放弃后必须放行；而 reclaim 的推进只认认证覆盖。
//
// 场景（用户原案）：区间 [0,200] 内位置 100–150 的字节物理永久损坏/被删/校验永败。
//
// ## 语义迁移（2026-10-29，必须读懂再改）
//
// 旧实现里「放弃」同时解两道门：`CanReclaim`（靠凭据放行分支）与 `ResumeAcquire`。
// 受管恢复分段机制删除后，`tryReclaimLocked` 只认两个真实证据：**REQUEST_DONE 的连续覆盖**
// 与 **max(Read, Durable)**，不再读 `Abandonments`。所以：
//
//	① 「卡住」不再由分段状态造成，而由**缺口本身**造成 —— ResumeAcquire 的 default-deny
//	   仍然拦住未解决缺口（这是缺口机制的核心，未动）；
//	② 放弃解除的是**恢复采集**这道门（本用例前半）；
//	③ reclaim 能推进到 min(认证覆盖末端, max(read,durable))，而它**不靠凭据**——
//	   本夹具里 REQUEST_DONE 已覆盖 [0,200)，所以推进到 200；后半的对照组证明：
//	   认证覆盖断在哪里，reclaim 就停在哪里，放弃凭据不能把它推过去。
//
// 转红方式（实测）：把 ResumeAcquire 的 default-deny 放宽成恒放行 ⇒ ① 立即红；
// 把 contiguousCertifiedEnd 改成读 Abandonments 当桥 ⇒ ③ 的对照组立即红。
func TestAbandonedHoleReleasesAcquireGateNotReclaim(t *testing.T) {
	led := New()
	key := keyOf("hole-exit", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "hole-exit", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))
	require.NoError(t, led.RecordDelivery(key, 0, 200, logtypes.DeliveryRequestDone))
	// 位置 100–150 永久丢失。
	require.NoError(t, led.RecordGap(key, 100, 150, GapReasonAppendRejected, "WAL 段物理损坏"))

	// ① 放弃前：恢复采集必须被挡住（缺口未解决）。
	require.Error(t, led.ResumeAcquire(key), "存在未解决缺口时不得恢复采集")
	require.Equal(t, 1, led.UnresolvedGapCount(key), "缺口必须可见（观测面照旧）")

	// ② 人工裁定放弃（带操作人）。
	resolved, err := led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.NoError(t, led.ResumeAcquire(key), "缺口已裁定放弃，恢复采集应放行")

	// ③ reclaim 推进到 min(认证覆盖, max(read,durable)) = 200。
	pos, err := led.TryReclaim(key)
	require.NoError(t, err, "新语义下「无事可做」也是 nil 错误（不再有 ErrReclaimNoSegment）")
	require.Equal(t, uint64(200), pos)
	require.Equal(t, uint64(200), led.Get(key).Positions.Reclaim)
}

// 对照组：放弃凭据**不是**回收的放行开关 —— reclaim 的上界只由认证覆盖决定，
// 与 Abandonments 无关（受管恢复分段机制删除后，凭据只剩两个消费者：
// ResumeAcquire 的缺口判据与 acquire.WAL 的剪枝）。
//
// 这条守的是「凭据是丢失的裁定，不是已投递的证据」。如果哪天有人把「放弃空洞」又接回
// 回收门禁（拿 Abandonments 当桥跨空洞），被放弃区间之后的健康字节会被当成已认证投递而丢弃。
//
// 与 reclaim_evidence_test.go 的 TestReclaimStopsAtRealHole 分工：那条只证明「无凭据时
// 停在空洞前」；本用例多了一个**有凭据**的自变量 —— 同样的几何下，凭据不得让结果多走一字节。
//
// 转红方式（实测）：把 contiguousCertifiedEnd 的 `break` 换成「跳过该区间继续往后扫」
// （即拿空洞/凭据当桥），本用例立即红。
func TestAbandonmentDoesNotBridgeCertifiedCoverage(t *testing.T) {
	led := New()
	key := keyOf("hole-no-bridge", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "hole-no-bridge", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))
	// 认证覆盖只有 [0,100)；[100,150) 是真正的空洞（≥2 字节，不被分隔符容差盖住）。
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 150, 200, logtypes.DeliveryRequestDone))

	// 对照组（无凭据）：先测一次基线。
	base, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(100), base, "前置：无凭据时回收停在真实空洞 [100,150) 之前")

	// 放弃这个空洞，带完整凭据（操作人 + 永久丢失原因码）。
	require.NoError(t, led.RecordGap(key, 100, 150, GapReasonPermanentlyLost, "物理损坏"))
	resolved, err := led.AbandonGapsThrough(key, 200, GapAbandonment{Operator: "ops@example"})
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	_, covered := led.AbandonmentsCovering(key, 120)
	require.True(t, covered, "前置：凭据必须已覆盖空洞（否则本用例无判别力）")

	// 被测点：凭据不得让 reclaim 多走一字节。
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(100), pos,
		"放弃凭据不得把 reclaim 推过认证空洞：凭据是丢失的裁定，不是已投递的证据")
	require.Equal(t, uint64(100), led.Get(key).Positions.Reclaim)

	// 且恢复采集照旧放行（凭据真正的消费者是它，不是回收）。
	require.NoError(t, led.ResumeAcquire(key))
}

// 同次裁定的离散空洞不得生成凸包，否则夹间健康位置会被冒充永久丢失。
func TestAbandonGapsKeepsDisjointEvidence(t *testing.T) {
	led, key := abandonFixture(t, "abandon-disjoint")
	require.NoError(t, led.RecordGap(key, 300, 350, GapReasonAppendRejected, "第二个洞"))
	count, err := led.AbandonGapsThrough(key, 400, GapAbandonment{Operator: "ops@example", Detail: "物理损坏", AtUTC: "2026-10-08T00:00:00Z"})
	require.NoError(t, err)
	require.Equal(t, 2, count)
	entry := led.Get(key)
	require.Len(t, entry.Abandonments, 2)
	for i, want := range []PositionRange{{From: 100, To: 150}, {From: 300, To: 350}} {
		ab := entry.Abandonments[i]
		require.Equal(t, want.From, ab.From)
		require.Equal(t, want.To, ab.To)
		require.Equal(t, "ops@example", ab.Operator)
		require.Equal(t, "2026-10-08T00:00:00Z", ab.AtUTC)
		require.Equal(t, uint64(400), ab.Through)
		require.Equal(t, GapReasonPermanentlyLost, ab.ReasonCode)
		require.Equal(t, "物理损坏", ab.Detail)
	}
	_, covered := led.AbandonmentsCovering(key, 200)
	require.False(t, covered, "夹间健康位置不能成为放弃凭据")
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

// 回归（LEDGER-2，2026-10-12 终审修复）：折叠（超上限并成凸包）过的缺口被人工放弃时，
// 凭据必须按**精确子区间**逐段落，绝不能用凸包把夹间健康位置谎报成「永久丢失」。
//
// 现场形态：单源同因未解决缺口 >64 条时，foldUnresolvedGapsLocked 把最旧一批并成**一条凸包**
// （只扩大区间、保守方向，对「可能未落库」判据是对的）。但 AbandonGapsThrough 会按缺口区间逐字
// 落 PERMANENTLY_LOST 凭据 ⇒ 直接抄凸包就把受害者之间的健康字节也登记成永久丢失。凭据比缺口
// 活得久（缺口会被 trimResolvedGapsLocked 裁掉）⇒ 误报长期留存，运维据此可能重复补账。
//
// 为什么既有的 TestAbandonGapsKeepsDisjointEvidence 抓不到：那条用例的离散空洞是**同一格
// Gaps 数组内的两条独立条目**（由 6b9d6b62 修复覆盖），而本用例的凸包是**折叠**产生的单条
// 条目（FoldSpans 为空时它自称的区间就是凸包）。
//
// 转红方式（实测）：把 AbandonGapsThrough 的 `gapEvidenceSpans(*gap)` 换回
// `[]{PositionRange{{From: gap.StartPos, To: gap.EndPos}}}`（即抄凸包，还原旧行为）
// ⇒ 断言②（健康位置不得被凭据覆盖）立即红。
func TestAbandonAfterFoldKeepsPreciseSpansNotHull(t *testing.T) {
	led := New()
	key := keyOf("fold-abandon", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})

	// 造 100 条互不相邻的同因缺口（各 10 字节，间隔 100 字节健康区）⇒ 触发折叠。
	// 位置区间：第 i 条 = [10+i*110, 19+i*110]，则前 100 条的凸包必然跨越大量健康位置。
	const victimCount = 100
	const stride = 110
	for i := 0; i < victimCount; i++ {
		start := uint64(10 + i*stride)
		require.NoError(t, led.RecordGap(key, start, start+9, GapReasonAppendRejected, "折叠夹具"))
	}
	require.NoError(t, led.AdvanceRead(key, 20000))
	require.NoError(t, led.AdvanceDurable(key, 20000))

	entry := led.Get(key)
	require.LessOrEqual(t, len(entry.Gaps), DefaultMaxUnresolvedGapsPerSource+1,
		"前置：未解决缺口必须已被折叠回上限附近")
	// 找到那条折叠出来的凸包条目（FoldSpans 非空）。
	var folded *Gap
	for i := range entry.Gaps {
		if len(entry.Gaps[i].FoldSpans) > 0 {
			folded = &entry.Gaps[i]
			break
		}
	}
	require.NotNil(t, folded, "前置：应存在一条由折叠产生的缺口（FoldSpans 非空）")
	require.Greater(t, len(folded.FoldSpans), 1, "前置：折叠条目的子区间应多于一条")

	// 关键几何量：凸包跨度 vs 单段跨度。
	// 折叠场景下凭据**必然被裁剪**（凭据上限 64；触发折叠时凭据总数 = 缺口数 > 64，
	// mergeAndTrimAbandonmentsLocked 保留最新的 64 条），故凸包的绝大多数段会被裁掉。
	// 这使「凭据是否抄了凸包」有一个无歧义的判据：**任何单条凭据的跨度都不得超过单段跨度**。
	// 旧实现恰好留下一条跨度 = 整个凸包（3969 字节）的凭据 ⇒ 本断言转红。
	hullSpan := folded.EndPos - folded.StartPos
	var maxSpan uint64
	for _, sp := range folded.FoldSpans {
		if d := sp.To - sp.From; d > maxSpan {
			maxSpan = d
		}
	}
	require.Greater(t, hullSpan, maxSpan,
		"前置：凸包跨度必须显著大于单段跨度（否则本用例无判别力）")

	_, err := led.AbandonGapsThrough(key, 20000, GapAbandonment{Operator: "ops@example", Detail: "折叠后放弃"})
	require.NoError(t, err)

	// ① 核心断言：凭据跨度必须与**单段**同量级，绝不得出现一条横跨整个凸包的凭据。
	for _, ab := range led.Get(key).Abandonments {
		require.LessOrEqual(t, ab.To-ab.From, maxSpan,
			"凭据 [%d,%d] 跨度 %d 超过单段跨度 %d ⇒ 抄用了折叠凸包（把夹间健康位置谎报成永久丢失）",
			ab.From, ab.To, ab.To-ab.From, maxSpan)
	}

	// ② 更直观的等价断言：凸包内部任意一处健康位置都不得被凭据覆盖。
	// 取倒数第二段的尾部健康位（它紧邻最后一段，且不被任何合法凭据覆盖）。
	require.GreaterOrEqual(t, len(folded.FoldSpans), 2)
	secondLast := folded.FoldSpans[len(folded.FoldSpans)-2]
	healthPos := secondLast.To + 50
	require.Less(t, healthPos, folded.EndPos, "前置：healthPos 必须落在凸包区间内部")
	_, coveredHealth := led.AbandonmentsCovering(key, healthPos)
	require.False(t, coveredHealth,
		"折叠凸包内的夹间健康位置不得被登记为永久丢失（凭据必须按 FoldSpans 精确展开）")

	// ③ 凭据必须精确覆盖真实空洞段：取最後一段（裁剪后必然幸存的那段）验证正向可达。
	lastSpan := folded.FoldSpans[len(folded.FoldSpans)-1]
	_, coveredReal := led.AbandonmentsCovering(key, lastSpan.From)
	require.True(t, coveredReal, "真实空洞位置必须有放弃凭据（末段 [%d,%d]）", lastSpan.From, lastSpan.To)
}

// TestTryReclaimNoOpWhenAlreadyReclaimed 锁定「已回收齐平读水位 ≠ 存在覆盖洞」这条判据。
//
// 2026-10-20 真机验收修复：Reclaim 追平 **Read**（读入的全部回收完）后，旧实现靠恢复分段
// 盖住这一段；分段被消费/裁剪后「无段覆盖」是常态。修前这里返回哨兵 ErrReclaimNoSegment，
// 令 runtime 的 repairStalledReclaim 对空区间反复补段 —— 补不出覆盖分段 ⇒ 哨兵每轮重现
// ⇒ 无进展自旋（生产 inst:317 实测约 34 次/分、位置三轮不变、空闲 CPU 18.9%，
// 并每轮刷一条 WARN 淹没真实信号）。
//
// ## 2026-10-29 重写（哨兵本身已删）
//
// 新语义下「无事可做」**恒为** `(当前Reclaim, nil)`，不再有可分类的哨兵——于是本用例的关切
// 从「不能报哨兵」升级为「**幂等且不推进**」，并且多了一条旧版没有的对照：
//
//   - 已齐平：重复调用仍返回同一位置且 nil；
//   - 有未回收前缀时：推进到 min(认证覆盖, max(read,durable))，而不是报错。
//
// 后者的实际意义：旧实现靠哨兵告诉调用方「这里推不动」，现在推不动只有两种形态——
// 认证覆盖没接上（停住，不报错）或读入量没跟上（停住，不报错）。两者都不应被调用方
// 误读为「需要补段」（补段链已不存在）。
//
// 转红方式（实测）：把 tryReclaimLocked 的 `target <= Reclaim` 提前返回删掉（让它
// 在无事可做时也重算并覆写 Reclaim），重复调用断言仍可能绿，但把 advanceDeliveryEnd 的
// max 去掉（让 Delivery 可回退）⇒ 本用例的幂等断言立即红。
func TestTryReclaimNoOpWhenAlreadyReclaimed(t *testing.T) {
	led := New()
	key := keyOf("already-reclaimed", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "already-reclaimed", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 100))
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	// 先让 Reclaim 真正追平 Read。
	if _, err := led.TryReclaim(key); err != nil {
		t.Fatalf("前置：首次回收应放行，实测 %v", err)
	}
	pre, ok := led.Positions(key)
	require.True(t, ok)
	require.Equal(t, pre.Read, pre.Reclaim, "前置：Reclaim 必须已追平 Read（%d vs %d）", pre.Reclaim, pre.Read)

	// 被测点：无待回收前缀 ⇒ 幂等成功，位置不变。
	pos, err := led.TryReclaim(key)
	require.NoError(t, err, "Reclaim 已追平 Read 时不得报错（旧哨兵已删，调用方也不会再补段）")
	require.Equal(t, pre.Reclaim, pos, "无事可做时回收位置不得变化")

	// 幂等：重复调用仍须成功且不推进。
	for i := 0; i < 3; i++ {
		pos, err = led.TryReclaim(key)
		require.NoError(t, err, "第 %d 次重复调用仍须是幂等无操作", i+2)
		require.Equal(t, pre.Reclaim, pos)
	}

	// 对照：出现未回收前缀时，推进到具体位置（而不是报错）。
	require.NoError(t, led.AdvanceRead(key, 250))
	require.NoError(t, led.AdvanceDurable(key, 250))
	require.NoError(t, led.RecordDelivery(key, 100, 250, logtypes.DeliveryRequestDone))
	pos, err = led.TryReclaim(key)
	require.NoError(t, err, "存在未回收前缀时新语义推进到具体位置，不再返回哨兵")
	require.Equal(t, uint64(250), pos)
	require.Equal(t, uint64(250), led.Get(key).Positions.Reclaim)
}
