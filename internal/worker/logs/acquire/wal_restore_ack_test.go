package acquire

// 重启后从持久化 REQUEST_DONE 证据重建 acknowledged 的回归（2026-10-28 生产停采）。
//
// ## 背景
//
// `WALEntry.acknowledged` 是进程内态（json:"-"），重启即丢。而补段路径
// `CaptureAcknowledgedPrefix` 要求它非 nil。于是当「delivery 早已满、reclaim 却停在 0」时：
//
//	重启 → 补段拿不到 ACK → 不建段 → reclaim 不动 → WAL 撞上限 → 采集暂停
//
// 真机形态：10 个源 `delivery_pos=1784453`、`reclaim_pos=0`、WAL 20165 条全 durable、
// `request_done_ranges=[{from:0,to:1784453}]`。
//
// ## 本文件钉住四条性质
//
//  1. 有 REQUEST_DONE 证据且 durable ⇒ 重启后 acknowledged 被重建，
//     CaptureAcknowledgedPrefix 能取到区间（死锁解开）。
//  2. 无证据（acked 为空）⇒ 全部 nil，与旧行为逐字一致（default-deny 未动）。
//  3. **红线**：未 durable 的条目即使位置落在已确认区间内，也**不得**重建
//     （未确认投递的数据一个字节都不许被越过）。
//  4. **红线**：位置不在任何 REQUEST_DONE 区间内的条目**不得**重建。
//
// 另有一条往返稳定性验证：CommitIdentity 的 BodyDigest 用 json.Marshal(event)，
// 而 body 持久化后要反序列化再重算——若两者不一致，重建出的身份就是假的。

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func ackTestKey() ledger.SourceKey {
	return ledger.SourceKey{LogSourceID: "inst:ack/file", SourceGeneration: "g1"}
}

// ackTestEntries 造三条相邻的事件：[0,10) [10,20) [20,30)。
func ackTestEntries(t *testing.T) []WALEntry {
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
// 为什么单独测：重建 acknowledged 的方法是用**从磁盘反序列化回来的** event
// 重算 CommitIdentity。只要 Fields（map）之类的序列化不稳定，重建出的身份就是假的，
// 而它与后续 CapturedMembersAcknowledgedLocked 的比对就会永远不匹配。
func TestCommitIdentityRoundtripStable(t *testing.T) {
	entries := ackTestEntries(t)
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
			"正文 JSON 往返后重算的 CommitIdentity 必须逐字段相等，否则重建的 ACK 是假的")
	}
}

// TestRestoreWithAckRebuildsCoveredEntries 是本修复的正例：
// 有 REQUEST_DONE 证据的区间，重启后 acknowledged 必须被重建。
func TestRestoreWithAckRebuildsCoveredEntries(t *testing.T) {
	led := ledger.New()
	w := NewWAL(led, ackTestKey())

	// 账本声明 [0,20) 已确认投递。
	acked := []ledger.PositionRange{{From: 0, To: 20}}
	require.NoError(t, w.RestoreWithAck(ackTestEntries(t), acked))

	// 前两条落在 [0,20) 内 ⇒ 能组成连续前缀 [0,20)。
	events, _, err := w.CaptureAcknowledgedPrefix(0, 30, 10)
	require.NoError(t, err)
	require.Len(t, events, 2,
		"落在 REQUEST_DONE 区间内的 durable 条目必须能重建 ACK（否则补段永远失败）")
	require.Equal(t, uint64(0), events[0].Record.Start)
	require.Equal(t, uint64(20), events[len(events)-1].Record.End)
}

// TestRestoreWithoutAckKeepsDefaultDeny 守住默认行为：
// 不传证据时全部 nil，与改动前逐字一致。
func TestRestoreWithoutAckKeepsDefaultDeny(t *testing.T) {
	led := ledger.New()
	w := NewWAL(led, ackTestKey())
	require.NoError(t, w.Restore(ackTestEntries(t)))

	events, _, err := w.CaptureAcknowledgedPrefix(0, 30, 10)
	require.NoError(t, err)
	require.Empty(t, events,
		"没有持久化 ACK 证据时不得凭空重建（default-deny）")
}

