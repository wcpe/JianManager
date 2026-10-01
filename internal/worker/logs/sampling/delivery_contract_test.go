package sampling

import (
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件把「采样与投递/校验/缺口判据的交互」固化成回归。
// 这些性质由投递路径的既有语义提出（记录不得失序、区间不得重叠、event_id 必须唯一、
// 最大 record_end 不得回退），一旦破坏，现场表现是投影校验失败或缺口永远挂着。
//
// 前三条由采集侧（opt-verify-cost）在对投递路径做单位成本优化时显式提出；
// 第四条（最大 record_end 保持）是既有的轮转覆盖判定所依赖的。

// TestDeliveryOrderIsMonotonicAndNonOverlapping：汇总必须**就地**产出，
// 不得把被抑制的行挪到批尾，也不得让输出区间互相重叠。
//
// 破坏后的现场：投递批内部记录失序 → 校验查询按 [min,max] 取窗、命中集合对不上；
// 轮转覆盖判定按「末条 record_end」推进 → 被挪到批尾的汇总会把水位推错。
func TestDeliveryOrderIsMonotonicAndNonOverlapping(t *testing.T) {
	levels := []string{"DEBUG", "INFO", "ERROR", "DEBUG", "WARN"}
	msgs := []string{"noise a", "noise b", "tick 1", "tick 2"}
	in := make([]logtypes.Event, 0, 60)
	for i := 0; i < 60; i++ {
		in = append(in, mkEvent("inst:1", i, levels[i%len(levels)], msgs[i%len(msgs)], baseTime, "stdout"))
	}
	policy := Policy{Enabled: true,
		Level:  LevelFilter{MinLevel: "WARN"},
		Burst:  Burst{Window: time.Second, Threshold: 2},
		Budget: Budget{MaxEventsPerWindow: 6, Window: time.Second, KeepEvery: 2}}
	out := New(policy).Process(in)

	if len(aggregatesOf(out)) == 0 {
		t.Fatal("前置条件：夹具必须触发汇总，否则本用例是空跑")
	}

	// 顺序：输出的 Record.Start 必须单调不减。
	prev := uint64(0)
	for i, e := range out {
		if i > 0 && e.Record.Start < prev {
			t.Fatalf("第 %d 条失序：Start=%d 小于前一条的 Start=%d（汇总被挪到了批尾？）",
				i, e.Record.Start, prev)
		}
		prev = e.Record.Start
	}

	// 不重叠：区间两两不相交。
	for i := 1; i < len(out); i++ {
		if out[i].Record.Start <= out[i-1].Record.End {
			t.Fatalf("区间重叠：第 %d 条 [%d,%d) 与前一条 [%d,%d) 相交",
				i, out[i].Record.Start, out[i].Record.End, out[i-1].Record.Start, out[i-1].Record.End)
		}
	}

	// 就地性（更强的判据）：对每个输出的汇总事件，其跨度内**必须**确实包含被抑制的行，
	// 且紧邻其后的事件起点应接着它——即它没有跳出原本的位置。
	for i, e := range out {
		if !IsAggregate(e) {
			continue
		}
		if i+1 < len(out) && out[i+1].Record.Start > e.Record.End+1 {
			t.Fatalf("第 %d 条汇总 [%d,%d) 与下一条 [%d,%d) 之间出现空洞——汇总不在原位",
				i, e.Record.Start, e.Record.End, out[i+1].Record.Start, out[i+1].Record.End)
		}
	}
}

// TestMaxRecordEndIsPreserved：一批内**最大的 record_end 不得回退**。
//
// 轮转覆盖判定与投递水位都按本批末条的 record_end 推进；若采样让最大值变小，
// 水位就会停在采样后的位置，那一段真实读过的字节永远不被认作已覆盖。
func TestMaxRecordEndIsPreserved(t *testing.T) {
	policies := map[string]Policy{
		"等级过滤": {Enabled: true, Level: LevelFilter{MinLevel: "WARN"}},
		"高频抑制": {Enabled: true, Burst: Burst{Window: time.Second, Threshold: 2}},
		"全折光":  {Enabled: true, Level: LevelFilter{MinLevel: "ERROR"}},
	}
	in := make([]logtypes.Event, 0, 50)
	for i := 0; i < 50; i++ {
		lvl := "DEBUG"
		if i == 49 {
			lvl = "DEBUG" // 末条也是 DEBUG：最容易被整体折叠掉
		}
		in = append(in, mkEvent("inst:1", i, lvl, "noise", baseTime, "stdout"))
	}
	in = append(in, mkEvent("inst:1", 49, "DEBUG", "noise", baseTime, "stdout")) // 保证末条存在

	wantEnd := in[0].Record.End
	for _, e := range in {
		if e.Record.End > wantEnd {
			wantEnd = e.Record.End
		}
	}
	for name, p := range policies {
		out := New(p).Process(in)
		if len(out) == 0 {
			t.Fatalf("策略[%s] 输出为空——抑制绝不能把整批折光", name)
		}
		gotEnd := out[0].Record.End
		for _, e := range out {
			if e.Record.End > gotEnd {
				gotEnd = e.Record.End
			}
		}
		if gotEnd != wantEnd {
			t.Fatalf("策略[%s] 最大 record_end 回退：期望 %d 得到 %d", name, wantEnd, gotEnd)
		}
	}
}

// TestEventIDIsUniqueWithinBatchAndContractDerived：一条投递批内 event_id 必须唯一，
// 且必须保持 logtypes.EventID(源,区间,parser) 的**契约推导形式**。
//
// 为什么不能改成 agg-<start>-<end>-<level> 之类的人造 ID：
//   - 账本去重与投影校验都按 event_id 认事件（verifyProjectionChunk 用它做 want/allowed 查表）；
//   - 契约（logtypes.EventID）规定它就是 (源, 区间, parser) 的哈希，人造 ID 会让
//     汇总事件在校验的 want 表里查不到自己，投递面直接判失败。
//
// 唯一性本身是**推出来的**，不是碰巧：汇总的 (start,end) 是其成员区间的并集，
// 而成员事件都不在输出里；输出中各事件的区间两两不相交 ⇒ (start,end) 两两不同 ⇒
// 由 sha256 推导的 event_id 两两不同。
func TestEventIDIsUniqueWithinBatchAndContractDerived(t *testing.T) {
	levels := []string{"DEBUG", "DEBUG", "INFO", "DEBUG", "WARN", "ERROR"}
	in := make([]logtypes.Event, 0, 80)
	for i := 0; i < 80; i++ {
		in = append(in, mkEvent("inst:9", i, levels[i%len(levels)], "m", baseTime, "stdout"))
	}
	policy := Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"},
		Burst: Burst{Window: time.Second, Threshold: 2}}
	out := New(policy).Process(in)

	if len(aggregatesOf(out)) == 0 {
		t.Fatal("前置条件：夹具必须触发汇总")
	}

	seen := make(map[string]int, len(out))
	for _, e := range out {
		seen[e.EventID]++
		// 契约推导形式：必须与 (源, 区间, parser 版本) 逐个自洽。
		if want := logtypes.EventID(e.Source, e.Record); e.EventID != want {
			t.Fatalf("event_id 不是契约推导形式：%s ≠ EventID(源,区间)=%s", e.EventID, want)
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("event_id 在同一批内重复 %d 次：%s", n, id)
		}
	}
}

