package ingest

// #57 的红/绿：cycleMu 持有者观测仪（纯观测件，零行为变更）。
//
// 现场动因：resolve-gaps 探针 20/20 全忙、19/20 耗时 5.020–5.075s（±55ms）⇒ 每次都吃满 5s 预算
// ⇒ cycleMu 近乎连续被持有；现场锚 `goroutine 3457 [sync.Mutex.Lock, 2 minutes]` 只能证明"有人在等"，
// 上游持有者定不了名。本文件守三面读数：慢持有计数、当前持有者快照（含 goroutine id）、
// 有界获取侧等锁读数 + 取锁密度。
//
// 红（未实现前）：观测面**不存在** ⇒ 读数不可读（本文件的断言无法成立，编译即失败）——
// 这是"缺观测"这一事实的最强形态；实现后绿。
// 变异回红（见文件末尾）：删掉 done() 的慢持有结算 ⇒ 计数断言红；删掉 locked() 的持有者登记 ⇒
// 快照断言红；删掉 noteCycleMuWait ⇒ 等锁读数断言红。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// newCycleMuProbeManager 返回观测仪已开启的 Manager（并预置告警节流窗：用例只验证计数/快照，
// 不打日志以避免输出噪声）。用 Options 打开（不依赖 env ⇒ 用例间无串扰）。
func newCycleMuProbeManager(t *testing.T, open bool) *Manager {
	t.Helper()
	root := t.TempDir()
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(nil), CycleMuProfile: open})
	require.NoError(t, err)
	m.cycleMuWarnAt.Store(time.Now().UnixNano())
	return m
}

// TestCycleMuSlowHoldIsObserved：合成「某 section 持 250ms」⇒ 三面读数必须同时成立：
//   - 慢持有计数 +1（阈值 100ms）；
//   - 持有期间"当前持有者"快照 = 该 section + 当前 goroutine id + heldFor > 0；
//   - 放锁后快照复位（held=false）。
//
// 对偶：短持有（5ms）不得计数；观测关闭时（零开销路径）一切读数保持 0。
func TestCycleMuSlowHoldIsObserved(t *testing.T) {
	m := newCycleMuProbeManager(t, true)
	require.True(t, m.CycleMuProfileEnabled(), "前置：观测仪必须已开启")

	w := m.observeCycleMu("test.slow")
	require.NotNil(t, w, "观测开启时必须返回 watcher")
	var held bool
	func() { // 独立块 + defer 放锁：断言失败（含变异回红）也不会把锁漏在这里挂死用例
		m.cycleMu.Lock()
		defer m.cycleMu.Unlock()
		w.locked()

		section, goid, heldFor, holding := m.CycleMuHolderSnapshot()
		held = holding
		require.True(t, holding, "持有期间快照必须报告 held=true")
		require.Equal(t, "test.slow", section, "快照必须给出持有点的 section tag")
		require.Equal(t, goroutineID(), goid, "快照必须给出持有者 goroutine id（现场锚要的就是这个字段）")
		require.GreaterOrEqual(t, heldFor, time.Duration(0))
		require.Zero(t, m.CycleMuSlowHoldCount(), "尚未超阈：不得计数")
		time.Sleep(250 * time.Millisecond)
	}()
	w.done()

	require.Equal(t, int64(1), m.CycleMuSlowHoldCount(), "持有 250ms（>100ms 阈值）必须计一次慢持有")
	_, _, _, held = m.CycleMuHolderSnapshot()
	require.False(t, held, "放锁后快照必须复位（否则读数是陈旧持有者 ✗）")

	// 对偶：短持有不报。
	fast := m.observeCycleMu("test.fast")
	func() {
		m.cycleMu.Lock()
		defer m.cycleMu.Unlock()
		fast.locked()
		time.Sleep(5 * time.Millisecond)
	}()
	fast.done()
	require.Equal(t, int64(1), m.CycleMuSlowHoldCount(), "短持有不得计数")

	// 关闭观测：零开销路径（observeCycleMu 返回 nil），长持有也不产生任何读数。
	off := newCycleMuProbeManager(t, false)
	require.False(t, off.CycleMuProfileEnabled())
	nilWatch := off.observeCycleMu("test.off")
	require.Nil(t, nilWatch, "观测关闭时必须返回 nil（调用点零开销）")
	func() {
		off.cycleMu.Lock()
		defer off.cycleMu.Unlock()
		nilWatch.locked() // nil 接收者必须安全
		time.Sleep(150 * time.Millisecond)
	}()
	nilWatch.done()
	require.Zero(t, off.CycleMuSlowHoldCount(), "观测关闭时不得产生任何读数")
	_, _, _, held = off.CycleMuHolderSnapshot()
	require.False(t, held, "观测关闭时快照必须恒为未持有")
}

