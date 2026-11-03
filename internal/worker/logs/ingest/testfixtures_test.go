package ingest

// 本文件保存**不依赖存量 JSON 迁移**的测试夹具与工具：
// 索引落库/读回、重启幂等、绑定往返、崩溃模拟等回归都要用它们。
//
// 2026-10-28：生产账本已全删重建、版本未发布，「旧 ingest.state.json → SQLite」
// 的一次性迁移路径被整体删除，但它顺带提供的这几个 helper 仍被其他用例使用，
// 故单独落在这里，而不是随着 migration_test.go 一起消失。

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// crashClose 关闭索引句柄但**不做持久化**，用于模拟「进程突然消失」的现场。
func crashClose(m *Manager) error {
	if m == nil || m.index == nil {
		return nil
	}
	return m.index.Close()
}

// fixtureNormalized 归一化后返回状态（比较口径：含派生字段统一口径）。
func fixtureNormalized(st *persistedState) *persistedState {
	return comparisonState(st)
}

// chunkLegacyState 构造一份规模足以触发切分的状态（每个源都带账本 + 内联 WAL）。
func chunkLegacyState(sources, walPerSource int) *persistedState {
	st := &persistedState{
		Sources:       make(map[string]persistedSource, sources),
		SourceConfigs: make(map[string]SourceConfig, sources),
		Instances:     map[string]InstanceBinding{},
	}
	for i := 0; i < sources; i++ {
		logID := sourceKeyName(i)
		key := logID + "/g1"
		identity := logtypes.SourceIdentity{LogSourceID: logID, SourceGeneration: "g1", ParserVersion: "v1"}
		entry := ledger.Entry{
			Key:           ledger.SourceKey{LogSourceID: logID, SourceGeneration: "g1"},
			Identity:      identity,
			Positions:     logtypes.Positions{Read: uint64(walPerSource * 100), Durable: uint64(walPerSource * 100), Reclaim: 100},
			DeliveryState: logtypes.DeliveryRequestDone,
			DeliveryBatches: []ledger.DeliveryBatch{
				{Start: 0, End: 100, State: logtypes.DeliveryRequestDone},
			},
		}
		saved := persistedSource{Ledger: []ledger.Entry{entry}, ProjectionGeneration: "projection-1"}
		for seq := 1; seq <= walPerSource; seq++ {
			event := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: uint64(seq * 100), End: uint64(seq*100 + 99)},
				"2026-09-30T00:00:00Z", "2026-09-30T00:00:01Z", "INFO", "stdout",
				"chunk fixture line")
			saved.WAL = append(saved.WAL, acquire.WALEntry{Seq: uint64(seq), Event: event, Appended: true, Durable: true})
		}
		st.Sources[key] = saved
		st.SourceConfigs[key] = SourceConfig{
			LogSourceID: logID, SourceGeneration: "g1", Path: filepath.Join("/data/logs", logID+".log"),
			Mode: pipeline.ModeFilePrimary, SourceCategory: logtypes.SourceInstance,
			StorageNamespace: logID, UTCDay: "2026-09-30",
		}
	}
	return st
}

func sourceKeyName(index int) string {
	return "node:" + string(rune('a'+index%26)) + string(rune('0'+index/26))
}

