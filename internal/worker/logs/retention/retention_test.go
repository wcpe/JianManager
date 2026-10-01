package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

// fakeDeleter 记录每一次下发，用于断言「删了什么、没删什么」。
type fakeDeleter struct {
	calls   []call
	failFor string // 过滤器含该子串时返回错误（用于验证错误隔离）
}

type call struct {
	Filter string
	Before time.Time
}

func (f *fakeDeleter) RunDeleteTask(_ context.Context, filter string, before time.Time) (string, error) {
	f.calls = append(f.calls, call{Filter: filter, Before: before})
	if f.failFor != "" && strings.Contains(filter, f.failFor) {
		return "", errors.New("模拟 VL 抖动")
	}
	return "task-" + filter, nil
}

func enabledPolicy() Policy {
	p := DefaultPolicy()
	p.Enabled = true
	return p
}

// discardPolicy 是**显式知情放弃**的策略：两道闸都开。
//
// 只有测「删除路径本身」的用例才该用它。任何默认策略都不该走到删除——
// 这正是硬规则「删前归档」要保证的事，故此处显式命名以免被顺手复用。
func discardPolicy() Policy {
	p := enabledPolicy()
	p.Discard = true
	p.Sweep.VLSweep = true
	return p
}

// ---------- 策略 ----------

// TestDefaultPolicyMatchesRecommendedTTLs 守住用户决策 D1 的推荐档位。
func TestDefaultPolicyMatchesRecommendedTTLs(t *testing.T) {
	d := DefaultPolicy()
	want := map[string]time.Duration{
		"DEBUG": 3 * 24 * time.Hour,
		"INFO":  7 * 24 * time.Hour,
		"WARN":  30 * 24 * time.Hour,
		"ERROR": 90 * 24 * time.Hour,
	}
	n := d.Normalize()
	for lvl, ttl := range want {
		if n.ByLevel[lvl] != ttl {
			t.Errorf("推荐保留期 %s 应为 %s，得到 %s", lvl, ttl, n.ByLevel[lvl])
		}
	}
	if d.Enabled {
		t.Error("策略默认必须是未启用（零行为变化）；推荐值填好不等于替运维决定现在就开始删")
	}
	if d.Sweep.VLSweep {
		t.Error("执行闸默认必须是关：删除不可逆，默认姿态是「先算出来、看得见」")
	}
}

// TestNormalizeFillsMissingLevelWithRecommended：漏配一级不得退化成「永久保留」。
func TestNormalizeFillsMissingLevelWithRecommended(t *testing.T) {
	p := Policy{Enabled: true, ByLevel: map[string]time.Duration{"DEBUG": 48 * time.Hour}}
	n := p.Normalize()
	if n.ByLevel["DEBUG"] != 48*time.Hour {
		t.Errorf("显式配置必须保留，得到 %s", n.ByLevel["DEBUG"])
	}
	if n.ByLevel["INFO"] != DefaultTTLInfo {
		t.Errorf("漏配的 INFO 应补推荐值 %s，得到 %s（漏配一档就永久保留会让最占地方的那级逃过策略）",
			DefaultTTLInfo, n.ByLevel["INFO"])
	}
}

// TestEffectiveTTLUnknownLevelIsKeepForever：级别归类不了就绝不删。
func TestEffectiveTTLUnknownLevelIsKeepForever(t *testing.T) {
	p := enabledPolicy()
	for _, lvl := range []string{"", "NOTICE", "   ", "garbage"} {
		if got := p.EffectiveTTL("inst:1", lvl); got != 0 {
			t.Errorf("级别 %q 无法归类时必须永久保留，得到 %s", lvl, got)
		}
	}
}

