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

// NPE 堆栈 + Caused by 归并为 ONE event；下一时间戳行才开新事件。
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

// Go slog level=INFO（stderr）不得按流兜底成 ERROR。
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

// 半条事件 FlushPartial 可显式吐出（PARTIAL）或丢弃。
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

// 跨 live 文件轮转的「堆栈打一半」必须留下**显式**未闭合痕迹（2026-10-02 补）。
//
// 背景：轮转闭合原先走 closeStatus（有 level/时间戳即 OK），于是一条被打断在两段文件之间的
// 堆栈会被静默标成 parse_status=OK —— 切分点从数据里彻底不可见，事后无法判断
// 「这条事件是完整的，还是被轮转切过」。
//
// 判据刻意只在 len(pending.lines) > 1 时成立：单行、以换行结束的记录本身就是完整记录
// （轮转只是恰好发生在两条记录之间），标记它会在**每次**轮转上制造误报。
//
// 转红方式（实测）：把 FlushUnclosed 的实现换成 closeStatus（即退回旧行为），
// 「半条堆栈」的两个断言立即红。
func TestFlushUnclosedMarksOnlyTruncatedMultiline(t *testing.T) {
	opts := testOpts("stdout")

	// ① 半条堆栈（已吃进续行）：必须 PARTIAL + 显式标记。
	n := New(opts)
	require.Empty(t, n.Feed(`[12:00:00] [Server thread/ERROR]: boom`))
	require.Empty(t, n.Feed("	at a.A(A.java:1)"))
	ev, ok := n.FlushUnclosed(UnclosedReasonCrossRotation)
	require.True(t, ok)
	require.Equal(t, StatusPartial, ParseStatus(ev.Fields[FieldParseStatus]),
		"半条堆栈不得伪装成完整事件")
	require.Equal(t, UnclosedReasonCrossRotation, ev.Fields[FieldMultilineUnclosed],
		"必须留下可审计的未闭合原因")
	require.Contains(t, ev.Message, "boom")
	require.Contains(t, ev.Message, "at a.A(A.java:1)")

	// ② 单行完整记录：不得标记（轮转恰好发生在两条记录之间是常态，标记即误报）。
	n2 := New(opts)
	require.Empty(t, n2.Feed(`[12:00:01] [Server thread/INFO]: single complete line`))
	ev2, ok := n2.FlushUnclosed(UnclosedReasonCrossRotation)
	require.True(t, ok)
	_, marked := ev2.Fields[FieldMultilineUnclosed]
	require.False(t, marked, "单行完整记录不得带未闭合标记（否则每次轮转都误报）")
	require.NotEqual(t, StatusPartial, ParseStatus(ev2.Fields[FieldParseStatus]))

	// ③ 闲置超时走同一标记通道，但原因可区分。
	n3 := New(opts)
	require.Empty(t, n3.Feed(`[12:00:02] [Server thread/ERROR]: boom`))
	require.Empty(t, n3.Feed("	at b.B(B.java:2)"))
	ev3, ok := n3.FlushUnclosed(UnclosedReasonIdle)
	require.True(t, ok)
	require.Equal(t, UnclosedReasonIdle, ev3.Fields[FieldMultilineUnclosed])
	require.Equal(t, StatusPartial, ParseStatus(ev3.Fields[FieldParseStatus]))
}

// 超过 multiline 上限 → TRUNCATED，不静默拼接下一事件。
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

// 级别别名 WARNING→WARN；解析失败不发明 level。
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

