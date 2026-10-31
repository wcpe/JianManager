package ledger

import "log/slog"

// DefaultResolvedGapKeepRecent 是已解决缺口的审计尾窗默认条数。
//
// 取值理由：与 DefaultDeliveryBatchKeepRecent 同量级即可——审计要回答的是
// 「最近出过什么缺口、怎么补的」，不需要保留全部历史。
// 留太少会让「刚补上就查不到」；留太多则失去裁剪意义。32 条足以覆盖
// 一个排查窗口内的全部缺口事件。
const DefaultResolvedGapKeepRecent = 32

// PruneResolvedGaps 裁剪「已解决且回收水位已越过」的缺口记录，保留近期审计尾窗。
//
// ## 为什么需要它
//
// `index_store.go` 的注释早已承诺「已解决缺口会被 trimResolvedGapsLocked 裁掉」，
// 但该函数从未实现——`gap` 因此是索引里**唯一无界增长**的维度：
// 已补上的缺口永远留在库里。而启动时 `Store.Load()` 要全表读 gap
// （实测每 1k 行约 5ms），且这段耗时**发生在向 CP 注册之前**，
// 堆积几十万行就意味着主控台要多等几秒才看得见这台机器（的原始形态）。
//
// ## 判据为什么不能只看「Resolved == true」
//
// 只看 `Resolved` 不安全，有两个独立理由：
//
// **① 重读治愈仍需要它**（runtime.go:9894）：已解决且 `Resolution` 可重读治愈的缺口，
// 只要 `EndPos > Delivery` 就会被选作「待重读缝」的候选——那段日志虽标记解决，
// 实际可能还没真正落盘，要靠重读补上。删掉它等于放弃这条自愈路径。
//
// **② 回收水位才是安全边界**：`watermark = min(Reclaim, Delivery)`（与
// `PruneDeliveryBatches` 同一把尺子，见 delivery_batch_prune.go 的 LEDGER-1 论证）。
// 取 min 而非直接取 Reclaim 是必须的：`Reclaim` 在某些分支（凭据放行、责任转移）
// 会越过 `Delivery`，此时若按 Reclaim 裁剪，会删掉 `EndPos` 落在 (Delivery, Reclaim]
// 区间的缺口——而「已发出未确认」这一段恰恰是读者唯一能区分状态的证据。
//
// 因此本函数的裁剪条件是三者同时成立：**已解决 + EndPos ≤ min(Reclaim, Delivery) + 在审计尾窗之外**。
//
// ## 参数
//
//   - gaps：该源的全部缺口，**按位置升序**（调用方保证，本函数保持顺序）。
//   - reclaim / delivery：两个水位（`Positions.Reclaim` / `Positions.Delivery`）。
//     内部取 `min(reclaim, delivery)` 作安全边界——见函数注释的 LEDGER-1 说明。
//     两者任一为 0 表示尚未推进过，此时一律不裁（没有边界就没有安全裁剪）。
//   - keepRecent：无条件保留的**末尾**已解决缺口条数（审计尾窗）。负数回退默认值。
//
// 返回裁剪后的切片、被裁条数、错误。空输入返回非 nil 空切片，
// 与 `normalizePersistedState` 的归一形态一致，便于两侧逐字段可比。
//
// 本函数不修改入参切片底层数组以外的任何状态，也不持有锁——调用方负责在
// 持锁的临界区内使用（与其它 ledger 变更同一约定）。
func PruneResolvedGaps(gaps []Gap, reclaim, delivery uint64, keepRecent int) ([]Gap, int, error) {
	if keepRecent < 0 {
		keepRecent = DefaultResolvedGapKeepRecent
	}
	if len(gaps) == 0 {
		return []Gap{}, 0, nil
	}
	// 安全边界 = min(Reclaim, Delivery)：与 PruneDeliveryBatches 同口径。
	// 任一为 0 说明还没推进过，没有边界就是没有安全裁剪的依据。
	watermark := reclaim
	if delivery < watermark {
		watermark = delivery
	}
	if watermark == 0 {
		out := make([]Gap, len(gaps))
		copy(out, gaps)
		return out, 0, nil
	}

	// 审计尾窗起点：从尾部往前数第 keepRecent 条**已解决**缺口的位置。
	// 该位置及之后的已解决缺口一律保留——它们可能刚发生、需要供排查查看。
	windowStart := len(gaps)
	if keepRecent > 0 {
		seen := 0
		for i := len(gaps) - 1; i >= 0; i-- {
			if !gaps[i].Resolved {
				continue
			}
			seen++
			if seen > keepRecent {
				windowStart = i + 1
				break
			}
			windowStart = i
		}
	}

	out := make([]Gap, 0, len(gaps))
	dropped := 0
	for i, g := range gaps {
		// 只裁「已解决 + 水位已越过 + 在审计尾窗之外」三条同时成立的记录。
		if g.Resolved && watermark >= g.EndPos && i < windowStart {
			dropped++
			continue
		}
		out = append(out, g)
	}
	return out, dropped, nil
}

// pruneResolvedGapsLocked 按当前水位裁一次该源的已解决缺口。调用方必须已持写锁。
//
// 形态与 `pruneDeliveryBatchesLocked` 保持一致（③ 附带修复）：
//
//   - **幂等 + 按水位去重**：水位未变则直接返回，稳态下是 O(1)；
//   - **失败只告警不阻断**：裁剪是空间优化而非正确性前提——
//     少裁/不裁永远是安全方向，绝不让「裁不掉」变成「采集出错」。
//
// 与批次裁剪的唯一差别是判据对象：批次看 `End <= 水位`，缺口额外要求 `Resolved`
// （未解决的缺口必须留下，它们仍待补齐）。
func (l *Ledger) pruneResolvedGapsLocked(e *Entry) {
	if e == nil {
		return
	}
	watermark := deliveryBatchWatermark(e)
	if e.gapPrunedThrough == e.Positions.Reclaim && watermark <= e.gapPrunedWatermark {
		return // 这个 (Reclaim, 水位) 组合已处理过，不可能有新可裁项
	}
	if len(e.Gaps) == 0 {
		e.gapPrunedThrough = e.Positions.Reclaim
		e.gapPrunedWatermark = watermark
		return
	}
	kept, dropped, err := PruneResolvedGaps(e.Gaps, e.Positions.Reclaim, e.Positions.Delivery, DefaultResolvedGapKeepRecent)
	// 无论成败都记下「已处理过该水位」：守卫拦下说明判据不自洽（非瞬时故障），
	// 对同一水位重复告警没有信息量；下次水位前进会再试。
	e.gapPrunedThrough = e.Positions.Reclaim
	e.gapPrunedWatermark = watermark
	if err != nil {
		slog.Warn("已解决缺口裁剪被守卫拦下，本轮保留完整缺口历史（不影响采集正确性）",
			"logSourceID", e.Key.LogSourceID, "generation", e.Key.SourceGeneration,
			"reclaim", e.Positions.Reclaim, "watermark", watermark,
			"gaps", len(e.Gaps), "error", err)
		return
	}
	if dropped == 0 {
		return
	}
	e.Gaps = kept
	slog.Debug("已裁剪水位之下的已解决缺口",
		"logSourceID", e.Key.LogSourceID, "generation", e.Key.SourceGeneration,
		"reclaim", e.Positions.Reclaim, "watermark", watermark,
		"dropped", dropped, "kept", len(kept))
}
