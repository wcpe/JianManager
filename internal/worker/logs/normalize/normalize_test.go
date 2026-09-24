package normalize

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func testOpts(stream string) Options {
	return Options{
		Source: logtypes.SourceIdentity{
			LogSourceID:      "src-npe",
			SourceGeneration: "g1",
		},
		Stream:   stream,
		BaseTime: time.Date(2026, 9, 20, 12, 0, 1, 0, time.UTC),
		Location: time.UTC,
		// 测试用 Result()/Events() 检查全部产出，故显式开启事件保留
		// （生产流式路径不保留，见 Options.RetainEvents）。
		RetainEvents: true,
	}
}

// FR-474：NPE 堆栈 + Caused by 归并为 ONE event；下一时间戳行才开新事件。
func TestNPEStackMergesIntoOneEvent(t *testing.T) {
	lines := []string{
		`[12:00:00] [Server thread/ERROR]: Encountered an unexpected exception`,
		`java.lang.NullPointerException: Cannot invoke "String.length()"`,
		`	at net.minecraft.server.MinecraftServer.tickChildren(MinecraftServer.java:1)`,
		`	at net.minecraft.server.MinecraftServer.tickServer(MinecraftServer.java:2)`,
		`Caused by: java.lang.IllegalStateException: nested`,
		`	at com.example.inner(Inner.java:9)`,
		`[12:00:01] [Server thread/INFO]: Done (1.0s)`,
	}

	opts := testOpts("stdout")
	n := New(opts)
	var completed []logtypes.Event
	for _, line := range lines {
		completed = append(completed, n.Feed(line)...)
	}

	require.Len(t, completed, 1, "堆栈+Caused by 必须并成一条事件，由下一时间戳闭合")
	ev := completed[0]
	require.Equal(t, "ERROR", ev.Level)
	require.Equal(t, ParserVersion, ev.Source.ParserVersion)
	require.Equal(t, "OK", ev.Fields[FieldParseStatus])
	require.Equal(t, uint64(0), ev.Record.Start)
	require.Equal(t, uint64(5), ev.Record.End)

	msg := ev.Message
	require.Contains(t, msg, "NullPointerException")
	require.Contains(t, msg, "Caused by: java.lang.IllegalStateException")
	require.Contains(t, msg, "MinecraftServer.java:1")
	require.Contains(t, msg, "Inner.java:9")
	require.Equal(t, msg, ev.Fields[FieldMessage])
	require.Equal(t, 6, strings.Count(msg, "\n")+1, "_msg 应覆盖全部原始行")
	require.NotEmpty(t, ev.EventID)
	require.NotEmpty(t, ev.CanonicalHash)

	// 事件身份可复算
	wantID := logtypes.EventID(ev.Source, ev.Record)
	require.Equal(t, wantID, ev.EventID)

	// Flush 剩余 Done 行后：事件数 vs 行数
	_, ok := n.FlushPartial()
	require.True(t, ok)
	res := n.Result()
	require.Equal(t, 7, res.Stats.LineCount)
	require.Equal(t, 2, res.Stats.EventCount)
	require.Less(t, res.Stats.EventCount, res.Stats.LineCount, "事件数不得与原文行数混用")
}

// FR-474：Go slog level=INFO（stderr）不得按流兜底成 ERROR。
func TestSlogINFOOnStderr(t *testing.T) {
	line := `time=2026-09-20T12:24:28.732+08:00 level=INFO msg=访问 方法=GET 路径=/beacon/v2/agent/registration 状态=200`
	opts := testOpts("stderr")
	n := New(opts)
	require.Empty(t, n.Feed(line))

	ev, ok := n.FlushPartial()
	require.True(t, ok)
	require.Equal(t, "INFO", ev.Level)
	require.Equal(t, "stderr", ev.Stream)
	require.Equal(t, "OK", ev.Fields[FieldParseStatus])
	require.Equal(t, line, ev.Message)
	require.True(t, strings.HasPrefix(ev.EventTimeUTC, "2026-09-20T04:24:28"), "slog time= 应转为 UTC，got %s", ev.EventTimeUTC)

	res := n.Result()
	require.Equal(t, 1, res.Stats.EventCount)
	require.Equal(t, 1, res.Stats.LineCount)
}

