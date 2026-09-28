package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// ingestTestEvents 构造 n 条位置相邻的 canonical 事件（from 为起始序号）。
func ingestTestEvents(id, gen string, from, n int, payload string) []logtypes.Event {
	evs := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		idx := from + i
		evs = append(evs, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: gen, ParserVersion: "v1"},
			logtypes.RecordRange{Start: uint64(idx * 100), End: uint64(idx*100 + 99)},
			"2026-09-28T00:00:00Z", "2026-09-28T00:00:01Z", "INFO", "stdout", payload,
		))
	}
	return evs
}

// B1a：`appendEvents` 落段后必须推进「段存储已覆盖到的最大 record_end」，且 `eventBodyLookup`
// 能按 EventID 取回正文——这两者是持久化「按条切分引用/内联」与加载水合的前提。
func TestAppendEventsAdvancesStoredThroughAndLookupHydrates(t *testing.T) {
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	m, err := New(Options{Root: root, Catalog: cat, Journal: journal})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	key := "inst:7/file/g1"
	events := ingestTestEvents("inst:7/file", "g1", 0, 3, "body")
	require.NoError(t, m.appendEvents(key, events))

	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	require.True(t, saved.EventsStored, "落段后应置位 EventsStored")
	require.Equal(t, uint64(299), saved.EventsStoredThrough,
		"应推进到权威集合的最大 record_end（区间 [0,99] [100,199] [200,299]）")

	lookup := m.eventBodyLookup(key)
	for _, ev := range events {
		got, ok := lookup(ev.EventID)
		require.True(t, ok, "已入库事件必须能按 EventID 水合")
		require.Equal(t, ev.Message, got.Message, "水合正文须一致")
		require.Equal(t, ev.CanonicalHash, got.CanonicalHash)
	}
	if _, ok := lookup("not-an-event-id"); ok {
		t.Fatal("不存在的 EventID 不得命中")
	}

	// 追加更大区间的事件后，覆盖位置必须单调前进。
	more := ingestTestEvents("inst:7/file", "g1", 3, 1, "body")
	require.NoError(t, m.appendEvents(key, append(events, more...)))
	m.mu.Lock()
	saved = m.state.Sources[key]
	m.mu.Unlock()
	require.Equal(t, uint64(399), saved.EventsStoredThrough, "覆盖位置应单调推进")
}
