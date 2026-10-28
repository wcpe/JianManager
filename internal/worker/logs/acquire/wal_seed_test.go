package acquire

// seedEntries 把一个既有条目集合装入内存 WAL。
//
// ## 为什么这是测试辅助而不是生产 API
//
// 2026-10-29：WAL 改为**纯内存队列**后，生产路径再也不会从磁盘恢复队列内容，
// 所以 `Restore` / `RestoreWithAck` / `RestoreMixed` / `RestoreMixedWithAck` /
// `restoreReservationsLocked` 整组被删掉了。
//
// 但仍有一批回归需要把「任意条目集合（含相同 Seq、非 Durable、来自回放）」直接装进队列
// 作为前置状态（pending cursor / prune / qualification / ACK 闸等仍是**内存**语义，与
// `source_wal` 表无关）——用 Append 做不到这些（Append 自增 Seq、恒为 Appended）。
// 因此把这一操作放在测试编译单元里，用**仍然存在**的生产机制实现，生产代码零残留。
//
// 与旧 Restore 的语义差异（刻意的）：
//   - 不再有任何“持久化 ACK 证据重建”——所有条目的 acknowledged 恒置 nil（default-deny）；
//   - 源身份校验保留（防夹具写错），预算重取与积压裁剪保留（它们是内存队列自身的语义）。

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func (w *WAL) seedEntries(entries []WALEntry) error {
	unlock := w.lockMutation(nil, true)
	defer unlock()
	for _, entry := range entries {
		if entry.Event.Source.LogSourceID != "" && entry.Event.Source.LogSourceID != w.key.LogSourceID {
			return fmt.Errorf("acquire: wal seed source mismatch %q", entry.Event.Source.LogSourceID)
		}
		if entry.Event.Source.SourceGeneration != "" && entry.Event.Source.SourceGeneration != w.key.SourceGeneration {
			return fmt.Errorf("acquire: wal seed generation mismatch %q", entry.Event.Source.SourceGeneration)
		}
	}
	restored := append([]WALEntry(nil), entries...)
	if w.revision == ^uint64(0) {
		return fmt.Errorf("acquire: WAL捕获序号已耗尽")
	}
	w.revision++
	w.pendingEpoch++
	for i := range restored {
		restored[i].Event = cloneWALEvent(restored[i].Event)
		restored[i].appendRevision = w.revision
		restored[i].captureToken = 0
		restored[i].captureBody = [32]byte{}
		restored[i].acknowledged = nil
	}
	// 预算：整体替换 ⇒ 归还旧条目的租约后重新取（失败保留 reservationErr，阻断后续 Append/Commit）。
	for i := range w.entries {
		w.entries[i].reservation.close()
	}
	for i := range restored {
		restored[i].reservation = nil
	}
	leases, err := reserveWALEntries(restored, w.reserveBytes)
	w.reservationErr = err
	if err != nil {
		w.entries = restored
		w.recountLocked()
		return err
	}
	for i := range restored {
		restored[i].reservation = leases[i]
	}
	w.entries = restored
	w.recountLocked() // #66-②：整体替换 ⇒ 重算一次（O(条数)，一次性）
	for _, entry := range entries {
		if entry.Durable && entry.Event.Record.End > w.lastDurableEnd {
			w.lastDurableEnd = entry.Event.Record.End
		}
	}
	// 装入后立刻裁一次积压：本夹具可能直接构造出超出内存上界的队列。
	w.enforceBacklogLimitLocked(0)
	return nil
}

// seedEvents 是 seedEntries 的便捷形态：把一组事件按给定起点顺序装入并全部标为已提交。
func (w *WAL) seedEvents(events []logtypes.Event, firstSeq uint64) error {
	entries := make([]WALEntry, 0, len(events))
	for i, ev := range events {
		entries = append(entries, WALEntry{Seq: firstSeq + uint64(i), Event: ev, Appended: true, Durable: true})
	}
	return w.seedEntries(entries)
}
