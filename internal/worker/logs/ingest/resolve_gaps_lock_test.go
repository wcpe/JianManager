package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResolveCoveredGapsLockHoldIsBoundedPerSource 给「变更段的最坏持有时长」一个量化口径。
//
// 合并门禁要求锁获取点标注锁序与**最坏持有量**（.claude/rules/gate-merge.md「长临界区审计」），
// 而 FR-499 的方法学纠正明确：量化持续有排队者的锁**不得**用 TryLock 探锁（Go 饥饿模式下锁空闲
// 也返回 false），要直接测「被它挡住的那件事」的时长——此处即并发采集轮的单轮时长。
//
// 判据：把解算人为拖长（观测口在**离锁**阶段按源睡 60ms，6 源 ⇒ 解算 ≥300ms），期间持续驱动
// 采集轮；采集轮单轮必须显著短于解算总时长（变更段是每源一小段，不是整段）。旧形态下采集轮会被
// 整段临界区挡到解算结束 ⇒ 单轮时长 ≈ 解算总时长 ⇒ 红。
func TestResolveCoveredGapsLockHoldIsBoundedPerSource(t *testing.T) {
	fixture := newBusyIngestFixture(t, 6, 30, 0)
	m := fixture.manager
	primeCompleteProjections(t, m)

	m.resolveGapsStage = func(string) { time.Sleep(60 * time.Millisecond) }

	var mu sync.Mutex
	var maxRound time.Duration
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			start := time.Now()
			m.pollOnce()
			elapsed := time.Since(start)
			mu.Lock()
			if elapsed > maxRound {
				maxRound = elapsed
			}
			mu.Unlock()
		}
	}()

	resolveStart := time.Now()
	err := m.ResolveCoveredGaps(context.Background())
	resolveElapsed := time.Since(resolveStart)
	close(stop)
	wg.Wait()

	require.NoError(t, err)
	require.Greater(t, resolveElapsed, 200*time.Millisecond, "夹具未把解算拖长，量化失去意义")
	mu.Lock()
	worstRound := maxRound
	mu.Unlock()
	t.Logf("解算总时长 %s；同期采集轮最坏单轮 %s（= 变更段与采集轮的争用上界）", resolveElapsed, worstRound)
	require.Less(t, worstRound, resolveElapsed/2,
		"采集轮单轮不得被解算整段挡住：单轮时长必须显著小于解算总时长")
}

// 本文件是 2026-10-02 压测现场缺陷的回归：整节点解算
// （`POST /nodes/:id/log-runtime/ingest/resolve-gaps` **不传** storageNamespace 的路径）
// 此前整段持有 `cycleMu`（与采集轮同一把锁）并逐源做投影查询，于是：
//
//   - **一次调用 = 整节点采集完全停摆**：现场实测 read_pos 冻结 30 分钟、source_wal 卡 560 万行、
//     索引 WAL 涨到 10.2 GB、Worker CPU 1.5 核（75 个源逐源做段级投影查询，且全程压着采集轮的锁）；
//   - **不可取消**：HTTP 300s 超时之后，服务端仍继续持锁把这一轮跑完。
//
// 三条断言（每条都可转红，转红方式见各用例注释）：
//  1. 解算进行中采集不停（采集轮照常完成、水位前进）；
//  2. ctx 取消 → 工作停止 + 明确错误 + 不落任何变更；
//  3. 全源遍历有界（源数 + 墙钟预算）且预算用尽后可续跑。
//
// 夹具复用 register_lock_contention_test.go 的忙期夹具（真实文件源 + 假 VL + 真账本/管道）。

// sortedSourceKeys 返回 Manager 当前管道键的**有序**副本——与 ResolveCoveredGaps 的遍历顺序
// 同一口径（源键排序 + 轮转游标），回归据此断言「处理了哪些源」。
func sortedSourceKeys(m *Manager) []string {
	m.mu.Lock()
	keys := make([]string, 0, len(m.pipes))
	for key := range m.pipes {
		keys = append(keys, key)
	}
	m.mu.Unlock()
	sort.Strings(keys)
	return keys
}

// projectionCovered 报告该源当前是否「已发布投影完整」（即整节点解算的准入判据）。
func projectionCovered(m *Manager, key string) bool {
	m.mu.Lock()
	pipe, ok := m.pipes[key]
	source := m.sources[key]
	saved := m.state.Sources[key]
	m.mu.Unlock()
	if !ok || pipe == nil {
		return false
	}
	_, complete := m.publishedClosedForSource(source, saved)
	return complete
}