// FR-474：半条事件 FlushPartial 可显式吐出（PARTIAL）或丢弃。
func TestPartialFlushEmitOrDiscard(t *testing.T) {
	lines := []string{
		`[12:00:00] [Server thread/ERROR]: boom`,
		`	at a.A(A.java:1)`,
	}

	opts := testOpts("stdout")
	n := New(opts)
	for _, line := range lines {
		require.Empty(t, n.Feed(line))
	}

	res := n.Result()
	require.Equal(t, 2, res.Stats.LineCount)
	require.Equal(t, 0, res.Stats.EventCount)
	require.Equal(t, 2, res.Stats.PendingLines)

	ev, ok := n.FlushPartial()
	require.True(t, ok)
	require.Equal(t, StatusPartial, ParseStatus(ev.Fields[FieldParseStatus]))
	require.Equal(t, "ERROR", ev.Level)
	require.Contains(t, ev.Message, "boom")
	require.Contains(t, ev.Message, "at a.A(A.java:1)")
	require.Equal(t, uint64(0), ev.Record.Start)
	require.Equal(t, uint64(1), ev.Record.End)

	res = n.Result()
	require.Equal(t, 1, res.Stats.EventCount)
	require.Equal(t, 2, res.Stats.LineCount)
	require.Equal(t, 1, res.Stats.PartialEvents)
	require.Equal(t, 0, res.Stats.PendingLines)

	// 第二次：丢弃路径
	n2 := New(opts)
	for _, line := range lines {
		n2.Feed(line)
	}
	dropped, ok := n2.DiscardPartial()
	require.True(t, ok)
	require.Equal(t, 2, dropped)
	res2 := n2.Result()
	require.Equal(t, 0, res2.Stats.EventCount)
	require.Equal(t, 2, res2.Stats.LineCount)
	require.Equal(t, 0, res2.Stats.PendingLines, "DiscardPartial 后缓冲已清空")
}

// FR-474：超过 multiline 上限 → TRUNCATED，不静默拼接下一事件。
func TestLimitExceededTruncated(t *testing.T) {
	lines := []string{
		`[12:00:00] [Server thread/ERROR]: boom`,
		`	at a.A(A.java:1)`,
		`	at b.B(B.java:2)`,
		`	at c.C(C.java:3)`,
		`[12:00:01] [Server thread/INFO]: next`,
	}

	opts := testOpts("stdout")
	opts.Limits.MaxLines = 2
	n := New(opts)
	var completed []logtypes.Event
	for _, line := range lines {
		completed = append(completed, n.Feed(line)...)
	}

	require.NotEmpty(t, completed)
	first := completed[0]
	require.Equal(t, StatusTruncated, ParseStatus(first.Fields[FieldParseStatus]))
	require.Equal(t, "ERROR", first.Level)
	require.Contains(t, first.Message, "boom")
	require.Contains(t, first.Message, "at a.A")
	require.NotContains(t, first.Message, "at c.C", "截断事件不得吞掉超限后的行")
	require.Equal(t, 2, strings.Count(first.Message, "\n")+1)

	// 后续时间戳事件不得丢失
	ev, ok := n.FlushPartial()
	require.True(t, ok)
	require.Equal(t, "INFO", ev.Level)
	require.Contains(t, ev.Message, "next")

	res := n.Result()
	require.GreaterOrEqual(t, res.Stats.TruncatedEvents, 1)
	require.Equal(t, res.Stats.LineCount, 5)
	// TRUNCATED + 超限后残留续行 + INFO
	require.Equal(t, res.Stats.EventCount, 3)
}

// FR-474：级别别名 WARNING→WARN；解析失败不发明 level。
func TestLevelAliasesAndNeverInvent(t *testing.T) {
	require.Equal(t, "WARN", CanonicalLevel("WARNING"))
	require.Equal(t, "WARN", CanonicalLevel("warning"))
	require.Equal(t, "WARN", CanonicalLevel("WARN"))
	require.Equal(t, "ERROR", CanonicalLevel("SEVERE"))
	require.Equal(t, "ERROR", CanonicalLevel("FATAL"))
	require.Equal(t, "INFO", CanonicalLevel("info"))
	require.Equal(t, "", CanonicalLevel(""))
	require.Equal(t, "INFO", extractSlogLevel(`level=INFO msg=x`))
	require.Equal(t, "WARN", extractSlogLevel(`level=WARNING msg=x`))
	require.Equal(t, "WARN", extractSlogLevel(`level="warning" msg=x`))
	require.Equal(t, "", extractSlogLevel(`plain boom no level`))

	n := New(testOpts("stderr"))
	n.Feed(`[12:00:00] [Server thread/WARNING]: disk almost full`)
	n.Feed(`[12:00:01] [Server thread/INFO]: ok`)
	evs := n.Events()
	require.Len(t, evs, 1)
	require.Equal(t, "WARN", evs[0].Level, "WARNING 必须归一为 WARN")
	require.Equal(t, "OK", evs[0].Fields[FieldParseStatus])

	// 完全无法解析的行：保留原文 + RAW，level 为空
	n2 := New(testOpts("stderr"))
	rawLine := `??? totally not a log line ???`
	n2.Feed(rawLine)
	evs2 := n2.Events()
	require.Len(t, evs2, 1)
	require.Equal(t, rawLine, evs2[0].Message)
	require.Equal(t, "", evs2[0].Level, "解析失败绝不发明 level")
	require.Equal(t, StatusRaw, ParseStatus(evs2[0].Fields[FieldParseStatus]))
	require.Equal(t, "stderr", evs2[0].Stream)
}