// migrationFixture 构造一份覆盖全部字段形态的账本状态，供「落库往返 / 重启幂等」类用例作夹具。
func migrationFixture() *persistedState {
	entry := ledger.Entry{
		Key:           ledger.SourceKey{LogSourceID: "node:1", SourceGeneration: "g1"},
		Identity:      logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "v1"},
		Positions:     logtypes.Positions{Read: 1024, Durable: 1000, Delivery: 900, Reclaim: 800},
		DeliveryState: logtypes.DeliveryRequestDone,
		DeliveryBatches: []ledger.DeliveryBatch{
			{Start: 0, End: 500, State: logtypes.DeliveryRequestDone},
			{Start: 500, End: 800, State: logtypes.DeliveryReplayRequired},
		},
		Segments: []ledger.Segment{
			{Path: "/data/logs/latest.log", Kind: ledger.SegmentLive, StartPos: 0, EndPos: 1024, IdentityBytes: 64, IdentitySHA256: "abc"},
			{Path: "/data/logs/archive.log.gz", Kind: ledger.SegmentGzip, StartPos: 0, EndPos: 900},
		},
		Rotations: []ledger.RotationLink{
			{FromPath: "/data/logs/latest.log", ToPath: "/data/logs/rotated.log", EndPosFrom: 900, Generation: "g1"},
		},
		Gaps: []ledger.Gap{
			{StartPos: 300, EndPos: 350, Reason: "ROTATION_HOLE", Detail: "previous process", Resolved: true, Resolution: "recovered"},
			{StartPos: 700, EndPos: 750, Reason: "PAUSED", Detail: "disk 95.0%"},
		},
		// 2026-10-29：RecoveryRefs 已随受管恢复分段机制删除，夹具不再携带它。
		ErrorCount:    3,
		IngestSeq:     42,
		AcquirePaused: false,
	}
	ev1 := logtypes.BuildEvent(entry.Identity, logtypes.RecordRange{Start: 100, End: 200},
		"2026-09-30T00:00:00Z", "2026-09-30T00:00:01Z", "INFO", "stdout", "line one")
	ev1.Fields = map[string]string{"mod": "core"}

	return &persistedState{
		Sources: map[string]persistedSource{
			"node:1/g1": {
				PublicationPending:   true,
				Ledger:               []ledger.Entry{entry},
				WAL:                  []acquire.WALEntry{{Seq: 7, Event: ev1, Appended: true, Durable: true}},
				EventsStoredThrough:  300,
				ProjectionGeneration: "projection-1",
				EventsStored:         true,
				Events:               nil,
			},
			"node:2/g1": {
				Ledger:               nil, // 刻意：空账本 + 无 WAL，检验 nil 形态
				ProjectionGeneration: "projection-2",
			},
		},
		SourceConfigs: map[string]SourceConfig{
			"node:1/g1": {
				LogSourceID: "node:1", SourceGeneration: "g1", Path: "/data/logs/latest.log",
				Mode: pipeline.ModeFilePrimary, RotateTo: "latest.log", ArchiveGlob: "*.gz",
				SourceCategory: logtypes.SourceInstance, StorageNamespace: "ns:1", UTCDay: "2026-09-30",
			},
			"node:2/g1": {LogSourceID: "node:2", SourceGeneration: "g1", Path: "/data/logs/2.log",
				Mode: pipeline.ModeFilePrimary, SourceCategory: logtypes.SourceInstance,
				StorageNamespace: "ns:2", UTCDay: "2026-09-30"},
		},
		Instances: map[string]InstanceBinding{
			"uuid-1": {UUID: "uuid-1", TargetID: "ns:1", Generation: "g1", Mode: pipeline.ModeFilePrimary, WorkDir: "/srv/inst-1"},
			"uuid-2": {UUID: "uuid-2", TargetID: "ns:2", Generation: "g2", Mode: pipeline.ModeStdioPrimary, WorkDir: "/srv/inst-2"},
		},
	}
}

// openIndexForTest 打开测试直接构造的 Manager 的索引库。
func openIndexForTest(m *Manager) (*stateindex.Store, error) {
	return stateindex.Open(m.indexPath())
}

// ── 以下 helper 原本定义在已删除的用例文件里，仍被其他用例使用，故迁到这里 ──

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

// captureSlogOutput 把默认 slog 输出重定向到 buf（返回还原函数）。
//
// 为什么要重定向而不是用 hook：用例要数的正是**日志行数**这一可观测量，
// 而 slog 无内置的"按前缀计数"钩子；重定向是唯一能直接数到行的做法。
func captureSlogOutput(buf *bytes.Buffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

// fakeAdjudicationClock 是可注入的时钟桩（`now()` 返回当前时刻，`advance` 推进），
// 供节流窗口类用例确定性地跨越窗口使用。
type fakeAdjudicationClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeAdjudicationClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeAdjudicationClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}
