package ingest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 本文件是「常驻增量对账」（后续项③）的回归，覆盖三条承重语义：
//   - 周期到点必跑，且**关闭即不跑**（连 VL 只读查询都不发）；
//   - 对账发现「VL 条数不足」→ 触发重发（复用既有缺失天重发机制）；
//   - 重发只写缺失天（不整窗重写）。
//
// 两条用例都必须能转红：
//   - 去掉周期驱动（循环空转）→ RunsOnSchedule 红；
//   - 去掉「缺失天 → replayMissingDays」这一步（变异见 .tmp/reconcile-loop-mutation.py）→ Resends 红。

// TestReconcileLoopRunsOnScheduleAndSkipsWhenDisabled 固定「到点必跑 / 关闭不跑」两条语义。
//
// 为什么能转红：把 RunReconcileLoop 的 ticker 分支去掉（或让 Enabled=false 仍执行一轮），
// 本用例的零查询断言或轮次断言必然失败。
func TestReconcileLoopRunsOnScheduleAndSkipsWhenDisabled(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	manager, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	defer func() { _ = manager.Stop() }()

	// ① 关闭：连续等若干个周期 → 零轮次、零**新增** VL 查询、零状态推进。
	// 注意基线：New 里的启动对账（reconcileStartup）本身会发条数查询，故断言增量而非总量。
	manager.SetReconcileLoopConfig(ReconcileLoopConfig{Enabled: false})
	queriesAtClosedStart := fixture.statsQueryCount()
	closedCtx, cancelClosed := context.WithCancel(context.Background())
	closedDone := make(chan struct{})
	go func() {
		manager.RunReconcileLoop(closedCtx)
		close(closedDone)
	}()
	time.Sleep(120 * time.Millisecond)
	require.Zero(t, manager.ReconcileLoopStats().Rounds, "关闭即不跑：轮次必须为 0")
	require.Equal(t, queriesAtClosedStart, fixture.statsQueryCount(), "关闭即不跑：不得新增任何 VL 条数查询")
	cancelClosed()
	select {
	case <-closedDone:
	case <-time.After(2 * time.Second):
		t.Fatal("关闭配置下 RunReconcileLoop 必须立即返回")
	}

	// ② 开启（短周期注入）：等到至少跑过一轮，且确实发出过 VL 条数查询。
	queriesBefore := fixture.statsQueryCount()
	manager.SetReconcileLoopConfig(ReconcileLoopConfig{
		Enabled: true, Interval: 30 * time.Millisecond,
		RoundBudget: time.Second, MaxSourcesPerRound: 4,
	})
	openCtx, cancelOpen := context.WithCancel(context.Background())
	defer cancelOpen()
	go manager.RunReconcileLoop(openCtx)
	require.Eventually(t, func() bool {
		return manager.ReconcileLoopStats().Rounds >= 1
	}, 3*time.Second, 10*time.Millisecond, "周期到点必须必跑")
	require.Greater(t, fixture.statsQueryCount(), queriesBefore,
		"开启后必须真的做过条数级对账（否则只是空转）")
}

