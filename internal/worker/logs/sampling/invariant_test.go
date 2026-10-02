package sampling

import (
	"math/rand"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件守住不变量 R（区间守恒，见 doc.go）：
//
//	MergePositionRanges(Process(in)) == MergePositionRanges(in)
//
// R 不是整洁性要求，而是「开启采样之后，既有的缺口自动消解判据仍然成立」的前提。
// 下面四类用例分别钉住：恰等、跨缺口的真实消解、无抑制恒等、以及
// 「朴素丢弃会怎样」的反证（证明这组用例确实承重，而不是恒真）。

// ledgerKeyOf 返回夹具源对应的账本键。
func ledgerKeyOf(id string) ledger.SourceKey {
	return ledger.SourceKey{LogSourceID: id, SourceGeneration: "gen-1"}
}

// newLedgerWithGap 建一个账本，并在 [from,to] 上登记一条 DELIVER_ERROR 缺口
// （该原因在自动消解的允许名单内，见 ingest.gapAutoResolvableReasons）。
func newLedgerWithGap(t *testing.T, id string, from, to uint64) *ledger.Ledger {
	t.Helper()
	led := ledger.New()
	key := ledgerKeyOf(id)
	led.Ensure(key, testSource(id))
	if err := led.RecordGap(key, from, to, ledger.GapReasonDeliverError, "夹具：一次投递失败"); err != nil {
		t.Fatalf("登记缺口失败：%v", err)
	}
	if got := led.UnresolvedGapCount(key); got != 1 {
		t.Fatalf("前置条件：应有 1 条未解决缺口，得到 %d", got)
	}
	return led
}

// resolveThrough 用**真实判据**尝试消解缺口：只有区间完全落在某一段连续覆盖内才会被消解，
// 允许名单用的是自动消解出口同一份（DELIVER_ERROR 等）。
func resolveThrough(t *testing.T, led *ledger.Ledger, id string, events []logtypes.Event) int {
	t.Helper()
	resolved, err := led.ResolveGapsCoveredByRanges(
		ledgerKeyOf(id), spanOf(events), "delivery confirmed for this range",
		ledger.GapReasonDeliverError, ledger.GapReasonDeliverErrorWorkerSource, ledger.GapReasonWALCommitFailed)
	if err != nil {
		t.Fatalf("消解缺口失败：%v", err)
	}
	return resolved
}

// TestInvariantR1_RangeUnionPreservedExactly 是 R 的主用例：随机批次 × 多组策略，
// 断言输出的连续覆盖与输入**逐段相等**（不是包含、不是近似）。
func TestInvariantR1_RangeUnionPreservedExactly(t *testing.T) {
	policies := map[string]Policy{
		"等级过滤":  {Enabled: true, Level: LevelFilter{MinLevel: "INFO"}},
		"高频抑制":  {Enabled: true, Burst: Burst{Window: time.Second, Threshold: 2}},
		"每源预算":  {Enabled: true, Budget: Budget{MaxEventsPerWindow: 3, Window: time.Second, KeepEvery: 3}},
		"降级":    {Enabled: true, Level: LevelFilter{MinLevel: "WARN"}, Degrade: Degrade{Enabled: true, TargetLevel: "ERROR", DiskPercent: 50, Hold: time.Minute}},
		"三规则叠加": {Enabled: true, Level: LevelFilter{MinLevel: "INFO"}, Burst: Burst{Window: time.Second, Threshold: 3}, Budget: Budget{MaxEventsPerWindow: 5, Window: time.Second, KeepEvery: 2}},
	}
	levels := []string{"DEBUG", "INFO", "WARN", "ERROR", ""}
	msgs := []string{"alpha tick 1", "alpha tick 2", "beta session 77", "gamma", ""}

	rng := rand.New(rand.NewSource(20261002)) // 固定种子：CI 必须可复现
	for name, policy := range policies {
		for round := 0; round < 40; round++ {
			s := New(policy)
			if policy.Degrade.Enabled {
				s.UpdateSignal(Signal{DiskPercent: 90}, baseTime) // 一半的轮次让它处在降级态
			}
			n := 1 + rng.Intn(120)
			in := make([]logtypes.Event, 0, n)
			for i := 0; i < n; i++ {
				in = append(in, mkEvent("inst:1", i,
					levels[rng.Intn(len(levels))], msgs[rng.Intn(len(msgs))], baseTime, "stdout"))
			}
			want := spanOf(in)
			out := s.Process(in)
			got := spanOf(out)
			if !sameRanges(want, got) {
				t.Fatalf("策略[%s] 第 %d 轮违反不变量 R：\n 输入覆盖 %v\n 输出覆盖 %v\n（抑制不得在连续覆盖上留空洞）",
					name, round, want, got)
			}
			// 事件总数不得增加（汇总只做替换，不复制）。
			if len(out) > len(in) {
				t.Fatalf("策略[%s] 第 %d 轮输出条数 %d 超过输入 %d", name, round, len(out), len(in))
			}
		}
	}
}

// TestInvariantR1b_InterleavedLevelsWithChaoticClock 用乱序/重复时间戳压一遍 R：
// 窗口类规则在时间倒退时也必须只影响「折与不折」，绝不能影响区间覆盖。
func TestInvariantR1b_InterleavedLevelsWithChaoticClock(t *testing.T) {
	policy := Policy{Enabled: true,
		Level:  LevelFilter{MinLevel: "INFO"},
		Burst:  Burst{Window: time.Second, Threshold: 2},
		Budget: Budget{MaxEventsPerWindow: 2, Window: time.Second, KeepEvery: 3}}
	offsets := []time.Duration{0, -time.Second, 5 * time.Second, -30 * time.Second, 0, time.Hour}
	in := mkBatch("inst:1", 90, []string{"DEBUG", "INFO", "WARN"}, []string{"m1", "m2", "m3"},
		func(i int) time.Time { return baseTime.Add(offsets[i%len(offsets)]) })

	out := New(policy).Process(in)
	if want, got := spanOf(in), spanOf(out); !sameRanges(want, got) {
		t.Fatalf("时间戳乱序下违反不变量 R：\n 输入 %v\n 输出 %v", want, got)
	}
}

// TestInvariantR2_GapStillAutoResolvesAcrossSuppressedSpan 是本项最关键的一条：
// 缺口跨越「大量被抑制的行」，开启采样后**仍然能被自动消解**。
//
// 这正是 R 要保护的现场形态：某段字节曾经投递失败登记了缺口，重投时这些行又被采样压成
// 汇总事件。若实现是「直接丢弃」，这段位置的连续覆盖就会断开，缺口永远消不掉，
// 源被 ResumeAcquire 的「零未解决缺口」门禁焊死 —— 2026-10-01/02 那次 13 小时零数据的形态。
func TestInvariantR2_GapStillAutoResolvesAcrossSuppressedSpan(t *testing.T) {
	const id = "inst:1"
	// 400 条连续行：编码区间 [0, 3999]。
	in := mkBatch(id, 400, []string{"DEBUG", "INFO"}, []string{"noise A", "noise B"}, fixedTS(baseTime))
	last := in[len(in)-1].Record.End

	policy := Policy{Enabled: true,
		Level: LevelFilter{MinLevel: "INFO"},
		Burst: Burst{Window: time.Second, Threshold: 5}}

	led := newLedgerWithGap(t, id, 0, last)
	s := New(policy)
	out := s.Process(in)

	if got := s.Stats().Suppressed; got == 0 {
		t.Fatal("前置条件：夹具必须确实触发了抑制，否则本用例是空跑")
	}
	if resolved := resolveThrough(t, led, id, out); resolved != 1 {
		t.Fatalf("缺口跨越被抑制区间时仍须能自动消解，得到 resolved=%d（不变量 R 被破坏）", resolved)
	}
	if got := led.UnresolvedGapCount(ledgerKeyOf(id)); got != 0 {
		t.Fatalf("消解后不应残留未解决缺口，得到 %d", got)
	}
}

// TestInvariantR2b_NaiveDropBreaksGapResolution 是 R2 的反证（转红对照）：
// 把「不产出汇总、直接丢弃」的朴素实现摆出来，同一个缺口就**消解不掉**。
//
// 这条用例的价值在于证明 R2 不是恒真的：它精确复现了朴素采样会把系统焊死的那一步。
func TestInvariantR2b_NaiveDropBreaksGapResolution(t *testing.T) {
	const id = "inst:1"
	in := mkBatch(id, 400, []string{"DEBUG", "INFO"}, []string{"noise A", "noise B"}, fixedTS(baseTime))
	last := in[len(in)-1].Record.End

	led := newLedgerWithGap(t, id, 0, last)

	// 「朴素采样」：低于 min_level 的行直接删掉，不承接区间。
	naive := make([]logtypes.Event, 0, len(in))
	for _, e := range in {
		if LevelBelow(e.Level, "INFO") {
			continue
		}
		naive = append(naive, e)
	}
	if got := spanOf(naive); len(got) <= 1 {
		t.Fatalf("前置条件：朴素丢弃必须真的在覆盖上留下空洞，得到 %v", got)
	}
	if resolved := resolveThrough(t, led, id, naive); resolved != 0 {
		t.Fatalf("前置条件失败：朴素丢弃竟然也能消解缺口（resolved=%d），说明 R2 用例不承重", resolved)
	}
	if got := led.UnresolvedGapCount(ledgerKeyOf(id)); got != 1 {
		t.Fatalf("前置条件：缺口应仍未被消解，得到 %d", got)
	}

	// 同一个账本与同一批原始事件，换成真正的实现（产出汇总）即可消解。
	// 对照组与实验组共用同一份输入与同一个缺口，差异只来自「有没有汇总事件承接区间」。
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"},
		Burst: Burst{Window: time.Second, Threshold: 5}}).Process(in)
	if !sameRanges(spanOf(in), spanOf(out)) {
		t.Fatal("本实现的覆盖应与输入相等")
	}
	if resolved := resolveThrough(t, led, id, out); resolved != 1 {
		t.Fatalf("同口径下本实现必须能消解，得到 resolved=%d", resolved)
	}
}

