package ingest

// 索引持久化切分（FR-498 P0）在采集合成层的回归：**迁移比对**必须与切分口径无关。
//
// 为什么单独测迁移这条路径：迁移是唯一「一次性写完整份状态」的调用点（migrateLegacyState 的
// index.Apply 全量形态），也是切分最容易被破坏的地方——它一次要写全部行，天然会切成很多提交
// 单元；而迁移后紧跟的**逐字段比对**（comparisonState + DeepEqual）会把任何「少写一行/多写
// 一行/行内容被单元边界切坏」立刻判成不一致并**拒绝启动采集**（生产上是拒绝启动的硬失败）。
//
// 断言方式：同一份旧 JSON 分别在「不切分」与「强切分」预算下迁移，两侧读回的索引内容必须
// 逐字段等价，且强切分那侧确实被切成了多个提交单元（否则等价是平凡结论）。

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// chunkLegacyState 构造一份规模足以触发切分的旧 JSON 状态（每个源都带账本 + 内联 WAL）。
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

// TestMigrationChunkedMatchesUnchunked 是迁移路径的切分等价回归（判据与转红说明见文件头）。
func TestMigrationChunkedMatchesUnchunked(t *testing.T) {
	migrate := func(budget stateindex.CommitBudget) (*persistedState, int) {
		root := t.TempDir()
		fixture := chunkLegacyState(12, 120)
		writeLegacyState(t, root, fixture)
		m, err := newTestManager(t, Options{
			Root: root, Catalog: catalog.New(catalog.NewMemJournal()), IndexCommit: &budget,
		})
		require.NoError(t, err, "迁移必须成功（不一致会拒绝启动采集）")
		// 迁移自带的逐字段比对已经跑过并通过；这里再对齐一次，确保「内存状态 = 旧 JSON」。
		require.Equal(t, fixtureNormalized(fixture), fixtureNormalized(&m.state),
			"迁移后内存状态必须与旧 JSON 逐字段一致")
		chunks := len(m.PersistSamples())
		require.NoError(t, crashClose(m))

		store, err := stateindex.OpenReadOnly(filepath.Join(root, "var", "log", "ingest.index.db"))
		require.NoError(t, err)
		defer func() { _ = store.Close() }()
		rows, err := store.Load()
		require.NoError(t, err)
		loaded, err := indexStateToState(rows)
		require.NoError(t, err)
		return loaded, chunks
	}

	// 对照臂必须把**两个上界**都抬掉：行数预算之外还有耗时硬上界（Target×5/4），竞态构建下
	// 单次整轮迁移会被它切成十几段——那正是设计要的（见 stateindex.CommitBudget），
	// 但会让「不切分」这一臂名不副实，故 Target 也一并抬到不可达。
	unchunked, plainChunks := migrate(stateindex.CommitBudget{MaxRows: 1 << 20, MinRows: 1 << 20, Target: time.Hour})
	chunked, splitChunks := migrate(stateindex.CommitBudget{MaxRows: 64, MinRows: 16})

	require.Equal(t, 1, plainChunks, "不切分预算下迁移必须是一个提交单元")
	require.Greater(t, splitChunks, 8,
		"强切分预算下迁移必须被切成多个提交单元（12 源 × 120 条 WAL ≈ 1600 行 / 64 行每单元）")
	require.Equal(t, comparisonState(unchunked), comparisonState(chunked),
		"切分前后的索引内容必须逐字段等价")
}
