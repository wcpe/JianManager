package sampling

import (
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func fixedTS(time.Time) func(int) time.Time {
	return func(int) time.Time { return baseTime }
}

// TestDisabledPolicyIsIdentity 守住「未启用 = 零行为变化」。
func TestDisabledPolicyIsIdentity(t *testing.T) {
	in := mkBatch("inst:1", 20, []string{"DEBUG"}, []string{"noise"}, fixedTS(baseTime))
	for _, p := range []Policy{{}, {Enabled: false, Level: LevelFilter{MinLevel: "WARN"}}} {
		got := New(p).Process(in)
		if len(got) != len(in) {
			t.Fatalf("未启用时事件数必须不变：%d → %d", len(in), len(got))
		}
		for i := range in {
			if got[i].EventID != in[i].EventID {
				t.Fatalf("未启用时第 %d 条被改动", i)
			}
		}
		if len(aggregatesOf(got)) != 0 {
			t.Fatal("未启用时不得产出汇总事件")
		}
	}
}

// TestLevelFilterFoldsBelowMinLevel：等级过滤要能把「一堆互不相同的 DEBUG 行」压成一条。
// 这是本条规则存在的意义：若按消息模式归并，一百条不同的 DEBUG 就会产出一百条汇总，等于没省。
func TestLevelFilterFoldsBelowMinLevel(t *testing.T) {
	msgs := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	in := make([]logtypes.Event, 0, 15)
	for i := 0; i < 15; i++ {
		lvl := "DEBUG"
		if i%5 == 4 {
			lvl = "INFO" // 每 5 条夹一条 INFO
		}
		in = append(in, mkEvent("inst:1", i, lvl, msgs[i%len(msgs)], baseTime, "stdout"))
	}
	p := Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}}
	out := New(p).Process(in)

	if got := countAtLevel(out, "DEBUG"); got != 0 {
		t.Fatalf("低于 min_level 的原文必须全部被折叠，仍有 %d 条", got)
	}
	if got := countAtLevel(out, "INFO"); got != 3 {
		t.Fatalf("min_level 及以上的原文必须原样保留，期望 3 条得到 %d", got)
	}
	aggs := aggregatesOf(out)
	if len(aggs) != 3 {
		t.Fatalf("三段相邻 DEBUG（各 4 条）应各产出一条汇总，得到 %d 条", len(aggs))
	}
	total := 0
	for _, a := range aggs {
		if a.Level != "DEBUG" {
			t.Errorf("汇总必须继承被抑制行的级别，得到 %q", a.Level)
		}
		total += atoiField(t, a, FieldCount)
	}
	if total != 12 {
		t.Fatalf("汇总承接的被抑制条数合计应为 12，得到 %d", total)
	}
	// 折叠后总条数应显著下降：3 条 INFO 原文 + 3 条汇总 = 6（原 15）。
	if len(out) != 6 {
		t.Fatalf("输出条数期望 6，得到 %d", len(out))
	}
}

// TestLevelFilterKeepsUnknownLevel：无法判定的级别一律放行（不得压掉看不清的东西）。
func TestLevelFilterKeepsUnknownLevel(t *testing.T) {
	in := []logtypes.Event{
		mkEvent("inst:1", 0, "", "no level parsed at all", baseTime, "stdout"),
		mkEvent("inst:1", 1, "NOTICE", "unrecognised token", baseTime, "stdout"),
		mkEvent("inst:1", 2, "DEBUG", "real noise", baseTime, "stdout"),
	}
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "WARN"}}).Process(in)

	for _, want := range []string{"no level parsed at all", "unrecognised token"} {
		found := false
		for _, e := range out {
			if e.Message == want {
				found = true
			}
		}
		if !found {
			t.Errorf("级别无法判定的行绝不能被等级过滤压掉：%q 丢失", want)
		}
	}
	if countAtLevel(out, "DEBUG") != 0 {
		t.Error("明确低于 min_level 的行应当被折叠")
	}
}