// TestBungeeClockOnlyLineGetsDeterministicEventTime 锁定 BungeeCord 形态的解析与时间确定性。
//
// 2026-10-20 真机验收根因：BungeeCord 写的是 `18:12:59 [INFO] body` —— 时刻**不带方括号**，
// 而当时三条 Java 形态正则都要求 `[]` 包住时刻，于是该行匹配不到任何时间形态：
// hasTime=false ⇒ EventTimeUTC 留空 ⇒ 落 normalizeCanonicalEvent 回退到 IngestTimeUTC（采集时刻）
// ⇒ 同一行每次重新导入都得到不同的 event_time_utc ⇒ CanonicalContentHash 不同；而 EventID
// 只由「来源+代次+区间」决定、不含内容 ⇒ 同一 event_id 多哈希、且「同区间等长」不可裁定
// ⇒ 每次启动对账重新裁定并累计 IdentityConflicts ⇒ ConflictCount>0 ⇒ 查询降级 + 禁用导出。
// 生产实测：inst:318 的 eventstore 13.0 万条里只有 6.4 万个唯一 event_id（6.4 万个有多份副本）。
//
// 判据：同一行在**同一 base** 下必须解析出确定的事件时间（= base 当日 + 行内时刻），
// 且必须带出级别；这样事件时间不再依赖「何时被采集」。
//
// 转红方式：移除 bungeeTimeLevelRe 分支，本用例第一处 require.NotEmpty 立即转红。
func TestBungeeClockOnlyLineGetsDeterministicEventTime(t *testing.T) {
	opts := testOpts("stdout")
	opts.BaseTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	n := New(opts)

	// 下一条带时刻的行闭合它。
	require.Empty(t, n.Feed("18:12:59 [INFO] Using mbed TLS based native cipher."))
	out := n.Feed("18:13:00 [INFO] next line")
	require.Len(t, out, 1)

	ev := out[0]
	require.NotEmpty(t, ev.EventTimeUTC,
		"BungeeCord 形态必须解析出确定事件时间，不得留空回落采集时刻")
	require.Equal(t, "2026-10-15T18:12:59Z", ev.EventTimeUTC,
		"事件时间必须由 base 的当日 + 行内时刻决定，与采集时刻无关")
	require.Equal(t, "INFO", ev.Level, "级别必须从 [INFO] 正确带出")
	require.Contains(t, ev.Message, "Using mbed TLS")

	// 同一个 base 下重复解析同一行：时间与哈希必须完全一致（这正是重导不漂移的前提）。
	n2 := New(opts)
	require.Empty(t, n2.Feed("18:12:59 [INFO] Using mbed TLS based native cipher."))
	out2 := n2.Feed("18:13:00 [INFO] next line")
	require.Len(t, out2, 1)
	require.Equal(t, ev.EventTimeUTC, out2[0].EventTimeUTC)
	require.Equal(t, ev.CanonicalHash, out2[0].CanonicalHash,
		"同一行在确定锚点下必须得到同一规范哈希（否则重导必然产生身份冲突）")
}

// driftTestBase 是新形态用例的固定锚点：取正午，使 18:12:59 一类时刻离 base 不足 12h，
// 不触发 applyClock 的跨午夜回拨——只考察「时刻/日期是否被解析出来」，不考察跨日猜测。
var driftTestBase = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

type timeFormCase struct {
	name    string
	line    string
	level   string
	thread  string
	body    string
	wantUTC string
}

// newTimeFormCases 覆盖本次新增的三族形态的样例（毫秒 clock / 毫秒方括号 / 裸日期）。
//
// 每一行的共同判据：必须解析出**确定**事件时间。缺任一条规则时这些行 hasTime=false
// ⇒ EventTimeUTC 留空 ⇒ ingest 的 normalizeCanonicalEvent 回退 IngestTimeUTC（采集时刻）
// ⇒ 同一行每次重导得到不同哈希，而 EventID 不含内容 ⇒ 同一 event_id 多哈希身份冲突。
var newTimeFormCases = []timeFormCase{
	{
		// 族 1：BungeeCord/Velocity 带毫秒（逗号小数，时刻不带方括号）。
		name: "bungee 毫秒逗号", line: `18:12:59,123 [INFO] Using mbed TLS based native cipher.`,
		level: "INFO", body: "Using mbed TLS based native cipher.",
		wantUTC: "2026-10-15T18:12:59.123Z",
	},
	{
		// 族 1：同一形态的点号小数写法。
		name: "bungee 毫秒点号", line: `18:12:59.456 [WARN] disk almost full`,
		level: "WARN", body: "disk almost full",
		wantUTC: "2026-10-15T18:12:59.456Z",
	},
	{
		// 族 2：方括号毫秒 + thread/LEVEL（logback/log4j2 `%d{HH:mm:ss.SSS}`）。
		name: "方括号毫秒 thread/level", line: `[18:12:59.789] [Server thread/ERROR]: Encountered an unexpected exception`,
		level: "ERROR", thread: "Server thread", body: "Encountered an unexpected exception",
		wantUTC: "2026-10-15T18:12:59.789Z",
	},
	{
		// 族 2：逗号毫秒 + thread/LEVEL。
		name: "方括号逗号毫秒 thread/level", line: `[18:12:59,012] [Server thread/INFO]: Done (1.234s)!`,
		level: "INFO", thread: "Server thread", body: "Done (1.234s)!",
		wantUTC: "2026-10-15T18:12:59.012Z",
	},
	{
		// 族 2：方括号毫秒 + LEVEL（无 thread）。
		name: "方括号毫秒 level", line: `[18:12:59,500 INFO]: ok`,
		level: "INFO", body: "ok",
		wantUTC: "2026-10-15T18:12:59.5Z",
	},
	{
		// 族 2：方括号毫秒，无级别（level 不发明）。
		name: "方括号毫秒无级别", line: `[18:12:59.001] plain body`,
		body:    "plain body",
		wantUTC: "2026-10-15T18:12:59.001Z",
	},
	{
		// 族 3：Log4j2 默认裸日期形态；日期 10-14 **早于** base 一天，
		// 必须原样保留（若误走 applyClock 会被改写成 10-15 ⇒ 本用例转红）。
		name: "裸日期", line: `2026-10-14 09:30:00 INFO [main] com.example.App - started`,
		body:    "INFO [main] com.example.App - started",
		wantUTC: "2026-10-14T09:30:00Z",
	},
	{
		// 族 3：裸日期 + 毫秒小数。
		name: "裸日期毫秒", line: `2026-10-14 09:30:00,250 [main/INFO] loaded`,
		body:    "[main/INFO] loaded",
		wantUTC: "2026-10-14T09:30:00.25Z",
	},
}

