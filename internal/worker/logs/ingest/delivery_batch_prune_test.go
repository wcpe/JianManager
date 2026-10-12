package ingest

// FR-496 索引有界化回归：历史投递批次（delivery_batch）裁剪。
//
// 背景（FR-498 实测 @60 源）：采集索引里唯一随总量线性增长的表是 delivery_batch
// （≈5 MB/天），原先没有裁剪路径。判据与证明见 ledger/delivery_batch_prune.go 的文件头注释：
// **batch.End <= Positions.Reclaim 的条目是 contiguousDeliveryEnd(…, Reclaim) 的恒等元**。
//
// 本文件守三件事：
//  1. 有界性——连造 N 千批后表行数/体积不随 N 线性增长（同夹具关闭裁剪即线性增长，
//     证明断言确实在测这件事）；
//  2. 正确性——裁剪后既有功能照旧（水位、最近状态、恢复引用、实例绑定、落库往返）；
//  3. 不误裁——刚越过水位一字节的条目必须留下，且迁移校验不被裁剪进度差异误判为不一致。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

const (
	// pruneTestAdvanceEvery 每多少条批次推进一次 reclaim 水位（模拟 releaseRecovery 的节奏）。
	pruneTestAdvanceEvery = 100
	// pruneTestBatchSpan 单条批次覆盖的字节跨度。
	pruneTestBatchSpan = 100
)

// advanceWatermarkTo 走完责任转移链把 reclaim 推到 to（生产里由 releaseRecovery 完成）。
func advanceWatermarkTo(t *testing.T, pipe *pipeline.Pipeline, to uint64) {
	t.Helper()
	from := pipe.Ledger().Get(pipe.Key()).Positions.Reclaim
	if to <= from {
		return
	}
	segID := fmt.Sprintf("seg-%d-%d", from, to)
	require.NoError(t, pipe.BindRecoverySegment(segID, "recovery://prune-test", from, to))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", "test:receiver"))
	pos, err := pipe.TryReclaim()
	require.NoError(t, err)
	require.Equal(t, to, pos, "水位应推进到恢复分段覆盖末端")
}

// pruneBoundFixture 建一个真实 pipeline 的 Manager（单源），返回管道与账本键。
func pruneBoundFixture(t *testing.T, prune *ledger.DeliveryBatchPruneConfig) (*Manager, *pipeline.Pipeline) {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("hello\n"), 0o600))
	configs := []SourceConfig{{
		LogSourceID: "node:000", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:000", UTCDay: runtimeTestUTCDay(),
	}}
	m, err := newTestManager(t, Options{
		Root: root, Catalog: catalog.New(catalog.NewMemJournal()), Sources: configs, IndexPrune: prune,
	})
	require.NoError(t, err)
	return m, m.pipes["node:000/g1"]
}

// driveDeliveries 连造 n 条投递批次并按 pruneTestAdvanceEvery 推进水位，返回「未裁剪口径」的
// 连续前缀作为对照值。
func driveDeliveries(t *testing.T, pipe *pipeline.Pipeline, n int) uint64 {
	t.Helper()
	led := pipe.Ledger()
	key := pipe.Key()
	full := make([]ledger.DeliveryBatch, 0, n)
	for i := 0; i < n; i++ {
		start := uint64(i) * pruneTestBatchSpan
		end := start + pruneTestBatchSpan
		require.NoError(t, led.RecordDelivery(key, start, end, logtypes.DeliveryRequestDone))
		full = append(full, ledger.DeliveryBatch{Start: start, End: end, State: logtypes.DeliveryRequestDone})
		if (i+1)%pruneTestAdvanceEvery == 0 {
			advanceWatermarkTo(t, pipe, end)
		}
	}
	// 对照值：完整历史（裁剪前后必须给出同一连续前缀）。
	return ledger.ContiguousDeliveryEnd(full, led.Get(key).Positions.Reclaim)
}

// deliveryBatchUsage 读出索引里 delivery_batch 的行数与有效载荷字节数（含键与状态文本）。
func deliveryBatchUsage(t *testing.T, m *Manager) (rows int, payload int64) {
	t.Helper()
	require.NoError(t, m.persist())
	store, err := stateindex.OpenReadOnly(m.indexPath())
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	state, err := store.Load()
	require.NoError(t, err)
	rows = len(state.Batches)

	db, err := sql.Open("sqlite", m.indexPath())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT COALESCE(SUM(LENGTH(key) + LENGTH(state) + 24), 0) FROM delivery_batch").Scan(&payload))
	return rows, payload
}

