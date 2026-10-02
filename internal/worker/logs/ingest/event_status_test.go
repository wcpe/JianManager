package ingest

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件是「事件级状态可见性」（后续项③）的回归。
//
// 承重语义三条：
//   - 派生态视图对**四种态**（未发 / 已发未认 / 已认 / 已校验）各能给出正确判定（外加已放弃）；
//   - 判据来自既有事实（positions / delivery_batches / verified_runs / abandonments / gaps），
//     **不新增任何持久化状态位**；
//   - 放弃凭据（Entry.Abandonments）必须跨重启存活（否则离线视图与 CanReclaim 依据一并消失）。
//
// 转红方式（变异见 .tmp/event-status-mutation.py）：
//   - 把 classifyEventStatus 的某条判据写死（如 verified 永远不命中）→ CoversAllStates 红；
//   - 去掉 indexAux 的 Abandonments 搬运 → SurvivalAcrossRestart 红。

// TestDeriveEventStatusCoversAllStates 构造「四种态 + 已放弃」各一段，固定判据与优先级。
func TestDeriveEventStatusCoversAllStates(t *testing.T) {
	facts := EventStatusFacts{
		Positions: logtypes.Positions{Read: 1000, Durable: 800, Delivery: 300, Reclaim: 100},
		Batches: []ledger.DeliveryBatch{
			{Start: 300, End: 400, State: logtypes.DeliveryUnknown},        // 已发未认
			{Start: 400, End: 500, State: logtypes.DeliveryReplayRequired}, // 未发（待重放）
		},
		VerifiedRuns: []ledger.PositionRange{{From: 150, To: 250}}, // 已校验（在已认前缀之内，优先级更高）
		Abandonments: []ledger.GapAbandonment{{
			From: 501, To: 600, ReasonCode: ledger.GapReasonPermanentlyLost,
			Operator: "ops", AtUTC: "2026-10-02T00:00:00Z",
		}},
		Gaps: []ledger.Gap{{StartPos: 700, EndPos: 750, Reason: "APPEND_REJECTED", Detail: "seed"}},
	}
	report := DeriveEventStatus("node:reconcile/g1", facts, 0, 0)

	require.Equal(t, uint64(0), report.From)
	require.Equal(t, uint64(1000), report.To, "整源查询上界取全部证据与水位的最大值")

	// 用单点探测断言判据（比枚举段边界更精确，也不受切分点集合变化影响）。
	probe := func(pos uint64) EventStatusSegment {
		t.Helper()
		pointReport := DeriveEventStatus("node:reconcile/g1", facts, pos, pos)
		require.Len(t, pointReport.Segments, 1, "单点查询必须恰好一段: pos=%d", pos)
		return pointReport.Segments[0]
	}

	// 已认：诚实连续前缀（delivery_position=300）之内。
	// 注意 pos 从 1 起探测：位置 (0,0) 是「整源」查询的哨兵值（见 eventStatusBounds）。
	require.Equal(t, EventStateAcked, probe(1).State)
	require.Equal(t, "delivery_prefix", probe(1).Basis)
	require.Equal(t, EventStateAcked, probe(149).State)
	require.Equal(t, EventStateAcked, probe(300).State)

	// 已校验：逐字段校验区间 [150,250] 优先于前缀。
	verified := probe(150)
	require.Equal(t, EventStateVerified, verified.State)
	require.Equal(t, "verified_run", verified.Basis)
	require.Equal(t, EventStateVerified, probe(250).State)
	require.Equal(t, EventStateAcked, probe(251).State, "校验区间之外回到前缀判据")

	// 已发未认：UNKNOWN 批次 [300,400]。
	unacked := probe(301)
	require.Equal(t, EventStateSentUnacked, unacked.State)
	require.Equal(t, string(logtypes.DeliveryUnknown), unacked.BatchState)
	require.Equal(t, EventStateSentUnacked, probe(400).State)

	// 未发：REPLAY_REQUIRED 批次 [400,500]（尚无成功凭证）。
	unsent := probe(401)
	require.Equal(t, EventStateUnsent, unsent.State)
	require.Equal(t, string(logtypes.DeliveryReplayRequired), unsent.BatchState)
	require.Equal(t, EventStateUnsent, probe(500).State)

	// 已放弃：人工裁定区间 [501,600]，留痕随段带出。
	abandoned := probe(501)
	require.Equal(t, EventStateAbandoned, abandoned.State)
	require.Equal(t, "abandonment", abandoned.Basis)
	require.Equal(t, "ops", abandoned.AbandonmentOperator)
	require.Equal(t, ledger.GapReasonPermanentlyLost, abandoned.AbandonmentReason)
	require.Equal(t, EventStateAbandoned, probe(600).State)

	// 无证据 → 未发；落在缺口内 → 未发且带原因。
	require.Equal(t, EventStateUnsent, probe(601).State)
	require.Equal(t, "no_evidence", probe(601).Basis)
	gapSegment := probe(700)
	require.Equal(t, EventStateUnsent, gapSegment.State)
	require.Equal(t, "gap", gapSegment.Basis)
	require.Contains(t, gapSegment.Reason, "APPEND_REJECTED")

	// 汇总读数必须覆盖五种态（整段视图确实把每种态都派生出来了）。
	summaryStates := map[EventState]int{}
	for _, item := range report.Summary {
		summaryStates[item.State] = item.Segments
	}
	for _, state := range []EventState{
		EventStateUnsent, EventStateSentUnacked, EventStateAcked, EventStateVerified, EventStateAbandoned,
	} {
		require.Positive(t, summaryStates[state], "每种态都必须至少出现一次: %s", state)
	}
}