// TestNewTimeFormsGetDeterministicEventTime 断言三族新形态都能解析出确定事件时间与级别/线程/正文。
//
// 转红方式（实测）：逐个移除 parser.go 中对应的新分支，相关子用例立即红——要么
// kind != kindEventStart（整行落空），要么 hasTime=false（事件时间留空）。
func TestNewTimeFormsGetDeterministicEventTime(t *testing.T) {
	for _, tc := range newTimeFormCases {
		t.Run(tc.name, func(t *testing.T) {
			p := parseLine(tc.line, time.UTC, driftTestBase)
			require.Equal(t, kindEventStart, p.kind, "必须识别为事件起点（否则事件时间无从谈起）")
			require.True(t, p.hasTime, "必须解析出确定事件时间，不得留空回落采集时刻")
			require.Equal(t, tc.wantUTC, p.eventTime.UTC().Format(time.RFC3339Nano),
				"事件时间必须由行内时刻/日期决定")
			require.Equal(t, tc.level, p.level, "级别只能来自行内显式 token，不发明")
			require.Equal(t, tc.thread, p.thread)
			require.Equal(t, tc.body, p.body)
		})
	}
}

// TestNewTimeFormDateRespectsSourceTimeZone 裸日期形态按**源时区**解释（与 clock 形态同口径）。
func TestNewTimeFormDateRespectsSourceTimeZone(t *testing.T) {
	hkt, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)

	p := parseLine(`2026-10-14 09:30:00 started`, hkt, driftTestBase)
	require.True(t, p.hasTime)
	require.Equal(t, "2026-10-14T01:30:00Z", p.eventTime.Format(time.RFC3339Nano),
		"源时区 09:30 必须换算为 01:30Z，而不是把本地时间直接当 UTC")
}

// TestNewTimeFormRejectsInvalidDate 形态像日期但分量越界时**不接管**：维持改动前的 RAW 行为。
//
// 为什么必须拒绝：time.Date 会把越界分量静默归一化成另一个合法时刻（2 月 30 日 → 3 月 2 日），
// 那等于往事件里写一个错误时间，比不解析更糟（会落到错误日期/分区）。
func TestNewTimeFormRejectsInvalidDate(t *testing.T) {
	for _, line := range []string{
		`2026-13-45 99:99:99 not a real timestamp`,
		`2026-02-30 10:00:00 normalized away`,
	} {
		p := parseLine(line, time.UTC, driftTestBase)
		require.Equal(t, kindUnknown, p.kind, "非法日期不得被接管为事件起点：%s", line)
		require.False(t, p.hasTime, "非法日期不得产出事件时间：%s", line)
	}
}