// TestBurstSuppressCountsAndFolds：同源同消息高频抑制的计数与阈值语义。
func TestBurstSuppressCountsAndFolds(t *testing.T) {
	in := mkBatch("inst:1", 10, []string{"INFO"}, []string{"repeating tick 42"}, fixedTS(baseTime))
	p := Policy{Enabled: true, Burst: Burst{Window: time.Second, Threshold: 3}}
	out := New(p).Process(in)

	if got := len(out) - len(aggregatesOf(out)); got != 3 {
		t.Fatalf("阈值 3 表示窗口内前 3 条放行原文，得到 %d 条", got)
	}
	aggs := aggregatesOf(out)
	if len(aggs) != 1 {
		t.Fatalf("余下 7 条同模式相邻行应合成 1 条汇总，得到 %d 条", len(aggs))
	}
	if got := atoiField(t, aggs[0], FieldCount); got != 7 {
		t.Fatalf("汇总计数应为 7，得到 %d", got)
	}
	if got := aggs[0].Fields[FieldRule]; got != RuleBurstSuppress {
		t.Fatalf("规则字段应为 %s，得到 %q", RuleBurstSuppress, got)
	}
}

// TestBurstWindowExpiry：窗口过期必须重新计数，否则长期运行会把一切压光。
func TestBurstWindowExpiry(t *testing.T) {
	in := mkBatch("inst:1", 8, []string{"INFO"}, []string{"same msg"},
		func(i int) time.Time { return baseTime.Add(time.Duration(i) * time.Second) })
	p := Policy{Enabled: true, Burst: Burst{Window: time.Second, Threshold: 3}}
	out := New(p).Process(in)

	if aggs := aggregatesOf(out); len(aggs) != 0 {
		t.Fatalf("每条都落在各自的窗口里、从未超阈值，不应产出汇总，得到 %d 条", len(aggs))
	}
	if len(out) != len(in) {
		t.Fatalf("应全部放行，期望 %d 得到 %d", len(in), len(out))
	}
}

// TestBurstDifferentMessagesNotFolded：不同模板各记各的账，互不牵连。
func TestBurstDifferentMessagesNotFolded(t *testing.T) {
	in := make([]logtypes.Event, 0, 8)
	for i := 0; i < 8; i++ {
		in = append(in, mkEvent("inst:1", i, "INFO", string(rune('a'+i))+"-distinct", baseTime, "stdout"))
	}
	out := New(Policy{Enabled: true, Burst: Burst{Window: time.Second, Threshold: 2}}).Process(in)
	if len(aggregatesOf(out)) != 0 || len(out) != len(in) {
		t.Fatalf("不同消息不得互相累计，期望全部放行 %d 得到 %d（汇总 %d）",
			len(in), len(out), len(aggregatesOf(out)))
	}
}

// TestBurstUnparseableTimeNeverSuppresses：算不出速率就不抑制（采样不得变成静默丢弃）。
func TestBurstUnparseableTimeNeverSuppresses(t *testing.T) {
	in := make([]logtypes.Event, 0, 10)
	for i := 0; i < 10; i++ {
		e := mkEvent("inst:1", i, "INFO", "same msg", baseTime, "stdout")
		e.EventTimeUTC = "not-a-timestamp"
		in = append(in, e)
	}
	out := New(Policy{Enabled: true, Burst: Burst{Window: time.Second, Threshold: 1}}).Process(in)
	if len(out) != len(in) {
		t.Fatalf("时间不可解析时必须全量放行，期望 %d 得到 %d", len(in), len(out))
	}
}

