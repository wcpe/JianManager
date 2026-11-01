package ingest

// 本文件是「不可裁定身份冲突的真实产生条件」取证（2026-11-01）。
//
// 目的：回答「冲突到底怎么产生的」，用实验而不是推理。
//
// 已知（前置实验已证）：
//   - 堆栈续行会被归并进头行（一条多行事件 = 一个区间），区间是**绝对字节位置**；
//   - 不同物理行绝不共享同一对 (record_start, record_end)。
// ⇒ 「批写把 tick 内多行拼成一批」**不会**造成同 event_id；它们各有各的区间。
//
// 那么同 event_id（= 同源+同代次+同区间+同 parser）就只能来自：
//   **同一个物理字节区间被解析了两次，而两次得到的内容不同**。

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestProbeMultilineEventIsSingleRange 证实：多行事件归并成一个区间，各自独立，不共享区间。
func TestIdentityConflictOriginMultilineIsSingleRange(t *testing.T) {
	lines := []string{
		"[12:00:01] [Server thread/ERROR]: Could not pass event PlayerInteractEvent to EssentialsX v2.20.1",
		"java.lang.NullPointerException: null",
		"    at com.earth2me.essentials.Essentials.onPlayerInteract(Essentials.java:123)",
		"    at org.bukkit.plugin.RegisteredListener.callEvent(RegisteredListener.java:70)",
		"[12:00:02] [Server thread/INFO]: next event starts here",
	}
	res := normalize.ProcessLines(lines, normalize.Options{
		Source:   logtypes.SourceIdentity{LogSourceID: "probe", SourceGeneration: "g1"},
		Stream:   "stdout",
		BaseTime: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	})
	for _, ev := range res.Events {
		t.Logf("record=[%d,%d) len=%d msg=%.30q", ev.Record.Start, ev.Record.End, len(ev.Message), ev.Message)
	}
	require.Len(t, res.Events, 2, "4 行堆栈 + 1 行新事件 ⇒ 2 个事件（堆栈归并）")
	require.Contains(t, res.Events[0].Message, "NullPointerException", "堆栈必须归并进头行")
	require.Greater(t, res.Events[0].Record.End, res.Events[0].Record.Start, "多行事件的区间跨多行")
	require.NotEqual(t, res.Events[0].Record, res.Events[1].Record, "两个事件区间互不相同")
}

// TestProbeDifferentRangeMeansDifferentID —— 否定形态 A（后人不要重走）：
// 残行 vs 完整行虽然起始位置相同，但 record_end 不同 ⇒ **event_id 不同** ⇒ 根本不进入身份裁定。
func TestIdentityConflictOriginDifferentRangeMeansDifferentID(t *testing.T) {
	src := logtypes.SourceIdentity{LogSourceID: "inst:probe/file", SourceGeneration: "g1", ParserVersion: "1.0.0"}
	partial := logtypes.BuildEvent(src, logtypes.RecordRange{Start: 100, End: 150},
		"2026-11-01T12:00:00Z", "2026-11-01T12:00:00Z", "ERROR", "stdout", strings.Repeat("x", 30))
	complete := logtypes.BuildEvent(src, logtypes.RecordRange{Start: 100, End: 200},
		"2026-11-01T12:00:00Z", "2026-11-01T12:00:00Z", "ERROR", "stdout", strings.Repeat("x", 80))

	require.NotEqual(t, partial.EventID, complete.EventID,
		"区间不同 ⇒ event_id 不同 ⇒ 不进入身份裁定（不是冲突）")
	t.Logf("残行 id=%.8s，完整行 id=%.8s —— 不同身份，各自入账（在段里共存）",
		partial.EventID, complete.EventID)
}