// TestDeliveryBatchPruneBoundsIndexGrowth 是有界性回归的主体：
// 同夹具下「裁剪开启」时表行数/体积不随批量 N 增长；「裁剪关闭」时严格线性增长
// （后者既是「改回不裁剪即红」的对照臂，也证明前一条断言确实在测裁剪这件事）。
func TestDeliveryBatchPruneBoundsIndexGrowth(t *testing.T) {
	const small, large = 1000, 8000

	measure := func(n int, prune *ledger.DeliveryBatchPruneConfig) (int, int64) {
		m, pipe := pruneBoundFixture(t, prune)
		driveDeliveries(t, pipe, n)
		rows, payload := deliveryBatchUsage(t, m)
		require.NoError(t, crashClose(m))
		return rows, payload
	}

	// 裁剪开启（默认口径）：连造 8000 条后，表里只剩最后一段水位之后仍未越界的批次。
	smallRows, smallPayload := measure(small, nil)
	largeRows, largePayload := measure(large, nil)
	require.LessOrEqual(t, largeRows, pruneTestAdvanceEvery,
		"裁剪开启时行数必须与批量 N 解耦，实测 %d 行", largeRows)
	require.Equal(t, smallRows, largeRows,
		"1000 条与 8000 条的行数必须相同（不随 N 增长），实测 %d vs %d", smallRows, largeRows)
	require.LessOrEqual(t, largePayload, smallPayload,
		"裁剪开启时表体积不得随 N 增长，实测 %d vs %d 字节", smallPayload, largePayload)

	// 对照臂：关闭裁剪（等价于「改回不裁剪」）⇒ 行数与体积随 N 线性增长。
	offSmallRows, offSmallPayload := measure(small, &ledger.DeliveryBatchPruneConfig{Enabled: false})
	offLargeRows, offLargePayload := measure(large, &ledger.DeliveryBatchPruneConfig{Enabled: false})
	require.Equal(t, small, offSmallRows)
	require.Equal(t, large, offLargeRows, "不裁剪时必须逐条留存（这条同时证明本用例对裁剪敏感）")
	require.Equal(t, 8, offLargeRows/offSmallRows, "不裁剪时 8 倍批量应给出 8 倍行数")
	require.Greater(t, offLargePayload, 6*offSmallPayload, "不裁剪时表体积随总量线性增长")
	// 两条臂的差就是裁剪的收益（同一夹具、同一轮数）。
	require.Greater(t, offLargePayload, largePayload*10, "裁剪必须实质减少落库体积")
}

// TestDeliveryBatchPruneKeepsBoundaryAndRoundTrip 是正确性回归：
// 水位之上的条目一条不裁；水位、最近状态、恢复引用、落库往返全部照旧。
//
// 夹具刻意让**裁剪路径真正运行一次**且带「刚越过水位一字节」的条目：先记满 400 条 100 字节批次
// （[0,40000)），再记一条 `[40000,40001)`——它的末位正好是即将推进到的水位 + 1（仍会把连续前缀
// 从水位推到水位+1），最后才推进水位到 40000。这一步同时被 -overlay 的判据变异臂用作端到端证据
// （放宽判据一字节 ⇒ 这条会被误裁）。
func TestDeliveryBatchPruneKeepsBoundaryAndRoundTrip(t *testing.T) {
	m, pipe := pruneBoundFixture(t, nil)
	led := pipe.Ledger()
	key := pipe.Key()

	const watermark = uint64(40000)
	for i := 0; i < 400; i++ {
		start := uint64(i) * pruneTestBatchSpan
		require.NoError(t, led.RecordDelivery(key, start, start+pruneTestBatchSpan, logtypes.DeliveryRequestDone))
	}
	// 边界条目：末位 = 水位 + 1。
	require.NoError(t, led.RecordDelivery(key, watermark, watermark+1, logtypes.DeliveryUnknown))
	const expectedDelivery = watermark + 1

	// 推进水位到 40000：裁剪按判据在这里取舍（水位之下的 400 条被裁，边界那条必须留下）。
	advanceWatermarkTo(t, pipe, watermark)
	require.Equal(t, watermark, led.Get(key).Positions.Reclaim)

	// 内存口径：只留水位之上的那一条，派生值不变。
	memory := led.Get(key)
	require.Equal(t, []ledger.DeliveryBatch{
		{Start: watermark, End: watermark + 1, State: logtypes.DeliveryUnknown},
	}, memory.DeliveryBatches)
	require.Equal(t, expectedDelivery, memory.Positions.Delivery, "裁剪不得改变 delivery_position")
	require.Equal(t, logtypes.DeliveryUnknown, memory.DeliveryState, "最近状态另存，不随裁剪丢失")
	require.NotEmpty(t, memory.RecoveryRefs, "恢复引用不受裁剪影响")

	// 落库口径：库里只剩边界那一条，且没有任何「水位之下」的残留行。
	require.NoError(t, m.persist())
	store, err := stateindex.OpenReadOnly(m.indexPath())
	require.NoError(t, err)
	state, err := store.Load()
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.Len(t, state.Batches, 1, "库中只应留下水位之上的那条")
	require.Equal(t, watermark, state.Batches[0].StartPos)
	require.Equal(t, watermark+1, state.Batches[0].EndPos)

	// 读回口径（重放/对账依赖它）：批列表仍能自证 delivery_position，水位逐项一致。
	reloaded, err := m.loadStateFromIndex()
	require.NoError(t, err)
	entry := reloaded.Sources["node:000/g1"].Ledger[0]
	require.Equal(t, memory.Positions, entry.Positions, "四水位必须逐项一致")
	require.Equal(t, expectedDelivery, entry.Positions.Delivery)
	require.Equal(t, expectedDelivery,
		ledger.ContiguousDeliveryEnd(entry.DeliveryBatches, entry.Positions.Reclaim),
		"读回的批次列表必须仍给出同一个连续前缀")
	require.Equal(t, logtypes.DeliveryUnknown, entry.DeliveryState)

	// 二次落库（读回后再持久化）必须零写入：裁剪是幂等的，稳态不得因裁剪反复重写。
	require.NoError(t, m.persist())
	before := len(m.PersistSamples())
	require.NoError(t, m.persist())
	samples := m.PersistSamples()
	require.Greater(t, len(samples), before)
	last := samples[len(samples)-1]
	require.Zero(t, last.RowsWritten, "裁剪后的稳态轮次不得再写行")
	require.Zero(t, last.RowsDeleted, "裁剪后的稳态轮次不得再删行")
}