// TestCycleMuWaitStatsRecorded：有界获取侧读数 —— 现场"每次都吃满 5s 预算"的直接对照面。
// 合成：他人持锁 150ms 后释放 ⇒ 本次获取应记下 ≥150ms 的等待；再合成一次必然超预算的获取 ⇒
// 超预算计数 +1。
func TestCycleMuWaitStatsRecorded(t *testing.T) {
	m := newCycleMuProbeManager(t, true)
	ctx := context.Background()

	m.cycleMu.Lock()
	go func() {
		time.Sleep(150 * time.Millisecond)
		m.cycleMu.Unlock()
	}()
	// 与真实站点（ResolveCoveredGapsForSource）同型：observe → acquireBounded → locked → done。
	w := m.observeCycleMu("test.wait")
	require.True(t, m.acquireCycleMuBounded(ctx, 3*time.Second), "有界获取最终必须成功")
	w.locked() // 取到即登记（同时累计取锁密度）
	require.GreaterOrEqual(t, w.waited, 150*time.Millisecond, "watcher 自记的等待时长也必须包含这段被挡住的等待")
	m.cycleMu.Unlock() // 成功即持有 ⇒ 本用例读完后必须归还（否则下一段会自锁）
	w.done()
	total, max, attempts, timeouts := m.CycleMuWaitStats()
	require.GreaterOrEqual(t, total, 150*time.Millisecond, "等锁总时长必须包含这段被挡住的等待")
	require.GreaterOrEqual(t, max, 150*time.Millisecond, "最长单次等待必须包含这段等待")
	require.Equal(t, int64(1), attempts, "尝试次数 = 有界获取的**调用次数**（每次调用记一条 ⇒ 与探针「20/20 次全忙」口径直接可比）")
	require.Zero(t, timeouts, "成功获取不得计超预算")

	// 超预算：先占住锁，再用极小预算获取 ⇒ 必然失败并计入超预算。
	m.cycleMu.Lock()
	require.False(t, m.acquireCycleMuBounded(ctx, 50*time.Millisecond), "预算内拿不到锁必须返回 false")
	m.cycleMu.Unlock()
	_, _, _, timeouts = m.CycleMuWaitStats()
	require.Equal(t, int64(1), timeouts, "超预算必须计一次")

	// 取消：ctx 已取消 ⇒ 立即失败（也计入超预算/被取消）。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	m.cycleMu.Lock()
	require.False(t, m.acquireCycleMuBounded(cancelled, 3*time.Second), "已取消的 ctx 必须立即失败")
	m.cycleMu.Unlock()
	_, _, _, timeouts = m.CycleMuWaitStats()
	require.Equal(t, int64(2), timeouts, "被取消也必须计一次（同为「没拿到」）")

	require.Greater(t, m.CycleMuDensityPerSec(), float64(0),
		"取锁密度必须可读（本用例已取锁多次 ⇒ 滚动窗内即时密度 > 0）")
}

// TestCycleMuProfileRecordsRealPollSnapshot：真实站点的接线验证 —— 打开观测后跑一轮采集轮，
// 其"读快照"段（tag=pollSnapshot）必须被记入取锁密度（证明 tag/观测点真的接在生产路径上）。
func TestCycleMuProfileRecordsRealPollSnapshot(t *testing.T) {
	m := newCycleMuProbeManager(t, true)
	m.pollOnce()
	require.Greater(t, m.CycleMuDensityPerSec(), float64(0),
		"采集轮读快照段（pollSnapshot）必须被观测到 ⇒ 密度读数 > 0")
}

// 变异记录（2026-10-05，逐字还原）：
//   - 删掉 done() 里的慢持有结算（计数与告警块）⇒ TestCycleMuSlowHoldIsObserved 的计数断言转红；
//   - 删掉 locked() 里的 cycleMuHolder 登记 ⇒ 同用例的快照断言（section/goid/held）转红；
//   - 删掉 acquireCycleMuBounded 里的 noteCycleMuWait 调用 ⇒ TestCycleMuWaitStatsRecorded 转红。
