package acquire

// WAL 重启恢复时的 acknowledged 重建（2026-10-28 生产停采事件）。
//
// ## 问题
//
// `WALEntry.acknowledged` 带 `json:"-"`，语义是「**本进程真实收到过该条正文的 ACK**」。
// 重启后它自然为空，原实现（wal.go 的 Restore / RestoreMixed）无条件置 nil。
//
// 这在「正常增量推进」的情况下是安全的：新数据还没投递，本就没有 ACK。
// 但在以下形态下会形成**死锁**（真机 2026-10-28）：
//
//	reclaim 落在无段覆盖处 → tryReclaim 报 ErrReclaimNoSegment
//	  → 走 backfillDeliveryConfirmedCoverage 补段
//	  → CaptureAcknowledgedPrefix 要求 entry.acknowledged != nil
//	  → 重启后全为 nil ⇒ 返回空 events
//	  → 补段放弃（no-confirmed-copy）⇒ 不建段
//	  → 下一轮重复，reclaim 永远停在原地
//	  → WAL 删不掉 ⇒ 撞 backlog 上限 ⇒ 采集暂停
//
// 生产特征很明确：10 个源 `delivery_pos` 已满（178 万），`reclaim_pos=0`，
// WAL 里 20165 条全部 `durable=1`，`request_done_ranges=[{0, 1784453}]`。
// 即「数据早就投递并确认过了，只是这个进程不知道」。
//
// ## 修法：用**精确持久化证据**重建，不靠区间推断
//
// 关键区分：`acknowledged` 不是「猜这条投过」，而是「这条的**完整正文**被确认过」。
// 账本里的 `RequestDoneRanges` **只是位置区间**，且有两个性质让它不能单独作为凭据：
//   - 两端各放宽 1 字节（`ledger.CoveredByPositionRanges`）；
//   - **永不裁剪**（只有 `DeliveryBatches` 会被 prune，ACK 区间 merge 后永久累积）。
//
// 仅凭区间重建有真实风险：位置空间一旦被复用（重读 / 槽位复用 / 回放替换），
// 旧区间会把从未投递的**新正文**当成已 ACK，再被 `reclaimEntryQualifiedLocked` 放行
// ⇒ 数据被剪掉。
//
// 所以本文件采用**双重凭据**，缺一不可：
//
//  1. 位置：该条 `Record` 完整落在某个 REQUEST_DONE 区间内，且条目 `Durable`；
//  2. 正文：`AckEvidence.Digests` 里该条目的摘要（取自 `source_wal.ack_body_digest`，
//     即**投递当时**那份正文的 BodyDigest）与当前正文重算出的摘要**逐字节相等**。
//
// 第 2 条是真正的防线：位置区间可以复用，正文摘要不能。正文被原位改写过
// （回放替换、inline↔ref 切换）⇒ 摘要不等 ⇒ 不重建，保持 nil，让常规路径重投。
//
// 拿不到证据的条目一个都不放行，default-deny 未动。

import (
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// WalAckKey 是 WAL 条目的主键标识（与 `source_wal` 的 PRIMARY KEY 同形），
// 用于把持久化的 ACK 正文摘要精确对应到恢复出来的条目。
//
// 导出（而不是包内私有）：ingest 侧要从索引库读回摘要并构造 `AckEvidence`。
type WalAckKey struct {
	Seq         uint64
	EventID     string
	RecordStart uint64
	RecordEnd   uint64
}

// ackKeyOf 由条目导出主键。
func ackKeyOf(entry WALEntry) WalAckKey {
	return WalAckKey{Seq: entry.Seq, EventID: entry.Event.EventID,
		RecordStart: entry.Event.Record.Start, RecordEnd: entry.Event.Record.End}
}

// AckEvidence 是恢复时可用的持久化 ACK 凭据。
//
// 零值（Ranges 为空）= 不启用重建，与旧行为逐字一致（default-deny）。
type AckEvidence struct {
	// Ranges 是账本里持久化的 REQUEST_DONE 位置区间（`ledger.RequestDoneRangesOf`）。
	//
	// 来源：`ledger.Entry.RequestDoneRanges`——只承接真实 REQUEST_DONE，
	// 请求结果前缀与回收水位都不生成 ACK。
	Ranges []ledger.PositionRange
	// Digests 是「投递当时那份正文」的 BodyDigest，键为 `walAckKey`。
	//
	// 来源：`source_wal.ack_body_digest`（由 persist 写入）。
	//
	// **nil 与空 map 语义不同**（必须分清）：
	//   - nil：调用方无法提供这个证据（旧库未升级等）
	//     ⇒ 退化为「只靠位置区间 + durable」；
	//   - 非 nil（含空 map）：调用方确实提供了，那么查不到摘要的条目就是**从未被 ACK**
	//     ⇒ 一律不重建（保守方向，防止“凭区间白送一个跟斗”）。
	Digests map[WalAckKey][32]byte
}

// restoreAcknowledged 按持久化 ACK 凭据为恢复后的 WAL 条目重建 `acknowledged`。
//
// 判定（三条全成立才重建，缺一不可）：
//  1. 条目 `Durable`；
//  2. 条目 `Record` 完整落在某个 REQUEST_DONE 区间内；
//  3. 若调用方提供了正文摘要映射（非 nil），则摘要必须逐字节相等。
func restoreAcknowledged(entries []WALEntry, ev AckEvidence) {
	for i := range entries {
		entries[i].acknowledged = nil
		if len(ev.Ranges) == 0 {
			continue
		}
		entry := &entries[i]
		if !entry.Durable {
			continue
		}
		if !ledger.CoveredByPositionRanges(ev.Ranges, entry.Event.Record.Start, entry.Event.Record.End) {
			continue
		}
		id, err := CommitIdentityOf(entry.Event)
		if err != nil {
			continue
		}
		if ev.Digests != nil {
			want, has := ev.Digests[ackKeyOf(*entry)]
			if !has || want != id.BodyDigest {
				// 没有「这份正文被 ACK 过」的持久化证据，或正文已被改写过 ⇒ 不重建。
				continue
			}
		}
		entries[i].acknowledged = &id
	}
}