// 超时：未闭合多行以 TIMEOUT 闭合。
func TestTimeoutClosesIncompleteMultiline(t *testing.T) {
	opts := testOpts("stdout")
	opts.Limits.Timeout = 2 * time.Second
	opts.BaseTime = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	n := New(opts)

	base := opts.BaseTime
	var completed []logtypes.Event
	completed = append(completed, n.FeedAt(`[12:00:00] [Server thread/ERROR]: boom`, base)...)
	completed = append(completed, n.FeedAt(`	at a.A(A.java:1)`, base.Add(time.Second))...)
	// 3s 后才来下一行：超时
	completed = append(completed, n.FeedAt(`	at b.B(B.java:2)`, base.Add(3*time.Second))...)

	require.Len(t, completed, 1)
	require.Equal(t, StatusTimeout, ParseStatus(completed[0].Fields[FieldParseStatus]))
	require.Contains(t, completed[0].Message, "boom")
	require.Contains(t, completed[0].Message, "at a.A")

	res := n.Result()
	require.Equal(t, 1, res.Stats.TimeoutEvents)
	require.Equal(t, 3, res.Stats.LineCount)
}

// MaxBytes 上限同样进入 TRUNCATED。
func TestMaxBytesTruncated(t *testing.T) {
	opts := testOpts("stdout")
	opts.Limits.MaxBytes = 40
	n := New(opts)
	var completed []logtypes.Event
	completed = append(completed, n.Feed(`[12:00:00] [Server thread/ERROR]: boom`)...)
	completed = append(completed, n.Feed(`	at very.long.frame.Name.method(File.java:12345)`)...)

	require.NotEmpty(t, completed)
	require.Equal(t, StatusTruncated, ParseStatus(completed[0].Fields[FieldParseStatus]))
}

// parser_version 常量冻结；源分段绑定后不得悄悄更换。
func TestParserVersionConstant(t *testing.T) {
	require.Equal(t, "1.0.0", ParserVersion)
	require.Equal(t, ParserVersion, ParserVersionOf())

	opts := testOpts("stdout")
	n := New(opts)
	n.Feed(`[12:00:00] [Server thread/INFO]: hello`)
	ev, ok := n.FlushPartial()
	require.True(t, ok)
	require.Equal(t, ParserVersion, ev.Source.ParserVersion)

	// 调用方显式指定 parser_version 时尊重原值
	opts2 := testOpts("stdout")
	opts2.Source.ParserVersion = "0.9.0"
	n2 := New(opts2)
	n2.Feed(`[12:00:00] [Server thread/INFO]: hello`)
	ev2, ok := n2.FlushPartial()
	require.True(t, ok)
	require.Equal(t, "0.9.0", ev2.Source.ParserVersion)
}

// ProcessLines 批量入口：事件数/行数分列；仅堆栈/Caused by/异常 FQCN 并入，无法解析行单独 RAW。
func TestProcessLinesStats(t *testing.T) {
	lines := []string{
		`[12:00:00] [Server thread/ERROR]: boom`,
		`java.lang.RuntimeException: x`,
		`	at a.A(A.java:1)`,
		`[12:00:01] [Server thread/WARNING]: warn`,
		`not a log`,
		`time=2026-09-20T12:00:02Z level=DEBUG msg=dbg`,
		`??? orphan raw ???`,
	}
	res := ProcessLines(lines, testOpts("stderr"))
	require.Equal(t, 7, res.Stats.LineCount)
	require.Equal(t, 5, res.Stats.EventCount, "ERROR 多行 + WARNING + RAW + DEBUG + RAW")

	var levels []string
	rawCount := 0
	for _, ev := range res.Events {
		levels = append(levels, ev.Level)
		if ev.Fields[FieldParseStatus] == string(StatusRaw) {
			rawCount++
			require.Equal(t, "", ev.Level, "RAW 事件绝不发明 level")
		}
	}
	require.Equal(t, []string{"ERROR", "WARN", "", "DEBUG", ""}, levels)
	require.Equal(t, 2, rawCount)
	require.NotContains(t, levels, "WARNING")
	require.Less(t, res.Stats.EventCount, res.Stats.LineCount)
}