// primeCompleteProjections 驱动采集轮到「每个源的投影都完整已发布」为止。
//
// 为什么必须先做这一步：整节点解算对**每个**源都要求完整已发布投影（判据未改），
// 未就绪的源会让它在遍历到该源时明确拒绝——那不是本文件要测的锁/取消/预算行为。
func primeCompleteProjections(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		m.pollOnce()
		var pending []string
		for _, key := range sortedSourceKeys(m) {
			if !projectionCovered(m, key) {
				pending = append(pending, key)
			}
		}
		if len(pending) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("夹具未能在时限内发布完整投影：未就绪源 %v", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// appendLogLines 向源文件追加日志行（模拟「采集期间源仍在产出」）。
func appendLogLines(t *testing.T, path string, lines int) {
	t.Helper()
	var body strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&body, "[12:30:%02d] [Server thread/INFO]: appended %d\n", i, i)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(body.String())
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

// TestResolveCoveredGapsDoesNotStallCollection：解算进行中，采集轮必须照常推进。
//
// 判据用「两件事同时成立」而不是耗时对比：
//   - 采集轮在解算**仍被钉住**期间完成（旧实现里它会一直等在 cycleMu 上）；
//   - 该轮的推进是真实的：源水位（read_pos）在解算进行期间前进，且新一轮投递被发布。
//
// 转红方式（实测）：把变更段整体放回 cycleMu（复刻旧形态）——采集轮被整段临界区挡在外面，
// 本用例在「采集轮无法完成」处当场变红。
func TestResolveCoveredGapsDoesNotStallCollection(t *testing.T) {
	fixture := newBusyIngestFixture(t, 2, 150, 0)
	m := fixture.manager
	primeCompleteProjections(t, m)

	const targetKey = "busy:0/g1"
	target := m.pipes[targetKey]
	require.NotNil(t, target)
	before, ok := target.Positions()
	require.True(t, ok)

	// 测试观测口：把「解算进行中」钉在处理第一个源的最前（新实现此时**未持** cycleMu）。
	reached := make(chan struct{})
	release := make(chan struct{})
	// 无论用例在哪一步失败都要放行被钉住的解算：否则清理阶段（Stop 取 cycleMu）会与它互等，
	// 测试不是「红」而是「挂死」——挂死会掩盖真正的失败点。
	var once sync.Once
	releaseResolve := func() { once.Do(func() { close(release) }) }
	defer releaseResolve()
	m.resolveGapsStage = func(string) {
		select {
		case <-reached:
		default:
			close(reached)
		}
		<-release
	}

	resolveDone := make(chan error, 1)
	go func() { resolveDone <- m.ResolveCoveredGaps(context.Background()) }()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("解算未在时限内进入处理阶段，夹具未制造出「解算进行中」")
	}

	// 解算进行中：源继续产出新行，采集轮照常跑（旧实现会在这里被 cycleMu 挡住）。
	appendLogLines(t, filepath.Join(fixture.root, "busy-0", "latest.log"), 3)
	roundDone := make(chan struct{})
	go func() {
		defer close(roundDone)
		// 直到新行被读取、投递并发布（即「投影追平水位」）为止；有界轮数，避免夹具抖动时死转。
		for i := 0; i < 40; i++ {
			m.pollOnce()
			if projectionCovered(m, targetKey) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	select {
	case <-roundDone:
	case <-time.After(15 * time.Second):
		t.Fatal("解算进行期间采集轮无法完成：cycleMu 被解算整段持有（旧形态）")
	}
	require.True(t, projectionCovered(m, targetKey), "采集轮必须把新行读入、投递并发布")

	// 断言采集确实推进了水位（不是「空转一轮」），且此刻解算仍被钉住。
	after, ok := target.Positions()
	require.True(t, ok)
	require.Greater(t, after.Read, before.Read, "解算进行期间采集轮必须照常推进读水位")
	select {
	case err := <-resolveDone:
		t.Fatalf("解算在断言完成前就结束了，夹具未把「进行中」钉住：%v", err)
	default:
	}

	releaseResolve()
	// 语义不变：投影完整时整节点解算必须成功（锁纪律修正不得改变判决）。
	require.NoError(t, <-resolveDone)
}

// TestResolveCoveredGapsStopsOnContextCancel：调用方取消 → 服务端真正停止 + 明确错误。
//
// 用例把一个**本可被自动解算**的缺口（TEST 原因 + 已暂停采集）放在第一个源上：
// 若取消被忽略，旧形态会在这一轮把它解掉并恢复采集——「取消后不得静默继续」就有了可转红判据。
//
// 转红方式（实测）：删掉 ctx 校验（遍历处与临界区内两处）——本用例在「必须回 Canceled」与
// 「不得落下变更」两处变红。
func TestResolveCoveredGapsStopsOnContextCancel(t *testing.T) {
	fixture := newBusyIngestFixture(t, 3, 40, 0)
	m := fixture.manager
	primeCompleteProjections(t, m)

	keys := sortedSourceKeys(m)
	require.Len(t, keys, 3)
	pipe := m.pipes[keys[0]]
	require.NotNil(t, pipe)
	positions, ok := pipe.Positions()
	require.True(t, ok)
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "operator review"))
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, positions.Reclaim, "TEST", "covered"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visited := 0
	m.resolveGapsStage = func(string) {
		visited++
		if visited == 1 {
			// 第一个源刚开始处理即取消（模拟 HTTP 300s 超时 / RPC 断连）。
			cancel()
		}
	}

	err := m.ResolveCoveredGaps(ctx)
	require.Error(t, err, "取消必须回明确错误，而不是静默继续")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, visited, "取消后不得继续遍历其余源")

	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.False(t, entry.Gaps[0].Resolved, "取消后不得落下任何解算变更")
	require.True(t, entry.AcquirePaused, "取消后不得恢复采集")
}

