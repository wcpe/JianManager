package acquire

// CommitIdentity 的往返稳定性与正文覆盖度回归。
//
// 这两条原属于 wal_restore_ack_test.go（重启时从持久化 ACK 证据重建 acknowledged）。
// 2026-10-29 WAL 改为纯内存队列后，重建路径（RestoreWithAck / restoreAcknowledged）整体删除，
// 但 CommitIdentity 仍是**内存投递资格**的核验凭据（CaptureDelivery / CaptureCommitted /
// RecordCurrentResults / reclaimEntryQualifiedLocked 都靠它逐字段比对），因此这两条保留。

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// identityTestEntries 造三条相邻的事件：[0,10) [10,20) [20,30)。
func identityTestEntries(t *testing.T) []WALEntry {
	t.Helper()
	mk := func(seq uint64, start, end uint64, msg string) WALEntry {
		ev := logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "inst:ack/file", SourceGeneration: "g1", ParserVersion: "v1"},
			logtypes.RecordRange{Start: start, End: end},
			"2026-10-28T00:00:00Z", "2026-10-28T00:00:01Z", "INFO", "stdout", msg)
		return WALEntry{Seq: seq, Event: ev, Appended: true, Durable: true}
	}
	return []WALEntry{mk(1, 0, 10, "a"), mk(2, 10, 20, "b"), mk(3, 20, 30, "c")}
}

// TestCommitIdentityRoundtripStable 验证「正文经 JSON 往返后重算的身份不变」。
//
// 为什么单独测：投递捕获与回执交集都是拿**同一份正文重算的** CommitIdentity 做逐字节比对。
// 只要 Fields（map）之类序列化不稳定，两侧算出的身份就会不相等，投递/回收资格会永远不匹配。
func TestCommitIdentityRoundtripStable(t *testing.T) {
	entries := identityTestEntries(t)
	for _, entry := range entries {
		entry.Event.Fields = map[string]string{"zzz": "2", "aaa": "1", "mod": "core"}
		orig, err := CommitIdentityOf(entry.Event)
		require.NoError(t, err)

		raw, err := json.Marshal(&entry.Event)
		require.NoError(t, err)
		var back logtypes.Event
		require.NoError(t, json.Unmarshal(raw, &back))

		again, err := CommitIdentityOf(back)
		require.NoError(t, err)
		require.Equal(t, orig, again,
			"正文 JSON 往返后重算的 CommitIdentity 必须逐字段相等，否则资格比对是假的")
	}
}

// TestCommitIdentityDigestCoversBody 验证 BodyDigest 真的覆盖了正文：
// 同 event_id / 同区间但正文不同 ⇒ 身份必须不同。
// 这是 ACK / 回收资格不会误认「正文已被改写的条目」的根据。
func TestCommitIdentityDigestCoversBody(t *testing.T) {
	a := identityTestEntries(t)[0]
	b := a
	b.Event.Message = a.Event.Message + "-changed"

	ida, err := CommitIdentityOf(a.Event)
	require.NoError(t, err)
	idb, err := CommitIdentityOf(b.Event)
	require.NoError(t, err)

	require.Equal(t, ida.EventID, idb.EventID, "event_id 只由区间与源身份派生，正文变了也不变")
	require.NotEqual(t, ida.BodyDigest, idb.BodyDigest,
		"BodyDigest 必须覆盖完整正文，否则无法识别正文已被改写的条目")
	expected := sha256.Sum256([]byte("probe"))
	require.NotEqual(t, expected, ida.BodyDigest)
}
