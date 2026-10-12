package ingest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLongPlanningDoesNotHoldGate（铁律「持门者只做提交、不做规划」①）：
// **长规划期间，别的落库调用照样能拿到门**——即规划不占门。
//
// 现场形态（2026-10-03 X 光片）：持门者在做 CPU 密集规划（persistSnapshotStep→planState→mirrorKey
// 复刻整源镜像行），6 个投递/采集单元全部卡在 sync.Cond.Wait，pollOnce 的 chan send 卡 4 分钟 ✗✗。
//
// 转红方式（实测）：把 `planPersist` 调用移回门的授予分支内（即"持门规划"）——第一个调用者会在
// 门的**门内**睡满 600ms，第二个调用者的 acquire 事件被推迟到 600ms 之后 ⇒ 本用例在
// 「第二次 acquire 必须发生在第一次规划结束之前」处变红。
func TestLongPlanningDoesNotHoldGate(t *testing.T) {
	m, _ := newGateFixture(t, 200, 16)

	var first atomic.Bool
	planningStarted := make(chan struct{})
	planningDone := make(chan struct{})
	m.persistPlanHold = func() {
		if first.CompareAndSwap(false, true) {
			close(planningStarted)
			time.Sleep(600 * time.Millisecond) // 模拟"长规划"（复刻整源镜像行量级）
			close(planningDone)
		}
	}

	// 判定核心（与"真实提交耗时"解耦）：**必须有一次 acquire 发生在长规划结束之前** ✓。
	//
	// 变异=持门规划 ⇒ 第一个调用者在门的**门内**睡满 600ms ⇒ 任何 acquire 都只能发生在
	// planningDone 关闭之后 ⇒ 本断言为假 ⇒ 红 ✓✓。
	var acquiredDuringPlanning atomic.Bool
	var secondStarted atomic.Bool
	m.persistGateStage = func(_ context.Context, event string, _ int64, _ bool) {
		if event != "acquire" || !secondStarted.Load() {
			return // 只关心**第二个调用者**的 acquire（第一个的 acquire 先于它自己的规划，无判别力 ✗）
		}
		select {
		case <-planningDone:
		default:
			acquiredDuringPlanning.Store(true)
		}
	}

	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		_ = m.persistCtx(context.Background(), persistPriority) // 第一个：规划 600ms
	}()
	<-planningStarted

	fastDone := make(chan struct{})
	secondStarted.Store(true)
	go func() {
		defer close(fastDone)
		_ = m.persistCtx(context.Background(), persistPriority) // 第二个：应能立刻拿到门
	}()

	select {
	case <-fastDone:
	case <-time.After(20 * time.Second):
		t.Fatal("第二个落库调用在 20s 内未完成（规划把门占死了 ✗）")
	}
	require.True(t, acquiredDuringPlanning.Load(),
		"长规划期间门必须是**空的**：应观察到发生在规划结束前的 acquire（持门规划的变异实现会让它为假）")
	<-slowDone
}

// TestRecoveryStepDoesNotHoldGate（铁律 ②）：恢复步走**有界等待 → 交后台 → 立即返回**，
// 门被别的调用者按住时它必须**有界返回**，不整步占门。
//
// 转红方式（实测）：把 persistRecoveryStep 改回 `persistCtx(ctx, persistNormalOneStep)`（无 maxWait）
// —— 本用例在「有界返回」处变红（它会一直等在门上，直到 500ms 后才被放行）。
func TestRecoveryStepDoesNotHoldGate(t *testing.T) {
	m, _ := newGateFixture(t, 200, 16)
	hold := make(chan struct{})
	m.persistStepHold = func() { <-hold }
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = m.persistCtx(context.Background(), persistPriority) // 门被按住
	}()
	time.Sleep(30 * time.Millisecond)

	m.persistHotBudget = 40 * time.Millisecond
	// 无论判定结果如何都必须**释放持门者**（否则变异实测会卡死夹具收尾，看不到断言 ✗）。
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	defer release()
	started := time.Now()
	stepDone := make(chan error, 1)
	go func() { stepDone <- m.persistRecoveryStep(context.Background()) }()
	var elapsed time.Duration
	bounded := true
	select {
	case err := <-stepDone:
		elapsed = time.Since(started)
		require.NoError(t, err)
	case <-time.After(3 * time.Second): // 判别力不依赖这个界：变异实现会**一直**等（持门者不放门 ✗）
		bounded = false
		elapsed = time.Since(started)
	}
	require.True(t, bounded,
		"恢复步必须**有界返回**（%s 内未返回；整步占门会让恢复链把采集/投递单元卡住 ✗）", elapsed)

	select {
	case <-holderDone:
		t.Fatal("夹具前提不成立：持门者已结束，测不到「门被按住」的形态")
	default:
	}
	release()
	select {
	case <-holderDone:
	case <-time.After(5 * time.Second):
		t.Fatal("持门者在放门后 5s 内未结束（夹具收尾异常）")
	}
}