// TestResolveCoveredGapsBudgetIsBoundedAndResumable：全源遍历有界（源数 + 墙钟）且可续跑。
//
// 转红方式（实测）：去掉预算判定——第一次调用即遍历全部 12 个源并返回 nil，本用例在
// 「必须报预算用尽 / 处理数必须 ≤ 预算」处变红。
func TestResolveCoveredGapsBudgetIsBoundedAndResumable(t *testing.T) {
	fixture := newBusyIngestFixture(t, 12, 40, 0)
	m := fixture.manager
	primeCompleteProjections(t, m)

	want := sortedSourceKeys(m)
	require.Len(t, want, 12)
	m.resolveGapsBudget = ResolveGapsBudget{MaxSources: 4}

	var visited []string
	m.resolveGapsStage = func(key string) { visited = append(visited, key) }

	err := m.ResolveCoveredGaps(context.Background())
	var exhausted *ResolveGapsBudgetExhaustedError
	require.ErrorAs(t, err, &exhausted, "预算用尽必须显式返回（不得伪装成成功）")
	require.ErrorIs(t, err, ErrResolveGapsBudgetExhausted)
	require.Equal(t, 4, exhausted.Processed)
	require.Equal(t, 12, exhausted.Total)
	require.Equal(t, want[:4], visited, "单次调用处理的源数必须被预算封顶")

	// 第二次调用必须**从断点继续**（游标），而不是又回到同一个前缀——否则「有界」会退化成
	// 「永远解不完整」。
	visited = nil
	require.ErrorIs(t, m.ResolveCoveredGaps(context.Background()), ErrResolveGapsBudgetExhausted)
	require.Equal(t, want[4:8], visited)

	// 第三次覆盖剩余四个源；走满一圈后游标回到起点（可反复循环，不饿死任何源）。
	visited = nil
	require.ErrorIs(t, m.ResolveCoveredGaps(context.Background()), ErrResolveGapsBudgetExhausted)
	require.Equal(t, want[8:12], visited)
	m.mu.Lock()
	cursor := m.resolveGapsCursor
	m.mu.Unlock()
	require.Equal(t, 0, cursor, "覆盖完整一圈后游标必须回到起点")

	// 墙钟预算同样生效：耗尽时一个源都不再处理（它服务于没有 deadline 的调用方）。
	m.resolveGapsBudget = ResolveGapsBudget{MaxSources: 12, MaxDuration: time.Nanosecond}
	visited = nil
	require.ErrorIs(t, m.ResolveCoveredGaps(context.Background()), ErrResolveGapsBudgetExhausted)
	require.Empty(t, visited, "墙钟预算耗尽时不得再处理源")
}
