package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestReconcileGroupingYieldsToCollection（对账 CPU 让路）：**对账分组必须在轮内主动让路**。
//
// 现场形态（2026-10-03 X 光片）：canonicalDayCounts→groupEventsByUTCDay 对整源事件全量分组，
// 与采集轮**抢核** ⇒ 采集轮"没等锁、没等落库"却推进不动 ✗。让路 = 每 slice 条 Sleep(yield)
// 交还 P，采集立即拿回 CPU ✓。
//
// 转红方式（实测）：把调用点接上的让路旋钮改成 0/0（或去掉 groupReconcileEvents 里的让路传参）
// —— 本用例在「让路次数 ≥ 切片数-1」处变红（0 次让路）。
func TestReconcileGroupingYieldsToCollection(t *testing.T) {
	source := SourceConfig{LogSourceID: "src:yield", SourceGeneration: "g1", UTCDay: "2026-10-02"}
	events := make([]logtypes.Event, 0, 20_000)
	for i := 0; i < 20_000; i++ {
		events = append(events, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: "src:yield", SourceGeneration: "g1", ParserVersion: "v1"},
			logtypes.RecordRange{Start: uint64(i + 1), End: uint64(i + 2)},
			"2026-10-02T01:00:00Z", "2026-10-02T01:00:01Z", "INFO", "stdout", "line"))
	}

	// ① 直接证明让路语义：20k 条 / 1000 条一切片 ⇒ 19 次让路，且确实 sleep 过（elapsed 有下界）。
	yields := 0
	started := time.Now()
	grouped, days, err := groupEventsByUTCDayYielding(source, events, 1000, 2*time.Millisecond, func() { yields++ })
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.NotEmpty(t, grouped)
	require.NotEmpty(t, days)
	require.GreaterOrEqual(t, yields, 19,
		"每 slice 条必须让路一次（实测 %d 次；不让路 = 与采集抢核 ✗）", yields)
	require.GreaterOrEqual(t, elapsed, 15*time.Millisecond,
		"让路必须是真睡眠（交还 P），不是空转（实测 %s）", elapsed)

	// ② 零让路形态保持纯函数语义（既有调用点逐字不变 ✓）。
	notify := 0
	_, _, err = groupEventsByUTCDayYielding(source, events, 0, 0, func() { notify++ })
	require.NoError(t, err)
	require.Zero(t, notify, "slice/yield 为 0 时不得让路（保持纯函数 ✓）")

	// ③ 接线：ReplayTuning（log_reconcile.yield/slice_events）经 groupReconcileEvents 生效。
	m := &Manager{reconcileSliceEvents: 1000, reconcileYield: 2 * time.Millisecond}
	counted := 0
	m.reconcileYieldNotify = func() { counted++ }
	_, _, err = m.groupReconcileEvents(source, events)
	require.NoError(t, err)
	require.GreaterOrEqual(t, counted, 19,
		"调用点必须接上让路旋钮（实测让路 %d 次）", counted)

	// ④ 默认值回退：未配置 ⇒ 5ms / 4096 条 ✓（不是 0 ⇒ 不会静默关闭让路 ✗）。
	dflt := &Manager{}
	require.Equal(t, defaultReconcileYield, dflt.reconcileYieldOf())
	require.Equal(t, defaultReconcileSliceEvents, dflt.reconcileSliceOf())
}
