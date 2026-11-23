package logcoord

import (
	"container/heap"
)

// mergeCursor K-way merge 中的单路游标。
type mergeCursor struct {
	workerID string
	targetID string
	items    []Event
	idx      int
}

func (c *mergeCursor) ok() bool { return c.idx < len(c.items) }

func (c *mergeCursor) head() Event {
	e := c.items[c.idx]
	e.WorkerID = c.workerID
	if e.TargetID == "" {
		e.TargetID = c.targetID
	}
	return e
}

// eventHeap 按稳定复合排序键的小顶堆（排序比较见 SortKey.Compare）。
type eventHeap []*mergeCursor

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	return h[i].head().SortKey().Compare(h[j].head().SortKey()) < 0
}
func (h eventHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x interface{}) { *h = append(*h, x.(*mergeCursor)) }
func (h *eventHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// MergeStreams 对多路已按 OrderVersion 排序的事件流做稳定 K-way merge。
//
// capRows > 0 时结果行数不超过 capRows；maxBytes > 0 时按 ApproxBytes 累计截断。
// 返回 (rows, pageMore, byteCut)。pageMore 只是正常分页，不降低 coverage；
// byteCut 才表示资源预算截断。
//
// 注意：merge 输入应来自同一 Query View 的逻辑事件集合；本函数不重新排序单路内部顺序以外的数据。
func MergeStreams(streams []EventStream, capRows int, maxBytes uint64) (rows []Event, pageMore bool, byteCut bool) {
	h := make(eventHeap, 0, len(streams))
	for _, s := range streams {
		if len(s.Items) == 0 {
			continue
		}
		c := &mergeCursor{workerID: s.WorkerID, targetID: s.TargetID, items: s.Items}
		h = append(h, c)
	}
	heap.Init(&h)

	var used uint64
	for h.Len() > 0 {
		if capRows > 0 && len(rows) >= capRows {
			pageMore = true
			break
		}
		c := h[0]
		ev := c.head()
		if maxBytes > 0 {
			sz := ev.ApproxBytes()
			if used+sz > maxBytes && len(rows) > 0 {
				byteCut = true
				break
			}
			used += sz
		}
		rows = append(rows, ev)
		c.idx++
		if c.ok() {
			heap.Fix(&h, 0)
		} else {
			heap.Pop(&h)
		}
	}
	return rows, pageMore, byteCut
}

// EventStream 一路（通常 = 一个 Worker 或一个目标）已排序事件。
type EventStream struct {
	WorkerID string
	TargetID string
	Items    []Event
}

// SortEventsInPlace 按 OrderVersion 排序（用于保证单路有序；生产 Worker 侧已排序时可跳过）。
func SortEventsInPlace(items []Event) {
	// 简单插入排序对小样本足够；避免额外依赖。
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].SortKey().Compare(items[j-1].SortKey()) < 0; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
