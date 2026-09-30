package ingest

// 持久化规模回归（FR-496 spec §3.3 的可转红替身）：
//   persist 的写入量必须只随「本批次变更」增长，不随索引总量增长。
//   退回整本重写（或让 FP 把写入时刻算进指纹）会让 RowsWritten/BytesWritten 随源数线性增长，
//   本用例即红。

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// scaleFixtureState 构造「n 个源」的大状态夹具：每个源带 24 条 WAL（对应 spec §1 的
// 24MB/源 外推口径的形态）+ 分段/轮转/缺口/投递批次。
func scaleFixtureState(n int) *persistedState {
	state := &persistedState{
		Sources:       make(map[string]persistedSource, n),
		SourceConfigs: make(map[string]SourceConfig, n),
		Instances:     make(map[string]InstanceBinding),
	}
	for i := 0; i < n; i++ {
		logID := fmt.Sprintf("node:%03d", i)
		key := logID + "/g1"
		identity := logtypes.SourceIdentity{LogSourceID: logID, SourceGeneration: "g1", ParserVersion: "v1"}
		entry := ledger.Entry{
			Key:      ledger.SourceKey{LogSourceID: logID, SourceGeneration: "g1"},
			Identity: identity,
			Positions: logtypes.Positions{
				Read: uint64(i * 1000), Durable: uint64(i * 1000), Reclaim: uint64(i * 900),
			},
			DeliveryState: logtypes.DeliveryRequestDone,
			DeliveryBatches: []ledger.DeliveryBatch{
				{Start: 0, End: uint64(i * 900), State: logtypes.DeliveryRequestDone},
			},
			Segments: []ledger.Segment{
				{Path: fmt.Sprintf("/data/logs/%03d/latest.log", i), Kind: ledger.SegmentLive, StartPos: 0, EndPos: uint64(i * 1000)},
			},
			Gaps:         []ledger.Gap{},
			RecoveryRefs: []ledger.RecoveryRef{},
			ErrorCount:   0,
			IngestSeq:    uint64(i),
		}
		wal := make([]acquire.WALEntry, 0, 24)
		for seq := 1; seq <= 24; seq++ {
			event := logtypes.BuildEvent(identity, logtypes.RecordRange{
				Start: uint64(seq * 10), End: uint64(seq*10 + 9),
			}, "2026-09-30T00:00:00Z", "2026-09-30T00:00:01Z", "INFO", "stdout",
				fmt.Sprintf("source %03d line %d %s", i, seq, strings.Repeat("x", 60)))
			wal = append(wal, acquire.WALEntry{Seq: uint64(seq), Event: event, Appended: true, Durable: true})
		}
		state.Sources[key] = persistedSource{
			Ledger: []ledger.Entry{entry}, WAL: wal, WALRefs: []acquire.WALRef{},
			EventsStoredThrough: uint64(i * 1000), ProjectionGeneration: fmt.Sprintf("projection-%d", i),
			EventsStored: true, Events: []logtypes.Event{},
		}
		state.SourceConfigs[key] = SourceConfig{
			LogSourceID: logID, SourceGeneration: "g1", Path: fmt.Sprintf("/data/logs/%03d/latest.log", i),
			Mode: pipeline.ModeFilePrimary, SourceCategory: logtypes.SourceInstance,
			StorageNamespace: logID, UTCDay: "2026-09-30",
		}
	}
	return state
}

// scaleManager 构造一个只带索引的 Manager（无 pipeline），用于直接驱动 persist 的写入路径。
func scaleManager(t *testing.T, state *persistedState) *Manager {
	t.Helper()
	root := t.TempDir()
	m := &Manager{
		root: root, statePath: filepath.Join(root, "var", "log", "ingest.state.json"),
		state:   *state,
		sources: map[string]SourceConfig{}, pipes: map[string]*pipeline.Pipeline{},
		cat: catalog.New(catalog.NewMemJournal()),
	}
	store, err := openIndexForTest(m)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.index.Close() })
	m.index = store
	return m
}