// TestEffectiveTTLSourceOverridePrecedence：来源覆盖按「精确 > 长前缀 > 短前缀 > 全局」解析。
func TestEffectiveTTLSourceOverridePrecedence(t *testing.T) {
	p := enabledPolicy()
	p.Sources = []SourceOverride{
		{Match: "inst:*", ByLevel: map[string]time.Duration{"DEBUG": 24 * time.Hour}},
		{Match: "inst:14*", ByLevel: map[string]time.Duration{"DEBUG": 48 * time.Hour}},
		{Match: "inst:147", ByLevel: map[string]time.Duration{"DEBUG": 72 * time.Hour}},
		{Match: "node:1", ByLevel: map[string]time.Duration{"DEBUG": 0}}, // 显式永久保留
	}
	cases := []struct {
		source, level string
		want          time.Duration
		why           string
	}{
		{"inst:147", "DEBUG", 72 * time.Hour, "精确匹配优先"},
		{"inst:140", "DEBUG", 48 * time.Hour, "更长前缀优先"},
		{"inst:999", "DEBUG", 24 * time.Hour, "短前缀兜底"},
		{"inst:999", "INFO", DefaultTTLInfo, "来源未单独配该级别 ⇒ 沿用全局"},
		{"node:1", "DEBUG", 0, "显式写 0 = 永久保留（与「未列出」语义不同）"},
		{"node:2", "DEBUG", DefaultTTLDebug, "未命中任何覆盖"},
	}
	for _, c := range cases {
		if got := p.EffectiveTTL(c.source, c.level); got != c.want {
			t.Errorf("EffectiveTTL(%q,%q)=%s 期望 %s（%s）", c.source, c.level, got, c.want, c.why)
		}
	}
}

