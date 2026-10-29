package ledger

// 零宽归档缺口（`ARCHIVE_CONTENT_UNKNOWN`，区间恒为 [p,p]）的回归（2026-10-29）。
//
// # 生产取证（不要重新验证，这是写本文件的出发点）
//
// 生产 `ingest.index.db` 里 19 个源共有 **253 条零宽未解决缺口**（`StartPos == EndPos`），
// 原因全是 `ARCHIVE_CONTENT_UNKNOWN`，位置恰好落在归档文件边界（17718、18301、25012、
// 25595、26178…）。它们最早 21 小时前、最新 4.2 小时前登记——**全都过了 1h 宽限期**。
//
// 于是「这些缺口现在到底还阻不阻塞 `ResumeAcquire`」成了必须被测试钉死的问题：
// 生产的诊断日志报 `blocking=12`，但同一批源的 `acquire_paused=0`。两个读数不可能同时为真，
// 说明至少有一处口径是错的（观测面的根因见交付报告，不在本文件里断言）。
//
// # 本文件钉住的四条语义
//
//  1. 零宽 `ARCHIVE_CONTENT_UNKNOWN` 过了宽限期**必须不阻塞**——否则 19 个源会被一段
//     「永远补不回来的历史字节」永久钉死在暂停态（这正是 2026-10-28 C 案要修的形态）。
//  2. 宽限期**内**必须仍然阻塞——保守方向不能被顺手放宽（否则等于静默放行）。
//  3. 零宽缺口**绝不能**被当成「已覆盖」：它不得让未解决计数变少、不得凭空产生投递证据
//     （`REQUEST_DONE` 只能来自真实 2xx）、不得让回收前进。这是安全红线，比口径问题更重要。
//  4. 零宽缺口参与合并/折叠后，**放弃裁定**必须拿到精确证据：区间被拉宽可以（保守方向），
//     但拉宽后的凸包绝不能被写成「永久丢失」凭据（LEDGER-2）。

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// zeroWidthArchiveContentUnknown 是生产账本里那 253 条缺口的实际原因串。
//
// 这里用本地常量而不是 gap_bounds.go 的导出常量：ledger 包内该原因是**字面量**
// （见 gapReasonsPermanentlyLost 的说明——acquire → ledger 的依赖方向决定了 ledger
// 不能反向依赖 acquire 的常量）。测试照抄生产字面量，才能守住「逐字相同」这条判据。
const zeroWidthArchiveContentUnknown = "ARCHIVE_CONTENT_UNKNOWN"

// zeroWidthTestBase 是注入时钟的固定「现在」。
//
// 为什么必须注入：宽限期判据读 `FirstSeenUnix` 与 `nowUnix()` 两个时间点，用真实时钟
// 就只能靠"把登记时间推到过去"来模拟超期（见 resume_criteria_test.go 的 forceGapExpired）。
// 那样断言的是"两个时间点相减"的算术，而这里要断言的是**边界**（差 1 秒 / 恰好 1 小时），
// 只有两侧都可控才写得准。
const zeroWidthTestBase int64 = 1_760_000_000

// zeroWidthFixture 造一个「已暂停 + 注入固定时钟」的源，与生产的时序形态一致：
// 源先因容量/归档问题被暂停，缺口在暂停期间登记，之后靠 ResumeAcquire 判定能否复活。
func zeroWidthFixture(t *testing.T, id string) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := keyOf(id, "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "g1"})
	led.SetClock(func() int64 { return zeroWidthTestBase })
	require.NoError(t, led.PauseAcquire(key, "wal backlog limit exceeded (test)"))
	return led, key
}

// zeroWidthGapOf 返回该源唯一的那条缺口（并断言确实只有一条、且确实是零宽）。
func zeroWidthGapOf(t *testing.T, led *Ledger, key SourceKey) Gap {
	t.Helper()
	entry := led.Get(key)
	require.NotNil(t, entry)
	require.Len(t, entry.Gaps, 1, "夹具应恰好只有一条缺口")
	gap := entry.Gaps[0]
	require.Equal(t, gap.StartPos, gap.EndPos,
		"生产形态是零宽（StartPos == EndPos），本用例的前提不能跑偏 ✓")
	return gap
}

