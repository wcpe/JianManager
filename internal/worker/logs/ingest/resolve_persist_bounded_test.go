package ingest

// #59 的红/绿：`ResolveCoveredGapsForSource` 的收尾落库**不得无界等持久化门**。
//
// 现场铁证（探针自栈两现）：
//   goroutine [sync.Cond.Wait]: runtime.go:6930（persistGate.Cond.Wait）
//     ← runtime.go:5743（persistCtx） ← runtime.go:1462（ResolveCoveredGapsForSource 收尾）
//     ← query/grpcsvc/service.go:670（LogResolveIngestGaps handler）
// ⇒ 45s 挂死卡在**持久化门排队**（不是 cycleMu/m.mu/账本锁），而该源的账本写（AbandonGapsThrough）
// 早已落账 ⇒ 卡住的是**可交办**的索引落库。
//
// 修法（单变量）：收尾由 `m.persist()`（= persistPriority，无界）改为 `persistResolveTail()`
// —— 同档（priority=true、不放宽计划复用），只在门上加了 `maxWait` 预算；超预算即交办后台并返回。
//
// 本用例是**确定性**的：用既有测试钩子 `persistStepHold` 把门按住（同
// TestCollectionHotPathIsNonBlockingUnderGatePressure 的手法），并用 `persistGateStage` 等到
// 持门者真正就位（不靠 sleep 猜时序）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resolveGateHoldCeiling 是"收尾必须及时返回"的观测上限（远大于预算 50ms，远小于无界等待）。
const resolveGateHoldCeiling = 3 * time.Second

func TestResolveCoveredGapsForSourceTailIsBoundedOnPersistGate(t *testing.T) {
	m, key := newGateFixture(t, 500, 16)
	p := m.pipes[key]
	require.NotNil(t, p)

	// 夹具：该源有一个未解决缺口（收尾会把"账本变更"落账后交办索引落库）。
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 10, 20, "TEST_PERMANENTLY_LOST", "fixture gap"))

	// 用一个测试钩子把持久化门**确定性地按住**；用 gate stage 钩子等到持门者真正就位。
	hold := make(chan struct{})
	m.persistStepHold = func() { <-hold }
	holderAcquired := make(chan struct{})
	var once sync.Once
	m.persistGateStage = func(_ context.Context, event string, _ int64, _ bool) {
		if event == "acquire" {
			once.Do(func() { close(holderAcquired) })
		}
	}
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = m.persistCtx(context.Background(), persistNormalOneStep)
	}()
	select {
	case <-holderAcquired:
	case <-time.After(5 * time.Second):
		t.Fatal("夹具前提不成立：持门者未就位")
	}

	// 预算压到 50ms：让"超预算 ⇒ 交办"确定性地发生（生产默认 2s）。
	m.resolvePersistBudget = 50 * time.Millisecond
	before := m.persistCovered.Load()

	// 有界观测：红（无界等待）时必须给出"被按住多久"的读数，而不是把用例一起挂死。
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- m.ResolveCoveredGapsForSource(context.Background(), key, "tester") }()
	var err error
	select {
	case err = <-done:
	case <-time.After(resolveGateHoldCeiling):
		close(hold) // 放行持门者，避免用例挂死
		err = <-done
		t.Errorf("#59：收尾落库不得无界等持久化门（实测被按住 > %s）", resolveGateHoldCeiling)
	}
	elapsed := time.Since(start)
	t.Logf("#59 观测：解算（含收尾落库）用时 %s · 预算 %s · 交办计数 %d",
		elapsed, m.resolvePersistBudget, m.ResolvePersistHandoffCount())

	require.NoError(t, err, "受理即返：不得把「门忙」报成失败")
	require.Less(t, elapsed, 2*time.Second,
		"#59：收尾必须在有界预算内返回（实测 %s；无界实现会一直等在门上）", elapsed)
	require.GreaterOrEqual(t, m.ResolvePersistHandoffCount(), int64(1),
		"必须留下明确的「已交办后台」读数（受理 vs 已落库要可测）")

	// 语义不变量①：账本变更已落账（放弃裁决 + 恢复采集）——与 #58 同口径。
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.True(t, entry.Gaps[0].Resolved, "缺口必须已被放弃（账本层已生效）")
	require.Len(t, entry.Abandonments, 1, "必须留下放弃凭据")
	require.False(t, entry.AcquirePaused, "必须已恢复采集")

	// 语义不变量②：交办的落库**必须最终完成**（写不丢）——放行持门者后由后台通道完成。
	select {
	case <-holderDone:
		t.Fatal("夹具前提不成立：持门者已结束，测不到「门被按住」的形态")
	default:
	}
	close(hold)
	<-holderDone
	require.Eventually(t, func() bool { return m.persistCovered.Load() > before },
		15*time.Second, 10*time.Millisecond,
		"交办的落库必须最终完成（覆盖水位只在真落库后推进 ⇒ 它前进即证明写已落地）")
}

// 变异记录（2026-10-05，逐字还原）：把收尾的 `_, err := m.persistResolveTail(); return err`
// 改回 `return m.persist()`（= 修前形态：persistPriority 无界等门）⇒ 本用例在"有界返回"处转红
// （实测被按住 ≥3s，最终由超时分支放行并判红）；还原后转绿。