// TestExistingTimeFormsUnchanged 守住「既有格式零行为变化」。
//
// 新增正则是严格加集的前提：它们只可能匹配毫秒/裸日期形态，绝不抢走既有行的归属，
// 也不得改变既有行的 level/thread/body/eventTime。本表就是这条不变量。
func TestExistingTimeFormsUnchanged(t *testing.T) {
	cases := []timeFormCase{
		{
			name: "Paper 原生 thread/LEVEL", line: `[12:00:01] [Server thread/INFO]: Done (1.0s)!`,
			level: "INFO", thread: "Server thread", body: "Done (1.0s)!",
			wantUTC: "2026-10-15T12:00:01Z",
		},
		{
			name: "Java 时刻+级别", line: `[12:00:02 WARN]: disk almost full`,
			level: "WARN", body: "disk almost full",
			wantUTC: "2026-10-15T12:00:02Z",
		},
		{
			name: "Java 仅时刻", line: `[12:00:03] plain body`,
			body:    "plain body",
			wantUTC: "2026-10-15T12:00:03Z",
		},
		{
			name: "Bungee 形态（无毫秒）", line: `12:00:04 [INFO] Using mbed TLS`,
			level: "INFO", body: "Using mbed TLS",
			wantUTC: "2026-10-15T12:00:04Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := parseLine(tc.line, time.UTC, driftTestBase)
			require.Equal(t, kindEventStart, p.kind)
			require.True(t, p.hasTime)
			require.Equal(t, tc.wantUTC, p.eventTime.UTC().Format(time.RFC3339Nano))
			require.Equal(t, tc.level, p.level)
			require.Equal(t, tc.thread, p.thread)
			require.Equal(t, tc.body, p.body)
		})
	}

	// slog 形态同样零变化：body 保留整行原文（slogRaw），时间来自 time=。
	slogLine := `time=2026-10-15T18:24:28.732+08:00 level=INFO msg=hello`
	p := parseLine(slogLine, time.UTC, driftTestBase)
	require.Equal(t, kindEventStart, p.kind)
	require.Equal(t, "INFO", p.level)
	require.Equal(t, slogLine, p.body, "slog 正文仍是整行原文")
	require.True(t, p.hasTime)
	require.Equal(t, "2026-10-15T10:24:28.732Z", p.eventTime.Format(time.RFC3339Nano))

	// 刻意**不**接管「裸时刻、无方括号、无级别」：本包既有口径是这类行不算事件起点
	// （javaTimeOnlyRe 要求方括号、bungeeTimeLevelRe 要求带级别）。新增毫秒规则
	// 只跟随该口径补 `[,.]mmm`，不借机放宽成 `HH:MM:SS.mmm body`。
	p = parseLine(`12:00:05.500 bare clock without level`, time.UTC, driftTestBase)
	require.Equal(t, kindUnknown, p.kind, "裸时刻无级别不接管（保持既有口径）")
}

// canonicalEventUnderIngest 复刻 ingest 运行时口径取一条事件的规范身份。
//
// 必须复刻 internal/worker/logs/ingest/runtime.go 的 normalizeCanonicalEvent：
// 事件时间留空时回退 IngestTimeUTC **并重算哈希**。少了这一步，「事件时间留空」
// 在两次解析里都是空串 ⇒ 哈希仍然相等 ⇒ 下面的防漂移用例无法转红。
func canonicalEventUnderIngest(t *testing.T, line string, base time.Time, ingest string) logtypes.Event {
	t.Helper()
	res := ProcessLines([]string{line}, Options{
		Source:        logtypes.SourceIdentity{LogSourceID: "src-drift", SourceGeneration: "g1"},
		Stream:        "stdout",
		Location:      time.UTC,
		BaseTime:      base,
		IngestTimeUTC: ingest,
	})
	require.Len(t, res.Events, 1)
	ev := res.Events[0]
	if ev.EventTimeUTC == "" {
		ev.EventTimeUTC = ev.IngestTimeUTC
		ev.CanonicalHash = logtypes.CanonicalContentHash(ev.EventTimeUTC, ev.Level, ev.Stream, ev.Message)
	}
	return ev
}

// TestNewTimeFormsHashStableAcrossReimport 是本次修复的**防漂移核心断言**。
//
// 同一行、同一锚点，在**不同采集时刻**（IngestTimeUTC）下重复解析，必须得到同一
// EventTimeUTC 与同一 CanonicalHash。这正是生产上「每次重新导入都重新裁定并累计
// IdentityConflicts」的根因闸门：只要事件时间来自行内，采集时刻就再也影响不到哈希。
//
// 转红方式（实测）：移除任一新增分支，该行的 EventTimeUTC 变空 ⇒ 回退各自的
// IngestTimeUTC（两个用例刻意不同）⇒ 哈希不同 ⇒ 断言红。
func TestNewTimeFormsHashStableAcrossReimport(t *testing.T) {
	// 两个差异极大的采集时刻：模拟同一段日志在不同时间被重新导入/接管。
	const ingestA = "2026-10-16T01:02:03.000000001Z"
	const ingestB = "2026-11-30T23:59:59.123456789Z"

	for _, tc := range newTimeFormCases {
		t.Run(tc.name, func(t *testing.T) {
			first := canonicalEventUnderIngest(t, tc.line, driftTestBase, ingestA)
			second := canonicalEventUnderIngest(t, tc.line, driftTestBase, ingestB)

			require.NotEmpty(t, first.EventTimeUTC, "事件时间不得留空（留空即回退采集时刻）")
			require.Equal(t, tc.wantUTC, first.EventTimeUTC)
			require.Equal(t, first.EventTimeUTC, second.EventTimeUTC,
				"事件时间不得随采集时刻变化")
			require.Equal(t, first.EventID, second.EventID)
			require.Equal(t, first.CanonicalHash, second.CanonicalHash,
				"同一行在确定锚点下必须得到同一规范哈希（否则重导必然产生身份冲突）")
		})
	}
}
