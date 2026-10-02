package ledger

import (
	"fmt"
	"log/slog"
)

// 历史投递批次（delivery_batch）裁剪：把「已确定不会再被需要」的条目从账本里摘掉。
//
// 为什么需要（FR-498 实测，60 源）：采集索引 stateindex 里唯一随总量线性增长的表是
// delivery_batch（≈5 MB/天 @60 源），而它原先没有任何裁剪路径——长跑会把索引无界撑大，
// 与 FR-496「索引稳态不随总量增长」的初衷相悖。本文件给出**判据、理由与调用点**。
//
// # 谁在读投递批次（消费者，全仓仅此一处有语义）
//
//	rows(delivery_batch) → ingest.indexStateToState → ledger.Entry.DeliveryBatches
//	  → contiguousDeliveryEnd(batches, floor=Positions.Reclaim)
//
// 三个调用点的 floor 都是 reclaim：RecordDelivery、Restore、以及 FR-496 迁移校验的
// comparisonState（ingest/index_store.go 的 ledger.ContiguousDeliveryEnd）。
// 除此之外只有 Entry.clone() 的深拷贝，没有别的读者。
//
// `Positions.Delivery` 是由它派生的值（落库保留原值，Restore/比对时按同一口径重算）；
// `DeliveryState`（最近一批的状态）与 `ErrorCount` 另存（ingest 的 indexLedgerCore），
// 而「该源最近一次投递是否 UNKNOWN」读的是账本字段 p.DeliveryState()，**不读批次列表**
// （见 pipeline.DeliveryState / reconcile 的 UNKNOWN 判定）。
//
// # 判据：水位 = Positions.Reclaim
//
// contiguousDeliveryEnd 的语义是「所有字节均有请求结果的连续前缀末端」：pos 从 floor 起、
// 只增；一个跨度 s 只有在 s.end > pos 时才推进 pos。因此对 end <= floor 的跨度，两个分支
// 都是恒假——它是**恒等元**：删掉它既不改变本次计算结果，也不改变后续跨度的处理结果
// （后续只依赖当前 pos，而 pos 未被它改变）。
//
//	⇒ batch.End <= Positions.Reclaim 的条目确定不会再被需要。
//
// 语义上与 reclaim 的既有含义完全一致：reclaim 只能经 CanReclaim 门禁推进（恢复分段到
// WAL_RESPONSIBILITY_TRANSFERRED 及其后、无 hold），即「这些字节的恢复责任已由受管恢复
// 分段/投影承担」——责任既已转移，批次级请求结果对这些字节不再承载任何决定性的信息。
//
// 单调性（为什么裁过一次之后永远安全）：水位只前进（TryReclaim 仅在 target > Reclaim 时
// 推进；AdvanceRead 只把读指针夹到水位之上），而「end <= W」在更晚的 W' >= W 下依然成立
// ⇒ 被裁条目在任意更晚时刻仍是恒等元。
//
// # 不做的事（如实登记）
//
//   - 不动 Positions.Delivery：它是派生值，裁剪前后按同一水位重算的结果逐位相同
//     （verifyPruneIdentity 守着这条），落库原值不变。
//   - 不裁判据之外的任何条目：end > 水位的一条都不裁（哪怕只差 1 字节）。
//   - 不做「按时间/保留期」的裁剪：批次没有时间戳，判据只有一个，越简单越可证。
//   - 保留期（VL retention）与 publishedClosed（发布关闭水位）都不在此判据内：前者不在
//     索引里，后者由 projection/source_aux 承载，与投递批次无读取关系。

// DefaultDeliveryBatchKeepRecent 是审计尾窗的默认条数：0 = 严格按水位裁。
//
// 为什么默认 0：唯一成立的判据就是水位，默认口径里不放任何「凭感觉的魔数」；需要留一段
// 可观测的历史批次（排障时直接用 sqlite3 看最近若干批的状态）时，由 log_index.batch_prune.
// keep_recent 显式开启。它只多留不少留，因此不弱化有界性（保留条数是常数）。
const DefaultDeliveryBatchKeepRecent = 0