// TestZeroWidthArchiveUnknownGapUnblocksAfterGrace 钉住语义 1（生产 253 条缺口的出路）。
//
// 判据（ledger.go:1561-1566）：
//
//	IsGapReasonPermanentlyLost(reason) && 已过宽限期 ⇒ continue（不阻塞）
//
// 转红方式（实测见交付报告）：
//   - 从 gapReasonsPermanentlyLost 删掉 "ARCHIVE_CONTENT_UNKNOWN" ⇒ 落到 default-deny ⇒ 红；
//   - 把 permanentlyLostGapExpiredLocked 的 `>=` 改成 `>` ⇒ 恰好 1h 的那条断言红。
func TestZeroWidthArchiveUnknownGapUnblocksAfterGrace(t *testing.T) {
	led, key := zeroWidthFixture(t, "inst:zw-unblock")
	require.NoError(t, led.RecordGap(key, 17718, 17718, zeroWidthArchiveContentUnknown,
		`archive content unknown path="/var/log/a/latest.log"`))

	gap := zeroWidthGapOf(t, led, key)
	require.False(t, gap.Resolved, "刚登记时不得是已解决态")
	require.Equal(t, zeroWidthTestBase, gap.FirstSeenUnix,
		"FirstSeenUnix 必须取注入时钟（否则宽限期判据不可精确推进）✓")

	graceSecs := int64(DefaultPermanentlyLostGapGrace / time.Second)

	// 差 1 秒未到期：必须仍然阻塞。这条断言守住「宽限期不得被悄悄缩短」——
	// 缩短宽限期的效果就是让告警窗口消失，即静默放行。
	led.SetClock(func() int64 { return zeroWidthTestBase + graceSecs - 1 })
	err := led.ResumeAcquire(key)
	require.Error(t, err, "宽限期差 1 秒仍必须阻塞（告警窗口不得被悄悄缩短）✓")
	require.Contains(t, err.Error(), "permanently-lost")
	require.Contains(t, err.Error(), zeroWidthArchiveContentUnknown, "错误串必须点名原因 ✓")

	// 恰好到期：必须放行（生产那 253 条缺口的位置就在这条线上）。
	led.SetClock(func() int64 { return zeroWidthTestBase + graceSecs })
	require.NoError(t, led.ResumeAcquire(key),
		"零宽归档缺口超过宽限期后必须放行——拦着也等不到那段字节，却会赔上之后所有新日志 ✓")

	st, ok := led.AcquireState(key)
	require.True(t, ok)
	require.False(t, st.Paused, "必须真的恢复（暂停态要被清掉）✓")
	require.Empty(t, st.PauseReason)

	// 不阻塞 ≠ 消解：缺口必须仍然可见（运维/告警还要能查到它）。
	require.Equal(t, 1, UnresolvedGapCountOf(led.Get(key)),
		"放行后缺口仍必须留在账本里（可见性一点不少）✓")
}

// TestZeroWidthArchiveUnknownGapBlocksWithinGrace 钉住语义 2（保守方向不能被误放宽）。
//
// 与上一条是同一判据的另一半：宽限期内它**必须**继续阻塞，否则「先告警再放行」
// 就退化成「静默放行」，而这类缺口代表的正是「这段字节我们证明不了」。
func TestZeroWidthArchiveUnknownGapBlocksWithinGrace(t *testing.T) {
	led, key := zeroWidthFixture(t, "inst:zw-block")
	require.NoError(t, led.RecordGap(key, 18301, 18301, zeroWidthArchiveContentUnknown,
		`archive content unknown path="/var/log/a/latest.log.1"`))
	zeroWidthGapOf(t, led, key)

	// 立刻（now == FirstSeen）调 ResumeAcquire。
	err := led.ResumeAcquire(key)
	require.Error(t, err, "宽限期内必须阻塞（否则等于静默放行）✓")
	require.Contains(t, err.Error(), "permanently-lost",
		"错误串必须带 permanently-lost 分类，运维据此区分「等告警」与「等回读」✓")
	require.Contains(t, err.Error(), zeroWidthArchiveContentUnknown)

	st, ok := led.AcquireState(key)
	require.True(t, ok)
	require.True(t, st.Paused, "被拦时不得清除暂停态 ✓")

	entry := led.Get(key)
	require.Equal(t, 1, UnresolvedGapCountOf(entry), "被拦时缺口仍是未解决 ✓")
	require.False(t, entry.Gaps[0].Resolved, "被拦时缺口不得被顺手标成已解决 ✓")
}

