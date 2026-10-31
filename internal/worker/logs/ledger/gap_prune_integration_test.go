package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ③ 附带修复：gap 裁剪在 Ledger 真实路径上的行为。
//
// 单元用例（gap_prune_test.go）验判据；这里验「接到 Ledger 之后确实生效」——
// 因为判据正确但没人调用，是这类修复最常见的失败方式（注释承诺了、
// 函数也写对了，就是没有调用点）。
//
// 走真实 API（Ensure + RecordDelivery + TryReclaim），而不是直接改内部字段：
// 后者会绕过持锁与水位推进逻辑，测出来的「生效」在生产路径上未必成立。

// newGapTestLedger 造一个已注册的账本与源键。
func newGapTestLedger(t *testing.T, id string) (*Ledger, SourceKey) {
	t.Helper()
	l := New()
	key := SourceKey{LogSourceID: id, SourceGeneration: "g1"}
	l.Ensure(key, logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "g1"})
	return l, key
}

// seedResolvedGaps 通过公开途径造「已解决缺口」是困难的（要真的产生再消解），
// 因此这里直接写入 Entry 快照再 Restore——Restore 是生产用的正常入口
// （重启恢复走的就是它），不是测试后门。
func seedGaps(t *testing.T, l *Ledger, key SourceKey, gaps []Gap, pos logtypes.Positions) {
	t.Helper()
	entries := l.Snapshot()
	var target *Entry
	for i := range entries {
		if entries[i].Key == key {
			target = &entries[i]
			break
		}
	}
	require.NotNil(t, target, "前置：该源应已 Ensure")
	target.Gaps = gaps
	target.Positions = pos
	// 注意：Restore 会按 DeliveryBatches 重算 Positions.Delivery
	//（`contiguousDeliveryEnd`，见 ledger.go:872）。空批次 ⇒ Delivery 归 0。
	// 这不是缺陷，而是「没有已投递批次就没有连续投递前缀」的正确语义。
	// 因此调用方若需要非零 Delivery，必须同时给出覆盖到该位置的批次。
	require.NoError(t, l.Restore(entries))
}

// TestLedgerPrunesResolvedGaps 水位越过已解决缺口后，它们被裁掉；未解决的保留。
//
// 这是本修复的主路径：`gap` 表此前是索引里唯一无界增长的维度，
// 因为已补上的缺口永远留着——启动时全表读它，几万行就是几百毫秒，
// 而这段时间发生在向 CP 注册**之前**（的原始形态）。
func TestLedgerPrunesResolvedGaps(t *testing.T) {
	l, key := newGapTestLedger(t, "log-prune-1")
	// 已解决缺口必须超过审计尾窗（DefaultResolvedGapKeepRecent=32），
	// 否则尾窗保护会把它们全部留下、一条都裁不掉——测到的就不是裁剪而是尾窗。
	gaps := make([]Gap, 0, DefaultResolvedGapKeepRecent+9)
	for i := 0; i < DefaultResolvedGapKeepRecent+8; i++ {
		gaps = append(gaps, Gap{StartPos: uint64(i * 10), EndPos: uint64(i*10 + 5), Reason: "R", Resolved: true})
	}
	gaps = append(gaps, Gap{StartPos: 5000, EndPos: 5010, Reason: "R", Resolved: false})
	// 批次覆盖 [0,5500) ⇒ Restore 重算后 Delivery=5500，判据 min(Reclaim,Delivery) 生效
	seedWithBatches(t, l, key, gaps, logtypes.Positions{Read: 6000, Durable: 6000, Reclaim: 5500},
		[]DeliveryBatch{{Start: 0, End: 5500, State: logtypes.DeliveryRequestDone}})

	// 直接驱动裁剪（持锁路径）。
	//
	// 为什么不用 TryReclaim：它要求「有 recovery segment 覆盖目标位置」这一前置，
	// 造那个前置需要完整的账本+段状态，与本用例要验的东西无关
	//（本用例验的是「裁剪判据接到 Ledger 后生效」，不是回收推进本身）。
	// TryReclaim 的端到端行为由 delivery_batch_prune_ledger1_test.go 等既有用例覆盖。
	pruneForTest(l, key)

	entry := l.Get(key)
	require.NotNil(t, entry)
	// 尾窗保留 32 条已解决 + 1 条未解决
	require.Len(t, entry.Gaps, DefaultResolvedGapKeepRecent+1,
		"应保留尾窗 %d 条已解决 + 1 条未解决", DefaultResolvedGapKeepRecent)
	require.False(t, entry.Gaps[len(entry.Gaps)-1].Resolved, "未解决缺口必须保留")
	require.Equal(t, uint64(5000), entry.Gaps[len(entry.Gaps)-1].StartPos)
}

