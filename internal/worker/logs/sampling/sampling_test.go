package sampling

import (
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// baseTime 是全部夹具的统一时间基准。测试里不读挂钟：采样判定只依赖事件自身数据，
// 用例也必须同样无时钟依赖，否则 CI 上会随机飘。
var baseTime = time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

func testSource(id string) logtypes.SourceIdentity {
	return logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "gen-1", ParserVersion: "parser-v1"}
}

// mkEvent 造一条事件：第 i 行占 [i*10, i*10+9)，相邻行之间恰好隔 1 字节（行分隔符），
// 与 rangesTouch 的口径一致——这样「相邻」的判定才是真实形态而不是巧合。
func mkEvent(id string, i int, level, msg string, ts time.Time, stream string) logtypes.Event {
	rec := logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)}
	return logtypes.BuildEvent(testSource(id), rec,
		ts.UTC().Format(time.RFC3339Nano), ts.UTC().Format(time.RFC3339Nano), level, stream, msg)
}

// mkBatch 造一批连续行，第 i 行取 levels/messages 的对应元素（按 i 取模，便于构造混合场景）。
func mkBatch(id string, n int, levels, messages []string, ts func(i int) time.Time) []logtypes.Event {
	out := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		lvl := levels[i%len(levels)]
		msg := messages[i%len(messages)]
		out = append(out, mkEvent(id, i, lvl, msg, ts(i), "stdout"))
	}
	return out
}

// recordsOf 取事件的 Record 区间。
func recordsOf(events []logtypes.Event) []ledger.PositionRange {
	out := make([]ledger.PositionRange, 0, len(events))
	for _, e := range events {
		out = append(out, ledger.PositionRange{From: e.Record.Start, To: e.Record.End})
	}
	return out
}

// spanOf 返回事件的连续覆盖区间（与 ingest.gapRangesOfEvents 完全同口径：
// 它就是 ledger.MergePositionRanges）。
func spanOf(events []logtypes.Event) []ledger.PositionRange {
	return ledger.MergePositionRanges(recordsOf(events))
}