// TestZeroWidthGapIsNeverTreatedAsCovered 钉住语义 3（安全红线，比口径问题更重要）。
//
// 零宽缺口「不覆盖任何字节」这件事很容易被顺手用来做两件**错误**的优化：
//
//   - 认为它不算缺口 ⇒ 未解决计数/阻塞判定把它跳过。后果：一条「证明不了归属」的
//     诊断记录被静默抹平，运维再也查不到它（可见性丢失 ✗）。
//   - 认为它已被别处的成功投递覆盖 ⇒ 让它产出投递证据或让回收越过它。后果更重：
//     `REQUEST_DONE` 是「确实拿到 2xx 且逐字段校验通过」的唯一凭据，一个缺口凭空造出
//     这段证据，就等于把「可能没落库」改写成「确认落库」（缺口语义红线 ✗✗）。
//
// 转红方式（实测见交付报告）：让 countUnresolved 跳过零宽缺口
// （`if gap.EndPos == gap.StartPos { continue }`）⇒ 第一条未解决计数断言立刻红。
func TestZeroWidthGapIsNeverTreatedAsCovered(t *testing.T) {
	const at = uint64(25012) // 生产取证里的一个归档边界位置
	led, key := zeroWidthFixture(t, "inst:zw-not-covered")

	// 夹具前提：读/耐久水位都已越过该位置，且**没有任何** REQUEST_DONE 证据。
	// 这正是「已读进来、尚未确认落库」的形态（生产里归档边界的缺口就是这个位置）。
	require.NoError(t, led.AdvanceRead(key, at))
	require.NoError(t, led.AdvanceDurable(key, at))
	before := led.Get(key)
	require.Empty(t, RequestDoneRangesOf(before), "夹具前提：此刻没有 REQUEST_DONE 证据")
	require.Empty(t, DeliveryEvidenceOf(before), "夹具前提：此刻没有投递证据")

	require.NoError(t, led.RecordGap(key, at, at, zeroWidthArchiveContentUnknown,
		`archive content unknown path="/var/log/a/2026-10-27-1.log.gz"`))
	after := led.Get(key)

	// ① 它仍是**一条未解决缺口**（零宽 ≠ 已覆盖 ≠ 不算缺口）。
	require.Equal(t, 1, UnresolvedGapCountOf(after),
		"零宽缺口必须照旧计入未解决条数（不阻塞 ≠ 不算缺口）✓")
	require.Len(t, after.Gaps, 1)
	require.False(t, after.Gaps[0].Resolved)
	require.Equal(t, at, after.Gaps[0].StartPos, "位置必须逐字保留（运维据此定位归档边界）✓")
	require.Equal(t, after.Gaps[0].StartPos, after.Gaps[0].EndPos, "不得被登记路径拉宽 ✓")

	// ② 它不得凭空产生**任何**投递证据，也不得改动任何水位。
	require.Empty(t, RequestDoneRangesOf(after), "零宽缺口不得多出 REQUEST_DONE 区间 ✓")
	require.Equal(t, RequestDoneRangesOf(before), RequestDoneRangesOf(after))
	require.Equal(t, DeliveryEvidenceOf(before), DeliveryEvidenceOf(after))
	require.Equal(t, before.Positions, after.Positions,
		"登记缺口不是投递结果：read/durable/delivery/reclaim 一个都不许被它推动 ✓")

	// ③ 回收只信 REQUEST_DONE 的连续覆盖 ⇒ 它不能借「缺口 + 已读水位」把回收放过去。
	require.Equal(t, uint64(0), ContiguousDeliveryEnd(after.DeliveryBatches, 0))
	reclaim, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Zero(t, reclaim, "没有 REQUEST_DONE 证据时，回收不得因缺口或读水位而前进 ✓")
	require.Equal(t, uint64(0), led.Get(key).Positions.Reclaim)

	// ④ 归因：它属于「已确认丢失」类——不是「回读可愈合」（放行不靠等待回读），
	// 也不是「容量处置」（不是系统主动丢弃）。三类的处置方向完全不同，混用会改变阻塞语义。
	require.True(t, IsGapReasonPermanentlyLost(zeroWidthArchiveContentUnknown))
	require.False(t, IsGapReasonHealableOnReResume(zeroWidthArchiveContentUnknown),
		"归档内容证明不了 ⇒ 回读造不出这段字节，绝不能进可愈合白名单 ✓")
	require.False(t, IsGapReasonNonBlocking(zeroWidthArchiveContentUnknown),
		"它仍需 1h 告警窗口，不得被划进「从第一秒起就不阻塞」的容量处置类 ✓")
}

