package ingest

// 缺口消解异步化的回归用例（2026-11-02）。
//
// 钉住三件事（任务要求的三条）：
//  1. 投递成功不再因缺口核对超时而阻塞（登记是 O(1)，不调 VL）；
//  2. 异步核对成功后缺口确实被消解（不只能证明“不阻塞”）；
//  3. 同源多次投递只跑一次核对（去重 + 合并）。
// 另加：队列有界、关停不泄漏 goroutine（上级补充的约束）。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func gapResolveTestEvent(i int) logtypes.Event {
	return logtypes.Event{
		EventID:      fmt.Sprintf("gre-%06d", i),
		Message:      "line",
		EventTimeUTC: "2026-11-02T00:00:00Z",
		Level:        "INFO",
		Stream:       "stdout",
	}
}

// 用例 1：**Manager 的登记入口**是 O(1)，不调 VL。
//
// 必须走 m.scheduleGapResolve（投递路径真正调的那个），不能直接调 s.schedule ——
// 后者绕过了被测的那一层，变异时测不出问题（第一版就这么写错了）。
//
// 构造：Manager 装一个调度器但**不启 worker**，这样登记后任务只排队不执行；
// 若 scheduleGapResolve 退回同步，它会立即调 resolveGapsLandedByDelivery（需取 m.mu、
// 走 VL 核对路径），耗时会显著不同。断言"有排队、且没执行"即可区分两者。
func TestGapResolveScheduleDoesNotBlock(t *testing.T) {
	s := newGapResolveScheduler()
	m := &Manager{gapResolves: s}
	src := SourceConfig{LogSourceID: "inst:a", SourceGeneration: "g1"}
	events := []logtypes.Event{gapResolveTestEvent(0)}

	start := time.Now()
	m.scheduleGapResolve(src, events)
	elapsed := time.Since(start)

	require.Less(t, elapsed, 50*time.Millisecond, "登记必须 O(1)，不得跑 VL 核对")
	st := s.stats()
	require.Equal(t, 1, st.Pending, "异步：任务应在排队")
	require.Equal(t, uint64(0), st.Done, "异步：此刻不得已执行（同步会立即 Done=1）")
	require.Equal(t, uint64(0), s.running.Load(), "不得已开始运行")
}

// 用例 2：同源多次登记 ⇒ 合并成一条待办，事件取并集（不新建）。
func TestGapResolveMergesSameSource(t *testing.T) {
	s := newGapResolveScheduler()
	src := SourceConfig{LogSourceID: "inst:a", SourceGeneration: "g1"}

	s.schedule(&gapResolveRequest{key: "inst:a/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(0)}})
	s.schedule(&gapResolveRequest{key: "inst:a/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(1)}})
	s.schedule(&gapResolveRequest{key: "inst:a/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(0)}}) // 重复

	st := s.stats()
	require.Equal(t, 1, st.Pending, "同源只能有一条待办")
	require.Equal(t, uint64(2), st.Merged, "后两次都走了合并")
	require.Len(t, s.pending["inst:a/g1"].events, 2, "并集去重后应为 2 条（重复的 0 不得重复计入）")
}

// 用例 3：队列有界，超限不静默丢（计数 + 告警）。
func TestGapResolveQueueIsBounded(t *testing.T) {
	s := newGapResolveScheduler()
	for i := 0; i < maxGapResolvePendingSources; i++ {
		src := SourceConfig{LogSourceID: fmt.Sprintf("inst:%d", i), SourceGeneration: "g1"}
		require.True(t, s.schedule(&gapResolveRequest{
			key: fmt.Sprintf("inst:%d/g1", i), source: src, events: []logtypes.Event{gapResolveTestEvent(i)},
		}))
	}
	// 第 N+1 个：应被拒且计数可见。
	src := SourceConfig{LogSourceID: "inst:overflow", SourceGeneration: "g1"}
	ok := s.schedule(&gapResolveRequest{
		key: "inst:overflow/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(9999)},
	})

	require.False(t, ok, "超上限必须拒绝")
	st := s.stats()
	require.Equal(t, uint64(1), st.RejectedCap, "超上限必须计数，不能静默丢")
	require.Equal(t, uint64(1), st.Skipped)
}

// 用例 4：关停后不再接受登记；且 pending 被清空。
func TestGapResolveCloseRejectsNew(t *testing.T) {
	s := newGapResolveScheduler()
	src := SourceConfig{LogSourceID: "inst:a", SourceGeneration: "g1"}
	s.schedule(&gapResolveRequest{key: "inst:a/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(0)}})

	s.close()

	require.False(t, s.schedule(&gapResolveRequest{
		key: "inst:a/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(1)},
	}), "关停后不得再登记")
	require.Equal(t, 0, s.stats().Pending, "关停时待办应清空")
}

// 用例 5：worker 真能把任务取走执行（不依赖 VL 也能跑，因为 resolve 内部失败即 return）。
func TestGapResolveWorkerPicksAndRuns(t *testing.T) {
	s := newGapResolveScheduler()
	m := &Manager{gapResolves: s}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.workWG.Add(1)
	go m.runGapResolveWorker(ctx)

	src := SourceConfig{LogSourceID: "inst:w", SourceGeneration: "g1"}
	s.schedule(&gapResolveRequest{key: "inst:w/g1", source: src, events: []logtypes.Event{gapResolveTestEvent(0)}})

	require.Eventually(t, func() bool {
		st := s.stats()
		return st.Done == 1
	}, 5*time.Second, 20*time.Millisecond, "worker 应把登记的任务执行完（失败也算执行完）")

	cancel()
	s.close()
	m.workWG.Wait()
}

// 用例 6：反复 Start/Stop 不泄漏 goroutine（上级补充约束）。
func TestGapResolveWorkerNoGoroutineLeak(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := newGapResolveScheduler()
		m := &Manager{gapResolves: s}
		ctx, cancel := context.WithCancel(context.Background())
		m.workWG.Add(1)
		go m.runGapResolveWorker(ctx)
		s.close()
		cancel()
		m.workWG.Wait() // 必须能等到退出，否则这个用例会卡死/超时
	}
}
