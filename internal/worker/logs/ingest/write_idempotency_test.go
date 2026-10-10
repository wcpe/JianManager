package ingest

// 写面幂等（replace 路径）的红绿用例（2026-10-10）。
//
// 现场背景（同一源、同一 2026-10-05 窗口，生产 VL 实查）：
//   `projection-5` 被**完整写入两次**、内容逐字节相同（events=544205、时间范围一致）
//   ⇒ 该代次条数翻倍（544205 × 2 = 1088410，与 VL 实查吻合）；
//   沙箱以生产等效容量复现出 VL 条数 / 唯一事件 = 3.45x（默认容量下 6.00x）。
//
// 为什么分两层验证：
//   ① 纯过滤层（dropEventsAlreadyWritten）：判据本身的三条分支（同 id 同 hash 剔除 /
//      同 id 异 hash 保留 / 未知 id 保留）——这是"判据不放宽"的直接证据；
//   ② 接线层（writeProjectionDay）：replace 路径确实调用了该过滤——这是"修复真的生效"的证据。
//   分离的理由：`deliver` 上游的 planDeliveryBatchOutsideLock 已按同 id 同 hash 去重，
//   走整条 deliver 的"同内容重放"会在更早处返回 204（撤掉修复仍通过 ⇒ 假绿），
//   故接线层必须直接调用 writeProjectionDay。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// newWriteIdempotencyFixture 造出「已发布一代、且该代内容已在 VL」的现场。
func newWriteIdempotencyFixture(t *testing.T) (*Manager, string, SourceConfig, []logtypes.Event, *reconcileVLFixture) {
	t.Helper()
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	return m, source.LogSourceID + "/" + source.SourceGeneration, source, events, fixture
}

// daySourceFor 返回按事件所属 UTC 日设置 UTCDay 的源。
func daySourceFor(t *testing.T, source SourceConfig, events []logtypes.Event) SourceConfig {
	t.Helper()
	require.NotEmpty(t, events)
	day, err := eventUTCDay(source, events[0])
	require.NoError(t, err)
	daySource := source
	daySource.UTCDay = day
	return daySource
}

// TestDropEventsAlreadyWritten_SameID_SameHashDropped —— ① a：同 id 同 hash ⇒ 剔除。
//
// 转红方式（实测）：把 `hash == event.CanonicalHash` 判据整个删掉（改为命中即跳过）——
// 本用例仍绿（它只断言"剔除"），但 T2 会红 ⇒ 两例合起来锁住判据两侧。
func TestDropEventsAlreadyWritten_SameID_SameHashDropped(t *testing.T) {
	m, key, source, events, _ := newWriteIdempotencyFixture(t)
	m.mu.Lock()
	published := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()
	require.NotEmpty(t, published)

	client, _, err := m.clientForSource(source)
	require.NoError(t, err)

	kept := m.dropEventsAlreadyWritten(client, source, published, events)
	require.Empty(t, kept, "同代次同 id 同 hash 已存在 ⇒ 应全部剔除")
	require.Greater(t, m.RepublishSkipped(), int64(0), "跳过条数须可观测 ✓")
}

// TestDropEventsAlreadyWritten_SameID_DifferentHashKept —— ① b：同 id **异 hash** ⇒ 必须保留。
//
// 这是"判据不放宽"的核心证据：真更正绝不能被当成重复剔掉。
// 转红方式（实测）：去掉 hash 比较（只看 id 命中即跳过）⇒ 本用例立即转红。
func TestDropEventsAlreadyWritten_SameID_DifferentHashKept(t *testing.T) {
	m, key, source, events, _ := newWriteIdempotencyFixture(t)
	m.mu.Lock()
	published := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()

	client, _, err := m.clientForSource(source)
	require.NoError(t, err)

	// 造"真更正"：同 id、新内容 ⇒ canonical hash 与 VL 中那份不同。
	corrected := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		event.Message = event.Message + " [corrected]"
		event.CanonicalHash = logtypes.CanonicalContentHash(
			event.EventTimeUTC, event.Level, event.Stream, event.Message)
		corrected = append(corrected, event)
	}

	kept := m.dropEventsAlreadyWritten(client, source, published, corrected)
	require.Len(t, kept, len(corrected),
		"同 id 异 hash 是真更正，必须逐条保留（去掉 hash 比较即转红）")
}

