package vlruntime

import (
	"testing"
	"time"
)

func TestParseRetentionLexicalBoundaryKeepsMonthAndCumulativePrecision(t *testing.T) {
	for _, raw := range []string{".5y", "1d.5h"} {
		if _, err := ParseRetention(raw); err == nil {
			t.Errorf("带单位分量点前必须有数字: %q", raw)
		}
	}
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{".5", 372 * time.Hour}, {".5M", 372 * time.Hour}, {"1d0.0009s0.0009s", 24*time.Hour + time.Millisecond}} {
		got, err := ParseRetention(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("裸月/M合法路径及累计毫秒不能退化成逐分量截断: %s got=%s want=%s err=%v", tc.raw, got, tc.want, err)
		}
	}
}