// TestProbeUnresolvableNeedsSameRangeSameLength —— 冲突的**唯一**输入形态：
// 同源+同代次+**同区间** ⇒ 同 event_id；且**等长**（长度判据不可用）+ **内容不同** ⇒ 不可裁定。
func TestIdentityConflictOriginUnresolvableShape(t *testing.T) {
	src := logtypes.SourceIdentity{LogSourceID: "inst:probe/file", SourceGeneration: "g1", ParserVersion: "1.0.0"}
	rec := logtypes.RecordRange{Start: 100, End: 200}
	a := logtypes.BuildEvent(src, rec, "2026-11-01T12:00:00Z", "2026-11-01T12:00:00Z", "ERROR", "stdout", strings.Repeat("x", 50))
	b := logtypes.BuildEvent(src, rec, "2026-11-01T12:00:00Z", "2026-11-01T12:00:00Z", "ERROR", "stdout", strings.Repeat("y", 50))
	require.Equal(t, a.EventID, b.EventID, "同区间 ⇒ 同 event_id")

	require.Equal(t, len(a.Message), len(b.Message), "① 等长 ⇒ 长度判据不可用")
	require.NotEqual(t, a.Message, b.Message, "② 内容不同 ⇒ message 判据不命中形态 2")
	keep, take := adjudicateSameIntervalIdentity(
		eventIdentity{hash: a.CanonicalHash, start: rec.Start, end: rec.End, size: len(a.Message),
			msg: a.Message, level: a.Level, stream: a.Stream}, b, len(b.Message))
	require.False(t, keep)
	require.False(t, take, "③ 两个出口都不命中 ⇒ 不可裁定 ⇒ 进 default 分支（即「身份冲突」）")
	t.Logf("⇒ 冲突输入形态：同区间 [%d,%d) + 等长 %d + 内容不同 ⇒ 不可裁定", rec.Start, rec.End, len(a.Message))
}

// TestProbeReaderReanchorsAfterTruncate —— 形态 B（真实可发生）：
// 源文件在**同一对绝对字节位置**上的内容变了（原地截断/重写、或轮转后同代位置被重用）。
// 此时区间相同、长度也相同 ⇒ 同 event_id + 同长度 + 异内容 ⇒ 不可裁定。
func TestIdentityConflictOriginSameBytesRewrittenMustEqualLength(t *testing.T) {
	// 必须走**真实采集路径**（NormalizeBoundary）：它把行号 record 重映射成绝对字节位置。
	// 注意：单行 Feed 不会立刻闭合事件（要等下一行或冲刷），所以每次喂两行取第一条。
	mk := func(body string) logtypes.Event {
		res := pipeline.NewNormalizeBoundary(normalize.Options{
			Source:   logtypes.SourceIdentity{LogSourceID: "inst:probe/file", SourceGeneration: "g1"},
			Stream:   "stdout",
			BaseTime: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
		})
		line := "[12:00:01] [Server thread/INFO]: " + body
		res.Feed([]byte(line), 0, uint64(len(line))+1)
		next := "[12:00:02] [Server thread/INFO]: boundary"
		res.Feed([]byte(next), uint64(len(line))+1, uint64(len(line))+1+uint64(len(next))+1)
		ev := res.DrainEvents()
		require.NotEmpty(t, ev)
		return ev[0]
	}

	a := mk(strings.Repeat("A", 40))
	b := mk(strings.Repeat("B", 40))
	t.Logf("第一次: record=[%d,%d) id=%.8s len=%d", a.Record.Start, a.Record.End, a.EventID, len(a.Message))
	t.Logf("第二次: record=[%d,%d) id=%.8s len=%d", b.Record.Start, b.Record.End, b.EventID, len(b.Message))

	require.Equal(t, a.Record, b.Record, "同长度行写在同一位置 ⇒ 绝对区间完全相同")
	require.Equal(t, a.EventID, b.EventID, "同区间 ⇒ 同 event_id")
	require.Equal(t, len(a.Message), len(b.Message), "等长 ⇒ 长度判据不可用")
	require.NotEqual(t, a.Message, b.Message, "内容不同")

	keep, take := adjudicateSameIntervalIdentity(
		eventIdentity{hash: a.CanonicalHash, start: a.Record.Start, end: a.Record.End, size: len(a.Message),
			msg: a.Message, level: a.Level, stream: a.Stream}, b, len(b.Message))
	require.False(t, keep)
	require.False(t, take)
	t.Logf("⇒ 这是**真实可发生**的冲突形态：同一对绝对字节位置上的内容被改写了")
}