func sameRanges(a, b []ledger.PositionRange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// countAtLevel 统计指定级别的**原文**条数（不含汇总事件）。
//
// 必须排除汇总：汇总继承被抑制行的级别（那是有意的——按级别的保留期要对得上它声称的级别），
// 所以「DEBUG 原文剩几条」和「level=DEBUG 的事件有几条」是两个不同的问题。
// 混淆二者会让「等级过滤是否生效」的断言永远为真。
func countAtLevel(events []logtypes.Event, level string) int {
	n := 0
	for _, e := range events {
		if !IsAggregate(e) && e.Level == level {
			n++
		}
	}
	return n
}

func aggregatesOf(events []logtypes.Event) []logtypes.Event {
	out := make([]logtypes.Event, 0, len(events))
	for _, e := range events {
		if IsAggregate(e) {
			out = append(out, e)
		}
	}
	return out
}

// ---------- 级别序 ----------

func TestRankAndLevelBelow(t *testing.T) {
	// 别名归口到 normalize.CanonicalLevel，不另立一套。
	if got := Rank("warning"); got != rankWarn {
		t.Fatalf("WARNING 应归一为 WARN(%d)，得到 %d", rankWarn, got)
	}
	if got := Rank("SEVERE"); got != rankError {
		t.Fatalf("SEVERE 应归一为 ERROR(%d)，得到 %d", rankError, got)
	}

	cases := []struct {
		level, min string
		want       bool
		why        string
	}{
		{"DEBUG", "INFO", true, "DEBUG 低于 INFO"},
		{"INFO", "INFO", false, "相等不算低于（min 语义是保留该级别及以上）"},
		{"ERROR", "INFO", false, "ERROR 高于 INFO"},
		{"", "INFO", false, "级别无法判定时一律放行"},
		{"NOTICE", "INFO", false, "未识别 token 视为无法判定，一律放行"},
		{"DEBUG", "", false, "min 为空等于未启用"},
		{"DEBUG", "BOGUS", false, "min 未识别等于未启用（不猜运维的意思）"},
	}
	for _, c := range cases {
		if got := LevelBelow(c.level, c.min); got != c.want {
			t.Errorf("LevelBelow(%q,%q)=%v 期望 %v（%s）", c.level, c.min, got, c.want, c.why)
		}
	}
}

// ---------- 模式掩码 ----------

func TestMaskMessageIsDeterministicAndMasksVolatile(t *testing.T) {
	a := MaskMessage("Started tick 12345 for world 67890")
	b := MaskMessage("Started tick 99999 for world 11111")
	if a != b {
		t.Fatalf("同模板不同数字应当掩码成同一模式：\n a=%q\n b=%q", a, b)
	}
	if MaskMessage("Started tick 1 for world 2") != MaskMessage("Started tick 1 for world 2") {
		t.Fatal("同一输入必须得到同一模式（确定性）")
	}
	for _, in := range []string{
		"trace 4bf92f3577b34da6a3ce929d0e0e4736 done",
		"conn 10.1.2.3 established",
		"id 550e8400-e29b-41d4-a716-446655440000 failed",
	} {
		if got := MaskMessage(in); got == in {
			t.Errorf("易变部分未被掩码：%q → %q", in, got)
		}
	}
	if strings.ContainsAny(MaskMessage("a\n b\tc"), "\n\t") {
		t.Error("空白应折叠为单个空格")
	}
}

func TestTruncateRunesKeepsValidUTF8(t *testing.T) {
	// 中文场景：按字节截断会把多字节字符切成两半留下非法 UTF-8。
	s := "地址已解析完成服务器列表"
	got := TruncateRunes(s, 3)
	if got != "地址已" {
		t.Fatalf("按字符截断期望 %q，得到 %q", "地址已", got)
	}
	if !strings.HasPrefix(s, got) {
		t.Fatal("截断必须是前缀")
	}
	if TruncateRunes(s, 1000) != s {
		t.Fatal("未超限时不得改动")
	}
	if !Truncated(s, 3) || Truncated(s, 1000) {
		t.Fatal("Truncated 判定与 TruncateRunes 口径不一致")
	}
}

func TestSignatureSeparatesLevelAndStream(t *testing.T) {
	// 跨级别/跨流不得归入同一签名——这是「混级别不合并」的第一道闸。
	l1 := SignatureOf("DEBUG", "stdout", "same message")
	l2 := SignatureOf("INFO", "stdout", "same message")
	l3 := SignatureOf("DEBUG", "stderr", "same message")
	if l1.Key == l2.Key {
		t.Error("不同级别必须得到不同签名")
	}
	if l1.Key == l3.Key {
		t.Error("不同流必须得到不同签名")
	}
}

// ---------- 策略校验 ----------

func TestPolicyValidateRejectsAndDefaults(t *testing.T) {
	if err := (Policy{}).Validate(); err != nil {
		t.Fatalf("未启用时不应校验：%v", err)
	}
	bad := []Policy{
		{Enabled: true, Level: LevelFilter{MinLevel: "BOGUS"}},
		{Enabled: true, Burst: Burst{Window: time.Second, Threshold: -1}},
		{Enabled: true, Budget: Budget{MaxEventsPerWindow: 10, KeepEvery: maxKeepEvery + 1}},
		{Enabled: true, Degrade: Degrade{Enabled: true, TargetLevel: "BOGUS"}},
		{Enabled: true, Degrade: Degrade{Enabled: true, DiskPercent: 120}},
	}
	for i, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("第 %d 个非法策略必须启动即拒（静默回退会让日志静默少存）", i)
		}
	}
	good := Policy{Enabled: true, Level: LevelFilter{MinLevel: "debug"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("合法策略不应被拒：%v", err)
	}
	if got := good.Normalize().MaxAggregateEvents; got != DefaultMaxAggregateEvents {
		t.Errorf("MaxAggregateEvents 默认值应为 %d，得到 %d", DefaultMaxAggregateEvents, got)
	}
	if !strings.Contains(good.Describe(), "minLevel=debug") {
		t.Errorf("Describe 应如实反映配置：%s", good.Describe())
	}
}
