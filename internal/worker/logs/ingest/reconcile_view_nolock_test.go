package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是「候选 1：对账只读快照（配置 + 视图）」的确定性红/绿：
//
// 现场：m.mu 是全局热锁（投递/发布等 14 个等待者），对账轮此前的两道坎都要取锁——
// ①`reconcileLoopConfigBounded`（配置读）②`nextReconcileCandidatesBounded`（取候选）；
// 锁被长持时两道先后超时（`config_lock_timeout` 9-12 轮 wall 停死 5.00s）。
// 修法：配置快照（写点即时发布）+ 只读视图（keys+sources 同代快照，持锁写侧发布）
// ⇒ 两者读侧零取 m.mu。
//
// 绿（现状）：发布配置+视图后，锁被长持 ⇒ 对账轮**不出现** config/candidate_lock_timeout，
// 且候选数来自视图（Candidates==2）；
// 红（变异：视图与配置快照路径双双 return false）⇒ 两道超时断言转红。
func TestReconcileRoundAvoidsLockWithSnapshots(t *testing.T) {
	m := &Manager{
		pipes: map[string]*pipeline.Pipeline{
			"inst:a/file": nil, "inst:b/file": nil,
		},
		sources: map[string]SourceConfig{
			"inst:a/file": {LogSourceID: "inst:a", SourceGeneration: "g1"},
			"inst:b/file": {LogSourceID: "inst:b", SourceGeneration: "g1"},
		},
		vlNotReady: map[string]string{},
	}
	m.SetReconcileLoopConfig(ReconcileLoopConfig{Enabled: true, RoundBudget: 30 * time.Second, MaxSourcesPerRound: 8})
	m.reconcileLockBudget = 200 * time.Millisecond

	// 发布只读视图（生产由采集轮/源变更发布；测试手动触发）
	m.mu.Lock()
	m.publishReconcileViewLocked(true)
	m.mu.Unlock()

	// 锁被长持：配置读与取候选都必须零锁走快照
	m.mu.Lock()
	go func() { time.Sleep(600 * time.Millisecond); m.mu.Unlock() }()
	res := m.ReconcileRoundNow(context.Background())
	detail := strings.Join(res.Errors, " | ")
	require.NotContains(t, detail, "config_lock_timeout", "配置快照命中时不得走等锁路径")
	require.NotContains(t, detail, "candidate_lock_timeout", "只读视图命中时不得走等锁路径")
	require.Equal(t, 2, res.Candidates, "候选应来自只读视图（2 源）")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：`nextReconcileCandidatesFromView` 与 `reconcileLoopConfigFromSnapshot` 双双直接
//     `return nil,false` / `return ReconcileLoopConfig{},false`（禁用快照路径）⇒ 锁长持下
//     出现 config_lock_timeout（或 candidate_lock_timeout）⇒ 用例转红；还原后转绿。

// TestReconcileRoundZeroLockAfterStartupSnapshots 是 v2 修复（2026-10-04）的红/绿：
//
// 现场（#49 首轮实证）：配置快照的常规写点 SetReconcileLoopConfig 在生产**从不被调用**
// ⇒ 快照恒空 ⇒ 对账首道"配置读"永久走 TryLock ⇒ 锁紧张即 5s 超时
// （detail=`config_lock_timeout@cursor=`、candidates=0）。
// 修法：RunReconcileLoop 启动处一次性取锁发布"配置 + 视图"快照。
//
// 绿（现状）：启动发布后，锁被长持 ⇒ 对账轮两道坎（配置读/取候选）全零锁 ⇒ 无任何 *_lock_timeout
// 且候选来自视图（Candidates==2）；红（变异：启动发布改为不发布）⇒ 出现 config_lock_timeout ⇒ 转红。
func TestReconcileRoundZeroLockAfterStartupSnapshots(t *testing.T) {
	m := &Manager{
		pipes: map[string]*pipeline.Pipeline{
			"inst:a/file": nil, "inst:b/file": nil,
		},
		sources: map[string]SourceConfig{
			"inst:a/file": {LogSourceID: "inst:a", SourceGeneration: "g1"},
			"inst:b/file": {LogSourceID: "inst:b", SourceGeneration: "g1"},
		},
		vlNotReady: map[string]string{},
	}
	m.reconcileLoop = ReconcileLoopConfig{Enabled: true, RoundBudget: 30 * time.Second, MaxSourcesPerRound: 8}
	m.reconcileLockBudget = 200 * time.Millisecond

	// 模拟 RunReconcileLoop 启动：一次性发布配置 + 视图快照
	m.publishStartupReconcileSnapshots()

	m.mu.Lock()
	go func() { time.Sleep(600 * time.Millisecond); m.mu.Unlock() }()
	res := m.ReconcileRoundNow(context.Background())
	detail := strings.Join(res.Errors, " | ")
	require.NotContains(t, detail, "config_lock_timeout", "启动发布后配置读必须零锁")
	require.NotContains(t, detail, "candidate_lock_timeout", "启动发布后取候选必须零锁")
	require.Equal(t, 2, res.Candidates, "候选应来自启动发布的视图")
}

// TestReconcileViewSurvivesBeyondOldTTL 是 v3（2026-10-04）的红/绿：
//
// 现场（合并窗第二轮 07:46:10 实证）：视图 07:16:05 发布后，pollOnce 刷新在重负载下追不上
// 设计频率（40min 仅 17 次校验完成）⇒ `publishedAt` 30min 未更新 ⇒ 命中原 TTL ⇒ 对账回退
// TryLock ⇒ `candidate_lock_timeout@cursor=inst:143`。修法：取消视图 TTL（源集合变化走 force 发布，
// 陈旧代价仅"晚发现新源"）。
//
// 绿（现状）：把发布时刻人为拨回 31 分钟前，锁被长持 ⇒ 仍零锁取候选（Candidates==2）；
// 红（变异：恢复 TTL 过期判断）⇒ 出现 candidate_lock_timeout ⇒ 转红。
func TestReconcileViewSurvivesBeyondOldTTL(t *testing.T) {
	m := &Manager{
		pipes: map[string]*pipeline.Pipeline{
			"inst:a/file": nil, "inst:b/file": nil,
		},
		sources: map[string]SourceConfig{
			"inst:a/file": {LogSourceID: "inst:a", SourceGeneration: "g1"},
			"inst:b/file": {LogSourceID: "inst:b", SourceGeneration: "g1"},
		},
		vlNotReady: map[string]string{},
	}
	m.reconcileLoop = ReconcileLoopConfig{Enabled: true, RoundBudget: 30 * time.Second, MaxSourcesPerRound: 8}
	m.reconcileLockBudget = 200 * time.Millisecond

	m.publishStartupReconcileSnapshots()
	// 模拟"pollOnce 刷新跟不上"：发布时刻拨回 31 分钟前（超过原 TTL 30min）
	m.reconcileViewPublishedAt.Store(time.Now().Add(-31 * time.Minute).UnixNano())

	m.mu.Lock()
	go func() { time.Sleep(600 * time.Millisecond); m.mu.Unlock() }()
	res := m.ReconcileRoundNow(context.Background())
	detail := strings.Join(res.Errors, " | ")
	require.NotContains(t, detail, "candidate_lock_timeout", "v3：视图不得因 TTL 过期而退化为等锁")
	require.Equal(t, 2, res.Candidates, "31 分钟前的视图仍应可用（Candidates==2）")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：在 currentReconcileView 恢复 TTL 过期判断（>30min ⇒ return nil）⇒ 本用例
//     `candidate_lock_timeout` 断言转红；还原后转绿。