// DeliveryBatchPruneConfig 是历史投递批次裁剪的配置面（worker.yml 键 log_index.batch_prune.*）。
//
// 零值语义：Enabled=false 的零值无法与「显式关闭」区分（与 log_reconcile.enabled、
// orphan_scan.auto_adopt 同口径），故配置装载侧（ingest.Options.IndexPrune 为 nil）用
// DefaultDeliveryBatchPruneConfig()，默认**开启**。
type DeliveryBatchPruneConfig struct {
	// Enabled 为 false 时完全不裁（应急逃生口：怀疑裁剪影响判定时先关它）。
	Enabled bool
	// KeepRecent 是无条件保留的最近批次条数（审计尾窗），只多留不少留；负数属误写，回退默认。
	KeepRecent int
}

// DefaultDeliveryBatchPruneConfig 返回默认裁剪配置（默认开启、严格按水位、不留尾窗）。
func DefaultDeliveryBatchPruneConfig() DeliveryBatchPruneConfig {
	return DeliveryBatchPruneConfig{Enabled: true, KeepRecent: DefaultDeliveryBatchKeepRecent}
}

// Normalized 把非法值收敛到默认：负数条数是误写（裁剪只会「留得比判据多」，负值无意义），
// 回退默认 0，绝不因为配置误写而放宽判据。
func (c DeliveryBatchPruneConfig) Normalized() DeliveryBatchPruneConfig {
	out := c
	if out.KeepRecent < 0 {
		out.KeepRecent = DefaultDeliveryBatchKeepRecent
	}
	return out
}

// PruneDeliveryBatches 按水位裁剪投递批次列表，返回（保留的列表, 裁剪条数, 错误）。
//
// 判据见文件头：batch.End <= watermark 的条目是 contiguousDeliveryEnd(…, watermark) 的恒等元。
// keepRecent 额外保留「最近 keepRecent 条被水位越过的条目」（审计尾窗，只多留不少留）。
//
// watermark 为 0 时一律不裁：水位 0 表示尚无任何字节被裁定安全（与 acquire.WAL.pruneReclaimed
// 同口径），此时连零长度退化条目也不动。
//
// 自证守卫：裁剪前后按**同一水位**重算的连续前缀必须逐位相等；不等即说明判据与派生计算
// 已不再自洽（有人改了 contiguousDeliveryEnd 的规则、或放宽了判据），此时**原样返回完整列表
// 与错误**，由调用方按「不裁」处理——保留更多永远是安全方向，少裁不会产生任何错误结果。
//
// 返回的列表在「无可裁条目」时与入参同一底层数组（稳态调用不分配）。
func PruneDeliveryBatches(batches []DeliveryBatch, watermark uint64, keepRecent int) ([]DeliveryBatch, int, error) {
	if len(batches) == 0 || watermark == 0 {
		return batches, 0, nil
	}
	if keepRecent < 0 {
		keepRecent = DefaultDeliveryBatchKeepRecent
	}
	// 审计尾窗起点：从尾部往前数第 keepRecent 条「被水位越过」的条目。
	// 它之后的条目（无论是否被越过）一律保留，它之前的按判据取舍。
	// 待裁条目不足 keepRecent 条时，tailFrom 停在最早的那条上 ⇒ 一条都不裁（尾窗只多留不少留）。
	tailFrom := len(batches)
	if keepRecent > 0 {
		count := 0
		for i := len(batches) - 1; i >= 0; i-- {
			if batches[i].End > watermark {
				continue
			}
			count++
			tailFrom = i
			if count >= keepRecent {
				break
			}
		}
	}
	var kept []DeliveryBatch
	dropped := 0
	for i, batch := range batches {
		if i >= tailFrom || batch.End > watermark {
			if kept != nil {
				kept = append(kept, batch)
			}
			continue
		}
		if kept == nil {
			// 首次出现待裁条目：一次性把前缀搬过来（无可裁条目时不做任何分配）。
			kept = make([]DeliveryBatch, 0, len(batches))
			kept = append(kept, batches[:i]...)
		}
		dropped++
	}
	if dropped == 0 {
		return batches, 0, nil
	}
	if err := verifyPruneIdentity(batches, kept, watermark); err != nil {
		return batches, 0, err
	}
	// 精确长度的新切片：既避免长期持有已被裁条目的引用，也避免「裁空」退化成 nil
	// （nil 与空切片在索引迁移的逐字段比对里是两种形态，见 normalizePersistedState）。
	out := make([]DeliveryBatch, len(kept))
	copy(out, kept)
	return out, dropped, nil
}