// TestDropEventsAlreadyWritten_UnknownIDsKept —— ① c：VL 中不存在的 id ⇒ 全保留。
func TestDropEventsAlreadyWritten_UnknownIDsKept(t *testing.T) {
	m, key, source, _, _ := newWriteIdempotencyFixture(t)
	m.mu.Lock()
	published := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()

	client, _, err := m.clientForSource(source)
	require.NoError(t, err)

	fresh := []logtypes.Event{
		logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"},
			logtypes.RecordRange{Start: 900, End: 910},
			"2026-09-24T01:00:00Z", "2026-09-24T01:00:01Z", "INFO", "stdout", "brand new line"),
	}
	kept := m.dropEventsAlreadyWritten(client, source, published, fresh)
	require.Len(t, kept, 1, "VL 中没有的新事件必须保留 ✓")
}

// TestDropEventsAlreadyWritten_EmptyGenerationMatchesAnyGeneration —— ① d：慢路径口径不变。
//
// generation 为空表示"不限代次标签"（既有行为）：跨代次的同 id 同 hash 也应被剔除。
// 转红方式：把空值分支也钉到某个具体代次名 ⇒ 本用例转红（旧代次的行不再被识别）。
func TestDropEventsAlreadyWritten_EmptyGenerationMatchesAnyGeneration(t *testing.T) {
	m, _, source, events, _ := newWriteIdempotencyFixture(t)

	client, _, err := m.clientForSource(source)
	require.NoError(t, err)

	// 夹具中这份内容写在 projection-1；用空代次（不限标签）应能匹配到它。
	kept := m.dropEventsAlreadyWritten(client, source, "", events)
	require.Empty(t, kept, "空 generation = 不限代次标签（既有慢路径口径）⇒ 仍应识别并剔除")
}

// TestWriteProjectionDayReplacePathFiltersInsertions —— ② 接线：replace 路径确实调用了过滤。
//
// 场景：以**同一个代次名**把一份"VL 中已完整存在"的事件整窗再写一遍
// （这正是现场 projection-5 被写两次的形态）。
// 断言：本次**插入载荷为空**。
//
// 转红方式（实测）：去掉 writeProjectionDay 里 replace 分支的过滤调用 ⇒ 载荷出现全量 ⇒ 转红。
func TestWriteProjectionDayReplacePathFiltersInsertions(t *testing.T) {
	m, key, source, events, fixture := newWriteIdempotencyFixture(t)
	daySource := daySourceFor(t, source, events)

	m.mu.Lock()
	published := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()
	require.NotEmpty(t, published)
	require.NotZero(t, fixture.rowCount(), "夹具前提：VL 中应已有该代次的行")

	dayEvents := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		if day, err := eventUTCDay(source, event); err == nil && day == daySource.UTCDay {
			dayEvents = append(dayEvents, event)
		}
	}
	require.NotEmpty(t, dayEvents)

	insertsBefore := fixture.insertCount()
	// replace=true（整窗语义）⇒ 走本次新增的过滤分支。
	_, _ = m.writeProjectionDay(daySource, dayEvents, dayEvents, published, nil, true, false, nil)

	require.Zero(t, payloadEventCount(fixture.payloadsSince(insertsBefore)),
		"同代次同 id 同 hash 已存在 ⇒ 插入载荷必须为空（去掉 replace 分支过滤即转红）")
}

// TestWriteProjectionDayTailPathUnaffected —— ② 接线：增量尾部不受影响。
//
// 场景：tailPlanned=true（增量尾部快路径）⇒ 不过滤，本批逐条照写。
// 用一个**全新的**代次名（尾部路径的正常形态：代次由 nextFreeProjectionGeneration 新分配）。
func TestWriteProjectionDayTailPathUnaffected(t *testing.T) {
	m, _, source, events, fixture := newWriteIdempotencyFixture(t)
	daySource := daySourceFor(t, source, events)

	dayEvents := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		if day, err := eventUTCDay(source, event); err == nil && day == daySource.UTCDay {
			dayEvents = append(dayEvents, event)
		}
	}
	require.NotEmpty(t, dayEvents)

	insertsBefore := fixture.insertCount()
	_, err := m.writeProjectionDay(daySource, dayEvents, dayEvents, "projection-tail-fresh", nil, false, true, nil)
	require.NoError(t, err)
	require.Equal(t, len(dayEvents), payloadEventCount(fixture.payloadsSince(insertsBefore)),
		"增量尾部不走过滤：本批就是新数据，须逐条照写")
}
