package catalog

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJournalReplayToleratesOutOfOrderLines 验收 2026-10-06 修复：journal 文件"行序≠seq 序"
// （崩溃残留尾／双写者先后共文件）不得再拒启 ✗ —— 按 seq 归并后重放即可 ✓。
func TestJournalReplayToleratesOutOfOrderLines(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLFileStore(filepath.Join(dir, "journal.jsonl"))
	require.NoError(t, err)

	// 刻意乱序写：seq=2 先、seq=1 后（模拟现场 got 33726 last 33787 的形态 ✓）。
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 2, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}))
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}))

	j, err := NewJournalWithStore(store)
	require.NoError(t, err, "行序乱序不得再拒启（现场确定性失败 ✗）")
	require.Equal(t, uint64(2), j.LastSeq())
	snap := j.Snapshot()
	require.Len(t, snap, 2)
	require.Equal(t, uint64(1), snap[0].Seq, "重放必须按 seq 升序应用 ✓")
	require.Equal(t, uint64(2), snap[1].Seq)
}

// TestJournalReplayRenumberedEntriesStayOrdered 回归 2026-10-06 第 7 窗实测缺陷：
// 改判条目曾按"原位置"插回 ⇒ 归并结果乱序 ⇒ 重放再撞 ErrJournalOrder ✗。交错冲突形态必须
// 一并覆盖（[1A, 3C, 1B] ⇒ 改判后须重排为 [1,2,3] ✓）。
func TestJournalReplayRenumberedEntriesStayOrdered(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLFileStore(filepath.Join(dir, "interleaved.jsonl"))
	require.NoError(t, err)
	// 文件行序：seq1(A)、seq3(C)、seq1(B 异内容) ⇒ 归并后若不重排 = [1A, 4B, 3C] ✗。
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "d-A"}}))
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 3, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "d-C"}}))
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "d-B"}}))

	j, err := NewJournalWithStore(store)
	require.NoError(t, err, "交错冲突+改判后必须按 seq 重排，不得再撞序号回退 ✗")
	snap := j.Snapshot()
	require.Len(t, snap, 3)
	for i := 1; i < len(snap); i++ {
		require.Less(t, snap[i-1].Seq, snap[i].Seq, "重放序列必须严格递增 ✓")
	}
	require.Equal(t, "d-A", snap[0].Key.UTCDay)
	require.Equal(t, "d-C", snap[1].Key.UTCDay)
	require.Equal(t, "d-B", snap[2].Key.UTCDay, "改判者排到全局尾部 ✓")
}

// TestJournalReplayRepairsSameSeqCollision 判据（现场修订）：同 seq 异内容＝**双写者各自计数**
// 的现场形态 ⇒ 不丢任何一条：后位置者改判到全局尾部 ✓；同 seq 同内容（精确重复）⇒ 去重跳过 ✓；
// seq==0 ⇒ 仍拒 ✗（写入器不可能产生）。
func TestJournalReplayRepairsSameSeqCollision(t *testing.T) {
	dir := t.TempDir()

	// ① 同 seq 异内容 ⇒ 改判不丢 ✓。
	store, err := NewJSONLFileStore(filepath.Join(dir, "dup-diff.jsonl"))
	require.NoError(t, err)
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}))
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-07"}}))
	j, err := NewJournalWithStore(store)
	require.NoError(t, err, "同 seq 异内容必须可修复重放（不丢任何一条 ✗）")
	snap := j.Snapshot()
	require.Len(t, snap, 2)
	require.Equal(t, uint64(1), snap[0].Seq)
	require.Equal(t, "2026-10-06", snap[0].Key.UTCDay)
	require.Equal(t, uint64(2), snap[1].Seq, "后位置者改判到全局尾部 ✓")
	require.Equal(t, "2026-10-07", snap[1].Key.UTCDay)

	// ② 同 seq 同内容 ⇒ 精确重复，跳过 ✓。
	store2, err := NewJSONLFileStore(filepath.Join(dir, "dup-same.jsonl"))
	require.NoError(t, err)
	e := JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}
	require.NoError(t, store2.AppendEntry(e))
	require.NoError(t, store2.AppendEntry(e))
	j2, err := NewJournalWithStore(store2)
	require.NoError(t, err)
	require.Len(t, j2.Snapshot(), 1, "精确重复必须去重 ✓")

	// ③ seq==0（写入器不可能产生）⇒ 拒 ✗。
	store3, err := NewJSONLFileStore(filepath.Join(dir, "zero.jsonl"))
	require.NoError(t, err)
	require.NoError(t, store3.AppendEntry(JournalEntry{Seq: 0, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}))
	_, err = NewJournalWithStore(store3)
	require.ErrorContains(t, err, "zero seq")
}
