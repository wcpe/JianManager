package pipeline

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// FR-483 阶段5：归一化边界与 normalize 的跨轮累积必须**有界**。
//
// 背景：linePos（每行绝对位置表）与 Normalizer.events（已产出事件）原先只 append、
// 从不释放，内存随**会话内总行数**线性增长——64 源 × 3000 行实测为其常驻堆的
// 主要部分（linePos ≈130MB、n.events ≈141MB）。它们与「单轮批量大小」无关，
// 因此限制单轮读取量并不解决该问题。
//
// 本文件固化的不变量：
//  1. linePos 窗口在每轮 DrainEvents 后收缩到当前未闭合多行事件，而非全会话历史；
//  2. 窗口收缩后 remap 仍能把行号正确映射回绝对位置（否则 event_id/RecordRange 会错乱）；
//  3. 流式路径（未开 RetainEvents）不保留已产出事件，故长会话内存不随之增长。

func boundedTestOpts() normalize.Options {
	return normalize.Options{
		Source: logtypes.SourceIdentity{LogSourceID: "src-bounded", SourceGeneration: "g1"},
		Stream: "stdout",
	}
}

// 逐行成事件（每行都以时间戳开头）时，窗口不应随会话增长，
// **且窗口收缩不得静默丢事件**——这是 trimLinePos 最危险的失效模式：
// 裁掉的行若仍被后续 remap 引用，事件会丢或位置错乱。
func TestNormalizeBoundaryLinePosWindowStaysBounded(t *testing.T) {
	b := NewNormalizeBoundary(boundedTestOpts())
	line := "[12:00:00] [Server thread/INFO]: " + strings.Repeat("x", 50)
	const total = 20000
	maxWindow := 0
	delivered := 0
	for i := 0; i < total; i++ {
		abs := uint64(i * (len(line) + 1))
		b.Feed([]byte(line), abs, abs+uint64(len(line)))
		delivered += len(b.DrainEvents())
		require.LessOrEqual(t, len(b.linePos), 8,
			"每轮取走事件后 linePos 窗口必须收缩（第 %d 行）", i)
		if len(b.linePos) > maxWindow {
			maxWindow = len(b.linePos)
		}
	}
	// 每行一个完整事件；末行仍是未闭合 pending，故应恰好交付 total-1 条。
	require.Equal(t, total-1, delivered,
		"窗口收缩不得丢事件：期望 %d 条，实得 %d 条", total-1, delivered)
	// lineBase 反映已被裁掉的行数：窗口有界而 lineBase 持续增长，二者共同覆盖全会话。
	require.Equal(t, total-1, b.lineBase, "被裁掉的行数应等于总行数减去窗口残留")
	require.LessOrEqual(t, maxWindow, 8, "窗口必须保持有界")
}

// 未闭合多行必须留在窗口内——FlushPartial/FlushComplete 关闭它时 remap 还要用这些行。
func TestNormalizeBoundaryKeepsPendingMultilineInWindow(t *testing.T) {
	b := NewNormalizeBoundary(boundedTestOpts())
	head := "[12:00:00] [Server thread/ERROR]: exception"
	b.Feed([]byte(head), 0, uint64(len(head)))
	b.DrainEvents()
	// 未闭合多行：该行必须仍在窗口里（此时尚无可裁的行）。
	require.Equal(t, 1, len(b.linePos), "pending 事件的首行不可被裁掉")

	cont := "\tat com.example.Foo(Foo.java:1)"
	b.Feed([]byte(cont), uint64(len(head)+1), uint64(len(head)+1+len(cont)))
	b.DrainEvents()

	// 关闭该多行事件：位置必须指向真实行，而不是被裁后失配。
	ev, ok := b.n.FlushComplete()
	require.True(t, ok, "未闭合多行应能被 FlushComplete 关闭")
	remapped := b.remap(ev)
	require.Equal(t, uint64(0), remapped.Record.Start, "多行事件起点应是首行的绝对位置")
	require.Equal(t, uint64(len(head)+1+len(cont)), remapped.Record.End, "多行事件终点应是末行的绝对位置")
}

// 窗口化后 remap 必须把**会话内行号**正确换算为窗口下标，否则位置会整体错位。
func TestNormalizeBoundaryRemapAcrossTrimmedWindow(t *testing.T) {
	b := NewNormalizeBoundary(boundedTestOpts())
	// 先喂若干完整事件把窗口推走，使 lineBase > 0。
	for i := 0; i < 5; i++ {
		line := "[12:00:00] [Server thread/INFO]: filler"
		abs := uint64(i * (len(line) + 1))
		b.Feed([]byte(line), abs, abs+uint64(len(line)))
		b.DrainEvents()
	}
	require.Greater(t, b.lineBase, 0, "前置喂入后窗口应已发生偏移")

	// 再喂一个多行事件并关闭：其行号是会话内行号，需减去 lineBase 才能定位。
	head := "[12:00:05] [Server thread/ERROR]: boom"
	headAbs := uint64(5 * (len("[12:00:00] [Server thread/INFO]: filler") + 1))
	b.Feed([]byte(head), headAbs, headAbs+uint64(len(head)))
	b.DrainEvents()
	ev, ok := b.n.FlushComplete()
	require.True(t, ok)
	remapped := b.remap(ev)
	require.Equal(t, headAbs, remapped.Record.Start,
		"窗口偏移后 remap 仍须把会话内行号映射回正确的绝对位置")
}

// 流式边界不得保留事件历史：长会话的存活堆不随总事件数增长。
func TestNormalizeBoundaryDoesNotRetainEventHistory(t *testing.T) {
	b := NewNormalizeBoundary(boundedTestOpts())
	line := "[12:00:00] [Server thread/INFO]: " + strings.Repeat("y", 200)

	// 先量一个基线（喂少量行），再喂大量行，比较存活堆增量。
	baseline := feedAndSettledHeap(b, line, 500, 0)
	after := feedAndSettledHeap(b, line, 20000, 500)
	require.Less(t, after-baseline, 2.0,
		"流式路径不应随行数增长持有内存：500 行=%.2fMiB，20500 行=%.2fMiB", baseline, after)

	// 统计仍须准确（不保留事件不影响计数）。
	// 末行仍处于未闭合状态（无后续行闭合它），故事件数比行数少 1。
	st := b.Stats()
	require.Equal(t, 20500, st.LineCount)
	require.Equal(t, 20499, st.EventCount, "最后一行仍是 pending，尚未产出事件")
	require.Equal(t, 1, st.PendingLines)
}

// feedAndSettledHeap 从 start 喂 count 行，返回强制 GC 后的存活堆（MiB）。
func feedAndSettledHeap(b *NormalizeBoundary, line string, count, start int) float64 {
	for i := 0; i < count; i++ {
		abs := uint64((start + i) * (len(line) + 1))
		b.Feed([]byte(line), abs, abs+uint64(len(line)))
		b.DrainEvents()
	}
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / 1048576
}
