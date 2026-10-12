package ingest

// #55-A 的红/绿：把「等锁」从「持有」里**拆出来**（仪器口径修正，零行为变更）。
//
// 现场：watchMutexHold 的起表点在 m.mu.Lock() **之前** ⇒ 读数混入等待 —— 9–19s 的慢段
// 无法区分"是本函数自己持锁 9–19s"还是"被别的长持锁者挡在外面 9–19s"✗。
// 修法：热点处拆两段计时 —— 等锁（Lock 前起表、Lock 后停表）+ 纯持有（Lock 后起表、
// Unlock 后停表），各自独立计数与告警行；阈值沿用 100ms，不改锁界、不加锁。
//
// 本文件是**确定性场景**（不依赖负载）：一个 goroutine 持 m.mu 若干十 ms，同期跑 deliver 的
// 规划段（它必然要等这把锁）。修前：这段等待被算进"持有" ⇒ MutexHoldSlowCount 记 1（红）；
// 修后：等待记成 MutexWaitSlowCount，持有侧的计数保持 0（绿）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// waitHoldHoldFor 是"陪测 goroutine"持 m.mu 的时长：> 100ms 阈值（余量 2.5×），
// 同时远长于 deliver 规划段自身的真实持有（O(1) 快照，微秒级）⇒ 两侧读数不应混淆。
const waitHoldHoldFor = 250 * time.Millisecond

// newWaitHoldProbeManager 复用 #54 探针夹具（最小 Manager：vlRoute 桩 + 空 state）：
// 本批用零宽事件收尾 ⇒ deliver 在规划段内即以 422 返回，不触达 VL/段存储/缺口消解。
// 另外预置两条告警节流窗口 ⇒ 用例只验证计数，不打日志（避免测试输出噪声）。
func newWaitHoldProbeManager(t *testing.T) *Manager {
	t.Helper()
	m := newDeliverPlanProbeManager()
	now := time.Now().UnixNano()
	m.mutexHoldWarnAt.Store(now)
	m.mutexWaitWarnAt.Store(now)
	return m
}

// TestMutexSlowWaitIsNotCountedAsSlowHold 断言「被挡住 ≠ 自己持锁」：
// 陪测持锁 250ms ⇒ deliver 的等待 > 100ms 必须计入**慢等待**，且**不得**计入慢持有。
//
// 转红记录（2026-10-05 实测，未改代码）：本用例的持有侧断言实测红 —— 等待被算成慢持有
// （expected 0, actual 1），正是现场"9–19s 无法区分等待与持有"的最小复现。
func TestMutexSlowWaitIsNotCountedAsSlowHold(t *testing.T) {
	m := newWaitHoldProbeManager(t)
	src := SourceConfig{LogSourceID: "probe", SourceGeneration: "g1"}

	locked := make(chan struct{})
	release := make(chan struct{})
	go func() {
		m.mu.Lock()
		close(locked)
		<-release
		m.mu.Unlock()
	}()
	<-locked
	go func() {
		time.Sleep(waitHoldHoldFor)
		close(release)
	}()

	start := time.Now()
	_, _ = m.deliver(src, []logtypes.Event{deliverPlanZeroWidthEvent()}, false)
	blocked := time.Since(start)

	require.GreaterOrEqual(t, blocked, waitHoldHoldFor-10*time.Millisecond,
		"前置：deliver 必须真的被挡住（否则测的不是等待路径）")
	require.GreaterOrEqual(t, m.MutexWaitSlowCount(), int64(1),
		"被挡住 %s ⇒ 必须记成慢等待（#55-A：等待要可见）", waitHoldHoldFor)
	require.Equal(t, int64(0), m.MutexHoldSlowCount(),
		"被挡住不得记成慢持有（#55-A 的核心区分：等锁 ≠ 持有）")
}

// TestMutexSlowHoldIsNotCountedAsSlowWait 是上一条的**对偶**：自己持锁却几乎不等待时，
// 必须记成慢持有、且不得记成慢等待（两侧读数互不串台 ⇒ 收窄方向唯一）。
func TestMutexSlowHoldIsNotCountedAsSlowWait(t *testing.T) {
	m := newWaitHoldProbeManager(t)

	w := m.watchMutexWait("test.hold")
	m.mu.Lock()
	w.acquired()
	time.Sleep(120 * time.Millisecond) // > 100ms 阈值
	m.mu.Unlock()
	w.done()

	require.Equal(t, int64(1), m.MutexHoldSlowCount(), "自己持锁超阈 ⇒ 记一次慢持有")
	require.Equal(t, int64(0), m.MutexWaitSlowCount(), "锁是空的（等待 ≈ 0）⇒ 不得记成慢等待")
}

// TestMutexOnePhaseWatchKeepsLegacyReading 守住未拆分调用点的**旧语义不变**：
// watchMutexHold（一段式，起表即持有）在慢段上仍按原样计入慢持有，且不产生等待读数。
func TestMutexOnePhaseWatchKeepsLegacyReading(t *testing.T) {
	m := newWaitHoldProbeManager(t)

	slow := m.watchMutexHold("test.legacy.slow")
	time.Sleep(120 * time.Millisecond)
	slow.done()
	require.Equal(t, int64(1), m.MutexHoldSlowCount(), "一段式：超阈计入慢持有（语义与 #55-A 之前一致）")
	require.Equal(t, int64(0), m.MutexWaitSlowCount(), "一段式不计等待（它没有等待段）")

	fast := m.watchMutexHold("test.legacy.fast")
	fast.done()
	require.Equal(t, int64(1), m.MutexHoldSlowCount(), "未超阈不得计数")
	require.Equal(t, int64(0), m.MutexWaitSlowCount(), "未超阈不得计数（等待侧）")
}

// 变异记录（2026-10-05，可转红）：
//   - 把 acquired() 的 `w.at = time.Now()` 去掉（即"仍在 Lock 前起表"）⇒ 上一条用例的
//     持有侧断言转红（等待被再次算进持有）；
//   - 把 done() 里的等待分支删掉 ⇒ 首条用例的等待侧断言转红。
