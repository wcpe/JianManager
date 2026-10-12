package ingest

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
	m, key, source, _, payloads, _ := newPendingConfirmFixture(t)

	m.mu.Lock()
	cur := m.state.Sources[key]
	require.NotEmpty(t, cur.ProjectionGeneration, "夹具应已有已发布代次")
	stored, err := m.canonicalRecoveryEvents(key, cur)
	require.NoError(t, err)
	require.NotEmpty(t, stored, "夹具应已把事件读入本源（段或内联）")
	cur.PublicationPending = true
	m.state.Sources[key] = cur
	m.mu.Unlock()

	// 本批＝一条**新**事件（不在已存集合里）⇒ 若判据回落整窗重发，载荷应为全量（>1）✗。
	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 30, End: 40},
			"2026-09-24T01:00:00Z", "2026-09-24T01:00:01Z", "INFO", "stdout", "pending-confirm batch"),
	}

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
	m, key, source, _, payloads, deleteDay := newPendingConfirmFixture(t)

	m.mu.Lock()
	cur := m.state.Sources[key]
	gen := cur.ProjectionGeneration
	require.NotEmpty(t, gen)
	stored, err := m.canonicalRecoveryEvents(key, cur)
	require.NoError(t, err)
	full := len(stored)
	require.NotZero(t, full)
	cur.PublicationPending = true
	m.state.Sources[key] = cur
	m.mu.Unlock()

	// 最近已发布代次在查询面缺一天 ⇒ 复核必然不完整 ⇒ 必须回落整窗重发 ✗。
	deleteDay()

	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 30, End: 40},
			"2026-09-24T01:00:00Z", "2026-09-24T01:00:01Z", "INFO", "stdout", "pending-confirm batch"),
	}

	_, err = m.deliver(source, batch, false)
	require.NoError(t, err)

	require.Zero(t, m.RepublishSkipped(), "VL 缺行不得跳过重发（判据不放宽）✗")
	got := payloadEventCount(payloads())
	require.Equal(t, full, got,
		"缺行必须照旧整窗重发：写入面＝权威全量（本批走归档/段面，见 projectionWritePlan.Archive）✓")
}