// TestAggregateNeverShrinksOrGrowsSpanBeyondMembers：汇总区间既不得小于其成员并集，
// 也不得大于它（大于就是凸包 / 覆盖了没被抑制的字节）。
func TestAggregateNeverShrinksOrGrowsSpanBeyondMembers(t *testing.T) {
	in := []logtypes.Event{
		mkEvent("inst:1", 0, "DEBUG", "d", baseTime, "stdout"),    // [0,9]
		mkEvent("inst:1", 1, "DEBUG", "d", baseTime, "stdout"),    // [10,19]
		mkEvent("inst:1", 2, "ERROR", "keep", baseTime, "stdout"), // [20,29] 放行
		mkEvent("inst:1", 3, "DEBUG", "d", baseTime, "stdout"),    // [30,39]
	}
	out := New(Policy{Enabled: true, Level: LevelFilter{MinLevel: "INFO"}}).Process(in)
	aggs := aggregatesOf(out)
	if len(aggs) != 2 {
		t.Fatalf("期望 2 条汇总（被 ERROR 隔开），得到 %d 条", len(aggs))
	}
	if aggs[0].Record.Start != 0 || aggs[0].Record.End != 19 {
		t.Errorf("首条汇总区间应为 [0,19)，得到 [%d,%d)", aggs[0].Record.Start, aggs[0].Record.End)
	}
	if aggs[1].Record.Start != 30 || aggs[1].Record.End != 39 {
		t.Errorf("次条汇总区间应为 [30,39)，得到 [%d,%d)", aggs[1].Record.Start, aggs[1].Record.End)
	}
	// 关键：汇总绝不能被拉宽到把 [20,29) 的 ERROR 圈进来。
	for _, a := range aggs {
		if a.Record.Start <= 25 && a.Record.End >= 25 {
			t.Fatalf("汇总 [%d,%d) 把未被抑制的 ERROR 行圈了进来（凸包）", a.Record.Start, a.Record.End)
		}
	}
	if !strings.Contains(aggs[0].Message, "区间=[0,19)") {
		t.Errorf("正文中的区间应与 Record 一致：%s", strings.SplitN(aggs[0].Message, "\n", 2)[0])
	}
}
