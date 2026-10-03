package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件是「per-source 校验预算」（2026-10-03 回退修复 ③）的**消队头阻塞**确定性红/绿：
//
// 现场（新进程 22:04–22:30 精确统计）：verify 13 批 / **0 成功** / 19 defer / 查询 1——深积压下
// 单轮墙钟 ≫ 轮级预算 30s ⇒ 轮内**后段源**的校验连查询都未发出即被 defer（queries=0）。
//
// 修复：每源在轮内被处理前各自设置截止（`pollOnce` → `setVerifySourceDeadline`）；校验
// **per-source 优先**（`verifyBudgetFor`），轮级仅作非"每源"路径的兜底。
//
// 红/绿口径（确定性、纯状态、无 I/O）：
//   - 绿（现状）：轮级预算已被"前段源"吃光（deadline 过期 + progressed=1）时，**有自身预算的源
//     仍然放行**（per-source 优先 ⇒ 不再连坐）；
//   - 红（变异：删除 per-source 分支、退回只读轮级）⇒ 同一断言返回 ErrVerifyDeferred ⇒ 转红。
func TestVerifySourceBudgetIsolatesHeadOfLineBlocking(t *testing.T) {
	m := &Manager{}
	m.verificationTimeout = 5 * time.Second
	src := SourceConfig{LogSourceID: "inst:headline", SourceGeneration: "g1"}

	// 模拟"轮内前段源已把轮级预算吃光"：
	m.verifyRoundDeadline.Store(time.Now().Add(-time.Second).UnixNano())
	m.verifyRoundProgress.Store(1)

	// 本源的独立预算充足：
	st := m.verifySourceState(src)
	st.deadlineNanos.Store(time.Now().Add(30 * time.Second).UnixNano())
	st.progressed.Store(0)

	_, err := m.verifyBudgetFor(src, "projection-x")
	require.NoError(t, err,
		"per-source 优先：轮级被前段源吃光后，本源仍应按自身预算放行（红证=退回只读轮级 ⇒ ErrVerifyDeferred）")
	require.Equal(t, int64(0), m.verifyDeferred.Load(), "本源的放行不得计入 defer 计数")
}

// 红（对照语义）：**未设 per-source 预算**的路径（启动恢复/对账）保持轮级兜底语义——
// 轮级耗尽时按原逻辑 defer（证明兜底分支未被修复误伤）。
func TestVerifyBudgetRoundFallbackStillDefersWhenExhausted(t *testing.T) {
	m := &Manager{}
	m.verificationTimeout = 5 * time.Second
	src := SourceConfig{LogSourceID: "inst:fallback", SourceGeneration: "g1"}

	m.verifyRoundDeadline.Store(time.Now().Add(-time.Second).UnixNano())
	m.verifyRoundProgress.Store(1) // 已有进展 ⇒ 不再触发"保底放行"

	_, err := m.verifyBudgetFor(src, "projection-y")
	require.ErrorIs(t, err, ErrVerifyDeferred, "轮级兜底路径语义保持：耗尽即延期（与修复前一致）")
}

// 保底语义 per-source 化：本源**尚无进展**时，即使预算已用尽也放行一块（"每源至少推进一块"）。
func TestVerifySourceBudgetKeepsPerSourceFloor(t *testing.T) {
	m := &Manager{}
	m.verificationTimeout = 5 * time.Second
	src := SourceConfig{LogSourceID: "inst:floor", SourceGeneration: "g1"}

	st := m.verifySourceState(src)
	st.deadlineNanos.Store(time.Now().Add(-time.Second).UnixNano()) // 已过期
	st.progressed.Store(0)                                          // 本源还没推进过任何一块

	_, err := m.verifyBudgetFor(src, "projection-z")
	require.NoError(t, err, "保底放行：本源首块即使超预算也必须放行（收敛性底线）")
	require.GreaterOrEqual(t, st.progressed.Load(), int64(1), "保底放行后计入本源进展")

	// 第二块起：预算仍耗尽且已有进展 ⇒ 正常延期。
	_, err = m.verifyBudgetFor(src, "projection-z")
	require.ErrorIs(t, err, ErrVerifyDeferred, "第二块不再保底（每源至多一块保底）")
}

// 变异记录（2026-10-03 实测，逐字还原）：
//   - 变异 1 —— 删除 verifyBudgetFor 的 per-source 分支（只保留轮级）：
//     TestVerifySourceBudgetIsolatesHeadOfLineBlocking 转红（NoError 断言收到 ErrVerifyDeferred）。
//   - 变异 2 —— 保底分支改为"始终 defer"（不判 progressed）：
//     TestVerifySourceBudgetKeepsPerSourceFloor 转红。
//   两变异均实测转红后逐字还原（见 `prod-deploy` 台账）。