func TestValidateRejectsHazardousConfig(t *testing.T) {
	bad := []struct {
		name string
		p    Policy
	}{
		{"未知级别键", Policy{Enabled: true, ByLevel: map[string]time.Duration{"NOISE": time.Hour}}},
		{"负数保留期", Policy{Enabled: true, ByLevel: map[string]time.Duration{"DEBUG": -time.Hour}}},
		{"保留期过短", Policy{Enabled: true, ByLevel: map[string]time.Duration{"DEBUG": time.Minute}}},
		{"空 match", Policy{Enabled: true, Sources: []SourceOverride{{Match: "  "}}}},
		{"纯通配 match", Policy{Enabled: true, Sources: []SourceOverride{{Match: "*"}}}},
		{"match 含注入字符", Policy{Enabled: true, Sources: []SourceOverride{{Match: `inst:1" OR (*)`}}}},
		{"覆盖里的未知级别", Policy{Enabled: true, Sources: []SourceOverride{{Match: "inst:1", ByLevel: map[string]time.Duration{"X": time.Hour}}}}},
	}
	for _, c := range bad {
		if err := c.p.Validate(); err == nil {
			t.Errorf("%s：必须启动即拒（保留期配错的后果是不可逆的数据丢失）", c.name)
		}
	}
	good := enabledPolicy()
	good.Sources = []SourceOverride{{Match: "inst:*", ByLevel: map[string]time.Duration{"DEBUG": 24 * time.Hour}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("合法策略不应被拒：%v", err)
	}
	if err := (Policy{}).Validate(); err != nil {
		t.Fatalf("未启用时不应校验：%v", err)
	}
}

// ---------- 过滤器生成（安全关键） ----------

// TestBuildFilterRejectsInjection 是保留策略里最要紧的一组用例。
//
// 删除过滤器一旦被注入，后果是**不可逆地删掉不该删的日志**（VL 的删除没有回收站）。
// 因此这里穷举的是「运维把配置写坏或被人改了配置」时会出现的载荷：只要有一条能通过，
// 就意味着配置文件具备了「删除任意日志」的能力。
func TestBuildFilterRejectsInjection(t *testing.T) {
	payloads := []string{
		`*`,
		`inst:1 OR (*)`,
		`inst:1" OR (*)`,
		"inst:1\n) OR (*",
		`inst:1 | delete`,
		`inst:1 AND level:error`,
		`inst:1; drop`,
		`inst:1' OR '1'='1`,
		`inst:1 level:*`,
		`inst:1,level:error`,
		`inst:1"`,
		`inst:1 `,
	}
	for _, pl := range payloads {
		if _, err := buildFilter(pl, "DEBUG"); err == nil {
			t.Errorf("非法源标识 %q 必须被拒（否则配置文件就能删掉任意日志）", pl)
		}
	}
	// 级别侧同样只认白名单，不接受调用方传来的任意字符串。
	for _, lvl := range []string{"", "DEBUG OR (*)", "*", "level:error", "debug;drop"} {
		if _, err := buildFilter("inst:1", lvl); err == nil {
			t.Errorf("非法级别 %q 必须被拒", lvl)
		}
	}
}

func TestBuildFilterShapesAndQuotesSource(t *testing.T) {
	got, err := buildFilter("", "DEBUG")
	if err != nil {
		t.Fatalf("全实例档应可用：%v", err)
	}
	if got != "level:DEBUG" {
		t.Errorf("全实例档过滤器应为 level:DEBUG，得到 %q", got)
	}

	// 源标识含冒号，必须加引号：不加会被 LogsQL 解析成字段过滤（语义完全不同）。
	got, err = buildFilter("inst:147", "WARN")
	if err != nil {
		t.Fatalf("来源档应可用：%v", err)
	}
	want := `level:WARN AND log_source_id:"inst:147"`
	if got != want {
		t.Errorf("来源档过滤器\n 期望 %q\n 得到 %q", want, got)
	}

	// 别名归一：WARNING/FATAL 等在归一化阶段已折成规范级别，这里也必须一致。
	for _, c := range []struct{ in, want string }{
		{"warning", "level:WARN"},
		{"FATAL", "level:ERROR"},
		{"severe", "level:ERROR"},
		{"trace", "level:TRACE"},
	} {
		if got, err := buildFilter("", c.in); err != nil || got != c.want {
			t.Errorf("级别别名 %q 应归一为 %q，得到 %q（err=%v）", c.in, c.want, got, err)
		}
	}
}

// ---------- 计划 ----------

// TestPlanDoesNotExpandEverySource：目标数不得与来源数成正比。
func TestPlanDoesNotExpandEverySource(t *testing.T) {
	p := enabledPolicy()
	p.Sources = []SourceOverride{{Match: "inst:147", ByLevel: map[string]time.Duration{"DEBUG": time.Hour}}}

	// 62 个源（含 147），只有 147 写了覆盖。
	sources := make([]string, 0, 62)
	for i := 1; i <= 61; i++ {
		sources = append(sources, "inst:"+itoa(i))
	}
	sources = append(sources, "inst:147")
	plan, err := p.Plan(baseTime, sources, DefaultScanLevels())
	if err != nil {
		t.Fatalf("计划失败：%v", err)
	}
	// 5 个级别各一条全局目标 + 147 的 DEBUG 例外一条。
	if len(plan) != 6 {
		names := make([]string, 0, len(plan))
		for _, t := range plan {
			names = append(names, t.String())
		}
		t.Fatalf("目标数应为 6（5 条全局 + 1 条来源例外），得到 %d：\n%s",
			len(plan), strings.Join(names, "\n"))
	}
	var sourceTargets int
	for _, tg := range plan {
		if tg.SourceID != "" {
			sourceTargets++
			if tg.SourceID != "inst:147" || tg.Level != "DEBUG" {
				t.Errorf("意外的来源级目标：%s", tg)
			}
		}
	}
	if sourceTargets != 1 {
		t.Errorf("只应有 1 条来源级目标，得到 %d", sourceTargets)
	}
}

// TestPlanSkipsKeepForeverAndOverridesMatchingGlobal：永久保留与等同于全局的覆盖都不下发。
func TestPlanSkipsKeepForeverAndOverridesMatchingGlobal(t *testing.T) {
	p := enabledPolicy()
	p.ByLevel = map[string]time.Duration{"DEBUG": 0, "INFO": time.Hour, "WARN": 0, "ERROR": 0, "TRACE": 0}
	p.Sources = []SourceOverride{
		{Match: "inst:1", ByLevel: map[string]time.Duration{"INFO": time.Hour}},      // 与全局相同 ⇒ 不单独下发
		{Match: "inst:2", ByLevel: map[string]time.Duration{"INFO": 48 * time.Hour}}, // 与全局不同 ⇒ 下发
	}
	plan, err := p.Plan(baseTime, []string{"inst:1", "inst:2"}, DefaultScanLevels())
	if err != nil {
		t.Fatalf("计划失败：%v", err)
	}
	for _, tg := range plan {
		if tg.TTL <= 0 {
			t.Errorf("永久保留的级别不得出现在计划里：%s", tg)
		}
	}
	var global, scoped int
	for _, tg := range plan {
		if tg.SourceID == "" {
			global++
			if tg.Level != "INFO" {
				t.Errorf("只有 INFO 有保留期，不该出现 %s 档", tg.Level)
			}
		} else {
			scoped++
			if tg.SourceID != "inst:2" {
				t.Errorf("与全局相同的覆盖不该单独下发，出现 %s", tg)
			}
		}
	}
	if global != 1 || scoped != 1 {
		t.Fatalf("期望 1 条全局 + 1 条来源，得到 global=%d scoped=%d", global, scoped)
	}
}

func TestPlanWhenDisabledIsEmpty(t *testing.T) {
	plan, err := DefaultPolicy().Plan(baseTime, []string{"inst:1"}, DefaultScanLevels())
	if err != nil || len(plan) != 0 {
		t.Fatalf("未启用时必须没有目标（零行为变化），得到 %d 条 err=%v", len(plan), err)
	}
}

// ---------- 执行器 ----------

// TestSweeperDryRunNeverDeletes：默认姿态是「算出来但不动手」。
func TestSweeperDryRunNeverDeletes(t *testing.T) {
	fd := &fakeDeleter{}
	sw := NewSweeper(enabledPolicy(), fd)
	res, err := sw.Sweep(context.Background(), baseTime, []string{"inst:1"}, DefaultScanLevels())
	if err != nil {
		t.Fatalf("干跑不应报错：%v", err)
	}
	if !res.DryRun || res.Submitted != 0 {
		t.Fatalf("默认必须只计算不执行：dryRun=%v submitted=%d", res.DryRun, res.Submitted)
	}
	if len(fd.calls) != 0 {
		t.Fatalf("干跑不得调用删除接口，实际调用 %d 次", len(fd.calls))
	}
	if res.Planned == 0 {
		t.Fatal("干跑仍应算出计划（否则运维看不到「开了会删什么」）")
	}
	if got := sw.Stats().SkippedDisabled; got != int64(res.Planned) {
		t.Errorf("干跑规模应被记录：期望 %d 得到 %d", res.Planned, got)
	}
}

// TestSweeperSubmitsCorrectFilterAndCutoff：执行闸打开后，过滤器与截止时间必须正确。
func TestSweeperSubmitsCorrectFilterAndCutoff(t *testing.T) {
	p := discardPolicy()
	fd := &fakeDeleter{}
	sw := NewSweeper(p, fd)

	res, err := sw.Sweep(context.Background(), baseTime, []string{"inst:1"}, DefaultScanLevels())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if res.DryRun || res.Submitted != res.Planned {
		t.Fatalf("执行闸打开后应全部下发：dryRun=%v submitted=%d planned=%d", res.DryRun, res.Submitted, res.Planned)
	}
	byFilter := map[string]call{}
	for _, c := range fd.calls {
		byFilter[c.Filter] = c
	}
	// 每个级别的截止时间必须等于 now - 该级别保留期。
	for _, c := range []struct {
		filter string
		ttl    time.Duration
	}{
		{"level:DEBUG", DefaultTTLDebug},
		{"level:INFO", DefaultTTLInfo},
		{"level:WARN", DefaultTTLWarn},
		{"level:ERROR", DefaultTTLError},
	} {
		got, ok := byFilter[c.filter]
		if !ok {
			t.Errorf("缺少目标过滤器 %q（实际：%v）", c.filter, keysOf(byFilter))
			continue
		}
		if want := baseTime.Add(-c.ttl); !got.Before.Equal(want) {
			t.Errorf("%s 的截止时间应为 %s，得到 %s", c.filter, want, got.Before)
		}
	}
}

// TestSweeperThrottlesRepeatedFilters：同一过滤器在间隔内不得重复下发。
func TestSweeperThrottlesRepeatedFilters(t *testing.T) {
	p := discardPolicy()
	p.Sweep.Interval = time.Hour
	fd := &fakeDeleter{}
	sw := NewSweeper(p, fd)

	first, _ := sw.Sweep(context.Background(), baseTime, []string{"inst:1"}, DefaultScanLevels())
	second, _ := sw.Sweep(context.Background(), baseTime.Add(time.Minute), []string{"inst:1"}, DefaultScanLevels())
	if second.Submitted != 0 {
		t.Errorf("间隔内的重复扫描不得再次下发，得到 %d 次", second.Submitted)
	}
	if second.Skipped != second.Planned {
		t.Errorf("应全部被节流，得到 skipped=%d planned=%d", second.Skipped, second.Planned)
	}

	// 超过间隔后恢复下发。
	third, _ := sw.Sweep(context.Background(), baseTime.Add(2*time.Hour), []string{"inst:1"}, DefaultScanLevels())
	if third.Submitted != third.Planned {
		t.Errorf("超过间隔后应重新下发，得到 submitted=%d planned=%d", third.Submitted, third.Planned)
	}
	if first.Submitted == 0 {
		t.Error("前置条件：首轮必须真的下发过")
	}
}

// TestSweeperIsolatesPerTargetFailures：单个目标失败不得影响其余目标。
//
// 保留清理是后台省空间动作，一次 VL 抖动不该让整轮作废——那会让过期数据一直留着。
func TestSweeperIsolatesPerTargetFailures(t *testing.T) {
	p := discardPolicy()
	fd := &fakeDeleter{failFor: "level:WARN"}
	sw := NewSweeper(p, fd)

	res, err := sw.Sweep(context.Background(), baseTime, []string{"inst:1"}, DefaultScanLevels())
	if err != nil {
		t.Fatalf("单个目标失败不应冒泡成整轮失败：%v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("应恰好 1 个目标失败，得到 %d", res.Failed)
	}
	if res.Submitted != res.Planned-1 {
		t.Fatalf("其余 %d 个目标必须照常下发，实际下发 %d", res.Planned-1, res.Submitted)
	}
	var warnFailed, othersOK bool
	for _, o := range res.Outcomes {
		if o.Target.Level == "WARN" && o.Err != nil {
			warnFailed = true
		}
		if o.Target.Level == "ERROR" && o.Err == nil && o.TaskID != "" {
			othersOK = true
		}
	}
	if !warnFailed {
		t.Error("失败的目标必须如实记录错误")
	}
	if !othersOK {
		t.Error("其余目标必须照常完成（错误隔离）")
	}
	if sw.Stats().LastError == "" {
		t.Error("最近错误应暴露到观测面")
	}
}

func TestSweeperDisabledPolicyIsNoop(t *testing.T) {
	fd := &fakeDeleter{}
	sw := NewSweeper(DefaultPolicy(), fd) // Enabled=false
	res, err := sw.Sweep(context.Background(), baseTime, []string{"inst:1"}, DefaultScanLevels())
	if err != nil || res.Planned != 0 || len(fd.calls) != 0 {
		t.Fatalf("未启用时必须完全不动：planned=%d calls=%d err=%v", res.Planned, len(fd.calls), err)
	}
}

func TestDescribeOutcomesSummarises(t *testing.T) {
	out := []TargetOutcome{
		{Target: SweepTarget{Level: "ERROR"}, TaskID: "t1"},
		{Target: SweepTarget{Level: "DEBUG"}, Skipped: true, SkipReason: "节流"},
	}
	got := DescribeOutcomes(out)
	if !strings.Contains(got, "ok(ERROR") || !strings.Contains(got, "skip(DEBUG)") {
		t.Errorf("摘要应区分成功与跳过，得到 %q", got)
	}
	if DescribeOutcomes(nil) != "无目标" {
		t.Errorf("空结果应有明确措辞，得到 %q", DescribeOutcomes(nil))
	}
}

func keysOf(m map[string]call) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
