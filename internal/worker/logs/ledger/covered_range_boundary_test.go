package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCoveredByPositionRangesToleratesSeparatorOnBothEnds（2026-10-04 现场主因 ✗✓）：
// **与已覆盖段两端各差 1 字节（行分隔符）的缺口必须可消解**。
//
// 现场实据：`inst:156` 未消解 DELIVER_ERROR 缺口 `[68,163,086 → 68,528,091]` vs ACKED 段
// `[68,163,087 → 68,528,090]` —— **两端各差 1 字节** ✗；原实现只放宽起点 ⇒ 因终点多 1 字节
// 永远无法消解 ⇒ 全库 67 条同型（3.3× 于 APPEND_REJECTED ✗）= "60 源死锁"主因 ✓。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：去掉终点容差（旧实现）⇒ 本用例「两端各差 1 字节」处红 ✓；
//   - 变异②：把容差放宽到 2 字节 ⇒ 「真正的空洞（≥2 字节）不得被当成已覆盖」处红 ✓（缺口语义红线 ✗）。
func TestCoveredByPositionRangesToleratesSeparatorOnBothEnds(t *testing.T) {
	ranges := []PositionRange{{From: 68_163_087, To: 68_528_090}}

	// 现场同型：两端各差 1 字节 ⇒ **必须**视为已覆盖 ✓。
	require.True(t, CoveredByPositionRanges(ranges, 68_163_086, 68_528_091),
		"两端各差 1 字节（行分隔符）的缺口必须可消解（现场 67 条同型的形态 ✗✓）")

	// 起点单侧容差（原实现既有语义 ✓ 保持不变）。
	require.True(t, CoveredByPositionRanges(ranges, 68_163_086, 68_528_090))

	// 红线：容差是 1 字节（分隔符），**真正的空洞 ≥2 字节不得通过** ✗。
	require.False(t, CoveredByPositionRanges(ranges, 68_163_085, 68_528_090),
		"起点早 2 字节 = 真有空洞，不得宣称已覆盖 ✗")
	require.False(t, CoveredByPositionRanges(ranges, 68_163_087, 68_528_092),
		"终点晚 2 字节 = 真有空洞，不得宣称已覆盖 ✗")

	// 溢出保护：上界端点不得因 +1 回绕 ✓。
	require.True(t, CoveredByPositionRanges([]PositionRange{{From: 0, To: ^uint64(0)}}, 0, ^uint64(0)))
}