// TestZeroWidthGapMergingKeepsExactZeroWidthEvidence 钉住语义 4 的前半：
// **反复上报同一个零宽缺口不得把它拉宽**，而且放弃凭据也必须保持零宽。
//
// 为什么这条重要：生产上同一个归档边界会被反复上报（每次重跑归档发现都会重记一次）。
// 若合并把区间拉宽 1 字节，`AbandonGapsThrough` 就会把那条相邻的**健康字节**写成
// 「永久丢失」凭据——而凭据比缺口活得久（缺口会被裁剪），误报会长期留存在审计链里，
// 运维据此可能重复补账。零宽缺口的价值就在于它的证据区间是精确的。
func TestZeroWidthGapMergingKeepsExactZeroWidthEvidence(t *testing.T) {
	const pos = uint64(25595) // 生产取证里的一个归档边界位置
	led, key := zeroWidthFixture(t, "inst:zw-merge")

	// 同因、同位置的重复上报（生产形态：每次归档发现失败都会重记一次）。
	for i := 0; i < 5; i++ {
		require.NoError(t, led.RecordGap(key, pos, pos, zeroWidthArchiveContentUnknown,
			fmt.Sprintf(`archive content unknown path="/var/log/a/latest.log" attempt=%d`, i)))
	}

	entry := led.Get(key)
	require.Len(t, entry.Gaps, 1, "同因同位置必须并成一条（否则条数随故障规模线性增长）✓")
	require.Equal(t, pos, entry.Gaps[0].StartPos)
	require.Equal(t, pos, entry.Gaps[0].EndPos,
		"反复上报零宽缺口不得把它拉宽——拉宽就等于凭空覆盖健康字节 ✓")
	require.Greater(t, led.GapObservability(key).MergedTotal, uint64(0),
		"合并计数必须可见（条数不再等于失败次数，运维要能读出真实规模）✓")

	// 零宽缺口的放弃凭据也必须是零宽的精确区间。
	resolved, err := led.AbandonGapsThrough(key, pos, GapAbandonment{
		Operator: "ops@example", Detail: "归档文件已删除，确认不再补齐",
	})
	require.NoError(t, err, "零宽且未折叠的缺口有精确证据 ⇒ 放弃必须被允许 ✓")
	require.Equal(t, 1, resolved)

	abs := led.Get(key).Abandonments
	require.Len(t, abs, 1)
	require.Equal(t, pos, abs[0].From)
	require.Equal(t, pos, abs[0].To,
		"放弃凭据的区间必须逐字等于零宽缺口，不得覆盖夹间健康位置 ✓")
}

