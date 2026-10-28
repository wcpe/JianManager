package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"

	"github.com/stretchr/testify/require"
)

// legacyBacklogPauseReason 是旧二进制写下的积压暂停原因前缀（本包只读兼容，绝不再写入生产）。
//
// 回归用例用它造“存量积压暂停”——那正是 `maybeResumeBacklogLocked` 要识别并收敛的对象。
const legacyBacklogPauseReason = "wal backlog limit exceeded (entries=5/4)"

// TestBackpressureSkipsRevivalWhileBacklogRises（用户指令的红证）：**上一轮积压净涨 ⇒ 本轮不复活**。
//
// 现场依据（+463k ✗）：复活让源开始读文件 ⇒ 常规新数据进入 WAL ⇒ 恶化期继续复活更多源就是
// "越复活越涨" ✗✗ ⇒ Q18 批准的背压：净涨期不放宽、不复活 ✓。
//
// 转红方式（实测）：去掉背压（无条件继续本轮）⇒ 本用例在「净涨期不得复活」处红 ✓。
func TestBackpressureSkipsRevivalWhileBacklogRises(t *testing.T) {
	m, _ := newGateFixture(t, 50, 16)
	require.NotNil(t, m)

	// 注入"一直在涨"的闸视图读数 ⇒ 每一轮都应被背压跳过 ✓。
	rising := int64(0)
	m.sweepBacklogOf = func() int64 { rising += 10; return rising }
	m.resumeInterval = 10 * time.Millisecond
	m.recoveryProbe = func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m.RunCapacityResumeLoop(ctx)

	require.GreaterOrEqual(t, m.ResumeSweepStats().SkipBackpressure, int64(1),
		"持续净涨时必须出现背压跳过（实测 %d 次；窗口阈值=3 ✓）", m.ResumeSweepStats().SkipBackpressure)
}

// TestBackpressureWindowDoesNotBlockOnSingleRoundJitter（现场 41 家之谜的第二条红证）：
// **单轮抖动上涨不得永久挡住复活** ✓ —— 窗口化背压要求"**连续 N 轮**净涨"才跳过 ✓。
//
// 现场依据：41 家 `paused + backlog_pause + blocking=0 + backlog<半闸`（三项前置全满足 ✗）
// 却无一复活 ✓ ⇒ 头号嫌疑是"单轮比较即跳过"使**任何噪声都永久挡复活** ✗。
//
// 转红方式（实测）：把背压改回单轮判定（阈值 1）⇒ 第 2 轮抖动即跳过 ⇒ 本用例在
// 「抖动轮必须仍被处理」处红 ✓。
func TestBackpressureWindowDoesNotBlockOnSingleRoundJitter(t *testing.T) {
	m, _ := newGateFixture(t, 50, 16)
	seq := []int64{100, 200 /* 抖动上涨一轮 */, 50, 50, 50, 50}
	i := 0
	m.mu.Lock()
	var fb *pipeline.Pipeline
	for _, p := range m.pipes {
		fb = p
	}
	m.mu.Unlock()
	require.NotNil(t, fb)
	// 2026-10-29：造暂停的方式换了（背压机制本身一字未动）。
	//
	// 旧写法只写一条 `wal backlog limit exceeded` 暂停，新语义下这条暂停会被
	// `maybeResumeBacklogLocked` 在任何一次 TryReclaim 里当场解除 ⇒ 兜底环扫到时它早已不是
	// 暂停态 ⇒ 候选数恒为 0，用例变成在测“没有候选”。
	//
	// 生产里**真实存在**的“存量积压暂停”形态是：旧二进制写下的积压暂停 + 一个未解决缺口（当时
	// 被拒的追加留下的 APPEND_REJECTED），后者让 `ResumeAcquire` 拒绝放行 ⇒ 暂停持续存在。
	// 那正是本用例需要的不变量：“持续暂停态的源在抖动轮之后仍要被扫到”。
	//
	// 缺口必须用**未分类原因**：`APPEND_REJECTED` / `DELIVER_ERROR` 属于“恢复后回读重放”类，
	// `maybeResumeBacklogLocked` 会（正确地）把它们分类消解掉并放行——那样暂停又没了。
	// 未分类原因走 default-deny，既不消解得掉、也不应当被消解，暂停就真正常驻。
	require.NoError(t, fb.Ledger().PauseAcquire(fb.Key(), legacyBacklogPauseReason))
	require.NoError(t, fb.Ledger().RecordGap(fb.Key(), 1, 2, "SOME_UNCLASSIFIED_REASON", "抖动轮夹具：阻塞恢复"))
	m.sweepBacklogOf = func() int64 {
		v := seq[i]
		if i < len(seq)-1 {
			i++
		}
		return v
	}
	m.resumeInterval = 10 * time.Millisecond
	m.recoveryProbe = func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.RunCapacityResumeLoop(ctx)

	require.Equal(t, int64(0), m.ResumeSweepStats().SkipBackpressure,
		"单轮抖动（1 轮上涨后回落）不得触发背压跳过（窗口阈值=3 ✓）；实测跳过 %d 次",
		m.ResumeSweepStats().SkipBackpressure)
	require.GreaterOrEqual(t, m.ResumeSweepStats().Candidates, int64(1),
		"抖动轮之后必须**照常扫描候选**（把猜测变读数：候选计数可读 ✓）")
}

// TestBackpressureSingleRoundRuleWouldSkipOnJitter（**常驻判别**，替代一次性变异 ✓）：
// 把阈值置 **1**（= 改动前的单轮判定行为 ✗）⇒ 同一抖动序列**必须**触发跳过 ✓。
//
// 意义：这不是"跑一次就丢"的临时变异，而是**常驻对照** ✓ —— 它把"窗口化才是 41 家的解"钉在
// 用例里 ✓：若有人把窗口化改回单轮（或把默认阈值改成 1）⇒ 这条立刻红 ✓✓。
func TestBackpressureSingleRoundRuleWouldSkipOnJitter(t *testing.T) {
	m, _ := newGateFixture(t, 50, 16)
	m.sweepBackpressureStreak = 1 // = 改动前行为（单轮判定 ✗）
	seq := []int64{100, 200, 50, 50, 50, 50, 50, 50}
	i := 0
	m.sweepBacklogOf = func() int64 {
		v := seq[i]
		if i < len(seq)-1 {
			i++
		}
		return v
	}
	m.resumeInterval = 10 * time.Millisecond
	m.recoveryProbe = func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.RunCapacityResumeLoop(ctx)

	require.GreaterOrEqual(t, m.ResumeSweepStats().SkipBackpressure, int64(1),
		"单轮判定（阈值=1）下，抖动轮必须触发跳过 ✓（这正是 41 家被永久挡住的原因 ✗）")
}