// TestDeliveryBatchPruneIsIdempotentAcrossRestart 验证「重启 → 读回 → 再落库」不产生行抖动
// （ordinal 是列表下标，裁剪若不确定会让重启后整段重写）。
func TestDeliveryBatchPruneIsIdempotentAcrossRestart(t *testing.T) {
	m, pipe := pruneBoundFixture(t, nil)
	driveDeliveries(t, pipe, 300)
	require.NoError(t, m.persist())
	root := m.root
	require.NoError(t, crashClose(m))

	restarted, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	require.NoError(t, restarted.persist())
	before := len(restarted.PersistSamples())
	require.NoError(t, restarted.persist())
	samples := restarted.PersistSamples()
	require.Greater(t, len(samples), before)
	require.Zero(t, samples[len(samples)-1].RowsWritten, "重启后的稳态轮次必须零写入（裁剪幂等）")
}

// TestDeliveryBatchPruneKeepsInstanceBindings 覆盖任务点「绑定照旧」：
// 裁剪只动 delivery_batch，实例绑定行原样往返。
func TestDeliveryBatchPruneKeepsInstanceBindings(t *testing.T) {
	m, _ := pruneBoundFixture(t, nil)
	work := t.TempDir()
	// 登记是同步落库的（FR-499：登记只做「读写 state.Instances + 幂等建管道 + persist」）。
	require.NoError(t, m.RegisterInstance("uuid-prune", "inst:prune", "holder-g1", "STDIO_PRIMARY", work))
	root := m.root
	require.NoError(t, m.Stop())

	// 重启读回：绑定必须完好（裁剪只动 delivery_batch，与绑定表无关）。
	restarted, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	binding, ok := restarted.state.Instances["uuid-prune"]
	require.True(t, ok, "实例绑定必须随索引往返（与裁剪无关）")
	require.Equal(t, "inst:prune", binding.TargetID)
	require.Equal(t, "holder-g1", binding.Generation)
}

// TestMigrationComparisonToleratesPruneProgress 是 FR-496 迁移校验与裁剪的相容性回归。
//
// 真机可达形态：迁移已在库中提交、归档尚未完成（migrateLegacyState 的那条分支），而索引里
// 的历史批次已按水位裁过、旧 JSON 里仍是完整历史。若按原样逐字段比对，会把一次正常启动
// 误判为「索引与旧状态不一致」而拒绝启动采集（硬失败）。比对口径归一后必须通过。
func TestMigrationComparisonToleratesPruneProgress(t *testing.T) {
	root := t.TempDir()
	fixture := migrationFixture() // 账本 reclaim=800，批次 [[0,500),[500,800)] 全在水位之下
	writeLegacyState(t, root, fixture)

	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	require.NoError(t, crashClose(m))

	// 模拟「库侧已按水位裁掉历史批次」（裁剪写路径删的就是这些行）：直接删行，
	// 等价于裁剪后的库内容。
	dbPath := filepath.Join(root, "var", "log", "ingest.index.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "DELETE FROM delivery_batch WHERE key = 'node:1/g1'")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// 把归档的旧 JSON 放回原路径（模拟归档前中断 / 人工回放归档）。
	legacyPath := filepath.Join(root, "var", "log", "ingest.state.json")
	archived, err := os.ReadFile(legacyPath + ".migrated")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacyPath, archived, 0o600))

	// 重启：两侧裁剪进度不同（旧 JSON 完整、库已裁）但语义一致 ⇒ 必须正常启动并补归档。
	restarted, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err, "裁剪进度差异不得被误判为迁移校验失败")
	require.NoError(t, crashClose(restarted))
	_, err = os.Stat(legacyPath)
	require.ErrorIs(t, err, os.ErrNotExist, "重启后旧 JSON 应被补归档")
}