// TestBudgetSamplingRatio：预算用尽后按 KeepEvery 确定性采样（不依赖随机数）。
func TestBudgetSamplingRatio(t *testing.T) {
	in := mkBatch("inst:1", 8, []string{"INFO"}, []string{"same msg"}, fixedTS(baseTime))
	p := Policy{Enabled: true, Budget: Budget{MaxEventsPerWindow: 2, Window: time.Second, KeepEvery: 3}}
	out := New(p).Process(in)

	// 期望：1,2 放行；3,4 折叠；5 放行；6,7 折叠；8 放行 ⇒ 4 条原文 + 2 条汇总。
	kept := 0
	for _, e := range out {
		if !IsAggregate(e) {
			kept++
		}
	}
	if kept != 4 {
		t.Fatalf("预算 2 + 每 3 条保留 1 条：8 条中应保留 4 条原文，得到 %d", kept)
	}
	aggs := aggregatesOf(out)
	if len(aggs) != 2 {
		t.Fatalf("应产出 2 条汇总，得到 %d 条", len(aggs))
	}
	for i, a := range aggs {
		if got := a.Fields[FieldRule]; got != RuleBudgetSample {
			t.Errorf("第 %d 条汇总规则应为 %s，得到 %q", i, RuleBudgetSample, got)
		}
		if got := atoiField(t, a, FieldCount); got != 2 {
			t.Errorf("第 %d 条汇总计数应为 2，得到 %d", i, got)
		}
	}
}

// TestBudgetDeterministicAcrossRuns：同一输入两次处理必须逐字节一致（重投幂等的前提）。
func TestBudgetDeterministicAcrossRuns(t *testing.T) {
	in := mkBatch("inst:1", 12, []string{"INFO", "DEBUG"}, []string{"m1", "m2"}, fixedTS(baseTime))
	p := Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"},
		Burst: Burst{Window: time.Second, Threshold: 3}, Budget: Budget{MaxEventsPerWindow: 4, Window: time.Second, KeepEvery: 2}}
	first := New(p).Process(in)
	second := New(p).Process(in)
	if len(first) != len(second) {
		t.Fatalf("两次处理条数不同：%d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].EventID != second[i].EventID || first[i].CanonicalHash != second[i].CanonicalHash {
			t.Fatalf("第 %d 条不确定：重投会写出不同的东西（破坏幂等）", i)
		}
	}
}

// TestMixedLevelsNeverShareAggregate：跨级别的行不得混入同一汇总。
//
// 夹具刻意做成「按级别成簇」（DEBUG×3 紧跟 INFO×2）：这样断言才落在
// 「归并键含级别」这一条上，而不是落在「交替出现必然各自成段」这种与实现无关的巧合上。
func TestMixedLevelsNeverShareAggregate(t *testing.T) {
	in := []logtypes.Event{
		mkEvent("inst:1", 0, "DEBUG", "d1", baseTime, "stdout"),
		mkEvent("inst:1", 1, "DEBUG", "d2", baseTime, "stdout"),
		mkEvent("inst:1", 2, "DEBUG", "d3", baseTime, "stdout"),
		mkEvent("inst:1", 3, "INFO", "i1", baseTime, "stdout"),
		mkEvent("inst:1", 4, "INFO", "i2", baseTime, "stdout"),
	}
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "WARN"}}).Process(in)

	aggs := aggregatesOf(out)
	if len(aggs) != 2 {
		t.Fatalf("DEBUG×3 与 INFO×2 应各成一条汇总，得到 %d 条", len(aggs))
	}
	want := map[string]int{"DEBUG": 3, "INFO": 2}
	for _, a := range aggs {
		expected, ok := want[a.Level]
		if !ok {
			t.Fatalf("汇总级别 %q 不在夹具里", a.Level)
		}
		if got := atoiField(t, a, FieldCount); got != expected {
			t.Errorf("%s 汇总应承接 %d 条，得到 %d（跨级别被混入或段划分有误）", a.Level, expected, got)
		}
		delete(want, a.Level)
	}
	if len(want) != 0 {
		t.Fatalf("有级别没有产出汇总：%v", want)
	}
}

