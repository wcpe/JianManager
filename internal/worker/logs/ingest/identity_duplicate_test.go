package ingest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestCanonicalRecoveryEventsAdjudicatesTailPartialDuplicate 验收 #179 身份冲突件（下游处置）：
// 同源、**同记录区间**、异哈希、一方严格更长（轮转尾部残行 vs 重读完整行，现场 947 B vs 1034 B）
// ⇒ 取较长者（完整行）✓；其余形态（异区间/等长）一律维持硬失败（判据不放宽 ✗）。
func TestCanonicalRecoveryEventsAdjudicatesTailPartialDuplicate(t *testing.T) {
	identity := logtypes.SourceIdentity{LogSourceID: "inst:179", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 328911045, End: 328911216}
	partial := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", strings.Repeat("x", 947))
	full := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", strings.Repeat("y", 1034))
	require.NotEmpty(t, partial.EventID)
	require.Equal(t, partial.EventID, full.EventID, "前提：同源同区间 ⇒ 同 event_id ✓")
	require.NotEqual(t, partial.CanonicalHash, full.CanonicalHash, "前提：异哈希 ✓")

	// 残行先入账、完整行后到 ⇒ 取完整行 ✓（并且是**替换**已入账残行，不是追加 ✓）。
	got, err := (&Manager{}).canonicalRecoveryEvents("inst:179/g1", persistedSource{
		Events: []logtypes.Event{partial, full},
	})
	require.NoError(t, err)
	require.Len(t, got, 1, "重复 id 只入账一条 ✓")
	require.Equal(t, full.CanonicalHash, got[0].CanonicalHash, "必须保留较长者（完整行）✓")

	// 反向顺序：完整行先、残行后 ⇒ 仍取完整行 ✓。
	got, err = (&Manager{}).canonicalRecoveryEvents("inst:179/g1", persistedSource{
		Events: []logtypes.Event{full, partial},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, full.CanonicalHash, got[0].CanonicalHash)

	// 边界红①：**异区间**（伪造同 id，模拟历史异常）⇒ 仍硬失败 ✗（判据不放宽）。
	// 注意：正常 id 由（源、区间、解析器版本）哈希而来 ⇒ 异区间天然异 id ✓；此处刻意伪造。
	other := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 328911045, End: 328911217},
		"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", strings.Repeat("z", 1034))
	other.EventID = partial.EventID
	_, err = (&Manager{}).canonicalRecoveryEvents("inst:179/g1", persistedSource{Events: []logtypes.Event{partial, other}})
	require.ErrorContains(t, err, "IDENTITY_CONFLICT")

	// 边界红②：同区间**等长**异哈希 ⇒ 仍硬失败 ✗。
	same := logtypes.BuildEvent(identity, rec, "2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", strings.Repeat("w", 947))
	_, err = (&Manager{}).canonicalRecoveryEvents("inst:179/g1", persistedSource{Events: []logtypes.Event{partial, same}})
	require.ErrorContains(t, err, "IDENTITY_CONFLICT")

	// 同哈希重复 ⇒ 单条入账（既有语义不变 ✓）。
	got, err = (&Manager{}).canonicalRecoveryEvents("inst:179/g1", persistedSource{Events: []logtypes.Event{partial, partial}})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