// TestPersistWritesOnlyChangedRowsAtScale 是规模断言回归：
// 全量写入一次后，只改动**一个**源，单次 persist 的写入行数与字节数必须与源总数无关。
func TestPersistWritesOnlyChangedRowsAtScale(t *testing.T) {
	const small, large = 8, 64

	measure := func(sources int) (first, incremental int64, rows int) {
		state := scaleFixtureState(sources)
		m := scaleManager(t, state)
		// 首轮：全量落库。
		startFirst := time.Now()
		require.NoError(t, m.persist())
		first = time.Since(startFirst).Nanoseconds()
		// 只改动一个源（推进游标 + 追加一条 WAL），其余源逐字节不变。
		key := "node:000/g1"
		saved := m.state.Sources[key]
		saved.Ledger = append([]ledger.Entry(nil), saved.Ledger...)
		saved.Ledger[0].Positions.Read += 4096
		saved.WAL = append(append([]acquire.WALEntry(nil), saved.WAL...), acquire.WALEntry{
			Seq: 25,
			Event: logtypes.BuildEvent(saved.Ledger[0].Identity, logtypes.RecordRange{Start: 250, End: 259},
				"2026-09-30T00:00:00Z", "2026-09-30T00:00:01Z", "INFO", "stdout", "brand new line"),
			Appended: true, Durable: true,
		})
		m.state.Sources[key] = saved

		before := len(m.PersistSamples())
		require.NoError(t, m.persist())
		samples := m.PersistSamples()
		require.Greater(t, len(samples), before, "增量写入必须产生一次采样")
		last := samples[len(samples)-1]
		return first, last.Duration.Nanoseconds(), last.RowsWritten
	}

	_, smallNs, smallRows := measure(small)
	_, largeNs, largeRows := measure(large)

	// ① 写入行数与源总数无关：单源变更只允许触及该源的行
	//    （position + projection + aux + 1 条新 WAL + source 行的少量变化）。
	require.LessOrEqual(t, smallRows, 6,
		"单源变更必须只写该源的少量行（整本重写会写 %d 行量级）", small*27)
	require.LessOrEqual(t, largeRows, 6,
		"单源变更必须只写该源的少量行（整本重写会写 %d 行量级）", large*27)

	// ② 写入耗时不得随总量线性增长：8 源 → 64 源（8 倍）时，单次增量持久化不得放大到 4 倍以上。
	//    阈值刻意留宽以吸收调度噪声；整本重写会随源数成倍上升，必然越界。
	require.Less(t, largeNs, smallNs*4+int64(5*time.Millisecond),
		"增量持久化耗时不得随索引总量线性增长：8 源=%dns 64 源=%dns", smallNs, largeNs)

	// ③ 绝对上界：真机验收口径是 ≤50ms，这里在 CI 上用宽阈值兜住「秒级阻塞」。
	require.Less(t, largeNs, int64(500*time.Millisecond),
		"单次增量持久化必须是毫秒级，实测 %dns", largeNs)
}

// TestPersistBytesStayBoundedWithManySources 断言写入字节数与源总数解耦
// （最直接的「O(全量) → O(变更行)」观测：全量重写会写 MB 级，增量只写 KB 级）。
func TestPersistBytesStayBoundedWithManySources(t *testing.T) {
	state := scaleFixtureState(64)
	m := scaleManager(t, state)
	first, err := applyIndex(m)
	require.NoError(t, err)
	require.Greater(t, first.BytesWritten, int64(100_000), "首轮全量写入应当是可观的字节量（夹具本身够大）")

	key := "node:031/g1"
	saved := m.state.Sources[key]
	saved.Ledger = append([]ledger.Entry(nil), saved.Ledger...)
	saved.Ledger[0].ErrorCount++
	m.state.Sources[key] = saved

	stats, err := applyIndex(m)
	require.NoError(t, err)
	require.LessOrEqual(t, stats.RowsWritten, 6)
	require.Less(t, stats.BytesWritten, int64(64*1024),
		"单源变更的写入字节必须远小于全量（全量=%d 字节），实测 %d 字节", first.BytesWritten, stats.BytesWritten)
}

// applyIndex 直接把当前内存状态写入索引（不经 pipeline 快照），返回写入统计。
func applyIndex(m *Manager) (indexStats, error) {
	desired, err := m.stateToIndexState(&m.state)
	if err != nil {
		return indexStats{}, err
	}
	stats, err := m.index.Apply(desired)
	if err != nil {
		return indexStats{}, err
	}
	return indexStats{RowsWritten: stats.RowsWritten, BytesWritten: stats.BytesWritten}, nil
}

