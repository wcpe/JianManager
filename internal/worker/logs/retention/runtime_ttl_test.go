package retention

import (
	"testing"
	"time"
)

func TestRuntimeTTLUsesApprovedVictoriaLogsUnits(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		days float64
	}{
		{"90d", 90}, {"730d", 730}, {"3", 93}, {"3M", 93}, {"0.5M", 15.5}, {"2y", 730},
		{"13w", 91}, {"24h", 1}, {"1d12h", 1.5}, {"3d-12h6h", 2.25}, {"1D", 1},
	} {
		got, err := ParseRuntimeTTL(tc.raw)
		if err != nil || got != time.Duration(tc.days*float64(24*time.Hour)) {
			t.Errorf("批准运行时 %q: got=%s err=%v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{"", " ", "90m", "NaN", "Inf", "1e999M", "-3M", "1201M", "0", "23h", "1month", "1ns", "90d "} {
		if _, err := ParseRuntimeTTL(raw); err == nil {
			t.Errorf("非法/运行时拒绝的TTL必须拒绝: %q", raw)
		}
	}
}

// VL 原生删除按 UTC 日比较，等值90d仅为兼容，不能冒充扫描或失败保护余量。
func TestHotWindowUTCDayAndScanBoundary(t *testing.T) {
	p := DefaultPolicy()
	p.Enabled = true
	part := PartitionRef{StorageNamespace: "inst:1", UTCDay: "20260101", Owner: "hot"}
	eligible := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		now time.Time
		due bool
	}{
		{eligible.Add(-time.Nanosecond), false}, {eligible, true}, {eligible.Add(p.Sweep.Interval), true},
	} {
		plan := p.PlanArchive(tc.now, []PartitionRef{part}, true)
		if len(plan) != 1 || !plan[0].EligibleAt.Equal(eligible) || plan[0].Due != tc.due {
			t.Fatalf("日结束与扫描边界错误: %+v", plan)
		}
	}
	// 官方 native 判据 day < floor((now-TTL)/day)，正好同一时刻越界。
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix() / 86400
	for _, runtime := range []time.Duration{90 * 24 * time.Hour, 91 * 24 * time.Hour} {
		minDay := (eligible.Add(p.Sweep.Interval).Unix() - int64(runtime/time.Second)) / 86400
		if expired := day < minDay; expired != (runtime == 90*24*time.Hour) {
			t.Fatalf("VL日分区TTL边界失真: runtime=%s expired=%v", runtime, expired)
		}
	}
}

func TestRetryableArchiveIgnoresAgeButNeverDiscardsWithoutMover(t *testing.T) {
	p := DefaultPolicy()
	p.Enabled = true
	p.Discard = true
	part := PartitionRef{StorageNamespace: "inst:1", UTCDay: "20261001", Owner: OwnerColdName, InFlight: true, Retryable: true}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for _, available := range []bool{false, true} {
		plan := p.PlanArchive(now, []PartitionRef{part}, available)
		if len(plan) != 1 || !plan[0].Due || plan[0].Action == ActionDiscard {
			t.Fatalf("恢复不得裸删或被年龄阻塞: %+v", plan)
		}
		if available && plan[0].Action != ActionMoveToCold {
			t.Fatalf("必须续跑搬运: %+v", plan)
		}
	}
}
