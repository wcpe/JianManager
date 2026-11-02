package ingest

// 2026-11-02 投递路径简化：本文件两条用例守「pending=true 时先按已发布代次做只读确认，
// 完整就跳过整窗重发、缺行才回落整窗重发」。那条链（PublicationPending + 代次白名单 +
// verifyProjectionQuery 复核 + 整窗重发）已随简化整体删除：`deliverCtx` 不再读 pending、
// 不再取名代次、也不再整窗重写。实测：`state.Sources[key].ProjectionGeneration` 恒为空
// （夹具首条断言即红），两条用例均失去被测对象。
//
// 它们记录的现场形态（实例原地重写 latest.log ⇒ 偏移漂移 ⇒ 每批都整窗重写、量级翻倍）
// 在简化后**结构上不会发生**：一批数据只写一次，没有「重发」这条路径。
// 仍然活着的同型关切（同 id 同 hash 不得重复写入）由 write_idempotency_test.go 守着。
//
// 发布确认快路径（协议 #5 的①）红绿用例：
//   T1 「pending=true ∧ VL 完整 ⇒ 零整窗重发」（修复前为红 ✗：每批都整窗重写）
//   T2 「pending=true ∧ VL 确实缺行 ⇒ 照旧整窗重发」（判据不得放宽 ✗）
// 现场背景：实例自身日志栈原地重写 latest.log ⇒ 偏移漂移 ⇒ IDENTITY_CONFLICT；
// pending 一旦长期不消，此后**每一批**投递都会整窗重写（re-publish 的量级即为复跑 #4 的重复来源之一）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// payloadEventCount 统计 VL 写入载荷里的事件条数（夹具记录的是 NDJSON 原文，每条一个事件）。
func payloadEventCount(payloads []string) int {
	n := 0
	for _, p := range payloads {
		n += strings.Count(p, `"event_id"`)
	}
	return n
}

func newPendingConfirmFixture(t *testing.T) (*Manager, string, SourceConfig, []logtypes.Event, func() []string, func()) {
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
	key := source.LogSourceID + "/" + source.SourceGeneration
	insertsBefore := fixture.insertCount()
	return m, key, source, events,
		func() []string { return fixture.payloadsSince(insertsBefore) },
		// 删除**事件真正所属的那一天**（夹具的权威事件在 2026-09-22/09-23；删"今天"等于没删 ✗）。
		func() { fixture.deleteDay("2026-09-22") }
}

// TestPendingConfirmSkipsFullRepublishWhenVLComplete —— T1。
func TestPendingConfirmSkipsFullRepublishWhenVLComplete(t *testing.T) {
	t.Skip("投递路径简化（2026-11-02）：pending 只读确认与整窗重发已删，「跳过重发」失去对象")
	m, key, source, _, payloads, _ := newPendingConfirmFixture(t)

	m.mu.Lock()
	cur := m.state.Sources[key]
	m.mu.Unlock()
	require.NotEmpty(t, cur.ProjectionGeneration, "夹具应已有已发布代次")
	stored, err := m.canonicalRecoveryEvents(key, cur)
	require.NoError(t, err)
	require.NotEmpty(t, stored, "夹具应已把事件读入本源（段或内联）")
	m.mu.Lock()
	cur = m.state.Sources[key]
	cur.PublicationPending = true
	m.state.Sources[key] = cur
	m.mu.Unlock()

	// 历史窗口的新事件不满足新尾判据，必须实际走发布确认而非尾部解耦路径。
	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 1},
			"2026-09-22T03:00:00Z", "2026-09-22T03:00:01Z", "INFO", "stdout", "pending-confirm batch"),
	}
	nonPending := cur
	nonPending.PublicationPending = false
	require.False(t, m.planDeliveryTail(key, nonPending, batch, false).Fast, "夹具必须覆盖历史发布确认")

	_, err = m.deliver(source, batch, false)
	require.NoError(t, err)

	got := payloadEventCount(payloads())
	require.Equal(t, 1, got, "VL 已完整 ⇒ 只应写本批（1 条），不得整窗重写 ✗")
	require.Equal(t, int64(1), m.RepublishSkipped(), "必须记一次 republishSkipped ✓")
	m.mu.Lock()
	require.False(t, m.state.Sources[key].PublicationPending,
		"pending 必须被清（终态与「写成功后清 pending」一致）✓")
	m.mu.Unlock()
}

// TestPendingConfirmFallsBackWhenVLMissing —— T2。
func TestPendingConfirmFallsBackWhenVLMissing(t *testing.T) {
	t.Skip("投递路径简化（2026-11-02）：pending 只读确认与整窗重发已删，「回落整窗重发」失去对象")
	m, key, source, _, payloads, deleteDay := newPendingConfirmFixture(t)

	m.mu.Lock()
	cur := m.state.Sources[key]
	m.mu.Unlock()
	gen := cur.ProjectionGeneration
	require.NotEmpty(t, gen)
	stored, err := m.canonicalRecoveryEvents(key, cur)
	require.NoError(t, err)
	full := len(stored)
	require.NotZero(t, full)
	m.mu.Lock()
	cur = m.state.Sources[key]
	cur.PublicationPending = true
	m.state.Sources[key] = cur
	m.mu.Unlock()

	// 最近已发布代次在查询面缺一天 ⇒ 复核必然不完整 ⇒ 必须回落整窗重发 ✗。
	deleteDay()

	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 1},
			"2026-09-22T03:00:00Z", "2026-09-22T03:00:01Z", "INFO", "stdout", "pending-confirm batch"),
	}
	nonPending := cur
	nonPending.PublicationPending = false
	require.False(t, m.planDeliveryTail(key, nonPending, batch, false).Fast, "夹具必须覆盖历史发布确认")

	_, err = m.deliver(source, batch, false)
	require.NoError(t, err)

	require.Zero(t, m.RepublishSkipped(), "VL 缺行不得跳过重发（判据不放宽）✗")
	got := payloadEventCount(payloads())
	require.Equal(t, full+len(batch), got,
		"缺行必须照旧整窗重发：写入面包含既有权威全量与本批新增历史事件")
}
