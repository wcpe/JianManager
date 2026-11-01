package ingest

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
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
		Positions:         logtypes.Positions{Read: 1000, Durable: 800, Delivery: 300, Reclaim: 100},
		RequestDoneRanges: []ledger.PositionRange{{From: 0, To: 300}},
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

	// 已认必须来自真实请求完成区间，不能由请求结果前缀推出。
	// 注意 pos 从 1 起探测：位置 (0,0) 是「整源」查询的哨兵值（见 eventStatusBounds）。
	require.Equal(t, EventStateAcked, probe(1).State)
	require.Equal(t, "request_done_range", probe(1).Basis)
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
	require.Equal(t, EventStateUnknown, report.Segments[1].State, "只有Delivery水位但无独立凭据的旧历史必须未知")
	require.Equal(t, "no_evidence", report.Segments[1].Basis)
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
	// 2026-11-02：索引库已改 `:memory:`（FR-505）⇒ **任何**索引状态都不跨重启，
	// 放弃凭据也不例外。原断言「跨重启仍 Len==1」已无对象（新进程 state 是空的）。
	// 这不是“凭据丢了”的缺陷，而是「索引不落盘」这条架构取舍的直接后果
	// （与实例绑定、投影代次同一类）。故改为**正面钉住**这条取舍。
	require.Empty(t, ranges,
		"索引不落盘 ⇒ 放弃凭据不跨重启（FR-505 的明确取舍；它不是缺陷，但必须被正面钉住）")

	// 因此重启后的视图也不再有「已放弃」那一段（同一根因，一并钉住）。
	restartedReport, err := reopened.SourceEventStatus(EventStatusQuery{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, From: 30, To: 40,
	})
	require.NoError(t, err)
	for _, seg := range restartedReport.Segments {
		require.NotEqual(t, EventStateAbandoned, seg.State,
			"放弃凭据不跨重启 ⇒ 重启后不得凭空报出一个「已放弃」段")
	}
}

// TestExportEventStatusCLIEndToEnd —— **已改为「能力下线」声明（2026-11-02）**。
//
// ## 原测什么
//
// 起一个真 Manager 采集一轮 → Stop → `ExportEventStatus(root, ...)` 从**磁盘索引库**
// 只读派生事件级状态视图，断言五态判据、`--at` 时间点定位、未知源报错。
//
// ## 为什么不再成立（两个独立原因，两处都失效）
//
//  1. **索引库改 `:memory:`（FR-505，2026-10-30 机械盘不落盘裁定）**：
//     `eventStatusIndexPath(root)` 拼的是 `<root>/var/log/ingest.index.db`，
//     而 `:memory:` 下**磁盘上没有这个文件**，进程一停库就没了。
//     ⇒「**停机后**离线取证」这个场景的前提（索引在磁盘上）已经不存在。
//  2. **事件段根搬出数据根（2026-11-02 `28abb04d`）**：
//     `eventStatusEventsRoot(root)` 拼的 `<root>/var/log/events` 也不再是段根
//     （现在由 `eventstore.SegmentRootFor` 派生）⇒ `--at` 定位那条腿也断了。
//
// ## 现在测什么（语义变了，所以改名并改写断言）
//
// 钉住「能力已下线，但**必须明确报错**而不是静默返回空」：
// 静默返回空结果会让运维以为「没有待处理事件」——那是比报错更危险的行为
// （这个判据在 `ExportEventStatus` 的注释里已经写明是它要排除的形态）。
//
// 离线取证的**正确入口现在是 VL**（长期真源），不是索引库。
func TestExportEventStatusCLIReportsCapabilityRetired(t *testing.T) {
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

	// ① 必须先钉住「磁盘上确实没有索引库」——这是能力下线的物理依据，
	// 而不是“没读到所以报错”。
	require.NoFileExists(t, eventStatusIndexPath(root),
		"索引库改为 :memory: 后不得在数据根下落盘（这正是离线取证失效的原因）")

	var out bytes.Buffer
	err = ExportEventStatus(root, []string{"--source=node:reconcile/g1"}, &out)
	// ② 关键：必须**报错**，不得静默成功。
	require.Error(t, err,
		"离线取证能力已随 :memory: 下线——必须明确报错，绝不能静默返回空结论（那会被误读成“没有待处理事件”）")
	require.Contains(t, err.Error(), "索引库不可读",
		"错误必须直接指向“读不到索引库”，而不是一个含糊的失败（排障需要一眼看懂）")
	require.Empty(t, out.String(), "报错时不得同时吐出一份看起来正常的报告")

	// ③ `--at` 那条腿也独立失效（段根搬出数据根），断言它同样不会静默成功。
	out.Reset()
	err = ExportEventStatus(root, []string{
		"--source=node:reconcile/g1", "--at=2026-09-22T01:00:00Z",
	}, &out)
	require.Error(t, err, "时间点查询不得静默返回空定位结果")

	// ④ 参数解析与未知源分类仍然独立于持久介质（这部分能力未变，继续钉着）。
	require.Error(t, ExportEventStatus(root, []string{"--source=node:nope"}, &out),
		"未知源必须报错")
}

