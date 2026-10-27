package ingest

// 2026-10-28 现场回归：`delivery == reclaim` 的补段死锁。
//
// 现场形态（生产 inst:317）：
//   reclaim        = 7674788
//   首个请求覆盖起点 = 7674897   （request_done_ranges=[7674897,7871859]）
//   delivery       = 7674788   （[.., 7674897) 不在任何批次里 ⇒ 连续前缀到此为止）
//
// 旧行为：`to <= from` ⇒ 直接 `no-material` 返回 ⇒ 每轮空转，reclaim 永不前进，
// 积压永不回落 ⇒ 源永久 PAUSED（生产 10/19 个源当时就在这个状态）。
//
// 新行为：`to <= from` 时先问“reclaim 之后、已有读取范围内，已确认覆盖从哪开始”；
// 若它 > from，则把补段左端前移到那个起点。上界仍是已确认范围（Read/Durable），
// **不越过任何未确认字节**。
//
// 为何判据是纯函数：它是本修复的全部新增语义，必须能被逐条穷举地钉住，
// 而不是藏在“造出真实缝”的重夹具后面。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// TestFirstConfirmedCoverageStartPicksEarliest 把取值口径钉死：取最早的起点，
// 而不是遍历到的第一个。
func TestFirstConfirmedCoverageStartPicksEarliest(t *testing.T) {
	ranges := []ledger.PositionRange{{From: 7, To: 14}, {From: 3, To: 5}, {From: 9, To: 12}}
	got, ok := firstConfirmedCoverageStart(ranges, 0, 24)
	require.True(t, ok)
	require.Equal(t, uint64(3), got, "必须取最早的起点（顺序无关，非“第一个”）✓")
}

// TestFirstConfirmedCoverageStartExcludesRangeBelowFrom 守住“从 from 起”的语义：
// 早于 from 的覆盖不得被当成补段起点（否则会带出“把 reclaim 往回退”的风险）。
func TestFirstConfirmedCoverageStartExcludesRangeBelowFrom(t *testing.T) {
	ranges := []ledger.PositionRange{{From: 3, To: 5}, {From: 10, To: 20}}
	got, ok := firstConfirmedCoverageStart(ranges, 7, 24)
	require.True(t, ok)
	require.Equal(t, uint64(10), got, "早于 from 的覆盖起点必须被排除 ✓")
}

// TestFirstConfirmedCoverageStartExcludesRangeAtOrAboveTo 守住上界：
// 起点恰好等于 to（或更后）的覆盖不参与——它根本不在本窗口内。
func TestFirstConfirmedCoverageStartExcludesRangeAtOrAboveTo(t *testing.T) {
	ranges := []ledger.PositionRange{{From: 24, To: 30}, {From: 40, To: 50}}
	_, ok := firstConfirmedCoverageStart(ranges, 0, 24)
	require.False(t, ok, "起点 >= to 的覆盖不得被选中 ✓")
}

// TestFirstConfirmedCoverageStartReturnsFalseWhenEmpty 守住 default-deny：
// 没有任何覆盖时必须报“找不到”，不得自造起点。
func TestFirstConfirmedCoverageStartReturnsFalseWhenEmpty(t *testing.T) {
	_, ok := firstConfirmedCoverageStart(nil, 0, 24)
	require.False(t, ok, "空名单必须报找不到 ✓")

	_, ok = firstConfirmedCoverageStart([]ledger.PositionRange{}, 0, 24)
	require.False(t, ok, "空切片必须报找不到 ✓")
}

// TestFirstConfirmedCoverageStartReturnsFalseWhenWindowAboveAllCoverage
// 现场反向：若窗口完全落在覆盖之外（真实无料），必须不返回起点。
func TestFirstConfirmedCoverageStartReturnsFalseWhenWindowAboveAllCoverage(t *testing.T) {
	ranges := []ledger.PositionRange{{From: 3, To: 24}}
	_, ok := firstConfirmedCoverageStart(ranges, 100, 200)
	require.False(t, ok, "窗口内无覆盖必须不返回起点（否则就是自造 material）✓")
}

// TestFirstConfirmedCoverageStartOnProductionSeamShape 用生产 inst:317 的真实数字：
// reclaim=7674788、已确认覆盖从 7674897 起。它必须返回 7674897——
// 这正是“把补段左端从 reclaim 前移到首个已确认字节”的依据。
func TestFirstConfirmedCoverageStartOnProductionSeamShape(t *testing.T) {
	ranges := []ledger.PositionRange{{From: 7674897, To: 7871859}}
	got, ok := firstConfirmedCoverageStart(ranges, 7674788, 8131943)
	require.True(t, ok)
	require.Equal(t, uint64(7674897), got,
		"生产缝起点必须精确命中 7674897（109 字节的缝）✓")
}
