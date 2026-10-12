package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 回归（P2-7）：SortEvents 必须保持契约全键次序（event_time DESC, log_source_id ASC,
// source_generation ASC, record_start DESC, record_end DESC, event_id ASC），并对**全键相同**
// 的事件保持输入相对次序（稳定序）：分页游标按 items[limit-1] 取值，不稳定序会让同一键的
// 行在两次调用/两次分页间换位，进而重复或漏返。
// 把实现换成不稳定排序即转红（见 TestSortEventsKeepsEqualKeysInInputOrder）。
func TestSortEventsOrdersByFullKey(t *testing.T) {
	srcA := logtypes.SourceIdentity{LogSourceID: "ns/a", SourceGeneration: "g2", ParserVersion: "p1"}
	srcB := logtypes.SourceIdentity{LogSourceID: "ns/b", SourceGeneration: "g1", ParserVersion: "p1"}
	at11 := "2026-09-20T11:00:00Z"
	items := []logtypes.Event{
		sortTestEvent(srcA, 10, 11, "2026-09-20T10:00:00Z", "oldest-a"),
		sortTestEvent(srcB, 30, 31, "2026-09-20T12:00:00Z", "newest-b"),
		sortTestEvent(srcA, 20, 21, at11, "mid-a"),
		sortTestEvent(srcA, 25, 26, at11, "mid-a-newer-record"),
		sortTestEvent(srcB, 5, 6, at11, "tie-src-b"),
	}

	SortEvents(items)

	require.Equal(t, []string{
		"newest-b",           // event_time DESC
		"mid-a-newer-record", // 同一时刻：log_source_id ASC → record_start DESC
		"mid-a",
		"tie-src-b", // 同一时刻：ns/a 在 ns/b 之前
		"oldest-a",
	}, messagesOf(items))
}

// 回归（P2-7）：稳定序。全键相同（同 source/record → 同 event_id，仅 message 不同）的
// 事件组必须保持输入先后——这是旧插入排序的既有语义，换成 sort.Slice（不稳定）即转红，
// 故实现必须用 sort.SliceStable。
func TestSortEventsKeepsEqualKeysInInputOrder(t *testing.T) {
	src := logtypes.SourceIdentity{LogSourceID: "ns/a", SourceGeneration: "g1", ParserVersion: "p1"}
	const group = 64
	items := make([]logtypes.Event, 0, group)
	want := make([]string, 0, group)
	for i := 0; i < group; i++ {
		msg := fmt.Sprintf("equal-%02d", i)
		items = append(items, sortTestEvent(src, 7, 8, "2026-09-20T11:00:00Z", msg))
		want = append(want, msg)
	}
	// 混入少量其它键的行，迫使排序真正执行分区/合并（全等切片可能被快速路径跳过）。
	for i := 0; i < 16; i++ {
		items = append(items, sortTestEvent(src, uint64(100+i), uint64(101+i),
			"2026-09-20T09:00:00Z", fmt.Sprintf("older-%02d", i)))
	}

	SortEvents(items)

	require.Len(t, items, group+16)
	require.Equal(t, want, messagesOf(items)[:group], "全键相同的事件必须保持输入相对次序（稳定序）")
}

// 回归（P2-7）：排序复杂度。旧实现是插入排序（O(n²)），而排序责任已交给本函数——
// 生产上一页合并可达 10^5 行量级，O(n²) 会让查询线程被 CPU 拖死。
// 逆序输入是插入排序的最坏情形（每次都要走到最前），n=2 万即约 2×10^8 次全键比较；
// 改用 sort.SliceStable 后同规模在 10ms 量级，两者相差两个数量级，故阈值取 500ms：
// 改回插入排序必然超时转红（实测 ~4s），而正确实现留有 >20× 余量。
func TestSortEventsHandlesLargeReversedInput(t *testing.T) {
	const n = 20000
	src := logtypes.SourceIdentity{LogSourceID: "ns/perf", SourceGeneration: "g1", ParserVersion: "p1"}
	items := make([]logtypes.Event, 0, n)
	base := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		// 时间随 i 递增 → 输入恰为结果序列（时间 DESC）的**逆序**：插入排序最坏情形。
		eventTime := base.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		items = append(items, sortTestEvent(src, uint64(i), uint64(i+1), eventTime, fmt.Sprintf("m-%05d", i)))
	}

	started := time.Now()
	SortEvents(items)
	elapsed := time.Since(started)
	t.Logf("SortEvents n=%d 逆序输入耗时 %s", n, elapsed)

	require.True(t, LessSortKey(SortKeyOf(items[0]), SortKeyOf(items[n-1])),
		"逆序输入排序后首行必须新于末行（次序断言，避免只测耗时）")
	require.Less(t, elapsed, 500*time.Millisecond,
		"排序必须接近 O(n log n)：插入排序 O(n²) 在最坏入力下耗时秒级，实际 %s", elapsed)
}

func sortTestEvent(src logtypes.SourceIdentity, recordStart, recordEnd uint64, eventTime, message string) logtypes.Event {
	return logtypes.BuildEvent(src, logtypes.RecordRange{Start: recordStart, End: recordEnd},
		eventTime, eventTime, "INFO", "stdout", message)
}

func messagesOf(items []logtypes.Event) []string {
	out := make([]string, 0, len(items))
	for _, ev := range items {
		out = append(out, ev.Message)
	}
	return out
}
