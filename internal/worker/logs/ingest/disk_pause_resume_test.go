package ingest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// F4（2026-11-05）：磁盘容量暂停过去**没有任何恢复通道**。
//
// # 缺陷形态
//
// 磁盘达 pauseAtPercent（默认 90%）⇒ ApplyCapacity 调 PauseAcquire，
// 原因写 `disk usage 91.2% >= 90.0%; ...`。而解除暂停的三条路径全都只认
// 积压原因前缀 `wal backlog limit exceeded`：
//
//	① 追加前重估 —— 源已暂停 ⇒ 不产生新批次 ⇒ 走不到；
//	② 兜底环候选门 `!IsBacklogPauseReason(...) ⇒ continue`；
//	③ WAL 的 maybeResumeBacklogLocked 同样只认积压前缀。
//
// ⇒ 源停到进程重启。而磁盘占用是常态波动量，"暂停"对磁盘毫无帮助。
//
// 这些用例钉住：① 磁盘暂停现在能自己恢复；② 但是**带滞回**（回到 degraded 线下，
// 不是回到暂停线以下）；③ 磁盘仍高时不乱恢复；④ 不越权处理其它原因的暂停。

// diskPauseFixture 造一个「因磁盘容量暂停」的源，并给出可控的磁盘读数。
type diskPauseFixture struct {
	m      *Manager
	pipe   *pipeline.Pipeline
	key    string
	usage  atomic.Int64 // float64 位模式（atomic 无 float64）
	closed chan struct{}
}

func (f *diskPauseFixture) setUsage(percent float64) {
	f.usage.Store(int64(percent * 100))
}

func (f *diskPauseFixture) budget() acquire.CapacityBudget {
	b := acquire.DefaultCapacityBudget()
	b.DiskUsagePercent = float64(f.usage.Load()) / 100
	return b
}

// newDiskPauseFixture 建一个单源 Manager，并把它置于**磁盘暂停**态。
func newDiskPauseFixture(t *testing.T, pausedUsagePercent float64) *diskPauseFixture {
	t.Helper()
	fx := &diskPauseFixture{closed: make(chan struct{})}
	fx.setUsage(pausedUsagePercent)

	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		CapacityProvider: func() (acquire.CapacityBudget, error) { return fx.budget(), nil },
		Sources: []SourceConfig{{
			LogSourceID: "inst:diskpause", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:diskpause", UTCDay: runtimeTestUTCDay(),
		}},
	})
	require.NoError(t, err)
	fx.m = m

	m.mu.Lock()
	for _, p := range m.pipes {
		fx.pipe = p
	}
	m.mu.Unlock()
	require.NotNil(t, fx.pipe, "夹具应已建立源管道")

	// 用**产品自己的渲染函数**造出暂停原因：手抄字面量会让"文案改了、判别没跟"
	// 这类回归在测试里也看不见（那正是 F4 的成因）。
	reason := acquire.DiskPauseReason(pausedUsagePercent, 90)
	require.True(t, acquire.IsDiskPauseReason(reason),
		"夹具前提：%q 必须被认作磁盘暂停", reason)
	require.NoError(t, fx.pipe.Ledger().PauseAcquire(fx.pipe.Key(), reason))
	require.True(t, fx.pipe.Ledger().Get(fx.pipe.Key()).AcquirePaused)
	return fx
}

// TestDiskPauseResumesWhenUsageFallsBelowDegraded 钉住 F4 的核心诉求：
// 磁盘回落到 degraded 线下后，兜底环必须把源拉回来，**不需要重启**。
//
// 转红方式（实测）：把 tryResumeDiskPausedSource 的调用从候选门前删掉
// （= 退回旧行为：`!IsBacklogPauseReason ⇒ continue`）⇒ 本用例超时不变活。
func TestDiskPauseResumesWhenUsageFallsBelowDegraded(t *testing.T) {
	fx := newDiskPauseFixture(t, 95)
	m := fx.m

	// 兜底环频率调快，用例不必真等默认间隔。
	m.resumeInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunCapacityResumeLoop(ctx)

	// 磁盘降下来了（95 → 70，低于 degraded 线 80）。
	fx.setUsage(70)

	require.Eventually(t, func() bool {
		got := fx.pipe.Ledger().Get(fx.pipe.Key())
		return got != nil && !got.AcquirePaused
	}, 12*time.Second, 10*time.Millisecond,
		"磁盘回落到 degraded 线下后，兜底环必须解除磁盘暂停（不得只在重启时清）")

	require.Empty(t, fx.pipe.Ledger().Get(fx.pipe.Key()).PauseReason,
		"解除后不得残留暂停原因")
	require.Positive(t, m.resumedDiskSources.Load(),
		"观测面：必须走的是磁盘恢复通道（resumedDiskSources 应增长），不是别的副作用")
}

// TestDiskPauseStaysHeldWhileUsageStillHigh 钉住"磁盘仍高时不乱恢复"。
func TestDiskPauseStaysHeldWhileUsageStillHigh(t *testing.T) {
	fx := newDiskPauseFixture(t, 95)
	fx.m.resumeInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fx.m.RunCapacityResumeLoop(ctx)

	// 给兜底环充分的机会，但磁盘**一直**高于暂停线。
	require.Never(t, func() bool {
		got := fx.pipe.Ledger().Get(fx.pipe.Key())
		return got != nil && !got.AcquirePaused
	}, 600*time.Millisecond, 20*time.Millisecond,
		"磁盘仍高于暂停线时不得恢复采集（否则会反复暂停→恢复→暂停）")
	require.Zero(t, fx.m.resumedDiskSources.Load())
}