// TestAggregatePayloadIsCompleteAndHonest：汇总必须能回答「丢了多少、丢了什么、为什么」。
func TestAggregatePayloadIsCompleteAndHonest(t *testing.T) {
	sample := strings.Repeat("x", aggregateSampleChars+50) // 超过样例上限，必须如实标注截断
	in := []logtypes.Event{
		mkEvent("inst:7", 0, "DEBUG", sample, baseTime, "stderr"),
		mkEvent("inst:7", 1, "DEBUG", "second line", baseTime, "stderr"),
	}
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}}).Process(in)
	aggs := aggregatesOf(out)
	if len(aggs) != 1 {
		t.Fatalf("期望 1 条汇总，得到 %d 条", len(aggs))
	}
	a := aggs[0]

	if a.Source.LogSourceID != "inst:7" {
		t.Errorf("汇总必须继承源身份，得到 %q", a.Source.LogSourceID)
	}
	if a.Stream != "stderr" {
		t.Errorf("汇总必须继承流，得到 %q", a.Stream)
	}
	if a.Record.Start != 0 || a.Record.End != 19 {
		t.Errorf("汇总区间应为被抑制行的精确并集 [0,19)，得到 [%d,%d)", a.Record.Start, a.Record.End)
	}
	for _, want := range []string{AggregateMarker, "规则=level_filter", "条数=2", "区间=[0,19)", "模板=", "首条=", "末条=second line"} {
		if !strings.Contains(a.Message, want) {
			t.Errorf("汇总正文缺少 %q：\n%s", want, a.Message)
		}
	}
	if !strings.Contains(a.Message, strings.Repeat("x", 100)) {
		t.Error("汇总正文应带首条样例全文（截断后仍应含开头）")
	}
	if len(a.Message) > 4*aggregateSampleChars {
		t.Errorf("汇总正文被样例撑得过大（%d 字符），违背成本治理初衷", len(a.Message))
	}
	if a.Fields[FieldTruncated] != "true" {
		t.Errorf("样例被截断必须如实标注，得到 %q", a.Fields[FieldTruncated])
	}
	if a.Fields[FieldRecordFrom] != "0" || a.Fields[FieldRecordTo] != "19" {
		t.Errorf("区间字段与 Record 不一致：%v", a.Fields)
	}
	if a.Fields[FieldLevel] != "DEBUG" {
		t.Errorf("级别字段应为 DEBUG，得到 %q", a.Fields[FieldLevel])
	}
	if !IsAggregate(a) {
		t.Error("IsAggregate 应能识别自身产出的汇总")
	}
}

// TestMaxAggregateEventsSplitsRun：超长段必须切分，否则一条汇总会跨度过大。
func TestMaxAggregateEventsSplitsRun(t *testing.T) {
	in := mkBatch("inst:1", 10, []string{"DEBUG"}, []string{"noise"}, fixedTS(baseTime))
	p := Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}, MaxAggregateEvents: 4}
	out := New(p).Process(in)

	aggs := aggregatesOf(out)
	if len(aggs) != 3 { // 4 + 4 + 2
		t.Fatalf("上限 4 时 10 条应切成 3 段，得到 %d 段", len(aggs))
	}
	sum := 0
	for _, a := range aggs {
		sum += atoiField(t, a, FieldCount)
	}
	if sum != 10 {
		t.Fatalf("切分不得丢条数，合计应为 10 得到 %d", sum)
	}
}

// TestPerSourceIsolation：不同源的采样器不共享状态（不串源）。
//
// 阈值取 3、每个采样器只见到 3 条 ⇒ 恰好都未超阈值。若状态被共享（例如误引入包级变量），
// 计数会累加到 6 并触发折叠，本用例随即转红。
func TestPerSourceIsolation(t *testing.T) {
	p := Policy{Enabled: true, Burst: Burst{Window: time.Second, Threshold: 3}}
	a := New(p)
	b := New(p)
	in := mkBatch("inst:1", 6, []string{"INFO"}, []string{"same msg"}, fixedTS(baseTime))

	// 交替喂给两个采样器：若状态被共享，计数会翻倍提前触发。
	var outA, outB []logtypes.Event
	for i := range in {
		if i%2 == 0 {
			outA = append(outA, a.Process(in[i:i+1])...)
		} else {
			outB = append(outB, b.Process(in[i:i+1])...)
		}
	}
	for name, out := range map[string][]logtypes.Event{"A": outA, "B": outB} {
		if got := len(aggregatesOf(out)); got != 0 {
			t.Errorf("采样器 %s 每源只见到 3 条、未超阈值 3，不应折叠，得到 %d 条汇总（状态疑似串源）", name, got)
		}
	}
}

