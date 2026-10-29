package ingest

// 历史未认领段丢弃（2026-10-30 用户裁定：「历史未认领应该直接丢掉」）。
//
// ## 什么是「未认领」
//
// 事件段（eventstore）里有数据，但账本的对该区间**既无 verified_runs 也无
// request_done**。也就是说：拿不出任何「本进程校验/投递过它」的证据。
//
// ## 为什么必须主动丢（生产死锁，inst:318 实测）
//
// 该源 VL 里 [313900,443835] 站着 1670 行真实数据（历史代次 projection-16391
// 写的，`event_id` 零重复、校验当时也过），但账本的
// `verified_runs` 与 `request_done` **都只有** [(0,313900), (4093898,4472739)]。
//
// 于是形成一条**永不推进的死锁**：
//
//	delivery 卡在 313900
//	  ⇒ resolveOneSourceGaps 的入口判据 `delivery >= read` 不成立
//	  ⇒ TryReclaim 从不被调用
//	  ⇒ reclaim 卡在 443835（= projection-16391 的 record_end 上界）
//	  ⇒ pending 永不消 ⇒ 重建死循环 ⇒ WAL 打满 5000 ⇒ **每分钟丢 2 条**
//
// 【关键】这不是任何判据的 bug。账本不认那段是**正确的**（它确实没被本进程校验过）。
// 死锁的根源是：**一段永远无法被认领的历史数据，却永久占据着水位推进的路径**。
//
// ⇒ 用户裁定：直接丢掉。它不属于当前权威集，留着只会无限拖死采集。
//
// ## 安全边界（保守方向）
//
//   - 只丢**已确认未认领**的：区间必须同时不在 verified_runs 与 request_done 内；
//   - 丢弃必须**可见**：登记 UNCLAIMED_HISTORY_DROPPED 缺口（进账本、可告警、可审计）；
//   - 丢弃后**不该推进 delivery**：那段字节确实没投递成功过，不允许谎报。
//     delivery 的推进靠缺口被归入 nonBlocking 后由 reclaim 栅栏自行重算。
//   - 已经有 verified/request_done 证据的区间**一律不动**（那是真投过的）。

import (
	"context"
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// dropUnclaimedHistory 丢弃「事件段里有、但账本从未认领」的区间并留下可查缺口。
//
// 返回本次丢弃的字节数（0 表示没有可丢的）。
//
// 调用方负责限频：它要遍历该源事件段做区间差异计算。
func (m *Manager) dropUnclaimedHistory(ctx context.Context, source SourceConfig, p *pipeline.Pipeline) uint64 {
	if m == nil || p == nil || ctx == nil {
		return 0
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	// ## O(1) 前置（每轮都被调用，不能无脑全段扫）
	//
	// 健康源的 delivery 已经追平段尾 ⇒ 不可能存在未认领区间，直接返回。
	// 只有「有欠账」的源才走下面的全段遍历（与 #66-⑤ 的「先 O(1) 判有欠账」同型）。
	//
	// 判据取 `Delivery < Read`：这正是「有未认领数据挡住了水位」的形态。
	// 水位已追平（含空源 read=0）⇒ 不可能有未认领区间，直接返回。
	if pos, ok := p.Ledger().Positions(p.Key()); ok && pos.Delivery >= pos.Read && pos.Read > 0 {
		return 0
	}
	if read, ok := p.Ledger().Positions(p.Key()); ok && read.Read == 0 {
		return 0
	}
	events, err := m.canonicalRecoveryEvents(key, m.stateOf(key))
	if err != nil || len(events) == 0 {
		return 0
	}
	// 已处置证据 = verified_runs ∪ request_done ∪ 已丢弃。三者都不覆盖的才是“真未认领”。
	//
	// 已丢弃必须计入：否则每轮都会把同一段重复登记一遍（幂等性）。
	verified := m.verifiedRuns(key)
	var claimed []ledger.PositionRange
	claimed = append(claimed, verified...)
	if entry := p.Ledger().Get(p.Key()); entry != nil {
		claimed = append(claimed, ledger.RequestDoneRangesOf(entry)...)
		// 已登记过的丢弃缺口也算已处置（否则永远幂等不了）。
		for _, g := range entry.Gaps {
			if g.Reason == ledger.GapReasonUnclaimedHistoryDropped {
				claimed = append(claimed, ledger.PositionRange{From: g.StartPos, To: g.EndPos})
			}
		}
	}
	claimed = ledger.MergePositionRanges(claimed)

	// 逐事件找出未认领区间，合并成连续段（避免为每个事件登记一条缺口）。
	var unclaimed []ledger.PositionRange
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return 0
		}
		if ledger.CoveredByPositionRanges(claimed, ev.Record.Start, ev.Record.End) {
			continue
		}
		unclaimed = append(unclaimed, ledger.PositionRange{From: ev.Record.Start, To: ev.Record.End})
	}
	if len(unclaimed) == 0 {
		return 0
	}
	unclaimed = ledger.MergePositionRanges(unclaimed)

	var total uint64
	for _, span := range unclaimed {
		if err := ctx.Err(); err != nil {
			return total
		}
		if span.To <= span.From {
			continue
		}
		detail := "event segment holds data never claimed by this process (no verified_runs, no request_done); " +
			"cannot be attributed to the current authoritative set — dropped per operator ruling"
		if err := p.Ledger().RecordGap(p.Key(), span.From, span.To,
			ledger.GapReasonUnclaimedHistoryDropped, detail); err != nil {
			slog.Warn("历史未认领段：登记丢弃缺口失败（下轮重试）",
				"logSourceID", source.LogSourceID, "from", span.From, "to", span.To, "error", err)
			continue
		}
		// 登记一个 DROPPED 投递批次，让 `delivery` 水位能跨过这段。
		//
		// 为什么必须做（否则丢弃白做）：`advanceDeliveryEnd` 只在有投递批次时才推进，而
		// `resolveOneSourceGaps` 的 `delivery >= read` 入口判据依赖它。只登记缺口不推进水位，
		// delivery 仍卡在 313900 ⇒ TryReclaim 永不被调 ⇒ 死锁原封不动。
		//
		// 为什么用 DROPPED 而不是 REQUEST_DONE：那段字节确实没投递成功过。
		// DROPPED 能推水位，但 `preserveDeliveryEvidence` 只把 REQUEST_DONE 写进
		// RequestDoneRanges ⇒ 投递证据不被污染（不谎报已投递）。
		if err := p.Ledger().RecordDroppedRange(p.Key(), span.From, span.To); err != nil {
			slog.Warn("历史未认领段：登记丢弃批次失败（水位本轫不会推进，下轮重试）",
				"logSourceID", source.LogSourceID, "from", span.From, "to", span.To, "error", err)
			continue
		}
		total += span.To - span.From
		slog.Warn("历史未认领段：账本从未校验/投递过该区间，已按裁定丢弃（缺口保留可查，水位已推进）",
			"logSourceID", source.LogSourceID, "from", span.From, "to", span.To,
			"bytes", span.To-span.From)
	}
	return total
}

// stateOf 取该 key 的内存持久化快照（受 m.mu 保护）。
func (m *Manager) stateOf(key string) persistedSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Sources[key]
}

var _ = logtypes.Event{}
