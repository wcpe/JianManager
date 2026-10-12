package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是「轮预算取消即全失」修复（2026-10-03，逐源检查点化）的确定性红/绿：
//
// 现场（23:17/23:29/23:43 连续多轮）：深积压下单源即可吃满 45s 轮预算 ⇒ 每轮 checkedSources=0–1；
// 而原实现**在"取候选"时就推进游标 8 格** ⇒ 未处理的 7–8 个源被跳过（要等整圈 ⌈源数/8⌉ 轮），
// 这正是"取消即失"的实质。
//
// 修复：取候选**只读**（不推进游标）；游标由 advanceReconcileCursor 在**每个源处理结束后**
// 逐源推进 ⇒ 取消时停在"第一个未处理源"——已完成的不重做、未完成的下一轮从断点继续（跨轮累积覆盖）。
//
// 红/绿口径：
//   - 绿（现状）：未处理任何源时连取两次候选必须一致；处理 1 源后下一轮从"第二个源"继续（不重做不跳过）；
//   - 红（变异：在 nextReconcileCandidates 末尾恢复 `cursor += count`）⇒ 第二次候选直接跳过前两源 ⇒ 转红。
func TestReconcileCursorCheckpointsPerSource(t *testing.T) {
	m := &Manager{
		pipes: map[string]*pipeline.Pipeline{
			"inst:a/file": nil, "inst:b/file": nil, "inst:c/file": nil, "inst:d/file": nil,
		},
		sources: map[string]SourceConfig{
			"inst:a/file": {}, "inst:b/file": {}, "inst:c/file": {}, "inst:d/file": {},
		},
	}

	c1 := m.nextReconcileCandidates(2)
	require.Len(t, c1, 2)
	c1again := m.nextReconcileCandidates(2)
	require.Equal(t, c1[0].key, c1again[0].key,
		"取候选必须只读：未处理任何源时连续两次候选从同一处开始（红证=取即推进 ⇒ 直接跳过前 8 个）")

	// 模拟"处理完 c1[0] 后逐源检查点"：
	m.advanceReconcileCursor()
	c2 := m.nextReconcileCandidates(2)
	require.Equal(t, c1[1].key, c2[0].key,
		"处理完 1 源后，下一轮必须从『第一个未处理源』继续（已完成不重做、未完成进入候选）")
	require.NotEqual(t, c1[0].key, c2[0].key, "已完成源不得重做")

	// 跨轮累积：再推进 1 格 ⇒ 覆盖第 3 源（4 源 × 每轮推进 1 ⇒ 4 轮覆盖全部）。
	m.advanceReconcileCursor()
	c3 := m.nextReconcileCandidates(1)
	require.Equal(t, "inst:c/file", c3[0].key, "逐源推进 2 格后，候选从第 3 源开始（跨轮累积覆盖）")
}

// 变异记录（2026-10-03 实测，逐字还原）：
//   - 变异：在 nextReconcileCandidates 末尾恢复 `m.reconcileLoopCursor = (cursor + count) % len(keys)`
//     （取即推进）⇒ 本用例第一断言转红（c1again 变为 [c,d]，c1[0]=a 不再相等）；还原后转绿。