// TestInvariantR3_NonContiguousInputKeepsItsHoles：输入本身就有洞（例如其中一段已在别处落库）
// 时，输出必须**原样保留这些洞**——抑制不得把两个分开的段缝成一段（那就是凸包）。
func TestInvariantR3_NonContiguousInputKeepsItsHoles(t *testing.T) {
	const id = "inst:1"
	var in []logtypes.Event
	for i := 0; i < 20; i++ {
		if i >= 8 && i < 12 {
			continue // 挖掉中间一段
		}
		in = append(in, mkEvent(id, i, "DEBUG", "noise", baseTime, "stdout"))
	}
	before := spanOf(in)
	if len(before) != 2 {
		t.Fatalf("前置条件：输入应有两段，得到 %v", before)
	}
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}, MaxAggregateEvents: 100}).Process(in)
	after := spanOf(out)
	if !sameRanges(spanOf(in), after) {
		t.Fatalf("必须原样保留输入的空洞：\n 输入 %v\n 输出 %v", before, after)
	}
	if len(after) != 2 {
		t.Fatalf("不得把两段缝成一段（凸包），得到 %v", after)
	}
}

// TestInvariantR3b_NoSuppressionIsExactIdentity：一条都没被抑制时必须逐字段恒等。
func TestInvariantR3b_NoSuppressionIsExactIdentity(t *testing.T) {
	in := mkBatch("inst:1", 30, []string{"ERROR", "WARN"}, []string{"problem"}, fixedTS(baseTime))
	policy := Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"},
		Burst: Burst{Window: time.Second, Threshold: 100}}
	out := New(policy).Process(in)
	if len(out) != len(in) {
		t.Fatalf("未触发任何规则时条数必须不变：%d → %d", len(in), len(out))
	}
	for i := range in {
		if out[i].EventID != in[i].EventID || out[i].CanonicalHash != in[i].CanonicalHash {
			t.Fatalf("第 %d 条被改写", i)
		}
	}
}