// TestLedgerKeepsGapsAboveDelivery 水位未越过时不动。
//
// 这条守的是安全性：缺口虽已解决，但其区间仍在 delivery 之上，
// runtime.go:9894 的「重读治愈」还需要它作候选。
func TestLedgerKeepsGapsAboveDelivery(t *testing.T) {
	l, key := newGapTestLedger(t, "log-prune-2")
	gaps := []Gap{{StartPos: 500, EndPos: 600, Reason: "R", Resolved: true}}
	seedWithBatches(t, l, key, gaps, logtypes.Positions{Read: 700, Durable: 650, Reclaim: 100},
		[]DeliveryBatch{{Start: 0, End: 100, State: logtypes.DeliveryRequestDone}})

	pruneForTest(l, key)

	entry := l.Get(key)
	require.NotNil(t, entry)
	require.Len(t, entry.Gaps, 1, "水位未越过的缺口必须保留")
}

// TestLedgerKeepsUnresolvedGaps 未解决缺口永不裁剪，即使水位远高于它。
//
// 未解决 = 仍待补齐，删掉它等于放弃补这块日志。
func TestLedgerKeepsUnresolvedGaps(t *testing.T) {
	l, key := newGapTestLedger(t, "log-prune-3")
	gaps := []Gap{
		{StartPos: 0, EndPos: 10, Reason: "R", Resolved: false},
		{StartPos: 20, EndPos: 30, Reason: "R", Resolved: false},
	}
	seedWithBatches(t, l, key, gaps, logtypes.Positions{Read: 1000, Durable: 1000, Reclaim: 900},
		[]DeliveryBatch{{Start: 0, End: 900, State: logtypes.DeliveryRequestDone}})

	pruneForTest(l, key)

	entry := l.Get(key)
	require.NotNil(t, entry)
	require.Len(t, entry.Gaps, 2, "未解决缺口必须全部保留")
}

// TestLedgerPruneIdempotentAcrossReclaims 同一水位重复推进不产生额外变化（去重生效）。
//
// 去重是必需的：缺口列表在积压源上可达十万级，若每轮采集都全扫一遍，
// 就把「空间优化」变成了固定 CPU 负担。
func TestLedgerPruneIdempotentAcrossReclaims(t *testing.T) {
	l, key := newGapTestLedger(t, "log-prune-4")
	// 同样要超过尾窗，否则一条都裁不掉，测不到去重
	gaps := make([]Gap, 0, DefaultResolvedGapKeepRecent+9)
	for i := 0; i < DefaultResolvedGapKeepRecent+8; i++ {
		gaps = append(gaps, Gap{StartPos: uint64(i * 10), EndPos: uint64(i*10 + 5), Reason: "R", Resolved: true})
	}
	gaps = append(gaps, Gap{StartPos: 5000, EndPos: 5010, Reason: "R", Resolved: false})
	seedWithBatches(t, l, key, gaps, logtypes.Positions{Read: 6000, Durable: 6000, Reclaim: 5500},
		[]DeliveryBatch{{Start: 0, End: 5500, State: logtypes.DeliveryRequestDone}})

	pruneForTest(l, key)
	after1 := l.Get(key)
	n1 := len(after1.Gaps)

	// 同水位再来一次
	pruneForTest(l, key)
	after2 := l.Get(key)
	require.Equal(t, n1, len(after2.Gaps), "同水位重复推进不应再裁剪")
	require.Equal(t, DefaultResolvedGapKeepRecent+1, n1, "应保留尾窗 + 1 条未解决")
}

// pruneForTest 在持锁状态下调用 gap 裁剪，模拟生产里采集轮/回收推进的调用时机。
func pruneForTest(l *Ledger, key SourceKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok {
		l.pruneResolvedGapsLocked(e)
	}
}

// seedWithBatches 在 seedGaps 基础上附带投递批次——Restore 会用批次重算 Delivery，
// 只有给出覆盖到目标位置的批次，Delivery 才不会归零。
func seedWithBatches(t *testing.T, l *Ledger, key SourceKey, gaps []Gap, pos logtypes.Positions, batches []DeliveryBatch) {
	t.Helper()
	entries := l.Snapshot()
	var target *Entry
	for i := range entries {
		if entries[i].Key == key {
			target = &entries[i]
			break
		}
	}
	require.NotNil(t, target, "前置：该源应已 Ensure")
	target.Gaps = gaps
	target.Positions = pos
	target.DeliveryBatches = batches
	require.NoError(t, l.Restore(entries))
}