// TestExportEventStatusCLIUsesPersistedRequestReceipts 给旧位置夹具补真实账本回执的持久化正控。
func TestExportEventStatusCLIUsesPersistedRequestReceipts(t *testing.T) {
	root := t.TempDir()
	key := ledger.SourceKey{LogSourceID: "node:receipt", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	require.NoError(t, led.RecordDelivery(key, 0, 400, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 400, 800, logtypes.DeliveryUnknown))
	entry := led.Get(key)
	entry.Positions.Read, entry.Positions.Durable = 1000, 1000
	manager := &Manager{root: root}
	rows, err := manager.stateToIndexState(&persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{*entry}}}})
	require.NoError(t, err)
	store, err := stateindex.Open(manager.indexPath())
	require.NoError(t, err)
	_, err = store.Apply(rows)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	for round := 1; round <= 3; round++ {
		var out bytes.Buffer
		require.NoError(t, ExportEventStatus(root, []string{"--source=" + key.String()}, &out))
		var payload struct {
			Reports []EventStatusReport `json:"reports"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
		require.Len(t, payload.Reports, 1)
		states := map[EventState]bool{}
		for _, segment := range payload.Reports[0].Segments {
			states[segment.State] = true
		}
		require.True(t, states[EventStateAcked], "第%d次实开：真实请求成功凭据必须可见", round)
		require.True(t, states[EventStateSentUnacked], "第%d次实开：UNKNOWN不得因Delivery越过而变成成功", round)
		require.False(t, states[EventStateVerified], "没有逐字段校验证据不能升级状态")
	}
}

// TestParseEventStatusArgsAcceptsSpaceSeparatedAndRejectsUnknown 锁定参数解析形态：
// 用法注释写的是空格分隔，旧实现只认等号 ⇒ 照注释执行的运维拿到「全量」的错结论；
// 未知参数/缺值必须报错（只读排障面宁可失败也不给错范围）。
func TestParseEventStatusArgsAcceptsSpaceSeparatedAndRejectsUnknown(t *testing.T) {
	// 等号与空格两种形态必须等价。
	eq, err := parseEventStatusArgs([]string{"--source=inst:1/stdout", "--from=100", "--to=200"})
	require.NoError(t, err)
	sp, err := parseEventStatusArgs([]string{"--source", "inst:1/stdout", "--from", "100", "--to", "200"})
	require.NoError(t, err)
	require.Equal(t, eq.source, sp.source, "两种分隔形态的 source 必须一致")
	require.NotNil(t, sp.from, "空格形态的 --from 必须被解析")
	require.NotNil(t, sp.to, "空格形态的 --to 必须被解析")
	require.Equal(t, uint64(100), *sp.from)
	require.Equal(t, uint64(200), *sp.to)

	// 显式 --from=0 与未传必须可区分（整源语义见 INGEST-6）。
	zeroOnly, err := parseEventStatusArgs([]string{"--from=0"})
	require.NoError(t, err)
	require.NotNil(t, zeroOnly.from, "显式 --from=0 必须可表达（不得与「未传」混淆）")
	require.Equal(t, uint64(0), *zeroOnly.from)
	require.Nil(t, parseEventStatusArgsEmptyFrom(), "占位断言：未传时 from 保持 nil")

	// --at 的 RFC3339 校验在两种形态下都生效。
	_, err = parseEventStatusArgs([]string{"--at", "2026-10-08T00:00:00Z"})
	require.NoError(t, err)
	_, err = parseEventStatusArgs([]string{"--at=not-a-time"})
	require.Error(t, err, "非法 --at 必须报错")

	// 未知参数不得静默忽略。
	_, err = parseEventStatusArgs([]string{"--sorce", "inst:1"})
	require.Error(t, err, "拼错的参数名必须报错（静默忽略会让运维以为筛选生效）")
	_, err = parseEventStatusArgs([]string{"--verbose"})
	require.Error(t, err, "未实现的开关必须报错")

	// 缺值（空格形态下吃掉末尾）必须报错。
	_, err = parseEventStatusArgs([]string{"--source"})
	require.Error(t, err, "--source 缺值必须报错")
	_, err = parseEventStatusArgs([]string{"--from"})
	require.Error(t, err, "--from 缺值必须报错")
}

// parseEventStatusArgsEmptyFrom 是「未传任何参数」的读取途径：from 必须保持 nil。
func parseEventStatusArgsEmptyFrom() *uint64 {
	got, err := parseEventStatusArgs(nil)
	if err != nil {
		panic(err)
	}
	return got.from
}
