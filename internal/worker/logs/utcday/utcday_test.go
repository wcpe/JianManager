package utcday

import (
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件是「归天」这件事的**唯一**回归点。
//
// 三个已修的生产缺陷都属于这一类，每个都在这里有对应用例：
//   - TestEventDay_FallsBackToIngestNotSourceDay ←「Catalog 记录建在源登记日而非事件日」
//   - TestWholeDayBefore_BoundaryDayNotFiltered  ← 保守方向：跨边界那天不许被当成过期
//   - TestEventDay_NoZeroDay                    ←「零水位被当成零上界」的同族约束：
//     0 值时间不得产生一个"第 0 天"。

func ev(eventTime, ingestTime string) logtypes.Event {
	return logtypes.Event{EventID: "e1", EventTimeUTC: eventTime, IngestTimeUTC: ingestTime}
}

// TestOf_NormalizesToUTC 任何偏移的时刻都归到同一个 UTC 日。
func TestOf_NormalizesToUTC(t *testing.T) {
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"UTC 正午", time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), "2026-11-02"},
		// HKT(+8) 的 11-03 01:00 = UTC 11-02 17:00 ⇒ 必须归 11-02。
		// 这正是「时间戳偏移 8 小时」缺陷的核心：用本地日会归错天。
		{"HKT 跨日", time.Date(2026, 11, 3, 1, 0, 0, 0, time.FixedZone("HKT", 8*3600)), "2026-11-02"},
		{"UTC 日末", time.Date(2026, 11, 2, 23, 59, 59, 999999999, time.UTC), "2026-11-02"},
		{"UTC 次日起点", time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), "2026-11-03"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Of(c.when); got != c.want {
				t.Fatalf("期望 %s，实测 %s", c.want, got)
			}
		})
	}
	if got := Of(time.Time{}); got != "" {
		t.Fatalf("零值时刻应返回空串（不得产生一个『第 0 天』），实测 %q", got)
	}
}

// TestBounds_HalfOpenAtMidnight 日界是左闭右开：End = 次日零点 - 1ns。
//
// 这个 1ns 的口径决定了「跨边界那天算不算过期」，写错一天就会误伤仍可见的数据。
func TestBounds_HalfOpenAtMidnight(t *testing.T) {
	start, end, ok := Bounds("2026-11-02")
	if !ok {
		t.Fatal("合法日期应解析成功")
	}
	wantStart := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC).Add(-time.Nanosecond)
	if !start.Equal(wantStart) {
		t.Fatalf("起点应为 %s，实测 %s", wantStart, start)
	}
	if !end.Equal(wantEnd) {
		t.Fatalf("终点应为 %s，实测 %s", wantEnd, end)
	}
	if _, _, ok := Bounds("2026-13-01"); ok {
		t.Fatal("非法日期应返回 ok=false（调用方据此保持原行为）")
	}
	if _, _, ok := Bounds(""); ok {
		t.Fatal("空串应返回 ok=false")
	}
}

// TestWholeDayBefore_BoundaryDayNotFiltered 跨越保留边界的那一天**不得**被判为过期。
//
// 保守方向：那天还有数据点落在边界之后（仍可见），滤掉它就是丢数据。
func TestWholeDayBefore_BoundaryDayNotFiltered(t *testing.T) {
	boundary := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	// 整日早于边界 ⇒ true
	if !WholeDayBefore("2026-10-04", boundary) {
		t.Fatal("整日早于边界的那天应判 true")
	}
	// 恰好在边界结束（dayEnd == 边界前一天 23:59:59.999999999）⇒ 仍是 true
	if !WholeDayBefore("2026-10-04", time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("日末恰在边界前 1ns 时应判 true")
	}
	// **跨边界的那天** ⇒ false（保守，不误伤）
	if WholeDayBefore("2026-10-05", boundary) {
		t.Fatal("边界当天仍可能有数据落在边界之后，不得判为整日过期")
	}
	if WholeDayBefore("2026-10-06", boundary) {
		t.Fatal("边界之后的天不应判为过期")
	}
	// 脏数据 ⇒ false（不因不可解析而丢弃）
	if WholeDayBefore("not-a-day", boundary) {
		t.Fatal("不可解析的日不得判为过期（那会静默丢数据）")
	}
}