// TestZeroWidthGapFoldingRefusesHullAbandonment 钉住语义 4 的后半：
// 零宽缺口被**折叠**（同因、条数越界）后，一旦 `FoldSpanMerges > 0`（精确子区间因预算
// 耗尽被并成凸包），`AbandonGapsThrough` 必须**拒绝**并报 `lacks exact evidence`。
//
// 为什么必须拒绝：折叠出的条目是**多个离散位置的凸包**，它跨越的那段区间里大部分是健康
// 位置。按凸包写凭据 = 把健康字节谎报成永久丢失，而凭据活得比缺口长（LEDGER-2）。
//
// 构造说明（为什么用"离散零宽 + 洪水"）：折叠只在**同因**未解决缺口超过
// DefaultMaxUnresolvedGapsPerSource 时发生，且每次折叠把最旧的若干条并进同一条
// （FoldSpans 逐次增长）；只有子区间数超过 DefaultMaxFoldSpansPerGap 时才会触发
// capFoldSpans 的凸包退化，即 FoldSpanMerges > 0。故本用例按生产形态逐条登记
// **位置离散的零宽**缺口（i*100，互不相接 ⇒ 不会被 recordGapLocked 合并），
// 直到折叠发生两次以上。
//
// 转红方式（实测见交付报告）：删掉 AbandonGapsThrough 开头的整批预检
// （`if !gap.Resolved && gap.EndPos <= position && (FoldSpansUnknown || FoldSpanMerges > 0)`）
// ⇒ 本用例「必须拒绝」的断言红。
func TestZeroWidthGapFoldingRefusesHullAbandonment(t *testing.T) {
	const first = uint64(17718) // 最旧的那条零宽缺口（生产取证的第一个边界）
	led, key := zeroWidthFixture(t, "inst:zw-fold")

	require.NoError(t, led.RecordGap(key, first, first, zeroWidthArchiveContentUnknown,
		`archive content unknown path="/var/log/a/latest.log"`))
	zeroWidthGapOf(t, led, key)

	// 洪水：同因、位置离散的零宽缺口（每条都代表另一份归档的边界）。
	// 条数取「上限 + 子区间预算 + 余量」，与既有 TestFoldSpanCapCannotCreateHullAbandonmentEvidence
	// 同一构造口径。
	flood := DefaultMaxUnresolvedGapsPerSource + DefaultMaxFoldSpansPerGap + 10
	for i := 0; i < flood; i++ {
		pos := uint64(1000 + i*100)
		require.NoError(t, led.RecordGap(key, pos, pos, zeroWidthArchiveContentUnknown,
			fmt.Sprintf(`archive content unknown path="/var/log/a/%d.log.gz"`, i)))
	}

	entry := led.Get(key)
	require.LessOrEqual(t, len(entry.Gaps), DefaultMaxUnresolvedGapsPerSource+1,
		"折叠后条数必须被上限钉住（内存有界是硬约束）✓")

	// 找到折叠出来的那条。折叠的载体是**最早插入**的那个受害者（占用它原来的位置索引），
	// 而它的 StartPos/EndPos 会立刻被拉宽成受害者集合的凸包 ⇒ 无法再按位置认它，
	// 只能按「FoldSpans 非空」+「Detail 保留了它自己当初的 path」来认。
	var folded *Gap
	for i := range entry.Gaps {
		if len(entry.Gaps[i].FoldSpans) > 0 {
			folded = &entry.Gaps[i]
			break
		}
	}
	require.NotNil(t, folded, "洪水规模应已触发折叠（否则本用例测不到语义 4）✓")
	require.False(t, folded.Resolved, "折叠不得把缺口标成已解决 ✓")
	require.Contains(t, folded.Detail, `path="/var/log/a/latest.log"`,
		"折叠条的 Detail 保留首条原文 ⇒ 那条零宽缺口确实成了载体 ✓")

	// **这就是语义 4 的前提**：一条零宽 [17718,17718] 的缺口，折叠后区间被拉宽成
	// 跨越上千条离散缺陷的凸包（实测 [1000,17718]），而凸包内部绝大部分是健康位置。
	require.Less(t, folded.StartPos, first,
		"凸包会把更早的其它受害者一并覆盖（区间只扩大不缩小）✓")
	require.Greater(t, folded.EndPos, folded.StartPos, "折叠后区间不再是零宽 ✓")

	// 精度子区间已因预算耗尽被并成凸包 ⇒ FoldSpanMerges>0 且 Unknown 同步置位。
	// 这条是**本用例能测到 LEDGER-2 防线的前提**：只有确实退化了，下面的拒绝才有对象。
	require.Greater(t, folded.FoldSpanMerges, 0,
		"洪水规模应已耗尽精确子区间预算（否则本用例测不到凸包退化）✓")
	require.True(t, folded.FoldSpansUnknown,
		"精度退化必须同步置 FoldSpansUnknown（两者不得只写其一）✓")
	require.Empty(t, gapEvidenceSpans(*folded),
		"精度退化后放弃凭据无从展开 ⇒ 证据集必须为空（不得退回凸包）✓")

	// 核心断言：凸包不得被写成永久丢失凭据。
	resolved, err := led.AbandonGapsThrough(key, uint64(1)<<40, GapAbandonment{Operator: "ops@example"})
	require.Error(t, err, "折叠精度已丢失时放弃必须被拒绝（凸包不是证据）✓")
	require.Contains(t, err.Error(), "lacks exact evidence")
	require.Zero(t, resolved, "拒绝时不得返回已裁定条数 ✓")

	// 整批预检：拒绝后不得留下任何部分成功（缺口状态与凭据都不许动）。
	constraints := led.Get(key)
	for i := range constraints.Gaps {
		require.False(t, constraints.Gaps[i].Resolved,
			"被拒绝的整批裁定不得留下已解决缺口 ✓")
	}
	require.Empty(t, constraints.Abandonments, "被拒绝的整批裁定不得留下凭据 ✓")
}