// TestPlanDeliveryBatchAdjudicatesTailPartialDuplicate 验收 #179 自愈面门闩（投递批规划）：
// 批内/批对既有存储撞上「同区间异哈希」时按较长者（完整行）裁定；不可裁定形态仍硬失败 ✗。
func TestPlanDeliveryBatchAdjudicatesTailPartialDuplicate(t *testing.T) {
	m := &Manager{}
	identity := logtypes.SourceIdentity{LogSourceID: "inst:179", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 328911045, End: 328911216}
	mk := func(message string) logtypes.Event {
		return logtypes.Event{EventID: "same-id", Source: identity, Record: rec,
			IngestTimeUTC: "2026-10-05T00:00:00.000000000Z", Level: "INFO", Stream: "stdout", Message: message}
	}
	partial, full := mk(strings.Repeat("x", 947)), mk(strings.Repeat("y", 1034))

	// ① 批内成对（残行先、完整行后）⇒ 完整行替换入账 ✓（只投一条、且是完整行）。
	got, fail := m.planDeliveryBatchOutsideLock([]logtypes.Event{partial, full}, map[string]eventIdentity{})
	require.Nil(t, fail)
	require.Len(t, got, 1)
	require.Equal(t, len(full.Message), len(got[0].Message))

	// ② 既有存储持残行、批携带完整行 ⇒ 完整行追加投出 ✓（不再硬失败 ⇒ 自愈面可推进）。
	pre := map[string]eventIdentity{"same-id": {hash: "stale", start: rec.Start, end: rec.End, size: 947, idx: -1}}
	got, fail = m.planDeliveryBatchOutsideLock([]logtypes.Event{full}, pre)
	require.Nil(t, fail)
	require.Len(t, got, 1)
	require.Equal(t, len(full.Message), len(got[0].Message))

	// ③ 既有存储持完整行、批携带残行 ⇒ 残行跳过 ✓（不重复投、不硬失败）。
	pre = map[string]eventIdentity{"same-id": {hash: "stale", start: rec.Start, end: rec.End, size: 1034, idx: -1}}
	got, fail = m.planDeliveryBatchOutsideLock([]logtypes.Event{partial}, pre)
	require.Nil(t, fail)
	require.Empty(t, got)

	// ④ 不可裁定（等长异哈希）⇒ 硬失败 ✗（判据不放宽）。
	other := mk(strings.Repeat("z", 947))
	pre = map[string]eventIdentity{"same-id": {hash: "stale", start: rec.Start, end: rec.End, size: 947, idx: -1}}
	_, fail = m.planDeliveryBatchOutsideLock([]logtypes.Event{other}, pre)
	require.NotNil(t, fail)
	require.ErrorContains(t, fail.err, "IDENTITY_CONFLICT")
}

// TestAdjudicatesSameIntervalDayShiftDuplicate 验收 #179 真实形态（fixture 实录 ✓）：
// 同一段字节两次读取把日期推成了两天（10-03 vs 10-02），**message/level/stream 全同**、
// **message 长度相等** ⇒ 长度判据不可用 ✗ ⇒ 按「仅时间不同 ⇒ 后写者（更正版）胜」裁定 ✓；
// 同区间但 message 不同（即便等长）⇒ 仍硬失败 ✗。
func TestAdjudicatesSameIntervalDayShiftDuplicate(t *testing.T) {
	identity := logtypes.SourceIdentity{LogSourceID: "inst:179", SourceGeneration: "g1", ParserVersion: "v1"}
	rec := logtypes.RecordRange{Start: 328911045, End: 328911216}
	build := func(eventTime string) logtypes.Event {
		return logtypes.BuildEvent(identity, rec, eventTime, "2026-10-03T00:17:08.875Z", "WARN", "stdout", "同样的那行")
	}
	old := build("2026-10-03T05:08:50Z")   // seq=9999：先写，日期被推成 10-03
	fixed := build("2026-10-02T05:08:50Z") // seq=10000：后写，更正版 10-02
	require.Equal(t, old.EventID, fixed.EventID)
	require.NotEqual(t, old.CanonicalHash, fixed.CanonicalHash)
	require.Equal(t, len(old.Message), len(fixed.Message), "现场形态：message 长度相等（长度判据不可用 ✗）")

	// ① 规划面：批内成对（先旧后更正）⇒ 取后写者，不再硬失败 ✓。
	got, fail := (&Manager{}).planDeliveryBatchOutsideLock([]logtypes.Event{old, fixed}, map[string]eventIdentity{})
	require.Nil(t, fail)
	require.Len(t, got, 1)
	require.Equal(t, fixed.CanonicalHash, got[0].CanonicalHash, "同区间仅时间差异 ⇒ 后写者（更正版）胜 ✓")

	// ② 重建面：同上 ⇒ 一条入账且为后写者 ✓。
	events, err := (&Manager{}).canonicalRecoveryEvents("inst:179/g1", persistedSource{Events: []logtypes.Event{old, fixed}})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, fixed.CanonicalHash, events[0].CanonicalHash)

	// ③ 边界：同区间但 message 不同且等长 ⇒ 仍硬失败 ✗（形态 2 不适用、形态 1 不成立 ✓）。
	a := logtypes.BuildEvent(identity, rec, "2026-10-03T05:08:50Z", "2026-10-03T00:17:08.875Z", "WARN", "stdout", "AAAAAAAA")
	b := logtypes.BuildEvent(identity, rec, "2026-10-02T05:08:50Z", "2026-10-03T00:17:08.875Z", "WARN", "stdout", "BBBBBBBB")
	_, fail = (&Manager{}).planDeliveryBatchOutsideLock([]logtypes.Event{a, b}, map[string]eventIdentity{})
	require.NotNil(t, fail)
	require.ErrorContains(t, fail.err, "IDENTITY_CONFLICT")
}
