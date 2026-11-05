package ingest

// P0-1「投递被 VL 接受但查不到」的降级出口回归（2026-11-05）。
//
// # 本组用例钉住什么
//
// 这条链原先只做「查不到就留在待办里下一轮再问」——那是**无界**的重试。
// 生产上 VL 因 retention / 字段数上限静默丢整批时，那些事件永远不会变得可见，
// 待办只增不减，直到 `maxGapResolvePendingSources` 把同源新登记一律拒掉，
// 而那个源的新日志也跟着一起卡死。
//
// 修法：连续 `maxGapResolveMissRounds` 轮「查询成功但查不到」且跨越
// `gapResolveMissWindow` ⇒ 记 `DELIVERED_NOT_VISIBLE` 缺口 + 降级 DROPPED，
// 从待办移除。
//
// # 为什么必须走真实调度器（`schedule` → `pickLocked` → `executeGapResolve`）
//
// 手写一个 req 直接调 `executeGapResolve` 会**绕开轮次传递**：待办回填、
// `missRounds` 跨轮携带、降级后清空，这三件事都只在调度器那条路上发生。
// 本仓已经因为「夹具绕开真实入口」让变异验证失效过三次（见 conversation 记忆），
// 所以这里走真路。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// visibilityDegradeFixture 造一个「三段互不相接的事件、一条都不会变得可见」的环境。
//
// 为什么让区间**不相接**（0-100 / 150-250 / 300-400）：相接的区间会被
// `gapRangesOfEvents` 归并成一段，那就测不到「按精确区间记账而不是取首尾凸包」这条红线
// ——而凸包恰好会把中间的健康字节一起标成丢失。
func visibilityDegradeFixture(t *testing.T) (*Manager, SourceConfig, string, []logtypes.Event) {
	t.Helper()
	spans := [][2]uint64{{0, 100}, {150, 250}, {300, 400}}
	proto := gapVisibilityEvents(SourceConfig{
		LogSourceID: "inst:gapvis/file", SourceGeneration: "g1",
	}, "2026-11-01", spans, "degrade")
	m, source := scopeFixture(t, proto, false) // false = 一条都不写进 VL
	// 把「连续不可见」的界压到用例尺度：轮次仍为 3，墙钟窗口几乎为零。
	// **只压窗口不压轮次**：轮次是「问过几次」的证据，用例不能把它降到 1
	// （那样就测不到「第二轮不降级、第三轮才降级」这条时序契约）。
	savedWindow := gapResolveMissWindow
	gapResolveMissWindow = time.Nanosecond
	t.Cleanup(func() { gapResolveMissWindow = savedWindow })
	// 压短“预算内未等到可见”的探测预算（默认 6s）。本轮用例关心的是**轮次**与
	// 降级时机，不是退避曲线；不压它则主用例要白白跑 3×6s。
	savedBudget := gapVisibilityBudgetForTest
	gapVisibilityBudgetForTest = 50 * time.Millisecond
	t.Cleanup(func() { gapVisibilityBudgetForTest = savedBudget })
	return m, source, source.LogSourceID + "/" + source.SourceGeneration, proto
}

// pumpGapResolve 取一个到期任务并执行它（不走后台 worker，但走真实入队/回填）。
//
// 回填的 `due` 是「偏后 30s 的退避」，用例用这个小助手把它提到当下——
// 等价于「时间已经过去」，而不是绕开退避逻辑（退避值本身不在本用例的断言面内）。
func pumpGapResolve(t *testing.T, m *Manager, key string) {
	t.Helper()
	s := m.gapResolves
	require.NotNil(t, s)
	s.mu.Lock()
	if pending := s.pending[key]; pending != nil {
		pending.due = time.Now()
	}
	req := s.pickLocked(time.Now())
	s.mu.Unlock()
	require.NotNil(t, req, "夹具前提：待办里应有该源的任务")
	m.executeGapResolve(context.Background(), req)
}