// indexStats 是 applyIndex 的轻量结果（避免测试直接依赖 stateindex 的完整 Stats）。
type indexStats struct {
	RowsWritten  int
	BytesWritten int64
}

// TestWALBodyFingerprintCoversEveryEventField 是「只写变更行」的正确性前提：
// WAL 行的变更判据必须覆盖正文的**每个** JSON 字段，否则正文变了却不落库（静默丢更新）。
//
// 做法：对基准事件逐个字段做单点变异，断言指纹每次都必须变化——新增/改动事件字段而忘记
// 纳入指纹时，本用例即红。
func TestWALBodyFingerprintCoversEveryEventField(t *testing.T) {
	identity := logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "v1"}
	base := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 100, End: 200},
		"2026-09-30T00:00:00Z", "2026-09-30T00:00:01Z", "INFO", "stdout", "hello")
	base.Fields = map[string]string{"mod": "core"}
	baseFP := walBodyFP(base)

	mutations := map[string]func(ev *logtypes.Event){
		"event_id":    func(ev *logtypes.Event) { ev.EventID = "changed" },
		"log_source":  func(ev *logtypes.Event) { ev.Source.LogSourceID = "node:2" },
		"generation":  func(ev *logtypes.Event) { ev.Source.SourceGeneration = "g2" },
		"parser":      func(ev *logtypes.Event) { ev.Source.ParserVersion = "v2" },
		"record_from": func(ev *logtypes.Event) { ev.Record.Start++ },
		"record_to":   func(ev *logtypes.Event) { ev.Record.End++ },
		"event_time":  func(ev *logtypes.Event) { ev.EventTimeUTC = "2026-09-30T00:00:09Z" },
		"ingest_time": func(ev *logtypes.Event) { ev.IngestTimeUTC = "2026-09-30T00:00:09Z" },
		"level":       func(ev *logtypes.Event) { ev.Level = "ERROR" },
		"stream":      func(ev *logtypes.Event) { ev.Stream = "stderr" },
		"message":     func(ev *logtypes.Event) { ev.Message = "hello!" },
		"hash":        func(ev *logtypes.Event) { ev.CanonicalHash = "changed" },
		"fields_add":  func(ev *logtypes.Event) { ev.Fields["extra"] = "1" },
		"fields_edit": func(ev *logtypes.Event) { ev.Fields["mod"] = "other" },
		"fields_drop": func(ev *logtypes.Event) { delete(ev.Fields, "mod") },
	}
	for name, mutate := range mutations {
		event := base
		event.Fields = map[string]string{}
		for key, value := range base.Fields {
			event.Fields[key] = value
		}
		mutate(&event)
		require.NotEqual(t, baseFP, walBodyFP(event),
			"字段 %q 的变异必须改变指纹，否则该字段的更新不会落库", name)
	}
	// 反向：同一事件的指纹必须稳定（否则会产生无谓写入，退化成整本重写）。
	require.Equal(t, baseFP, walBodyFP(base), "同一事件的指纹必须稳定")
}

// TestPersistKeepsIndexUsableAfterFloodBound 抽样验证：大夹具下反复 persist 后索引仍可读、
// 且内容与内存状态一致（防止「只写变更行」实现成「漏写」）。
func TestPersistKeepsIndexUsableAfterFloodBound(t *testing.T) {
	state := scaleFixtureState(16)
	m := scaleManager(t, state)
	for round := 0; round < 5; round++ {
		saved := m.state.Sources["node:003/g1"]
		saved.Ledger = append([]ledger.Entry(nil), saved.Ledger...)
		saved.Ledger[0].Positions.Read += uint64(round)
		m.state.Sources["node:003/g1"] = saved
		require.NoError(t, m.persist())
	}
	require.NoError(t, m.index.IntegrityCheck())
	loaded, err := m.loadStateFromIndex()
	require.NoError(t, err)
	require.Equal(t, comparisonState(&m.state), comparisonState(loaded),
		"增量写入后索引必须与内存状态逐字段一致")
}