// verifyPruneIdentity 校验裁剪的恒等元性质：按同一水位重算的连续前缀必须逐位相等。
//
// 它守的是「判据 ⇔ 派生计算」的自洽性，而不是某个具体水位下的取值——两侧用同一 watermark，
// 因此与落库里的旧 Positions.Delivery 无关（那个值本来就允许滞后，见 comparisonState 注释）。
func verifyPruneIdentity(before, after []DeliveryBatch, watermark uint64) error {
	want := contiguousDeliveryEnd(before, watermark)
	got := contiguousDeliveryEnd(after, watermark)
	if want != got {
		return fmt.Errorf("ledger: 投递批次裁剪破坏连续前缀（水位 %d：裁前 %d → 裁后 %d），已放弃本次裁剪",
			watermark, want, got)
	}
	return nil
}

// SetDeliveryBatchPrune 设置本账本的投递批次裁剪配置（非法值在 Normalized 里回退默认）。
//
// 采集合成侧（ingest.Options.IndexPrune）在建账本时下发；nil 配置走 New() 里的默认（开启）。
func (l *Ledger) SetDeliveryBatchPrune(cfg DeliveryBatchPruneConfig) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.batchPrune = cfg.Normalized()
}

// DeliveryBatchPruneConfig 返回本账本当前的裁剪配置（观测/回归用）。
func (l *Ledger) DeliveryBatchPrune() DeliveryBatchPruneConfig {
	if l == nil {
		return DefaultDeliveryBatchPruneConfig()
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.batchPrune
}

// pruneDeliveryBatchesLocked 按当前水位裁一次该源的投递批次。调用方必须已持写锁。
//
// 幂等 + 按水位去重：水位未变则直接返回（见 Entry.deliveryPrunedThrough 的注释），因此本方法在
// 稳态（每次投递、每轮采集）都是 O(1)。
//
// 失败只告警不阻断：裁剪是**空间优化**而非正确性前提（被裁条目对任何派生值都是恒等元），
// 因此任何异常都收敛为「本轮不裁 + 一条 Warn」——绝不冒泡到投递登记/回收推进/持久化路径，
// 绝不把「裁不掉」变成「采集出错」。少裁/不裁永远是安全方向。
func (l *Ledger) pruneDeliveryBatchesLocked(e *Entry) {
	if e == nil || !l.batchPrune.Enabled {
		return
	}
	if e.deliveryPrunedThrough == e.Positions.Reclaim {
		return // 这个水位已经裁过：水位不变则没有任何条目会变得可裁
	}
	if len(e.DeliveryBatches) == 0 {
		e.deliveryPrunedThrough = e.Positions.Reclaim
		return
	}
	kept, dropped, err := PruneDeliveryBatches(e.DeliveryBatches, e.Positions.Reclaim, l.batchPrune.KeepRecent)
	// 无论成功与否都记下「这个水位已处理」：守卫拦下意味着判据不自洽（不是瞬时故障），
	// 再对同一水位重复告警没有信息量；下一次水位前进时会再试。
	e.deliveryPrunedThrough = e.Positions.Reclaim
	if err != nil {
		slog.Warn("历史投递批次裁剪被守卫拦下，本轮保留完整历史（不影响采集正确性）",
			"logSourceID", e.Key.LogSourceID, "generation", e.Key.SourceGeneration,
			"reclaim", e.Positions.Reclaim, "batches", len(e.DeliveryBatches), "error", err)
		return
	}
	if dropped == 0 {
		return
	}
	e.DeliveryBatches = kept
	slog.Debug("已裁剪水位之下的历史投递批次",
		"logSourceID", e.Key.LogSourceID, "generation", e.Key.SourceGeneration,
		"reclaim", e.Positions.Reclaim, "dropped", dropped, "kept", len(kept))
}