// TestRestoreWithAckSkipsNonDurable 是红线用例之一：未 durable 的条目不得重建 ACK。
//
// 为什么直接测 `restoreAcknowledged` 而不是走 CaptureAcknowledgedPrefix：
// 后者**自身也**有 `!entry.Durable` 判据（wal.go:1760），构成纵深防御。
// 若只做端到端断言，去掉本函数的 durable 判据时用例仍然绿（变异存活）——
// 本用例第一版就是这样，被变异验证当场抑住。故此处直接断言内存状态。
func TestRestoreWithAckSkipsNonDurable(t *testing.T) {
	entries := ackTestEntries(t)
	entries[0].Durable = false // 第一条未 durable

	restoreAcknowledged(entries, []ledger.PositionRange{{From: 0, To: 30}})

	require.Nil(t, entries[0].acknowledged,
		"未 durable 的条目不得重建 ACK（红线：未确认投递的数据不许被越过）")
	require.NotNil(t, entries[1].acknowledged,
		"同区间内 durable 的邻居应当重建（否则本用例测不到区分力）")
}

// TestRestoreWithAckSkipsUncoveredRange 是红线用例之二：位置不在 REQUEST_DONE 区间内不得重建。
//
// 同样直接断言内存状态，理由同上（纵深防御会让纯端到端用例失去区分力）。
func TestRestoreWithAckSkipsUncoveredRange(t *testing.T) {
	entries := ackTestEntries(t)

	// [0,5) 只覆盖第一条的一半，而条目是 [0,10)——完整覆盖要求整条落在区间内。
	restoreAcknowledged(entries, []ledger.PositionRange{{From: 0, To: 5}})

	require.Nil(t, entries[0].acknowledged,
		"只有完整落在 REQUEST_DONE 区间内的条目才可重建 ACK")
	require.Nil(t, entries[1].acknowledged)
	require.Nil(t, entries[2].acknowledged)

	// 反向对照：区间完整覆盖前两条时，前两条应当重建。
	entries2 := ackTestEntries(t)
	restoreAcknowledged(entries2, []ledger.PositionRange{{From: 0, To: 20}})
	require.NotNil(t, entries2[0].acknowledged)
	require.NotNil(t, entries2[1].acknowledged)
	require.Nil(t, entries2[2].acknowledged)
}

// TestRestoreMixedWithAckRebuildsCoveredEntries 验证引用恢复路径同样生效
//（生产上 saved.WALRefs 非空时走的就是这条）。
func TestRestoreMixedWithAckRebuildsCoveredEntries(t *testing.T) {
	led := ledger.New()
	w := NewWAL(led, ackTestKey())
	entries := ackTestEntries(t)

	// 把正文放进「事件段存储」，恢复时按 EventID 水合。
	bodies := make(map[string]logtypes.Event, len(entries))
	refs := make([]WALRef, 0, len(entries))
	for _, entry := range entries {
		bodies[entry.Event.EventID] = entry.Event
		refs = append(refs, WALRef{Seq: entry.Seq, Appended: entry.Appended, Durable: entry.Durable,
			EventID: entry.Event.EventID, RecordStart: entry.Event.Record.Start, RecordEnd: entry.Event.Record.End})
	}
	hydrate := func(eventID string) (logtypes.Event, bool) { ev, ok := bodies[eventID]; return ev, ok }

	missing := w.RestoreMixedWithAck(nil, refs, hydrate, []ledger.PositionRange{{From: 0, To: 20}})
	require.Empty(t, missing)

	events, _, err := w.CaptureAcknowledgedPrefix(0, 30, 10)
	require.NoError(t, err)
	require.Len(t, events, 2, "引用恢复路径也必须能重建 ACK")
}

// TestRestoreWithAckEmptyRangesIsNoop 守住向后兼容：传空切片 = 没传。
func TestRestoreWithAckEmptyRangesIsNoop(t *testing.T) {
	led := ledger.New()
	w := NewWAL(led, ackTestKey())
	require.NoError(t, w.RestoreWithAck(ackTestEntries(t), []ledger.PositionRange{}))

	events, _, err := w.CaptureAcknowledgedPrefix(0, 30, 10)
	require.NoError(t, err)
	require.Empty(t, events)
}

// TestCommitIdentityDigestCoversBody 验证 BodyDigest 真的覆盖了正文：
// 同 event_id / 同区间但正文不同 ⇒ 身份必须不同。
// 这是重建 ACK 不会“误认正文已被改写的条目”的根据。
func TestCommitIdentityDigestCoversBody(t *testing.T) {
	a := ackTestEntries(t)[0]
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
