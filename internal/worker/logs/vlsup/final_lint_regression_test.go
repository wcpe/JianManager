package vlsup

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinalLintQueryTimeRangeCompatibilityReturnsUnavailableOnInvalidRetention(t *testing.T) {
	for _, raw := range []string{"0", "not-a-duration", "1d1q", "1e999y"} {
		t.Run(raw, func(t *testing.T) {
			cfg := InstanceConfig{RetentionPeriod: raw}
			_, err := cfg.QueryTimeRangeLimitChecked()
			require.Error(t, err)
			require.Empty(t, cfg.QueryTimeRangeLimit(), "兼容入口必须明确返回不可用而不是猜测窗口")
		})
	}
	for _, raw := range []string{"120d", "1.1d", ".5M", "2y", "1d1h"} {
		cfg := InstanceConfig{RetentionPeriod: raw}
		want, err := cfg.QueryTimeRangeLimitChecked()
		require.NoError(t, err)
		require.NotEmpty(t, want)
		require.Equal(t, want, cfg.QueryTimeRangeLimit())
	}
}