// pendingMissRounds 读当前待办里该源的连续不可见轮次（无待办返回 0）。
func pendingMissRounds(m *Manager, key string) int {
	s := m.gapResolves
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending := s.pending[key]; pending != nil {
		return pending.missRounds
	}
	return 0
}

// pendingEventCount 读当前待办里该源的事件数（无待办返回 0）。
func pendingEventCount(m *Manager, key string) int {
	s := m.gapResolves
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending := s.pending[key]; pending != nil {
		return len(pending.events)
	}
	return 0
}

// visibilityGapCount 返回账本上该源未解决的 `DELIVERED_NOT_VISIBLE` 缺口条数。
func visibilityGapCount(t *testing.T, m *Manager, key string) int {
	t.Helper()
	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	count := 0
	for _, gap := range entry.Gaps {
		if !gap.Resolved && gap.Reason == ledger.GapReasonDeliveredNotVisible {
			count++
		}
	}
	return count
}

// TestDeliveredNotVisibleDegradesOnlyAfterBoundedRetries 是本修复的**主验收**。
//
// 转红方式（实测）：
//   - 去掉 `degraded` 分支里的 `unresolved = nil`（待办永远不清）⇒ 最后一次断言必红；
//   - 把 `degraded` 的阈值判据从 `>=` 改成 `>` ⇒ 满 3 轮仍不降级，必红；
//   - 把回填里的 `missRounds` 改成恒 0（不跨轮携带）⇒ 永远凑不够轮次，必红。
func TestDeliveredNotVisibleDegradesOnlyAfterBoundedRetries(t *testing.T) {
	m, source, key, events := visibilityDegradeFixture(t)

	// 真实入口：与投递成功后 `scheduleGapResolve` 走同一条登记路径（不经后台 worker）。
	require.True(t, m.gapResolves.schedule(&gapResolveRequest{
		key: key, source: source, events: events, due: time.Now(),
	}))

	// 第 1 轮：问过了，查不到 ⇒ 计数 1，**不降级**（这批还在可见延迟的正常范围内）。
	pumpGapResolve(t, m, key)
	require.Equal(t, 1, pendingMissRounds(m, key), "第一轮查不到只应计数 1")
	require.Equal(t, len(events), pendingEventCount(m, key), "未降级前待办一条都不能少")
	require.Zero(t, visibilityGapCount(t, m, key), "第一轮绝不能记账（否则可见延迟就被当成真丢）")

	// 第 2 轮：仍查不到 ⇒ 计数 2，仍不降级（轮次界是 3）。
	pumpGapResolve(t, m, key)
	require.Equal(t, 2, pendingMissRounds(m, key))
	require.Zero(t, visibilityGapCount(t, m, key), "第二轮仍不得到定论")

	// 第 3 轮：轮次与墙钟双界都满足 ⇒ 确定不可见 ⇒ 记缺口 + 降级 DROPPED + 清待办。
	pumpGapResolve(t, m, key)
	require.Equal(t, len(events), visibilityGapCount(t, m, key),
		"三段互不相接的不可见区间必须各登一条缺口（取首尾凸包会把中间的健康字节一并标成丢失）")
	require.Zero(t, pendingEventCount(m, key),
		"降级后必须清空待办：留着就会与下一批新事件做并集 ⇒ 无界重试（这正是要堵的那个洞）")

	// 水位必须真的被推过去（否则 reclaim 永远停在第一个洞上）。
	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pos, ok := pipe.Ledger().Positions(pipe.Key())
	require.True(t, ok)
	require.GreaterOrEqual(t, pos.Delivery, uint64(300), "降级必须把 delivery 水位推过全部降级区间")

	// 红线：绝不谎报已投递。
	entry := pipe.Ledger().Get(pipe.Key())
	require.Empty(t, ledger.RequestDoneRangesOf(entry),
		"降级绝不写 RequestDoneRanges：那会把“没落库”说成“已投递”")
}

