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

// TestJournalReplayRejectsTrueCorruption 判据不放宽：同 seq 异内容／seq==0 ⇒ 仍拒 ✗；
// 同 seq 同内容（精确重复）⇒ 去重跳过 ✓。
func TestJournalReplayRejectsTrueCorruption(t *testing.T) {
	dir := t.TempDir()

	// ① 同 seq 异内容 ⇒ 拒 ✗。
	store, err := NewJSONLFileStore(filepath.Join(dir, "dup-diff.jsonl"))
	require.NoError(t, err)
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}))
	require.NoError(t, store.AppendEntry(JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-07"}}))
	_, err = NewJournalWithStore(store)
	require.ErrorContains(t, err, "same seq 1 with different content")

	// ② 同 seq 同内容 ⇒ 精确重复，跳过 ✓。
	store2, err := NewJSONLFileStore(filepath.Join(dir, "dup-same.jsonl"))
	require.NoError(t, err)
	e := JournalEntry{Seq: 1, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}
	require.NoError(t, store2.AppendEntry(e))
	require.NoError(t, store2.AppendEntry(e))
	j, err := NewJournalWithStore(store2)
	require.NoError(t, err)
	require.Len(t, j.Snapshot(), 1, "精确重复必须去重 ✓")

	// ③ seq==0（写入器不可能产生）⇒ 拒 ✗。
	store3, err := NewJSONLFileStore(filepath.Join(dir, "zero.jsonl"))
	require.NoError(t, err)
	require.NoError(t, store3.AppendEntry(JournalEntry{Seq: 0, Key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-10-06"}}))
	_, err = NewJournalWithStore(store3)
	require.ErrorContains(t, err, "zero seq")
}