// TestMixedSourceInOneBatchNotMerged：同一批里混入两个源时不得合并（否则汇总会伪造归属）。
func TestMixedSourceInOneBatchNotMerged(t *testing.T) {
	in := []logtypes.Event{
		mkEvent("inst:1", 0, "DEBUG", "noise", baseTime, "stdout"),
		mkEvent("inst:2", 1, "DEBUG", "noise", baseTime, "stdout"),
	}
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}}).Process(in)
	aggs := aggregatesOf(out)
	if len(aggs) != 2 {
		t.Fatalf("不同源必须各出一条汇总，得到 %d 条", len(aggs))
	}
	for _, a := range aggs {
		if atoiField(t, a, FieldCount) != 1 {
			t.Errorf("源 %s 的汇总只应承接自己的 1 条", a.Source.LogSourceID)
		}
		if a.Record.End-a.Record.Start >= 10 {
			t.Errorf("汇总区间跨越了另一源的行：源 %s 区间 [%d,%d)", a.Source.LogSourceID, a.Record.Start, a.Record.End)
		}
	}
}

// TestStormDegradeStateMachine：降级触发、保持期与恢复。
func TestStormDegradeStateMachine(t *testing.T) {
	p := Policy{Enabled: true,
		Degrade: Degrade{Enabled: true, TargetLevel: "ERROR", DiskPercent: 80, Hold: 30 * time.Second}}
	s := New(p)

	sig := Signal{DiskPercent: 75}
	if changed, active := s.UpdateSignal(sig, baseTime); changed || active {
		t.Fatal("未达阈值不得降级")
	}

	sig.DiskPercent = 85
	if changed, active := s.UpdateSignal(sig, baseTime); !changed || !active {
		t.Fatal("达到阈值必须立即降级")
	}
	if active, reason := s.Degraded(); !active || !strings.Contains(reason, "disk") {
		t.Fatalf("降级原因应可见，得到 active=%v reason=%q", active, reason)
	}

	in := []logtypes.Event{
		mkEvent("inst:1", 0, "INFO", "chatter", baseTime, "stdout"),
		mkEvent("inst:1", 1, "ERROR", "real problem", baseTime, "stdout"),
	}
	out := s.Process(in)
	if countAtLevel(out, "INFO") != 0 {
		t.Error("降级期间 INFO 原文必须被压掉")
	}
	aggs := aggregatesOf(out)
	if len(aggs) != 1 || aggs[0].Fields[FieldRule] != RuleStormDegrade {
		t.Fatalf("降级折叠的规则必须是 %s，得到 %d 条汇总", RuleStormDegrade, len(aggs))
	}
	if aggs[0].Fields[FieldDegraded] != "true" {
		t.Error("汇总应标注它产生于降级期间")
	}
	if countAtLevel(out, "ERROR") != 1 {
		t.Error("降级只压到 error-only，ERROR 原文必须保留")
	}

	sig.DiskPercent = 70
	if changed, active := s.UpdateSignal(sig, baseTime.Add(time.Second)); changed || !active {
		t.Fatal("读数回落但未过保持期，应仍在降级态")
	}
	if changed, active := s.UpdateSignal(sig, baseTime.Add(time.Second+29*time.Second)); changed || !active {
		t.Fatal("保持期未满不得恢复")
	}
	if changed, active := s.UpdateSignal(sig, baseTime.Add(time.Second+31*time.Second)); !changed || active {
		t.Fatal("保持期满必须恢复")
	}
	if got := s.Stats().DegradeTransitions; got != 2 {
		t.Fatalf("进/出各计一次应为 2，得到 %d", got)
	}
}