// TestComparisonStateNormalizesPruneProgressOnly 是上一条的单元形态，并给出**负向对照**：
// 归一化只吃掉「水位之下的恒等元差异」，水位之上的任何差异必须照样判为不一致。
func TestComparisonStateNormalizesPruneProgressOnly(t *testing.T) {
	entryWith := func(batches []ledger.DeliveryBatch) ledger.Entry {
		return ledger.Entry{
			Key:             ledger.SourceKey{LogSourceID: "node:1", SourceGeneration: "g1"},
			Positions:       logtypes.Positions{Read: 1024, Durable: 1000, Delivery: 800, Reclaim: 800},
			DeliveryState:   logtypes.DeliveryRequestDone,
			DeliveryBatches: batches,
		}
	}
	stateWith := func(entry ledger.Entry) *persistedState {
		return &persistedState{Sources: map[string]persistedSource{"node:1/g1": {Ledger: []ledger.Entry{entry}}}}
	}

	full := entryWith([]ledger.DeliveryBatch{
		{Start: 0, End: 500, State: logtypes.DeliveryRequestDone},
		{Start: 500, End: 800, State: logtypes.DeliveryReplayRequired},
	})
	pruned := entryWith([]ledger.DeliveryBatch{})

	require.Equal(t,
		comparisonState(stateWith(pruned)).Sources["node:1/g1"].Ledger,
		comparisonState(stateWith(full)).Sources["node:1/g1"].Ledger,
		"水位之下的差异（恒等元）在比对口径下必须等价")

	// 负向对照一：水位之上仍被需要的条目缺失 ⇒ 必须判为不一致。
	neededMissing := entryWith([]ledger.DeliveryBatch{
		{Start: 0, End: 500, State: logtypes.DeliveryRequestDone},
		{Start: 500, End: 801, State: logtypes.DeliveryReplayRequired},
	})
	require.NotEqual(t,
		comparisonState(stateWith(neededMissing)).Sources["node:1/g1"].Ledger,
		comparisonState(stateWith(full)).Sources["node:1/g1"].Ledger,
		"水位之上的条目差异必须照样被抓出（归一化不得掩盖真差异）")

	// 负向对照二：水位本身不同 ⇒ 必须判为不一致（裁剪口径随水位变化，不能互相抵消）。
	reclaimShifted := entryWith([]ledger.DeliveryBatch{{Start: 0, End: 500, State: logtypes.DeliveryRequestDone}})
	reclaimShifted.Positions.Reclaim = 600
	require.NotEqual(t,
		comparisonState(stateWith(reclaimShifted)).Sources["node:1/g1"].Ledger,
		comparisonState(stateWith(full)).Sources["node:1/g1"].Ledger,
		"水位不同必须判为不一致")
}

// TestIndexPruneConfigFallback 验证配置面：nil 用默认（开启）、非法值回退、显式关闭生效。
func TestIndexPruneConfigFallback(t *testing.T) {
	require.Equal(t, ledger.DefaultDeliveryBatchPruneConfig(), indexPruneConfigOf(nil),
		"未配置时用默认（开启、严格按水位）")
	require.True(t, indexPruneConfigOf(nil).Enabled, "默认必须开启")
	invalid := indexPruneConfigOf(&ledger.DeliveryBatchPruneConfig{Enabled: true, KeepRecent: -5})
	require.Equal(t, ledger.DeliveryBatchPruneConfig{Enabled: true, KeepRecent: 0}, invalid,
		"非法尾窗回退默认 0")
	off := indexPruneConfigOf(&ledger.DeliveryBatchPruneConfig{Enabled: false})
	require.False(t, off.Enabled, "显式关闭必须生效（应急逃生口）")

	m, _ := pruneBoundFixture(t, &ledger.DeliveryBatchPruneConfig{Enabled: true, KeepRecent: -1})
	require.Equal(t, ledger.DeliveryBatchPruneConfig{Enabled: true, KeepRecent: 0}, m.indexPrune,
		"Manager 生效配置必须经归一化")
}