// TestInvariantR4_AggregateIsAnOrdinaryEvent：汇总事件必须在各条既有判据下与普通事件等价：
// 有合法的 EventID/规范哈希、区间非空、能被 MergePositionRanges 正常吸收。
func TestInvariantR4_AggregateIsAnOrdinaryEvent(t *testing.T) {
	in := mkBatch("inst:1", 12, []string{"DEBUG"}, []string{"same noise"}, fixedTS(baseTime))
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}}).Process(in)

	aggs := aggregatesOf(out)
	if len(aggs) != 1 {
		t.Fatalf("期望 1 条汇总，得到 %d", len(aggs))
	}
	a := aggs[0]
	if a.EventID == "" || a.CanonicalHash == "" {
		t.Fatal("汇总必须带 EventID 与规范内容哈希（账本去重与投影校验都依赖它们）")
	}
	if a.Record.End < a.Record.Start {
		t.Fatalf("汇总区间非法：[%d,%d)", a.Record.Start, a.Record.End)
	}
	if a.Source.LogSourceID == "" || a.Source.SourceGeneration == "" || a.Source.ParserVersion == "" {
		t.Fatal("汇总必须带完整源身份")
	}
	// 汇总的 EventID 必须与其参数一致——重投时才可能命中账本去重。
	wantID := logtypes.EventID(a.Source, a.Record)
	if a.EventID != wantID {
		t.Fatalf("汇总 EventID 与 (源,区间,parser) 不一致：%s vs %s", a.EventID, wantID)
	}
	wantHash := logtypes.CanonicalContentHash(a.EventTimeUTC, a.Level, a.Stream, a.Message)
	if a.CanonicalHash != wantHash {
		t.Fatalf("汇总规范哈希与正文不一致：%s vs %s", a.CanonicalHash, wantHash)
	}
}