// TestReconcileLoopResendsDeletedUTCDay 固定「对账发现条数不足 → 只重发该天」。
//
// 场景与启动版回归同构：seed 出两天的已发布数据 → 人为删掉其中一天的 VL 记录 →
// 常驻对账必须检出并只补该天（另一天不得被重写）。
func TestReconcileLoopResendsDeletedUTCDay(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	manager, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	defer func() { _ = manager.Stop() }()
	manager.SetReconcileLoopConfig(ReconcileLoopConfig{
		Enabled: true, Interval: time.Minute, RoundBudget: 5 * time.Second, MaxSourcesPerRound: 4,
	})

	fixture.deleteDay("2026-09-22")
	insertsBefore := fixture.insertCount()
	result := manager.ReconcileRoundNow(context.Background())

	require.Equal(t, []string{"node:reconcile/g1:2026-09-22"}, result.Missing,
		"必须检出被删的那一天（且只有那一天）")
	require.Equal(t, 1, result.ResentSources, "缺失必须触发重发")
	require.Equal(t, 1, result.ResentDays)
	require.Equal(t, 1, result.CheckedSources)
	require.Empty(t, result.Errors)

	require.Greater(t, fixture.insertCount(), insertsBefore, "重发必须真的写入 VL")
	payloads := fixture.payloadsSince(insertsBefore)
	require.Len(t, payloads, 1, "只重发缺失天：不得整窗重写（另一天不得被重发）")
	require.Contains(t, payloads[0], "day one alpha")
	require.NotContains(t, payloads[0], "day two alpha", "非缺失天不得被重写")

	stats := manager.ReconcileLoopStats()
	require.Equal(t, uint64(1), stats.Rounds)
	require.Equal(t, uint64(1), stats.ResentSources)
	require.Equal(t, uint64(1), stats.ResentDays)
	require.Zero(t, stats.Errors)
	require.Contains(t, strings.Join(stats.LastMissing, ","), "2026-09-22")
}

// TestReconcileLoopSkipsResendWhenCountsMatch 固定「一致即零重发」（不打扰投递主路径）。
func TestReconcileLoopSkipsResendWhenCountsMatch(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	manager, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	defer func() { _ = manager.Stop() }()
	manager.SetReconcileLoopConfig(ReconcileLoopConfig{
		Enabled: true, Interval: time.Minute, RoundBudget: 5 * time.Second, MaxSourcesPerRound: 4,
	})

	insertsBefore := fixture.insertCount()
	result := manager.ReconcileRoundNow(context.Background())

	require.Empty(t, result.Missing)
	require.Zero(t, result.ResentSources)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, insertsBefore, fixture.insertCount(), "对账一致时零写入")
}

// TestReconcileLoopIndependentOfStartupReconcileSwitch 固定「两个开关互相独立」：
// 启动对账的应急逃生口（Reconcile.Enabled=false）不得连带把常驻对账判成"未启用"。
//
// 为什么能转红：把 ReconcileRoundNow 里 `queryCfg.Enabled = true` 去掉，
// 启动对账关闭时每源都会 Fallback（reconcile_disabled）→ Errors 非空，本用例失败。
func TestReconcileLoopIndependentOfStartupReconcileSwitch(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	manager, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
		Reconcile: &ReconcileConfig{Enabled: false}, // 启动对账的应急逃生口
	})
	require.NoError(t, err)
	defer func() { _ = manager.Stop() }()
	manager.SetReconcileLoopConfig(ReconcileLoopConfig{
		Enabled: true, Interval: time.Minute, RoundBudget: 5 * time.Second, MaxSourcesPerRound: 4,
	})

	result := manager.ReconcileRoundNow(context.Background())
	require.Equal(t, 1, result.CheckedSources)
	require.Empty(t, result.Errors, "启动对账关闭不得让常驻对账判成未启用")
	require.Zero(t, result.ResentSources, "数据一致时零重发")
	require.Equal(t, 1, result.Skipped)
}

// TestReconcileLoopConfigNormalization 固定配置归一（误配不得让常驻对账失控）。
func TestReconcileLoopConfigNormalization(t *testing.T) {
	cfg := ReconcileLoopConfig{Interval: time.Millisecond, RoundBudget: -1, MaxSourcesPerRound: -3}.normalized()
	require.GreaterOrEqual(t, cfg.Interval, reconcileLoopMinInterval)
	require.Equal(t, reconcileLoopDefaultRoundBudget, cfg.RoundBudget)
	require.Equal(t, reconcileLoopDefaultMaxSources, cfg.MaxSourcesPerRound)

	cfg = ReconcileLoopConfig{MaxSourcesPerRound: reconcileLoopMaxSourcesLimit + 100}.normalized()
	require.Equal(t, reconcileLoopMaxSourcesLimit, cfg.MaxSourcesPerRound)

	require.True(t, DefaultReconcileLoopConfig().Enabled, "默认启用（温和参数）")
}
