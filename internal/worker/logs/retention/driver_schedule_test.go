package retention

import (
	"context"
	"testing"
	"time"
)

// 本文件守住「搬运的调度口径」：每天固定时刻一轮（默认凌晨 3 点），而不是间隔 ticker。
//
// 为什么单独测纯函数 nextSweepAt：调度最容易出错的地方是**跨日边界**
// （早于目标时刻 → 当天；晚于 → 次日；恰好等于 → 次日），
// 而这三种情况的差别在现场只表现为「这一轮早跑了一天 / 晚跑了一天」，极难从日志里发现。
// 循环本身（Run）只负责「首轮立即执行 + 等到下一个时刻」，由 TestDriverRunStopsOnContextCancel
// 与 TestDriverRunUsesDailySchedule 覆盖。

func mustParse(t *testing.T, layout, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(layout, value, time.UTC)
	if err != nil {
		t.Fatalf("测试数据无法解析 %q：%v", value, err)
	}
	return parsed
}

// TestNextSweepAtDailyBoundaries 覆盖「给定当前时刻算下一次触发」的全部边界。
func TestNextSweepAtDailyBoundaries(t *testing.T) {
	cases := []struct {
		name string
		now  string
		hour int
		want string
		why  string
	}{
		{"3 点之前 → 当天 3 点", "2026-10-02 01:30:00", 3, "2026-10-02 03:00:00", "当天还没到目标时刻"},
		{"3 点之前（跨零点后不久）→ 当天 3 点", "2026-10-02 00:00:01", 3, "2026-10-02 03:00:00", "刚过午夜仍在当天"},
		{"3 点之后 → 次日 3 点", "2026-10-02 03:00:01", 3, "2026-10-03 03:00:00", "当天目标时刻已过"},
		{"3 点之后（临近午夜）→ 次日 3 点", "2026-10-02 23:59:59", 3, "2026-10-03 03:00:00", "跨月/跨日都必须顺延到次日"},
		{"恰好 3 点整 → 次日 3 点", "2026-10-02 03:00:00", 3, "2026-10-03 03:00:00", "严格大于：避免刚跑完就立刻再跑一轮"},
		{"小时为 0（午夜）且当前更晚 → 次日 0 点", "2026-10-02 00:00:01", 0, "2026-10-03 00:00:00", "0 是有效值，不是「未配置」"},
		{"小时为 0（午夜）且当前在前一天 → 当天 0 点", "2026-10-01 23:59:59", 0, "2026-10-02 00:00:00", "跨日靠 AddDate 保持整点"},
		{"小时为 23 且当前更早 → 当天 23 点", "2026-10-02 22:00:00", 23, "2026-10-02 23:00:00", "最大值边界"},
		{"小时为 23 且当前已过 → 次日 23 点", "2026-10-02 23:00:01", 23, "2026-10-03 23:00:00", "最大值边界 + 跨日"},
		{"月末跨到次月首日", "2026-10-31 04:00:00", 3, "2026-11-01 03:00:00", "月边界必须由 AddDate 处理，不能手算 +24h 之类"},
		{"年末跨到次年首日", "2026-12-31 04:00:00", 3, "2027-01-01 03:00:00", "年边界"},
	}
	for _, c := range cases {
		now := mustParse(t, "2006-01-02 15:04:05", c.now)
		got := nextSweepAt(now, c.hour)
		want := mustParse(t, "2006-01-02 15:04:05", c.want)
		if !got.Equal(want) {
			t.Errorf("%s：now=%s hour=%d 应得 %s，得到 %s（%s）",
				c.name, c.now, c.hour, want.Format(time.RFC3339), got.Format(time.RFC3339), c.why)
		}
		if !got.After(now) {
			t.Errorf("%s：下一次触发必须严格晚于当前时刻，now=%s got=%s", c.name, now, got)
		}
		// 结果必须落在整点（秒/纳秒为 0）：否则「凌晨 3 点」会漂成 3:00:00.000000123。
		if got.Minute() != 0 || got.Second() != 0 || got.Nanosecond() != 0 {
			t.Errorf("%s：触发时刻必须是整点，得到 %s", c.name, got.Format(time.RFC3339Nano))
		}
		// 间隔必须落在 (0, 24h]：比 24h 大说明跳过了某一天。
		if d := got.Sub(now); d <= 0 || d > 24*time.Hour {
			t.Errorf("%s：等待时长应在 (0, 24h]，得到 %s", c.name, d)
		}
	}
}

