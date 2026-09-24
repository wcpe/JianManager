package normalize

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// FR-474 规格 §5：「跨午夜/无时间/损坏编码/超长堆栈可恢复；事件数和行数统计分别正确」。
//
// 本文件补齐此前**无任何测试覆盖**的两类边界：跨午夜与无时间。
// （损坏编码见 pipeline/encoding_integrity_test.go；超长堆栈见 TestLimitExceededTruncated。）

// 跨午夜：时刻相对 base 过远时回拨一天，不得把时间猜成未来。
func TestMidnightRolloverBackdatesInsteadOfGuessingFuture(t *testing.T) {
	// base 锚定 2026-09-21 00:05（刚过午夜）。
	opts := testOpts("stdout")
	opts.Location = time.UTC
	opts.BaseTime = time.Date(2026, 9, 21, 0, 5, 0, 0, time.UTC)
	n := New(opts)

	// 23:59:00 与 base 相差约 23h54m（>12h）→ 应判为前一天，而不是「今天 23:59（未来）」。
	n.Feed(`[23:59:00] [Server thread/INFO]: 午夜前一行`)
	ev, ok := n.FlushPartial()
	require.True(t, ok)
	got, err := time.Parse(time.RFC3339Nano, ev.EventTimeUTC)
	require.NoError(t, err)
	want := time.Date(2026, 9, 20, 23, 59, 0, 0, time.UTC)
	require.True(t, got.Equal(want),
		"跨午夜回拨：应为 %s，实得 %s（不得猜成未来）", want.Format(time.RFC3339), got.Format(time.RFC3339))
}

// 跨午夜：稍晚于 base 的时刻应留在当天（不误判成后一天）。
func TestMidnightKeepsNearbyTimeOnSameDay(t *testing.T) {
	opts := testOpts("stdout")
	opts.Location = time.UTC
	opts.BaseTime = time.Date(2026, 9, 21, 0, 5, 0, 0, time.UTC)
	n := New(opts)

	n.Feed(`[00:06:00] [Server thread/INFO]: 午夜后一行`)
	ev, ok := n.FlushPartial()
	require.True(t, ok)
	got, err := time.Parse(time.RFC3339Nano, ev.EventTimeUTC)
	require.NoError(t, err)
	want := time.Date(2026, 9, 21, 0, 6, 0, 0, time.UTC)
	require.True(t, got.Equal(want), "相近时刻应留在当天：应为 %s，实得 %s",
		want.Format(time.RFC3339), got.Format(time.RFC3339))
}

// 无时间行：不发明时间，但必须保留原文且标记解析状态。
func TestLineWithoutClockKeepsRawAndDoesNotInventTime(t *testing.T) {
	opts := testOpts("stdout")
	n := New(opts)

	raw := `完全无法解析的一行，没有时间戳也没有级别`
	// 无可识别事件头的行按 RAW 立即产出（不会被当作待闭合的多行事件）。
	evs := n.Feed(raw)
	require.Len(t, evs, 1)
	ev := evs[0]

	require.Equal(t, StatusRaw, ParseStatus(ev.Fields[FieldParseStatus]),
		"无法解析的行必须标记 RAW，不得伪装成 OK")
	require.Equal(t, "", ev.Level, "解析失败绝不发明 level")
	require.Contains(t, ev.Message, raw, "解析失败必须保留原文")
	// 无语义时间：事件时间留空由上层回退 ingest 时间，不由本层发明。
	require.Equal(t, "", ev.EventTimeUTC, "本层不得为无时间行发明事件时间")
}

// 事件数与行数统计必须分列且各自正确（规格 §5）。
func TestEventAndLineCountsAreSeparateAndCorrect(t *testing.T) {
	lines := []string{
		`[12:00:00] [Server thread/ERROR]: Encountered an unexpected exception`,
		`java.lang.NullPointerException: boom`,
		`	at a.A(A.java:1)`, // 缩进续行
		`Caused by: java.lang.IllegalStateException: nested`,
		`	at b.B(B.java:2)`,
		`[12:00:01] [Server thread/INFO]: done`, // 新事件起点
	}
	opts := testOpts("stdout")
	n := New(opts)
	for _, line := range lines {
		n.Feed(line)
	}
	res := n.Result()

	require.Equal(t, 6, res.Stats.LineCount, "行数应为原始行数 6")
	// 前 5 行归并为 1 个已闭合事件；末行 INFO 仍是未闭合尾行，计入 PendingLines 而非 EventCount。
	require.Equal(t, 1, res.Stats.EventCount, "已闭合事件数应为 1，不得与行数混用")
	require.Equal(t, 1, res.Stats.PendingLines, "末行应处于未闭合状态")
	require.Equal(t, 1, len(res.Events))
}