// TestEventDay_FallsBackToIngestNotSourceDay 无语义时间时用**摄取时间**归天，
// 而不是源的登记日。
//
// 这是「Catalog 记录建在源登记日而非事件日」的直接回归：那次缺陷里
// 18 个源的记录 utcDay 停在登记日 2026-10-30，而数据落在 11-04，
// 查询 11-04 时按时间范围选不到那条记录 ⇒ 整页空。
//
// 转红方式（实测）：把 EventDay 的循环顺序改成先看 fallbackDay，本用例变红。
func TestEventDay_FallsBackToIngestNotSourceDay(t *testing.T) {
	// 语义时间缺失，摄取时间是 11-04，源登记日是 10-30（stale）。
	got, err := EventDay(ev("", "2026-11-04T08:00:00Z"), "2026-10-30")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got != "2026-11-04" {
		t.Fatalf("必须归到摄取日 2026-11-04（而不是源登记日 2026-10-30），实测 %s", got)
	}
	// 语义时间优先于摄取时间（跨天补采：11-02 的行在 11-04 被读到）
	got, err = EventDay(ev("2026-11-02T23:00:00Z", "2026-11-04T08:00:00Z"), "2026-10-30")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got != "2026-11-02" {
		t.Fatalf("语义时间必须优先，实测 %s", got)
	}
}

// TestEventDay_NoZeroDay 两者都缺失时只能用 fallback；fallback 也空则报错，绝不返回空串。
//
// 空串若被当成"某一天"继续往下走，就是「零水位当零上界」那一族错误的温床。
func TestEventDay_NoZeroDay(t *testing.T) {
	if _, err := EventDay(ev("", ""), ""); err == nil {
		t.Fatal("无任何时间来源时必须报错，不得静默返回空日")
	}
	got, err := EventDay(ev("", ""), "2026-10-30")
	if err != nil {
		t.Fatalf("有 fallback 时不应报错: %v", err)
	}
	if got != "2026-10-30" {
		t.Fatalf("应返回 fallback，实测 %s", got)
	}
	// 不可解析的时间串不能被当成有效时间
	if _, err := EventDay(ev("garbage", "also-garbage"), ""); err == nil {
		t.Fatal("不可解析的时间不得被接受")
	}
}

// TestEventDays_AllDaysSortedDeduped 一批跨天事件必须返回**全部**天（升序去重）。
//
// 只取第一天就是「一批跨天时其余天永远查不到」的成因。
func TestEventDays_AllDaysSortedDeduped(t *testing.T) {
	events := []logtypes.Event{
		ev("2026-11-04T01:00:00Z", ""),
		ev("2026-11-02T01:00:00Z", ""),
		ev("2026-11-04T02:00:00Z", ""), // 同天重复
		ev("2026-11-03T01:00:00Z", ""),
		ev("", "2026-11-05T01:00:00Z"), // 只有摄取时间 ⇒ EventDays 不看它
	}
	got := EventDays(events)
	want := []string{"2026-11-02", "2026-11-03", "2026-11-04"}
	if len(got) != len(want) {
		t.Fatalf("期望 %v，实测 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("期望升序去重 %v，实测 %v", want, got)
		}
	}
	if days := EventDays(nil); days != nil {
		t.Fatalf("空入参应返回 nil，实测 %v", days)
	}
}

// TestSorted_DoesNotMutateInput 排序返回副本（调用方可能还在用原切片）。
func TestSorted_DoesNotMutateInput(t *testing.T) {
	in := []string{"2026-11-04", "2026-11-02"}
	out := Sorted(in)
	if out[0] != "2026-11-02" || out[1] != "2026-11-04" {
		t.Fatalf("应先序，实测 %v", out)
	}
	if in[0] != "2026-11-04" {
		t.Fatalf("不得修改入参，实测 %v", in)
	}
}

// TestParse_RejectsBlankAndGarbage 解析必须不猜。
func TestParse_RejectsBlankAndGarbage(t *testing.T) {
	for _, raw := range []string{"", "   ", "garbage", "2026-11-02"} {
		if _, ok := Parse(raw); ok {
			t.Fatalf("%q 不应被解析为时刻", raw)
		}
	}
	when, ok := Parse("2026-11-02T03:04:05.123456789+08:00")
	if !ok {
		t.Fatal("合法 RFC3339Nano 应解析成功")
	}
	if when.Location() != time.UTC {
		t.Fatalf("必须归一到 UTC，实测 %s", when.Location())
	}
	// 带 +08:00 的 03:04 = UTC 前一日 19:04
	if got := Of(when); got != "2026-11-01" {
		t.Fatalf("应按 UTC 归天 2026-11-01，实测 %s", got)
	}
}
