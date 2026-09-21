package directprobe

import (
	"testing"
	"time"
)

// TestInvariant_CollectBudgetFitsHeartbeatTick 锁定「可配超时上界」与「心跳护栏」的数值关系
// （FR-446 复审 NEW-ISSUE A）：
//
//	MaxCollectBudget() + HeartbeatTickReserve ≤ HeartbeatInterval
//
// 若有人放宽 MaxTimeout 而不动预算/节拍，本用例立即失败——这正是旧实现缺失的自洽约束
// （硬编码 15s 预算 vs 可配到 30s 的直探超时）。
func TestInvariant_CollectBudgetFitsHeartbeatTick(t *testing.T) {
	if got, limit := MaxCollectBudget(), HeartbeatInterval-HeartbeatTickReserve; got > limit {
		t.Fatalf("采集预算上界 %s 必须 ≤ 节拍 − 余量 %s，否则心跳积压", got, limit)
	}
	if !BudgetCoversTick(MaxCollectBudget()) {
		t.Fatalf("预算上界 %s 与节拍 %s 不自洽", MaxCollectBudget(), HeartbeatInterval)
	}
	// 预算必须严格小于节拍本身。
	if MaxCollectBudget() >= HeartbeatInterval {
		t.Fatalf("预算上界 %s 必须严格小于节拍 %s", MaxCollectBudget(), HeartbeatInterval)
	}
}

// TestInvariant_BudgetCoversSingleInstanceWorst 预算上界必须 ≥ 单实例（全来源）同源串行最坏，
// 否则该实例的心跳时序样本每拍都会被预算砍成「不可用」。
func TestInvariant_BudgetCoversSingleInstanceWorst(t *testing.T) {
	budget := MaxCollectBudget()
	worst := WorstCaseSerial(true, true, true, MaxTimeout, MaxTimeout)
	if budget < worst {
		t.Fatalf("预算上界 %s 小于单实例最坏 %s：实例指标会被静默砍掉", budget, worst)
	}
}

// TestInvariant_NormalizeKeepsWithinUpperBound 归一结果永不超过上界，且非正值回退默认。
func TestInvariant_NormalizeKeepsWithinUpperBound(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, DefaultTimeout},
		{-1, DefaultTimeout},
		{DefaultTimeout, DefaultTimeout},
		{MaxTimeout, MaxTimeout},
		{MaxTimeout + time.Second, MaxTimeout},
		{time.Hour, MaxTimeout},
	}
	for _, tc := range cases {
		if got := NormalizeTimeout(tc.in); got != tc.want {
			t.Errorf("NormalizeTimeout(%s) = %s，期望 %s", tc.in, got, tc.want)
		}
		if got := NormalizeTimeout(tc.in); got > MaxTimeout {
			t.Errorf("NormalizeTimeout(%s) = %s 突破上界 %s", tc.in, got, MaxTimeout)
		}
	}
}

// TestCollectBudgetFor_FollowsEffectiveTimeouts 预算随生效超时推导（不是定值）：
// 超时越大预算越大，且始终 ≥ 对应来源组合的单实例最坏。
func TestCollectBudgetFor_FollowsEffectiveTimeouts(t *testing.T) {
	def := CollectBudgetFor(DefaultTimeout, DefaultTimeout)
	max := CollectBudgetFor(MaxTimeout, MaxTimeout)
	if !(max > def) {
		t.Fatalf("预算应随生效超时增大：默认 %s，上界 %s", def, max)
	}
	if max != MaxCollectBudget() {
		t.Fatalf("上界预算 %s 应等于 MaxCollectBudget() %s", max, MaxCollectBudget())
	}

	for _, tc := range []struct{ slp, query time.Duration }{
		{DefaultTimeout, DefaultTimeout},
		{MaxTimeout, DefaultTimeout},
		{MaxTimeout, MaxTimeout},
		{5 * time.Second, 5 * time.Second},
	} {
		budget := CollectBudgetFor(tc.slp, tc.query)
		worst := WorstCaseSerial(true, true, true, tc.slp, tc.query)
		if budget < worst {
			t.Errorf("slp=%s query=%s：预算 %s < 单实例最坏 %s", tc.slp, tc.query, budget, worst)
		}
		if !BudgetCoversTick(budget) {
			t.Errorf("slp=%s query=%s：预算 %s 与节拍 %s 不自洽", tc.slp, tc.query, budget, HeartbeatInterval)
		}
	}

	// 超上界的下发值被收敛到上界预算，不会把预算撑到节拍之外。
	if got, want := CollectBudgetFor(MaxTimeout+time.Minute, MaxTimeout+time.Minute), MaxCollectBudget(); got != want {
		t.Errorf("超上界值应收敛到上界预算 %s，实际 %s", want, got)
	}
	// 非正值回退默认（不把预算缩到不覆盖默认最坏）。
	if got, want := CollectBudgetFor(0, 0), CollectBudgetFor(DefaultTimeout, DefaultTimeout); got != want {
		t.Errorf("非正超时应回退默认预算 %s，实际 %s", want, got)
	}
}

// TestWorstCaseSerial_CountsOnlyConfiguredSources 未配置的来源不占用最坏时延。
func TestWorstCaseSerial_CountsOnlyConfiguredSources(t *testing.T) {
	if got := WorstCaseSerial(false, false, false, MaxTimeout, MaxTimeout); got != 0 {
		t.Errorf("无来源时最坏应为 0，实际 %s", got)
	}
	if got := WorstCaseSerial(true, false, false, MaxTimeout, MaxTimeout); got != ProbeScrapeTimeoutCap {
		t.Errorf("仅探针时最坏应为 %s，实际 %s", ProbeScrapeTimeoutCap, got)
	}
	if got := WorstCaseSerial(false, true, false, MaxTimeout, MaxTimeout); got != MaxTimeout {
		t.Errorf("仅 SLP 时最坏应为 %s，实际 %s", MaxTimeout, got)
	}
}
