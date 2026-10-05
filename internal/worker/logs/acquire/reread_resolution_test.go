package acquire

// #179-① 小测：待重读消解词的**真源**必须可导出、谓词判定必须只认这两条（行为中性 ✓）。

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsReReadHealableResolutionMatchesExportedSource(t *testing.T) {
	require.Equal(t, ResolutionReReadOnResume, gapResolutionReReadOnResume, "别名必须与真源同值 ✓")
	require.Equal(t, ResolutionReReadOnResumeDelivery, gapResolutionReReadOnResumeDelivery, "别名必须与真源同值 ✓")
	require.True(t, IsReReadHealableResolution(ResolutionReReadOnResume), "复投类待重读词必须命中 ✓")
	require.True(t, IsReReadHealableResolution(ResolutionReReadOnResumeDelivery), "投递错类待重读词必须命中 ✓")
	require.False(t, IsReReadHealableResolution(""), "空词不得命中 ✓")
	require.False(t, IsReReadHealableResolution("operator-abandon"), "非待重读词不得命中（不放宽判据 ✓）")
}
