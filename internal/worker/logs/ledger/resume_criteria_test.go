package ledger

// ResumeAcquire 阻塞判据的回归（2026-10-28，对齐 Filebeat / Fluent Bit）。
//
// 它守的是三条不得退让的性质：
//   1. **能救回来**的缺口（回读可愈合类）仍然**阻塞**恢复——放行就真丢数据；
//   2. **已确认丢失**的历史缺口（永久不可恢复类）**不阻塞**——拦着也等不到；
//   3. 两个名单**不得重叠**（重叠会让同一个原因既阻塞又放行，行为不确定）。
//
// 另：未被归类的未知原因必须继续 default-deny（不得因为本次修改而放行）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func newResumeLedger(t *testing.T) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := SourceKey{LogSourceID: "inst:resume", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	require.NoError(t, led.PauseAcquire(key, "wal backlog limit exceeded (test)"))
	return led, key
}

// TestResumeAcquireBlocksOnHealableGap 守住第 1 条：
// 暂停期从未进 WAL 的批（APPEND_REJECTED）必须等回读重放 ⇒ 仍阻塞。
func TestResumeAcquireBlocksOnHealableGap(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 10, 20, GapReasonAppendRejected, "never entered WAL"))

	err := led.ResumeAcquire(key)
	require.Error(t, err, "能救回来的缺口必须继续阻塞 ✓")
	require.Contains(t, err.Error(), "re-read-healable")

	st, ok := led.AcquireState(key)
	require.True(t, ok)
	require.True(t, st.Paused, "被拦时不得清除暂停态 ✓")
}

// TestResumeAcquireBlocksOnDeliverError 同样属于可愈合类：投递失败可以重投。
func TestResumeAcquireBlocksOnDeliverError(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 10, 20, GapReasonDeliverError, "VL write failed"))

	require.Error(t, led.ResumeAcquire(key), "DELIVER_ERROR 属于可重投类，必须继续阻塞 ✓")
}

// TestResumeAcquireAllowsPermanentlyLostGap 守住第 2 条（本次修改的核心）：
// 已确认丢失的历史字节，**过了宽限期**后不得再阻塞采集。
func TestResumeAcquireAllowsPermanentlyLostGap(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 100, 200, GapReasonWALBodyMissing, "body gone"))

	// 新登记的缺口：先验“宽限期内仍阻塞”（保留旧契约“先告警”的意图）。
	// 注意：本用例的时钟是真实时钟，新缺口 FirstSeenUnix ≈ now ⇒ 未超时。
	require.Error(t, led.ResumeAcquire(key), "宽限期内仍应阻塞（保留告警窗口）✓")

	// 把缺口时间推到“很久以前”（模拟超时降级）。
	forceGapExpired(t, led, key)

	require.NoError(t, led.ResumeAcquire(key), "超过宽限期后必须放行（对齐业界：不永久停采）✓")

	st, _ := led.AcquireState(key)
	require.False(t, st.Paused, "必须真的恢复 ✓")

	// **关键：缺口仍必须可见**（不阻塞 ≠ 消解）。
	entry := led.Get(key)
	require.Equal(t, 1, UnresolvedGapCountOf(entry),
		"缺口必须仍在账本里（不阻塞不等于消解）✓")
}

// TestResumeAcquireAllowsArchiveUnknownGap 同上，覆盖归档类。
func TestResumeAcquireAllowsArchiveUnknownGap(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 300, 300, "ARCHIVE_CONTENT_UNKNOWN", "legacy quarantine"))

	require.Error(t, led.ResumeAcquire(key), "宽限期内仍应阻塞 ✓")
	forceGapExpired(t, led, key)
	require.NoError(t, led.ResumeAcquire(key), "超期后不得阻塞采集 ✓")
	require.Equal(t, 1, UnresolvedGapCountOf(led.Get(key)), "缺口仍需可见 ✓")
}

// TestResumeAcquireBlocksPermanentlyLostWithinGrace 守住 C 案的“先告警”一半：
// 刚登记的永久丢失缺口**必须**仍然阻塞，给告警留出被看到的窗口。
func TestResumeAcquireBlocksPermanentlyLostWithinGrace(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 100, 200, GapReasonWALBodyMissing, "fresh"))

	err := led.ResumeAcquire(key)
	require.Error(t, err, "宽限期内必须阻塞（否则等于静默放行）✓")
	require.Contains(t, err.Error(), "within grace")
}