// TestDiskPauseRequiresHysteresisNotJustBelowPauseLine 钉住滞回上限的**下沿**：
// 仅降到暂停线（90）以下、但仍在 degraded 线（80）以上时，**不得**恢复。
//
// 这是本修复最容易写错的地方：直觉是"降到 90 以下就恢复"，
// 但那会在 89.9 ⇄ 90.1 之间反复重建采集通道。
//
// 转红方式（实测）：把 `resumeBelow` 改成 budget.PauseAtPercent ⇒ 本用例转红
// （85% 会被当成"已回落"而放行）。
func TestDiskPauseRequiresHysteresisNotJustBelowPauseLine(t *testing.T) {
	fx := newDiskPauseFixture(t, 95)
	fx.m.resumeInterval = 20 * time.Millisecond

	// 85：已低于暂停线 90，但仍高于恢复线 80。
	fx.setUsage(85)

	ok := fx.m.tryResumeDiskPausedSource(fx.pipe, fx.pipe.Ledger().Get(fx.pipe.Key()))
	require.False(t, ok,
		"85%% 低于暂停线但仍高于 degraded 线，不得恢复（否则会在阈值附近抖动）")
	require.True(t, fx.pipe.Ledger().Get(fx.pipe.Key()).AcquirePaused)
	require.Zero(t, fx.m.resumedDiskSources.Load())
}

// TestDiskPauseResumePathIgnoresOtherPauseReasons 钉住「不越权」：
// **积压类**暂停（且账本无缺口）不得被磁盘通道解除。
//
// 夹具设计的要点（前两版都失败在这里）：
//   - 不能带缺口——带缺口时 `ResumeAcquire` 无论谁来调都会被拒，“源仍暂停”在
//     正确与错误实现下**结果相同** ⇒ 用例没有分辨力；
//   - 磁盘读数必须**很低**——否则磁盘通道自己就会因读数而返回，也分不清它是
//     “按原因拒绝”还是“按读数拒绝”。
//
// 于是两个实现的结果会**真正分叉**：
//
//	正确：按原因（IsDiskPauseReason=false）直接拒 ⇒ 源仍暂停
//	错误：无原因判别 ⇒ 磁盘 60% < 80% ⇒ 调 ResumeAcquire ⇒ 账本无缺口 ⇒ **成功**
//	       ⇒ 积压源被磁盘通道放行 ✗（绕过了积压自己的滞回）
func TestDiskPauseResumePathIgnoresOtherPauseReasons(t *testing.T) {
	fx := newDiskPauseFixture(t, 95)
	// 积压类暂停，**不加任何缺口**（让 ResumeAcquire 一旦被调就会成功）。
	require.NoError(t, fx.pipe.Ledger().PauseAcquire(fx.pipe.Key(), legacyBacklogPauseReason+": 4242"))
	// 磁盘读数故意很低（60 < 80）：磁盘通道若误接，就会真的去解除它。
	fx.setUsage(60)

	state, ok := fx.pipe.Ledger().AcquireState(fx.pipe.Key())
	require.True(t, ok)
	require.True(t, acquire.IsBacklogPauseReason(state.PauseReason))
	require.False(t, acquire.IsDiskPauseReason(state.PauseReason),
		"夹具前提：积压原因不得被认作磁盘暂停")

	resumed := fx.m.tryResumeDiskPausedSource(fx.pipe, fx.pipe.Ledger().Get(fx.pipe.Key()))
	require.False(t, resumed,
		"磁盘通道不得接管积压类暂停（积压有自己的滞回与缺口闸）")
	require.Zero(t, fx.m.resumedDiskSources.Load())

	after, ok := fx.pipe.Ledger().AcquireState(fx.pipe.Key())
	require.True(t, ok)
	require.True(t, after.Paused,
		"积压源必须仍暂停：磁盘已很低且无缺口，若磁盘通道不按原因拒绝，它就会被错误地解除")
	require.Equal(t, state.PauseReason, after.PauseReason,
		"暂停原因不得被改写（否则现场分不清是谁暂停的）")
}

// TestDiskPauseReasonClassification 钉住两个原因前缀互不串扰。
//
// 这条用例守的是 F4 的**根因本身**：原因判别靠前缀，一旦文案改了而常量没跟，
// 恢复会静默失效——正是本次缺陷。用产品函数产出的真实文案做断言，不抄字面量。
func TestDiskPauseReasonClassification(t *testing.T) {
	disk := acquire.DiskPauseReason(91.2, 90)
	require.Contains(t, disk, "disk usage")
	require.True(t, acquire.IsDiskPauseReason(disk), "磁盘暂停必须被认得：%q", disk)
	require.False(t, acquire.IsBacklogPauseReason(disk),
		"磁盘原因**不得**被积压判别认下（那会让它走错恢复路径）")

	legacy := legacyBacklogPauseReason + ": 1234"
	require.True(t, acquire.IsBacklogPauseReason(legacy))
	require.False(t, acquire.IsDiskPauseReason(legacy),
		"积压原因不得被磁盘判别认下")
}
