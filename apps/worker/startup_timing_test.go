package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStartupTimerMarksMonotonically 分段耗时必须单调递且能算出各段自身耗时。
//
// 变异：把 SinceLast 改成与 Elapsed 相同，第二条断言转红。
func TestStartupTimerMarksMonotonically(t *testing.T) {
	timer := newStartupTimer()

	time.Sleep(10 * time.Millisecond)
	elapsed1 := timer.Mark("config")
	time.Sleep(20 * time.Millisecond)
	elapsed2 := timer.Mark("bootstrap")

	require.Len(t, timer.phases, 2)
	p1, p2 := timer.phases[0], timer.phases[1]

	// 累计耗时必须递增（时间不能倒流）。
	require.GreaterOrEqual(t, elapsed2, elapsed1, "第二次的累计耗时不应当小于第一次")
	require.Greater(t, p2.Elapsed, p1.Elapsed)

	// 关键：SinceLast 是「本段自身」而不是累计。
	// 若它误算成累计，p2.SinceLast 会远大于 p1.SinceLast（~30ms vs ~10ms 的差距）。
	require.Less(t, p2.SinceLast, p2.Elapsed,
		"第二段的自身耗时必须小于它的累计耗时（否则就误把累计当自身了）")
	require.GreaterOrEqual(t, p1.SinceLast, 10*time.Millisecond)
	require.GreaterOrEqual(t, p2.SinceLast, 20*time.Millisecond)
}

// TestStartupTimerNilSafe 零值 nil 不得 panic。
//
// 为什么需要：timer 在 runWorker 里创建，但测试路径/异常路径可能拿到 nil。
func TestStartupTimerNilSafe(t *testing.T) {
	var timer *startupTimer
	require.NotPanics(t, func() {
		timer.Mark("x")
		timer.Report()
	})
}

// TestStartupTimerReportDoesNotPanic 空 timer 报也不得 panic。
func TestStartupTimerReportDoesNotPanic(t *testing.T) {
	timer := newStartupTimer()
	require.NotPanics(t, func() { timer.Report() }, "没有任何阶段时 Report 应当安静返回")
}

// TestStartupSlowThresholdReasonable 阈值必须是一个「慢了但没到故障」的量级。
//
// 写死这个断言的理由：阈值被随手改小会让正常启动刷 Warn，
// 改大则真慢的时候看不见。生产实测冷启动 3-8s，30s 是合理上限。
func TestStartupSlowThresholdReasonable(t *testing.T) {
	require.GreaterOrEqual(t, startupSlowThreshold, 10*time.Second,
		"阈值太小：正常启动就会刷 Warn，告警失去意义")
	require.LessOrEqual(t, startupSlowThreshold, 60*time.Second,
		"阈值太大：真卡住时运维看不见")
}