// TestNextSweepAtKeepsWallClockAcrossDST 验证跨夏令时用 AddDate 保持墙上整点。
//
// 用一个真实带夏令时的时区（美国东部）：2026-03-08 02:00 本地时间不存在（跳到 03:00），
// 此时「次日 3 点」必须仍是本地 3 点，而不是「当前 + 24h」算出来的 4 点。
func TestNextSweepAtKeepsWallClockAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("测试环境缺少时区库（%v），跳过夏令时用例", err)
	}
	// 2026-03-07 12:00 EST（UTC-5）；下一个本地 3 点是 2026-03-08 03:00 EDT（UTC-4，夏令时首日）。
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, ny)
	got := nextSweepAt(now, 3)
	if got.Hour() != 3 {
		t.Fatalf("跨夏令时后必须仍是本地 3 点，得到 %s", got.Format(time.RFC3339))
	}
	if want := time.Date(2026, 3, 8, 3, 0, 0, 0, ny); !got.Equal(want) {
		t.Fatalf("应为 %s，得到 %s", want.Format(time.RFC3339), got.Format(time.RFC3339))
	}
	// 该日只有 23 小时：用 +24h 的实现会落到 4 点，这里显式钉住。
	if d := got.Sub(now); d >= 24*time.Hour {
		t.Errorf("夏令时首日不足 24h，得到等待 %s", d)
	}
}

// TestDriverScheduleModeSelection 守住「默认每日固定时刻、显式 -1 才回退 ticker」的档位选择。
func TestDriverScheduleModeSelection(t *testing.T) {
	// 默认策略（未显式配 sweep_at_hour）⇒ 凌晨 3 点。
	d := NewDriver(enabledPolicy(), &fakeLister{}, nil, nil)
	if got := d.SweepAtHour(); got != DefaultSweepAtHour {
		t.Errorf("默认应为每日 %d 点，得到 %d", DefaultSweepAtHour, got)
	}
	if got := d.untilNext(mustParse(t, "2006-01-02 15:04:05", "2026-10-02 01:00:00")); got != 2*time.Hour {
		t.Errorf("01:00 时应等待 2h 到 03:00，得到 %s", got)
	}

	// 显式 -1 ⇒ ticker 兼容档，节奏回到 trigger.interval。
	p := enabledPolicy()
	p.Trigger.SweepAtHour = SweepAtHourIntervalTicker
	p.Trigger.Interval = 20 * time.Minute
	d = NewDriver(p, &fakeLister{}, nil, nil)
	if got := d.SweepAtHour(); got != SweepAtHourIntervalTicker {
		t.Errorf("显式 -1 应保留兼容档标记，得到 %d", got)
	}
	if got := d.untilNext(mustParse(t, "2006-01-02 15:04:05", "2026-10-02 01:00:00")); got != 20*time.Minute {
		t.Errorf("兼容档应按 trigger.interval 等待 20m，得到 %s", got)
	}

	// SetPolicy 必须立即切换档位（否则热更新配置后节奏不变）。
	p.Trigger.SweepAtHour = 5
	d.SetPolicy(p)
	if got := d.SweepAtHour(); got != 5 {
		t.Errorf("SetPolicy 后应切到 5 点，得到 %d", got)
	}
}

// TestDriverRunUsesDailySchedule 验证 Run 的循环体：**首轮立即执行**，之后进入长等待
//（下一个每日时刻），且 ctx 取消能及时退出。
//
// 目标时刻取「当前小时 +2」，使下一次触发至少在 1 小时后——这样 800ms 观察窗口内
// 若还跑出第二轮，就说明调度退回了短间隔 tick。
func TestDriverRunUsesDailySchedule(t *testing.T) {
	p := enabledPolicy()
	p.Trigger.SweepAtHour = (time.Now().Hour() + 2) % 24
	mv := &fakeMover{}
	d := NewDriver(p, &fakeLister{parts: driverFixture()}, mv, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	// 首轮立即执行：等到搬运真的发生（而不是靠 sleep 猜）。
	deadline := time.Now().Add(5 * time.Second)
	for len(mv.calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(mv.calls) == 0 {
		cancel()
		<-done
		t.Fatal("首轮必须立即执行，不等第一个目标时刻（启动时正是过期分区最需要处理的时候）")
	}
	callsAfterFirstRound := len(mv.calls)
	// 每日调度下，第二轮至少要到下一个整点：800ms 内绝不应再跑。
	time.Sleep(800 * time.Millisecond)
	if got := len(mv.calls); got != callsAfterFirstRound {
		cancel()
		<-done
		t.Fatalf("每日调度不得在 800ms 内跑第二轮，首轮 %d 次调用后变成 %d 次", callsAfterFirstRound, got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未随 context 取消及时退出（等待期间必须 select ctx.Done）")
	}
}

// TestDriverRunDisabledDoesNotRun 守住既有语义：未启用策略时 Run 直接返回、不做任何事。
func TestDriverRunDisabledDoesNotRun(t *testing.T) {
	mv := &fakeMover{}
	d := NewDriver(DefaultPolicy(), &fakeLister{parts: driverFixture()}, mv, nil)
	done := make(chan struct{})
	go func() {
		d.Run(context.Background()) // ctx 永不取消：未启用时必须自己立刻返回
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("策略未启用时 Run 必须直接返回，不得进入等待循环")
	}
	if len(mv.calls) != 0 {
		t.Fatalf("策略未启用时不得搬运，却搬了 %v", mv.calls)
	}
}