// TestDegradeOnlyRaisesMinLevel：降级只抬高、不降低——运维配得更严时以运维配置为准。
func TestDegradeOnlyRaisesMinLevel(t *testing.T) {
	p := Policy{Enabled: true,
		Level:   LevelFilter{MinLevel: "ERROR"},
		Degrade: Degrade{Enabled: true, TargetLevel: "WARN", DiskPercent: 80, Hold: time.Minute}}
	s := New(p)
	if _, active := s.UpdateSignal(Signal{DiskPercent: 90}, baseTime); !active {
		t.Fatal("应已降级")
	}
	in := []logtypes.Event{mkEvent("inst:1", 0, "INFO", "chatter", baseTime, "stdout")}
	out := s.Process(in)
	aggs := aggregatesOf(out)
	if len(aggs) != 1 {
		t.Fatalf("期望 1 条汇总，得到 %d 条", len(aggs))
	}
	// INFO 被压掉的原因应当是运维自己的 min_level=ERROR，而不是降级（降级目标是 WARN，更宽）。
	if got := aggs[0].Fields[FieldRule]; got != RuleLevelFilter {
		t.Errorf("运维配得更严时应记 level_filter，得到 %q", got)
	}
}

// TestStatsReporting：观测读数必须能算出「实际省了多少」。
func TestStatsReporting(t *testing.T) {
	in := mkBatch("inst:1", 100, []string{"DEBUG", "INFO"}, []string{"noise"}, fixedTS(baseTime))
	s := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}})
	out := s.Process(in)

	st := s.Stats()
	if st.Seen != 100 {
		t.Fatalf("Seen 应为 100，得到 %d", st.Seen)
	}
	if st.Passed+st.Suppressed != st.Seen {
		t.Fatalf("放行+折叠必须等于判定总数：%d+%d != %d", st.Passed, st.Suppressed, st.Seen)
	}
	if st.Suppressed != 50 {
		t.Fatalf("交替 DEBUG/INFO：应折叠 50 条，得到 %d", st.Suppressed)
	}
	if st.Aggregates != 50 {
		t.Fatalf("每条 DEBUG 左右都被 INFO 隔开，应产出 50 条汇总，得到 %d", st.Aggregates)
	}
	if got := st.ByRule[RuleLevelFilter]; got != 50 {
		t.Fatalf("按规则统计应为 50，得到 %d", got)
	}
	if ratio := st.SavedRatio(); ratio < 0.49 || ratio > 0.51 {
		t.Fatalf("节省比例应约为 0.5，得到 %v", ratio)
	}
	if saved := len(out); saved != 50+50 {
		t.Fatalf("输出条数应为 50 原文 + 50 汇总 = 100？实际 %d（此用例只校验读数自洽）", saved)
	}
	if st.Signatures != 0 {
		t.Fatalf("本用例只启用了等级过滤、没有高频抑制，模式表应为空，得到 %d", st.Signatures)
	}
}

// TestSignatureTableIsBounded：采样自身不得成为无界增长点。
func TestSignatureTableIsBounded(t *testing.T) {
	p := Policy{Enabled: true, Burst: Burst{Window: time.Second, Threshold: 100, MaxSignatures: 32}}
	s := New(p)
	in := make([]logtypes.Event, 0, 200)
	for i := 0; i < 200; i++ {
		in = append(in, mkEvent("inst:1", i, "INFO", "distinct-"+string(rune('A'+i%26))+string(rune('0'+i%10))+string(rune('a'+i%7)), baseTime, "stdout"))
	}
	s.Process(in)
	if got := s.Stats().Signatures; got > 32 {
		t.Fatalf("模式表必须被限制在 32 以内，得到 %d", got)
	}
}

func atoiField(t *testing.T, e logtypes.Event, key string) int {
	t.Helper()
	raw := e.Fields[key]
	n := 0
	if raw == "" {
		t.Fatalf("字段 %s 缺失（%v）", key, e.Fields)
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			t.Fatalf("字段 %s=%q 不是数字", key, raw)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
