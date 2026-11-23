package tailer

// 本文件验证多行语义**逐格**对齐 Filebeat 官方表。
//
// # 为何要逐格验
//
// `negate` 与 `match` 的四种组合里，只有一种是我们生产要用的（Java 堆栈：
// negate=true + match=after），但其余三种同样要正确——因为配置是运维写的，
// 写错时不能绷成一团脏数据，而应该按官方语义给出结果。
//
// 另一个真实踩过的坑：`negate` **只翻转“哪些行是接续行”**，不翻转方向。
// 把 negate 当成“反过来拼”是这类实现最常见的错误。

import (
	"strings"
	"testing"
	"time"
)

func mustMultiline(t *testing.T, cfg MultilineConfig) *Multiline {
	t.Helper()
	m, err := NewMultiline(cfg)
	if err != nil {
		t.Fatalf("编译多行规则: %v", err)
	}
	return m
}

// collect 把整段行喂给聚合器，返回完结的事件（含尾部 Flush）。
func collect(agg *MultilineAggregator, lines []string) []string {
	var out []string
	for _, l := range lines {
		out = append(out, agg.Add(l)...)
	}
	out = append(out, agg.Flush()...)
	return out
}

// TestMultilineFalseAfter 对应官方表第一行：
// negate=false, match=after ⇒ **匹配**的行追加到前面不匹配的行。
func TestMultilineFalseAfter(t *testing.T) {
	// 官方示例：pattern: ^b  negate: false  match: after
	m := mustMultiline(t, MultilineConfig{Pattern: `^b`})
	agg := NewMultilineAggregator(m, time.Minute)
	got := collect(agg, []string{"a1", "b1", "b2", "c1"})

	// a1 开启事件；b1/b2 匹配 ⇒ 并入 a1；c1 不匹配 ⇒ 开启新事件。
	want := []string{"a1\nb1\nb2", "c1"}
	if len(got) != len(want) {
		t.Fatalf("事件数 = %d，期望 %d：%#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// TestMultilineFalseBefore 对应官方表第二行：
// negate=false, match=before ⇒ **匹配**的行前置到后面不匹配的行。
func TestMultilineFalseBefore(t *testing.T) {
	// 官方示例：pattern: \\$  negate: false  match: before（续行标记）
	m := mustMultiline(t, MultilineConfig{Pattern: `\\$`, Match: MatchBefore})
	agg := NewMultilineAggregator(m, time.Minute)
	got := collect(agg, []string{"line1 \\", "line2 \\", "line3"})

	// line1/line2 匹配（以反斜杠结尾）⇒ 前置到 line3，拼成一条事件。
	want := []string{"line1 \\\nline2 \\\nline3"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got = %#v，期望 %#v", got, want)
	}
}

// TestMultilineTrueAfterIsJavaStacktrace 是最重要的一条：
// negate=true, match=after ⇒ **不匹配**的行追加到前面匹配的行。
//
// 这正是 MC / Java 日志的形态：日志行以 `[` 开头，堆栈行不以 `[` 开头。
// 这一条描住的是生产真实形态（不只是拿下官方示例）。
func TestMultilineTrueAfterIsJavaStacktrace(t *testing.T) {
	m := mustMultiline(t, MultilineConfig{Pattern: `^\[`, Negate: true})
	agg := NewMultilineAggregator(m, time.Minute)
	lines := []string{
		`[18:00:01] [Server thread/ERROR]: Could not pass event`,
		`\tat com.example.Book.getTitle(Book.java:16)`,
		`\tat com.example.Bootstrap.main(Bootstrap.java:14)`,
		`[18:00:02] [Server thread/INFO]: recovered`,
	}
	got := collect(agg, lines)

	if len(got) != 2 {
		t.Fatalf("事件数 = %d，期望 2：%#v", len(got), got)
	}
	// 第一条事件包含完整堆栈（三行）
	if !strings.Contains(got[0], "Could not pass event") ||
		!strings.Contains(got[0], "Book.java:16") ||
		!strings.Contains(got[0], "Bootstrap.java:14") {
		t.Errorf("堆栈行未被合并到事件里：%q", got[0])
	}
	if strings.Count(got[0], "\n") != 2 {
		t.Errorf("第一条事件应有 2 个换行（共 3 行），实际 %d：%q", strings.Count(got[0], "\n"), got[0])
	}
	if got[1] != `[18:00:02] [Server thread/INFO]: recovered` {
		t.Errorf("第二条事件错：%q", got[1])
	}
}

// TestMultilineTrueBefore 对应官方表第四行：
// negate=true, match=before ⇒ **不匹配**的行前置到后面匹配的行。
func TestMultilineTrueBefore(t *testing.T) {
	m := mustMultiline(t, MultilineConfig{Pattern: `^END$`, Negate: true, Match: MatchBefore})
	agg := NewMultilineAggregator(m, time.Minute)
	got := collect(agg, []string{"x1", "x2", "END"})

	// x1/x2 不匹配（negate 后算接续）⇒ 前置到匹配的 END。
	want := []string{"x1\nx2\nEND"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got = %#v，期望 %#v", got, want)
	}
}

// TestMultilineMaxLinesDropsExtra 验证 max_lines 超出后**丢弃多出的行**（官方语义）。
func TestMultilineMaxLinesDropsExtra(t *testing.T) {
	m := mustMultiline(t, MultilineConfig{Pattern: `^\[`, Negate: true, MaxLines: 3})
	agg := NewMultilineAggregator(m, time.Minute)
	// 首行 + 5 条接续行，maxLines=3 ⇒ 只保留 3 行，其余丢弃。
	got := collect(agg, []string{"[t] head", " a", " b", " c", " d", " e"})

	if len(got) != 1 {
		t.Fatalf("事件数 = %d，期望 1：%#v", len(got), got)
	}
	if n := len(strings.Split(got[0], "\n")); n != 3 {
		t.Errorf("事件行数 = %d，期望 3（max_lines）：%q", n, got[0])
	}
	if agg.Dropped() != 3 {
		t.Errorf("丢弃行数 = %d，期望 3", agg.Dropped())
	}
}

// TestMultilineFlushPattern 验证 flush_pattern 立即结束当前事件。
func TestMultilineFlushPattern(t *testing.T) {
	m := mustMultiline(t, MultilineConfig{
		Pattern: `^\[`, Negate: true, FlushPattern: `END$`,
	})
	agg := NewMultilineAggregator(m, time.Minute)

	var out []string
	for _, l := range []string{"[t] start", " body", "END"} {
		out = append(out, agg.Add(l)...)
	}
	// END 命中 flush_pattern ⇒ 事件在此结束（不等下一行）。
	if len(out) != 1 {
		t.Fatalf("flush_pattern 未立即冲刷，得 %#v", out)
	}
	if !strings.Contains(out[0], "body") || !strings.HasSuffix(out[0], "END") {
		t.Errorf("事件内容不对：%q", out[0])
	}
}

// TestMultilineTimeoutFlush 验证超时能强制冲出悬挂的多行事件。
//
// # 为何这一条很关键
//
// 源静默后若没有超时，最后一条事件会一直挂在缓冲里不发出，而它的 offset 已推进——
// 那段时间的数据在查询侧就是“不存在”，而且没有任何报错。
func TestMultilineTimeoutFlush(t *testing.T) {
	m := mustMultiline(t, MultilineConfig{Pattern: `^\[`, Negate: true})
	agg := NewMultilineAggregator(m, 50*time.Millisecond)

	if out := agg.Add("[t] head"); len(out) != 0 {
		t.Fatalf("首行不应立刻完结：%#v", out)
	}
	if out := agg.Add(" stack frame"); len(out) != 0 {
		t.Fatalf("接续行不应立刻完结：%#v", out)
	}
	// 未超时前 Due 必须为 false（否则正常的多行事件会被切成碎片）。
	if agg.Due(time.Now()) {
		t.Fatal("刚收到行就判定超时了")
	}
	// 超时后 Due 为 true，调用方据此冲刷。
	if !agg.Due(time.Now().Add(100 * time.Millisecond)) {
		t.Fatal("超时后 Due 应为 true（否则悬挂事件永不发出）")
	}
	out := agg.Flush()
	if len(out) != 1 {
		t.Fatalf("超时冲刷应得 1 条事件，实际 %#v", out)
	}
	if !strings.Contains(out[0], "stack frame") {
		t.Errorf("冲刷出的事件丢了接续行：%q", out[0])
	}
	// 冲刷后缓冲清空：再次 Due 必须为 false，否则会变成空转。
	if agg.Due(time.Now().Add(time.Hour)) {
		t.Error("冲刷后缓冲为空，Due 应为 false")
	}
}

// TestMultilineNoRulePassesThrough 验证未配置多行时行为不变（每行一条事件）。
func TestMultilineNoRulePassesThrough(t *testing.T) {
	agg := NewMultilineAggregator(nil, time.Minute)
	got := collect(agg, []string{"a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("无规则时应每行一条事件，实际 %#v", got)
	}
}

// TestMultilineRejectsBadConfig 验证非法配置在**启动时**就被拒。
//
// 若放到运行期，表现会是「日志少了/乱了」而不是报错——那比直接拒启难查得多。
func TestMultilineRejectsBadConfig(t *testing.T) {
	if _, err := NewMultiline(MultilineConfig{Pattern: `[`}); err == nil {
		t.Error("非法 pattern 应当报错")
	}
	if _, err := NewMultiline(MultilineConfig{Pattern: `x`, Match: "sideways"}); err == nil {
		t.Error("非法 match 应当报错")
	}
	if _, err := NewMultiline(MultilineConfig{Pattern: `x`, FlushPattern: `[`}); err == nil {
		t.Error("非法 flush_pattern 应当报错")
	}
	// 默认值：match 缺省为 after，max_lines 缺省 500
	m := mustMultiline(t, MultilineConfig{Pattern: `x`})
	if m.MaxLines() != DefaultMaxLines {
		t.Errorf("max_lines 默认应为 %d，实际 %d", DefaultMaxLines, m.MaxLines())
	}
}
