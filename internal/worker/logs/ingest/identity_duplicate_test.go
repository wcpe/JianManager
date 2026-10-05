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