// TestDeliveredNotVisibleNeverCountsFailedQueryAsMiss 钉住三态里最关键的那条分界。
//
// 现场形态：VL 因重启/OOM 短暂不可用，**每一次查询都失败**。若把这种情形当成
// 「连续多轮查不到」，一段完好的数据会被记成「VL 静默丢弃」并放行回收——
// 那比漏报更糟：漏报只是少一条告警，误报是不可逆地丢弃正常数据。
//
// 转红方式（实测）：把 `probeOK = proof.probeErr == nil` 改成 `probeOK = true` ⇒ 必红。
func TestDeliveredNotVisibleNeverCountsFailedQueryAsMiss(t *testing.T) {
	m, source, key, events := visibilityDegradeFixture(t)
	// 让每一次 VL 查询都**失败**（区别于“查询成功但查不到”）。
	m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) {
		return nil, errors.New("dial tcp: VictoriaLogs unreachable")
	}

	require.True(t, m.gapResolves.schedule(&gapResolveRequest{
		key: key, source: source, events: events, due: time.Now(),
	}))

	for round := 1; round <= 5; round++ {
		pumpGapResolve(t, m, key)
		require.Zero(t, pendingMissRounds(m, key),
			"第 %d 轮：查询失败不能计为“不可见”（否则 VL 抖动一小时就把正常数据记成丢了）", round)
		require.Equal(t, len(events), pendingEventCount(m, key),
			"第 %d 轮：待办必须原样保留，下轮再问", round)
		require.Zero(t, visibilityGapCount(t, m, key),
			"第 %d 轮：查询失败绝不能触发降级", round)
	}
}

// TestDeliveredNotVisibleDoesNotDegradeWhenVisible 钉住反方向：正常可见时零行为变化。
//
// 转红方式（实测）：把 `probeOK && len(unresolved) > 0` 里的 `len(unresolved) > 0` 去掉 ⇒ 本用例必红。
func TestDeliveredNotVisibleDoesNotDegradeWhenVisible(t *testing.T) {
	spans := [][2]uint64{{0, 100}, {100, 200}, {200, 300}}
	proto := gapVisibilityEvents(SourceConfig{
		LogSourceID: "inst:gapvis/file", SourceGeneration: "g1",
	}, "2026-11-01", spans, "visible")
	m, source := scopeFixture(t, proto, true) // true = 全部可见
	savedWindow := gapResolveMissWindow
	gapResolveMissWindow = time.Nanosecond
	t.Cleanup(func() { gapResolveMissWindow = savedWindow })
	key := source.LogSourceID + "/" + source.SourceGeneration

	require.True(t, m.gapResolves.schedule(&gapResolveRequest{
		key: key, source: source, events: proto, due: time.Now(),
	}))
	for round := 1; round <= 5; round++ {
		if pendingEventCount(m, key) == 0 {
			break // 已全部消解：待办本来就该空
		}
		pumpGapResolve(t, m, key)
	}
	require.Zero(t, visibilityGapCount(t, m, key),
		"全部可见时不得登记任何可见性缺口（否则正常路径会被误报成丢数据）")
}

// TestDeliveredNotVisibleReasonIsReclaimPassable 钉住缺口归类。
//
// 这个原因必须满足两件事，缺一不可：
//   - **不阻塞采集**：它等多久都不会变好（retention / 字段数上限不会自愈）；
//   - **可当 reclaim 穿行凭据**：否则回收链永久停在第一个洞上，
//     之后所有已确认投递的字节都回收不了（一个洞抬死整条源）。
//
// 转红方式（实测）：把它挪回 `gapReasonsPermanentlyLost` ⇒ 本用例必红。
func TestDeliveredNotVisibleReasonIsReclaimPassable(t *testing.T) {
	require.True(t, ledger.IsGapReasonNonBlocking(ledger.GapReasonDeliveredNotVisible),
		"必须不阻塞采集：走 1h 宽限会让源反复“投一批→暂停 1h→再投”，之后所有新日志一起死")
	for _, reason := range ledger.GapReasonsHealableOnReResume() {
		require.NotEqual(t, ledger.GapReasonDeliveredNotVisible, reason,
			"不得进可愈合名单：回读不会重新产生这段字节，那只会造成无意义的重复投递")
	}
}
