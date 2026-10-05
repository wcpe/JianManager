package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// TestDirectDeliveryClosesResolvedReReadGap 验收 #179-③：破环直投步把「已决但非终局」的
// 待重读缝**按投递证据改判为终局**，且 delivery 越过缝、批次覆盖缝区间、连调幂等。
//
// 证据口径说明：夹具的投递桩设在 pipeline 层（p.SetDelivery），而直投步走 Manager 面
// （m.deliver）⇒ 桩不在其调用路径上 ✓；因此「零丢失」以**账本层双证**为准：
//  ① delivery 越过缝；② delivery_batch 出现覆盖缝区间的批次记录（该批的字节区间 ⊇ 缝）。
//
// 场景即现场 179 的等价形态：待重读类缝钉住 delivery ⇒ reclaim 无法前进（依赖环 ✗）。
func TestDirectDeliveryClosesResolvedReReadGap(t *testing.T) {
	f := newDeliveryBacklogFixture(t)
	key := f.p.Key()

	// 夹具：事件覆盖 [0,24]、已确认投递 [0,9] ⇒ 欠账前缀 [10,24]（3 条事件 ✓）。
	from, to := uint64(10), uint64(24)

	// 造「已决但非终局」的待重读缝：先记 DELIVER_ERROR 缺口，再按既有分类路径消解为待重读类。
	require.NoError(t, f.p.Ledger().RecordGap(key, from, to, ledger.GapReasonDeliverError, "transport down"))
	n, err := f.p.Ledger().ResolveGapsCoveredByRanges(key,
		[]ledger.PositionRange{{From: from, To: to}},
		acquire.ResolutionReReadOnResumeDelivery, ledger.GapReasonDeliverError)
	require.NoError(t, err)
	require.Equal(t, 1, n, "前置：缝必须已决且消解词属待重读类 ✓")

	before, ok := f.p.Ledger().Positions(key)
	require.True(t, ok)
	require.Equal(t, uint64(9), before.Delivery, "前置：投递欠账（delivery=9 ✓）")
	require.Equal(t, before.Delivery, before.Reclaim, "前置：回收追平投递 ⇒ 环在场的形态 ✓")

	// 行动：破环直投步（resume 不可达的等价形态：缝是待重读类且源非暂停也推不动 ✓）。
	f.m.directDeliverResolvedReReadGap(f.p, f.src)

	after, ok := f.p.Ledger().Positions(key)
	require.True(t, ok)
	require.Equal(t, uint64(24), after.Delivery, "证书①：直投一批发完 ⇒ delivery 越过缝、追平已读水位 ✓")

	entry := f.p.Ledger().Get(key)
	require.NotNil(t, entry)
	covered := false
	for _, b := range entry.DeliveryBatches {
		if b.Start <= from && b.End >= to {
			covered = true
			break
		}
	}
	require.True(t, covered, "证书②：delivery_batch 必须存在覆盖缝 [10,24] 的批次记录 ✓")

	closed := false
	for _, g := range entry.Gaps {
		if g.StartPos == from && g.EndPos == to {
			require.True(t, g.Resolved, "缝必须保持已决 ✓")
			require.False(t, acquire.IsReReadHealableResolution(g.Resolution),
				"证书③：缝必须由投递证据改判为终局（不再是待重读类）✓")
			closed = true
			break
		}
	}
	require.True(t, closed, "缝必须仍在账本中（未被裁掉）✓")

	// 幂等：再次直投 ⇒ 缝隙已非待重读类 ⇒ 本步不再触发，账本不变。
	f.m.directDeliverResolvedReReadGap(f.p, f.src)
	again, ok := f.p.Ledger().Positions(key)
	require.True(t, ok)
	require.Equal(t, after.Delivery, again.Delivery, "连调两次不得双增（幂等 ✓）")
}