// TestDeriveEventStatusClippedInterval 固定「指定区间查询」的裁剪与越界安全。
func TestDeriveEventStatusClippedInterval(t *testing.T) {
	facts := EventStatusFacts{
		Positions:    logtypes.Positions{Read: 100, Durable: 100, Delivery: 50},
		VerifiedRuns: []ledger.PositionRange{{From: 10, To: 20}},
	}
	report := DeriveEventStatus("s/g", facts, 15, 60)
	require.Equal(t, uint64(15), report.From)
	require.Equal(t, uint64(60), report.To)
	require.Len(t, report.Segments, 3)
	require.Equal(t, EventStateVerified, report.Segments[0].State, "区间起点落在校验段内 → 该段从查询起点开始")
	require.Equal(t, uint64(15), report.Segments[0].From)
	require.Equal(t, uint64(20), report.Segments[0].To)
	require.Equal(t, EventStateAcked, report.Segments[1].State)
	require.Equal(t, EventStateUnsent, report.Segments[2].State)
}

// TestAbandonmentCredentialsSurviveRestart 是「放弃凭据跨重启存活」的回归（附带修复）。
//
// 为什么必须持久化：ADR-101 明确该凭据要「比缺口记录活得久」——已解决缺口会被裁剪，
// 凭据要一直支撑回收链放行判定与查询面的"永久缺失"标记；丢在重启里等于两者一并静默消失。
func TestAbandonmentCredentialsSurviveRestart(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	manager, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	key := ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration}
	pipe := manager.pipes[key.String()]
	require.NotNil(t, pipe)
	require.NoError(t, pipe.Ledger().RecordGap(key, 30, 40, "APPEND_REJECTED", "seed gap"))
	abandoned, err := pipe.Ledger().AbandonGapsThrough(key, 40, ledger.GapAbandonment{
		Operator: "ops", ReasonCode: ledger.GapReasonPermanentlyLost,
	})
	require.NoError(t, err)
	require.Equal(t, 1, abandoned)

	// 运行期视图：放弃段必须出现（内存凭据）。
	runtimeReport, err := manager.SourceEventStatus(EventStatusQuery{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, From: 30, To: 40,
	})
	require.NoError(t, err)
	require.Len(t, runtimeReport.Segments, 1)
	require.Equal(t, EventStateAbandoned, runtimeReport.Segments[0].State)
	require.Equal(t, "ops", runtimeReport.Segments[0].AbandonmentOperator)

	require.NoError(t, manager.Stop())

	// 重启：凭据必须原样回来（From/To/操作人/原因码）。
	reopened, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	defer func() { _ = reopened.Stop() }()
	ranges := reopened.AbandonedRanges(nil)
	require.Len(t, ranges, 1, "放弃凭据必须跨重启存活（否则查询面标记与回收放行依据一并消失）")
	require.Equal(t, uint64(30), ranges[0].From)
	require.Equal(t, uint64(40), ranges[0].To)
	require.Equal(t, "ops", ranges[0].Operator)
	require.Equal(t, ledger.GapReasonPermanentlyLost, ranges[0].ReasonCode)

	restartedReport, err := reopened.SourceEventStatus(EventStatusQuery{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, From: 30, To: 40,
	})
	require.NoError(t, err)
	require.Len(t, restartedReport.Segments, 1)
	require.Equal(t, EventStateAbandoned, restartedReport.Segments[0].State)
}

// TestExportEventStatusCLIEndToEnd 覆盖离线出口（索引库只读 + 事件段时间点定位）。
func TestExportEventStatusCLIEndToEnd(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	manager, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, manager.Stop())

	var out bytes.Buffer
	require.NoError(t, ExportEventStatus(root, []string{"--source=node:reconcile/g1"}, &out))
	var payload struct {
		Reports []EventStatusReport `json:"reports"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Len(t, payload.Reports, 1)
	report := payload.Reports[0]
	require.Equal(t, "node:reconcile/g1", report.Source)
	require.NotEmpty(t, report.Segments, "离线视图不得为空（正是本项验收要排除的形态）")
	states := map[EventState]bool{}
	for _, segment := range report.Segments {
		states[segment.State] = true
	}
	require.True(t, states[EventStateAcked] || states[EventStateVerified],
		"已落库区间必须能判为已认/已校验")

	// --at 时间点查询：真打开事件段定位（端到端）。
	out.Reset()
	require.NoError(t, ExportEventStatus(root, []string{
		"--source=node:reconcile/g1", "--at=2026-09-22T01:00:00Z",
	}, &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Len(t, payload.Reports, 1)
	require.NotEmpty(t, payload.Reports[0].LocatedEventID, "时间点查询必须定位到事件（不得静默为空）")

	// 未知源：必须报错（而不是静默给空结论）。
	require.Error(t, ExportEventStatus(root, []string{"--source=node:nope"}, &out))
}
