package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 红绿（#14 收尾 / 44157707）：**仅目录轮换**（同 owner、同 generation，只有 owner_dir_id 变化）
// ＝整窗重发新建的物理位置刷新 ✗，**不得**判成"互相矛盾的权威提交"。
//
// 现行红：原判据只放行 OWNER_SWITCHED+ATTACHED_STAGING 链 ⇒ 该形态被判冲突 ⇒ 分区永久
// CONFLICT_GENERATION/recovery_required（现网 inst:207 的 13 个日分区全中 ✗）。
// 同时守住不回归：真正的 owner/generation 变更**仍必须**判冲突 ✓（语义零放宽）。
func TestReplayJournalTreatsDirRefreshAsNonConflict(t *testing.T) {
	key := PartitionKey{StorageNamespace: "ns-dirrefresh", UTCDay: "2026-10-06"}
	base := NewStableRecord(key, OwnerHot, 1, "hot-projection-74")

	// ① 仅目录轮换（74→80）：期望**不判冲突**，且记录采用新目录。
	dirOnly := []JournalEntry{
		{Seq: 1, Key: key, AuthorityCommit: true, Owner: OwnerHot, Generation: 1, DirID: "hot-projection-74"},
		{Seq: 2, Key: key, AuthorityCommit: true, Owner: OwnerHot, Generation: 1, DirID: "hot-projection-80"},
	}
	rec := base.Clone()
	require.Empty(t, ReplayJournal(rec, dirOnly),
		"仅目录轮换（同 owner/同 generation）不得判权威冲突（inst:207 现场：整窗重发新建 hot-projection-NN）✗")
	require.Equal(t, "hot-projection-80", rec.OwnerDirID, "记录应采用轮换后的目录 ✓")

	// ② 不回归：真 owner 变更（hot→cold，generation 不变）**仍必须**判冲突。
	ownerChange := []JournalEntry{
		{Seq: 1, Key: key, AuthorityCommit: true, Owner: OwnerHot, Generation: 1, DirID: "hot-projection-74"},
		{Seq: 2, Key: key, AuthorityCommit: true, Owner: OwnerCold, Generation: 1, DirID: "cold-g2"},
	}
	rec2 := base.Clone()
	require.NotEmpty(t, ReplayJournal(rec2, ownerChange),
		"真正的 owner 变更必须仍判冲突（语义零放宽）✗")

	// ③ 不回归：generation 变更同样仍判冲突。
	genChange := []JournalEntry{
		{Seq: 1, Key: key, AuthorityCommit: true, Owner: OwnerHot, Generation: 1, DirID: "hot-projection-74"},
		{Seq: 2, Key: key, AuthorityCommit: true, Owner: OwnerHot, Generation: 2, DirID: "hot-projection-74"},
	}
	rec3 := base.Clone()
	require.NotEmpty(t, ReplayJournal(rec3, genChange),
		"generation 变更必须仍判冲突（语义零放宽）✗")
}
