package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是「取候选/读配置等 m.mu 有界化」（2026-10-04 收敛加速器）的确定性红/绿：
//
// 现场：轮预算（`roundCtx`，45s）在**取候选之前**建立，而"读配置"与"取候选"都无界等 m.mu
// （深积压实测 4m38s——"长持 m.mu 的规划者"）⇒ 等锁把整轮预算吃光、拿到锁时 ctx 已过期
// ⇒ 循环头立即 break（checked=0、detail 仅 round_budget_exhausted、**无任何源名**）——
// "轮从未开始"的总根源、duration 波动之源。
//
// 修复：① 读配置与取候选**都有界**（TryLock 轮询 ≤ lockBudget，超时留痕并快速放弃本轮）；
// ② **轮预算在两者之后建立** ⇒ 等锁不消耗预算。
//
// 绿（现状）：锁被持 1s、上限 200ms ⇒ **轮整体有界返回（≈200ms 量级）**且留痕
// `config_lock_timeout`、未处理任何源；
// 红（变异：读配置改回无界 `reconcileLoopConfig()`）⇒ 轮等满 1s、detail 无 timeout 留痕 ⇒ 转红。
func TestReconcileLockWaitIsBoundedAndTracked(t *testing.T) {
	m := &Manager{
		pipes:      map[string]*pipeline.Pipeline{"inst:lock/file": nil},
		sources:    map[string]SourceConfig{"inst:lock/file": {LogSourceID: "inst:lock", SourceGeneration: "g1"}},
		vlNotReady: map[string]string{}, // 最小 Manager 未走 New：noteVLNotReady 需已初始化
	}
	m.reconcileLoop = ReconcileLoopConfig{Enabled: true, RoundBudget: 30 * time.Second, MaxSourcesPerRound: 8}
	m.reconcileLockBudget = 200 * time.Millisecond // 可注入（测试用小值走真实限额路径）

	// 模拟"m.mu 被长持有"（1s ≫ 上限 200ms）：轮必须**有界返回**并留痕，不等满锁。
	m.mu.Lock()
	go func() { time.Sleep(1 * time.Second); m.mu.Unlock() }()

	t0 := time.Now()
	res := m.ReconcileRoundNow(context.Background())
	elapsed := time.Since(t0)
	detail := strings.Join(res.Errors, " | ")

	require.Contains(t, detail, "config_lock_timeout",
		"读配置等锁超上限 ⇒ 本轮快速放弃并留痕（红证=无界读取 ⇒ 等满 1s、无留痕）")
	require.Zero(t, res.CheckedSources, "未开始处理任何源（等锁轮）")
	require.Less(t, elapsed, 900*time.Millisecond, "有界返回：不得等满 1s 的锁（红证下 elapsed≈1s）")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：把 `cfg, okCfg := m.reconcileLoopConfigBounded(...)` 改回 `cfg := m.reconcileLoopConfig()`
//     （无界）⇒ 本用例转红（elapsed≈1s、detail 无 config_lock_timeout）；还原后转绿。