// TestResumeAcquireTreatsLegacyZeroTimestampAsExpired 守住旧数据兼容：
// 存量缺口没有 FirstSeenUnix（零值）⇒ 视为已超时，不能让它白等一小时。
func TestResumeAcquireTreatsLegacyZeroTimestampAsExpired(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 100, 200, GapReasonWALBodyMissing, "legacy"))
	zeroGapTimestamps(t, led, key)

	require.NoError(t, led.ResumeAcquire(key),
		"零值时间戳（旧账本）必须直接视为已超时 ✓")
}

// TestPermanentlyLostGraceIsConfigurable 守住“阈值可配”。
func TestPermanentlyLostGraceIsConfigurable(t *testing.T) {
	led, key := newResumeLedger(t)
	require.Equal(t, DefaultPermanentlyLostGapGrace, led.PermanentlyLostGapGrace(), "默认 1 小时")

	led.SetPermanentlyLostGapGrace(10 * time.Minute)
	require.Equal(t, 10*time.Minute, led.PermanentlyLostGapGrace())

	// 阈值调小后，刚登记的缺口仍不应立即放行（FirstSeen 是 now）。
	require.NoError(t, led.RecordGap(key, 1, 2, GapReasonWALBodyMissing, "x"))
	require.Error(t, led.ResumeAcquire(key), "刚登记的不应超时 ✓")

	// 0 或负值 ⇒ 回退默认。
	led.SetPermanentlyLostGapGrace(0)
	require.Equal(t, DefaultPermanentlyLostGapGrace, led.PermanentlyLostGapGrace())
}

// forceGapExpired 把所有缺口的 FirstSeenUnix 推到宽限期之外。
//
// 用公开的 SetClock 做不到这一点（它影响的是“现在”，而我们要动“登记时间”），
// 所以直接通过 SetClock 把时钟拨到未来即可——这与“时间过去了”等价。
func forceGapExpired(t *testing.T, led *Ledger, key SourceKey) {
	t.Helper()
	led.SetClock(func() int64 { return time.Now().Add(2 * DefaultPermanentlyLostGapGrace).Unix() })
	t.Cleanup(func() { led.SetClock(func() int64 { return time.Now().Unix() }) })
}

// zeroGapTimestamps 把缺口的 FirstSeenUnix 清零（模拟旧账本）。
func zeroGapTimestamps(t *testing.T, led *Ledger, key SourceKey) {
	t.Helper()
	led.mu.Lock()
	e := led.entries[key]
	require.NotNil(t, e)
	for i := range e.Gaps {
		e.Gaps[i].FirstSeenUnix = 0
	}
	led.mu.Unlock()
}

// TestResumeAcquireDefaultDeniesUnknownReason 守住第 3 条的基础：
// 未归类的原因必须继续拒绝（default-deny 未动）。
func TestResumeAcquireDefaultDeniesUnknownReason(t *testing.T) {
	led, key := newResumeLedger(t)
	require.NoError(t, led.RecordGap(key, 1, 2, "SOME_FUTURE_REASON", "unknown"))

	err := led.ResumeAcquire(key)
	require.Error(t, err, "未归类原因必须 default-deny ✓")
	require.Contains(t, err.Error(), "SOME_FUTURE_REASON")
}

// TestGapReasonListsDoNotOverlap 守住第 3 条：两个名单不得重叠。
//
// 重叠的后果：同一原因既“必须等回读”又“视为永久丢失”——
// 判据顺序一变，行为就变，且两个名单的注释都在说相反的话。
func TestGapReasonListsDoNotOverlap(t *testing.T) {
	healable := GapReasonsHealableOnReResume()
	lost := GapReasonsPermanentlyLost()
	for _, h := range healable {
		for _, l := range lost {
			require.NotEqual(t, h, l,
				"原因 %q 同时出现在可愈合名单与永久丢失名单中（方向矛盾）✗", h)
		}
	}
}

// TestPermanentlyLostListIsConservative 守住收录标准：
// 可愈合白名单里的原因绝不得被标为永久丢失（方向一致的第二道保险）。
func TestPermanentlyLostListIsConservative(t *testing.T) {
	for _, reason := range GapReasonsPermanentlyLost() {
		require.False(t, IsGapReasonHealableOnReResume(reason),
			"%q 在可愈合名单里，不得同时标为永久丢失 ✗", reason)
	}
}
