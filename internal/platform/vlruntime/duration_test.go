package vlruntime

import (
	"testing"
	"time"
)

func TestParseRetentionApprovedUnitsAndPrecision(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"12M", 372 * 24 * time.Hour}, {"12", 372 * 24 * time.Hour}, {"30w", 210 * 24 * time.Hour}, {"2y", 730 * 24 * time.Hour},
		{"1.1d", 26*time.Hour + 24*time.Minute}, {"0.5M", 372 * time.Hour}, {"1d0.001s", 24*time.Hour + time.Millisecond},
		{"3d-12h6h", 54 * time.Hour}, {"1200M", 1200 * 31 * 24 * time.Hour}, {"1D", 24 * time.Hour},
	} {
		got, err := ParseRetention(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("%s: got=%s want=%s err=%v", tc.raw, got, tc.want, err)
		}
	}
	for _, raw := range []string{"", " ", "90d ", "NaN", "Inf", "NaNM", "+InfM", "1e999M", "1200.001M", "1201M", "999999999999999999999d", "90m", "0", "23h", "-3M", "1ns"} {
		if _, err := ParseRetention(raw); err == nil {
			t.Errorf("非法输入必须拒绝: %q", raw)
		}
	}
}
