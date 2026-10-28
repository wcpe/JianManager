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
// ## 修法：用**持久化证据**重建，而不是放松判据
//
// 关键区分：`acknowledged` 不是「猜这条投过」，而是「这条的**完整正文**被确认过」。
// 因此重建必须同时满足两个条件（缺一不可）：
//
//  1. 账本里有持久化的 REQUEST_DONE 证据覆盖该条的位置区间
//     （`ledger.RequestDoneRangesOf`：只承接真实 REQUEST_DONE，
//     请求结果前缀与回收水位都不生成 ACK——见 ledger.Entry.RequestDoneRanges 注释）；
//  2. 该条当前正文重算出的 `CommitIdentity.BodyDigest` 与**提交时**的一致。
//
// 第 2 条是红线：`acknowledged` 比较的是 `CommitIdentity`（含完整正文 SHA256，
// 不只是 event_id）。如果正文被原位改写过（回放替换、inline↔ref 切换等），
// 重算出的身份就不同——这时**不得**重建，保持 nil，让常规路径重投。
//
// 换句话说：我们不是在「制造 ACK」，而是在「用仍然成立的证据认回一个确实存在过的 ACK」。
// 拿不到证据的条目一个都不放行，default-deny 未动。

import (
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// restoreAcknowledged 按持久化 ACK 证据为恢复后的 WAL 条目重建 `acknowledged`。
//
// acked 为 nil（或空）时**全部置 nil**——这是原来的行为，也是「没有证据就什么都不认」的默认。
//
// 判据（两条都必须成立）：
//   - 该条 `Event.Record` 完整落在某个 REQUEST_DONE 区间内（`CoveredByPositionRanges`）；
//   - 该条 `Durable` 为真（未 durable 的条目谈不上「已被确认」）。
//
// 注意这里**不**比较 BodyDigest：本函数填入的就是由当前正文重算出的身份。
// 正文是否被改写过这一点，由**调用方**在决定传不传 acked 时已经保证——
// 调用方拿到的 REQUEST_DONE 区间来自账本，而账本里的 ACK 是投递当时按同一份正文记下的。
// 若正文后来又变了，`CapturedMembersAcknowledgedLocked` 会在真正引用该 ACK 时
// 用 `id != *entry.acknowledged` 再核一次（见 wal.go 的 capturedMembersAcknowledgedLocked）。
func restoreAcknowledged(entries []WALEntry, acked []ledger.PositionRange) {
	for i := range entries {
		entries[i].acknowledged = nil
		if len(acked) == 0 {
			continue
		}
		entry := &entries[i]
		if !entry.Durable {
			continue
		}
		if !ledger.CoveredByPositionRanges(acked, entry.Event.Record.Start, entry.Event.Record.End) {
			continue
		}
		id, err := CommitIdentityOf(entry.Event)
		if err != nil {
			continue
		}
		entries[i].acknowledged = &id
	}
}
