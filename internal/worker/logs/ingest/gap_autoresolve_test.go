package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// flakyVLFixture 给「投影写」加一个可切换的失败开关（读路径复用既有 fixture）。
//
// 为什么要真 HTTP 而不是打桩 deliver：缺陷 A 的现场是「VL 不可用期间投递持续失败」，
// 缺口由真实失败路径（writeProjectionDay → insertInBatches 非 2xx）产生；
// 只在 hook 层打桩会绕过缺口登记的真实条件。
type flakyVLFixture struct {
	inner      *projectionVLFixture
	failWrites atomic.Bool
}

func (f *flakyVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/insert/jsonline" && f.failWrites.Load() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.inner.ServeHTTP(w, r)
}

func (f *flakyVLFixture) writeCount() int {
	f.inner.mu.Lock()
	defer f.inner.mu.Unlock()
	return len(f.inner.writes)
}

func newFlakyVL(t *testing.T) (*vlsup.Client, *flakyVLFixture) {
	t.Helper()
	fixture := &flakyVLFixture{inner: &projectionVLFixture{}}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

func appendLogLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	_, err = f.WriteString(line)
	require.NoError(t, err)
}

// TestDeliveryFailureGapsAutoResolveAfterDeliveryRecovers 是缺陷 A 的核心转红闸门：
// 投递持续失败 → 缺口有界 → 投递恢复 → 缺口**自动清零**（不再依赖人工按源解算）。
//
// 现场（2026-10-01/02）：缺口 50,863–50,864 条/源；投递恢复后缺口仍在（没有任何自动消解路径），
// 而 ResumeAcquire 要求零未解决缺口 → 采集焊死 13+ 小时，最后靠人工解算 13 个源。
//
// 转红：改动前本用例在第 3 步断言（缺口清零）处必红——缺口只能靠 ResolveCoveredGaps/人工路径消解，
// 而两者都不会在投递恢复时被自动触发。
func TestDeliveryFailureGapsAutoResolveAfterDeliveryRecovers(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	// 两行起步：normalize 以「下一条事件头」闭合多行事件，单行只会停在缓冲里（不产生投递）。
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:gap/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gap", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 阶段一：VL 写持续失败。每次失败的批次区间相邻 → 缺口必须合并（有界）。
	fixture.failWrites.Store(true)
	m.pollOnce()
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"投递失败必须留下可见缺口（绝不静默丢弃）")
	for i := 0; i < 5; i++ {
		appendLogLine(t, logPath, fmt.Sprintf("[12:00:0%d] [Server thread/INFO]: retry batch\n", i+3))
		m.pollOnce()
	}
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry),
		"连续同因失败必须合并为一条覆盖区间（改动前这里是 6 条）")
	require.Greater(t, entry.ErrorCount, 1, "失败次数仍必须可观测")

	// 阶段二：VL 恢复。下一次成功投递（全量重放）即构成「已确认落库」证据。
	fixture.failWrites.Store(false)
	writesBefore := fixture.writeCount()
	appendLogLine(t, logPath, "[12:00:09] [Server thread/INFO]: recovered\n")
	m.pollOnce()
	require.Greater(t, fixture.writeCount(), writesBefore, "投递恢复后必须真正写入 VL")

	// 阶段三：断言缺口自动清零（且留有消解依据，不是被删除）。
	entry = pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.Zero(t, ledger.UnresolvedGapCountOf(entry),
		"投递恢复后缺口必须自动消解，不得再等人工")
	require.NotEmpty(t, entry.Gaps)
	require.True(t, entry.Gaps[0].Resolved)
	require.NotEmpty(t, entry.Gaps[0].Resolution, "消解依据必须可审计")
	// 就绪判据里的缺口阻塞必须随之消失（其它条目如投影覆盖不在本用例范围）。
	for _, reason := range m.CutoverReadiness().Reasons {
		require.NotContains(t, reason, ":unresolved_gaps")
	}
}

// TestPausedSourceSelfHealsEndToEnd 覆盖现场主路径：投递失败 + 积压越界 → 源暂停 →
// 投递恢复 → 存量外发 → 缺口自动消解 → 采集自动恢复（全程无人介入）。
//
// 现场（2026-10-01/02）：10 个源 paused=1、各带 5 万级未解决缺口、13+ 小时零新数据；
// 人工解算 13 个源后才恢复。暂停期间 FileTailer 不读新数据，若没有本用例覆盖的自愈链，
// 「投递 → 回收 → 恢复评估」在暂停源上没有任何触发点。
func TestPausedSourceSelfHealsEndToEnd(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:paused/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:paused", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 自愈默认按源限频 10 秒（避免每轮重投失败存量）；回归用真实路径驱动，
	// 因此把间隔压到「两次轮询之间必然过期」的量级，而不是 sleep 或改默认值。
	m.SetSelfHealInterval(time.Nanosecond)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 阶段一：投递失败 + 积压越界 → 暂停。
	fixture.failWrites.Store(true)
	pipe.WAL().SetLimits(1, 0)
	m.pollOnce()
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))
	appendLogLine(t, logPath, "[12:00:03] [Server thread/INFO]: third\n")
	m.pollOnce()
	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused, "积压越界后该源必须暂停（既有语义）")
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry), "相邻失败必须合并")

	// 阶段二：VL 恢复后，一次采集轮应当自愈（暂停期间 tailer 不读，只有自愈入口能推动）。
	fixture.failWrites.Store(false)
	m.pollOnce()
	entry = pipe.Ledger().Get(pipe.Key())
	require.Zero(t, ledger.UnresolvedGapCountOf(entry), "缺口必须自动消解")
	require.False(t, entry.AcquirePaused, "缺口清零 + 积压回落后采集必须自动恢复")
	require.Empty(t, entry.PauseReason)
}

// TestAppendRejectedGapInsideDeliveryHullIsNeverAutoResolved 是复审 P1-1 的核心转红闸门：
// 落在**凸包内部**、却从未进入 WAL 的批次（APPEND_REJECTED）绝不能被一次成功投递宣称
// 「delivery confirmed for this range」。
//
// 旧实现用 [min(Record.Start), max(Record.End)] 凸包当「已落库」证据，且只排除
// STDIO_RAW_WRITE_FAILED。于是「凸包内部的一切」都被当成已覆盖——包括 append 被拒、
// 从未进入 WAL 的批次区间，以及所有 (0,0) 形态的合成缺口（ARCHIVE_* / ROTATED_SEGMENT_NOT_READY）。
// replay=true 时 evidence 被扩为全量 canonical 集，凸包覆盖整段历史，风险最大。
//
// 转红：①判据退回凸包（或退回单区间 from/to 写法）→ 空洞中的缺口被误消解 → 必红；
// ②允许名单退回「只排除 STDIO_RAW_WRITE_FAILED」→ APPEND_REJECTED 被消解 → 必红。
func TestAppendRejectedGapInsideDeliveryHullIsNeverAutoResolved(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:hull/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:hull", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 阶段一：真实投递一轮，建立「已确认落库」证据（也可由此得到该源事件区间的凸包）。
	m.pollOnce()
	saved := m.state.Sources[key]
	evidence, err := m.canonicalRecoveryEvents(key, saved)
	require.NoError(t, err)
	require.NotEmpty(t, evidence, "夹具必须先有可作证据的权威事件集合")
	hullFrom, hullTo := evidence[0].Record.Start, evidence[0].Record.End
	for _, event := range evidence {
		if event.Record.Start < hullFrom {
			hullFrom = event.Record.Start
		}
		if event.Record.End > hullTo {
			hullTo = event.Record.End
		}
	}
	require.Greater(t, hullTo, hullFrom+20, "夹具需要足够宽的凸包以便把被拒批次放在内部")

	// 阶段二：用**真实拒绝路径**产生 APPEND_REJECTED 缺口——源处于暂停态时 append 被拒，
	// 该批从未进入 WAL（这正是现场形态：暂停窗口内的批次永远不会出现在 canonical 集里）。
	inside := ledger.PositionRange{From: hullFrom + 5, To: hullFrom + 15}
	require.Less(t, inside.To, hullTo, "被拒批次必须落在凸包内部（这正是旧实现过度放行的位置）")
	batch := []logtypes.Event{logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"},
		logtypes.RecordRange{Start: inside.From, End: inside.To},
		runtimeTestUTCDay()+"T12:00:30Z", runtimeTestUTCDay()+"T12:00:30Z", "INFO", "stdout", "never appended",
	)}
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "测试：模拟 append 被拒的暂停窗口"))
	rejecting := acquire.NewPipeline(pipe.Ledger(), pipe.Key(), pipe.WAL())
	require.Error(t, rejecting.Ingest(batch), "暂停态下 append 必须被拒（不得静默丢弃）")
	entry := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry))
	require.Equal(t, ledger.GapReasonAppendRejected, entry.Gaps[0].Reason)
	require.Equal(t, inside.From, entry.Gaps[0].StartPos)
	require.Equal(t, inside.To, entry.Gaps[0].EndPos)

	// 阶段三：同一源上再发生一次成功投递（存量重放，写入面 = 全量 canonical），
	// 其凸包完整覆盖 [hullFrom, hullTo] —— 但缺口必须原样保留。
	fixture := &flakyVLFixture{inner: &projectionVLFixture{}}
	_ = fixture
	m.resolveGapsLandedByDelivery(source, evidence)
	entry = pipe.Ledger().Get(pipe.Key())
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry),
		"落在凸包内部、却从未进入 WAL 的批次区间绝不能被自动消解（否则空洞被永久掩盖）")
	require.False(t, entry.Gaps[0].Resolved)

	// 正向对照：同一区间上、允许名单内的原因**必须**被消解——证明上面不是因为「什么都不消解」而通过。
	require.True(t, pipe.Ledger().GapObservability(pipe.Key()).Unresolved == 1)
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), inside.From, inside.To,
		ledger.GapReasonDeliverError, "transport down"))
	m.resolveGapsLandedByDelivery(source, evidence)
	entry = pipe.Ledger().Get(pipe.Key())
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry), "只应消解允许名单内的那一条")
	for _, gap := range entry.Gaps {
		if gap.Reason == ledger.GapReasonAppendRejected {
			require.False(t, gap.Resolved, "原因级允许名单：未登记的原因不得被自动消解")
		} else {
			require.True(t, gap.Resolved, "允许名单内、且被连续区间覆盖的缺口必须消解")
			require.Equal(t, gapResolutionDeliveryRetry, gap.Resolution)
		}
	}
}

// TestDeliveryEvidenceRequiresContiguousCoverage 守住「连续覆盖 ≠ 凸包」这一判据本身：
// 写入面里两段证据之间的空洞（该处字节从未落库）中的允许原因缺口也不得被消解。
//
// 模型：replay/整窗重发的 expected 集在位置空间上跨过一段从未进入 WAL 的批次，
// 该段的字节没有任何事件与之对应——凸包会把它算作已覆盖，连续覆盖不会。
//
// 转红：判据退回凸包（min..max）即红——空洞中的缺口会被误判为已覆盖。
func TestDeliveryEvidenceRequiresContiguousCoverage(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: one\n[12:00:02] [Server thread/INFO]: two\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:hole/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:hole", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 建立三条事件（两轮采集），使「挖掉中间一条」后证据仍有两侧。
	appendLogLine(t, logPath, "[12:00:03] [Server thread/INFO]: three\n")
	m.pollOnce()
	appendLogLine(t, logPath, "[12:00:04] [Server thread/INFO]: four\n")
	m.pollOnce()
	evidence, err := m.canonicalRecoveryEvents(key, m.state.Sources[key])
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(evidence), 3, "夹具需要至少三条事件才能构造内部空洞")

	// 构造带空洞的写入面：保留首尾，挖掉中间一条（其字节区间即「从未落库」的空洞）。
	hole := evidence[1].Record
	holed := []logtypes.Event{evidence[0], evidence[len(evidence)-1]}
	require.GreaterOrEqual(t, hole.Start, holed[0].Record.End, "空洞必须在首段证据之后")
	require.LessOrEqual(t, hole.End, holed[1].Record.Start, "空洞必须在末段证据之前")
	runs := ledger.MergePositionRanges([]ledger.PositionRange{
		{From: holed[0].Record.Start, To: holed[0].Record.End},
		{From: holed[1].Record.Start, To: holed[1].Record.End},
	})
	require.Len(t, runs, 2, "挖掉中间一条后写入面必须是两段互不相接的连续区间（即存在空洞）")
	require.False(t, ledger.CoveredByPositionRanges(runs, hole.Start, hole.End),
		"空洞区间不得被判为被连续覆盖（凸包判据会把它算进 min..max 里）")

	// 两条缺口都登记在同一个原因上，故必须彼此**不相邻**（相邻同因缺口会被合并成一条区间，
	// 那会把正向对照卷进空洞里，使断言失去判别力）：
	//   - 空洞缺口：中间那条事件的区间（两段证据之间，凸包会覆盖它）；
	//   - 对照缺口：首条证据区间内、离空洞起点至少 2 字节的一段（必被连续覆盖）。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), hole.Start, hole.End,
		ledger.GapReasonDeliverError, "batch in the hole"))
	covered := ledger.PositionRange{From: holed[0].Record.Start, To: hole.Start - 5}
	require.Less(t, covered.To, hole.Start-1, "对照缺口不得与空洞缺口相邻")
	require.Greater(t, covered.To, covered.From)
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), covered.From, covered.To,
		ledger.GapReasonDeliverError, "covered batch"))

	m.resolveGapsLandedByDelivery(source, holed)
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	byRange := map[uint64]ledger.Gap{}
	for _, gap := range entry.Gaps {
		byRange[gap.StartPos] = gap
	}
	require.False(t, byRange[hole.Start].Resolved,
		"缺口落在两段证据之间的空洞里：凸包会误判已覆盖，连续覆盖必须拒绝")
	require.True(t, byRange[covered.From].Resolved,
		"被某一段连续证据完全覆盖的缺口必须消解（正向对照）")
}

// TestPublishedProjectionGapResolutionNeedsVerifiedRuns 是复审 P1-2 的转红闸门：
// 「已发布投影」自动消解出口不得再用**末端水位**（缺口末端 ≤ closed）放行，必须有
// 「该区间至少被一次逐字段校验覆盖」这一区间级凭据，且原因在允许名单内。
//
// 旧实现：ResolveGapsThroughExcept(closed) —— closed 是已发布投影的封闭水位，与「被拒批次是否
// 真的落库」毫无关系；任何末端 ≤ closed 的缺口（含 APPEND_REJECTED、ARCHIVE_*、(0,0) 合成缺口）
// 都被标成「verified published projection」。
//
// 转红：判据退回末端水位即红（本用例三个子断言都会红）。
func TestPublishedProjectionGapResolutionNeedsVerifiedRuns(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:published/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:published", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	m.pollOnce()
	closed, complete := m.publishedClosedForSource(source, m.state.Sources[key])
	require.True(t, complete, "夹具必须先有已发布投影")
	require.Greater(t, closed, uint64(0))

	// 子断言一：原因不在允许名单内 → 即使末端 ≤ 已发布水位也不得消解。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, closed,
		ledger.GapReasonAppendRejected, "append rejected while paused"))
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"末端水位不是「该区间已落库」的证据：允许名单之外的原因不得被自动消解")
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))

	// 子断言二：允许名单内、且被逐字段校验区间覆盖 → 必须消解（正向对照）。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, closed,
		ledger.GapReasonDeliverError, "transport down"))
	require.True(t, m.autoResolveGapsFromPublished(source, pipe))
	entry := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry), "只应消解允许名单内的那一条")
	for _, gap := range entry.Gaps {
		if gap.Reason == ledger.GapReasonAppendRejected {
			require.False(t, gap.Resolved)
		} else {
			require.True(t, gap.Resolved)
			require.Equal(t, gapResolutionPublished, gap.Resolution)
		}
	}

	// 子断言三：缺少「逐字段校验覆盖」凭据（旧版本状态）时，即便水位成立也不得按末端消解；
	// 凭据存在但缺口落在两段凭据之间的空洞里时同样不得消解。
	setVerifiedRuns := func(runs []ledger.PositionRange) {
		m.mu.Lock()
		saved := m.state.Sources[key]
		saved.VerifiedRuns = runs
		m.state.Sources[key] = saved
		m.mu.Unlock()
	}
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, closed,
		ledger.GapReasonDeliverError, "again"))
	setVerifiedRuns(nil)
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"没有任何逐字段校验凭据时不得按末端水位消解（旧版本状态重启后正是这一形态）")
	require.Equal(t, 2, pipe.Ledger().UnresolvedGapCount(pipe.Key()), "两条未消解缺口必须原样保留")

	setVerifiedRuns([]ledger.PositionRange{{From: 0, To: 10}, {From: 20, To: closed}})
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"缺口跨越两段凭据之间的空洞时不得消解（连续覆盖判据）")
	require.Equal(t, 2, pipe.Ledger().UnresolvedGapCount(pipe.Key()))

	setVerifiedRuns([]ledger.PositionRange{{From: 0, To: closed}})
	require.True(t, m.autoResolveGapsFromPublished(source, pipe),
		"凭据连续覆盖且水位成立时必须消解（正向对照）")
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"只剩允许名单之外的那一条未解决")
}

// TestStdioRawWriteFailureGapIsNeverAutoResolved 守住语义红线：
// 「原始字节写入失败」无法由任何投影/重投证据证明已落库，自动路径必须放手，交人工确认。
//
// 转红：若自动消解不带原因排除，本用例会在断言处红（该缺口被错误地标记为已解决）。
func TestStdioRawWriteFailureGapIsNeverAutoResolved(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:raw/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:raw", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 正常投递一轮：建立「已发布投影」这一全局证据。
	m.pollOnce()
	require.True(t, m.CutoverReadiness().LedgerReady, "%v", m.CutoverReadiness().Reasons)

	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, 20,
		ledger.GapReasonStdioRawWriteFailed, "raw bytes may never reach disk"))
	// 全局证据成立（该源已在已发布投影内），但该原因必须被排除。
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"不可证明的原因不得被自动消解")
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))

	// 同一区间上的普通投递失败缺口则可以自动消解：证明排除是按原因而非按区间。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, 20, "DELIVER_ERROR", "transport down"))
	require.True(t, m.autoResolveGapsFromPublished(source, pipe))
	entry := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(entry))
	for _, gap := range entry.Gaps {
		if gap.Reason == ledger.GapReasonStdioRawWriteFailed {
			require.False(t, gap.Resolved)
		} else {
			require.True(t, gap.Resolved)
		}
	}
}

// TestSilentSourceExitIsMarkedAsNoCopy 是复审 P2-13 的转红闸门：静默源出口补出的恢复分段
// **没有任何物理副本**，因此不得把它包装成「已验证副本」。
//
// 旧实现：路径写 `manual://admin-confirmed`（看着像「管理员确认过一个已验证副本」），
// 释放依据写 `NEXT_COPY_VERIFIED`（字面就是「下一份副本已验证」）——事后审计分不清这段
// 到底有没有副本，而事实是：凭据只有「整段已投递并被逐字段校验」。
//
// 夹具形态（与现场一致：投递前缀已到读位置、但 durable 落后且尾部无分段可依）：
//  1. 正常投递一批 → 有已发布投影、回收推进到该批末端；
//  2. 直接向 WAL 追加下一批（不提交）→ read 前进、durable 落后（「durable 落后于 read」正是
//     静默源出口成立的前提条件之一）；
//  3. 按既有语义解算该批的投递（ResolveDeliveryThroughRecovery，与启动对账解算 UNKNOWN 同路径）
//     → delivery 追平 read，而回收责任证明仍未建立（无分段覆盖尾部）。
//
// 转红：把路径退回 `manual://admin-confirmed` 或把释放依据退回 `NEXT_COPY_VERIFIED` 即红。
func TestSilentSourceExitIsMarkedAsNoCopy(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:silent/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:silent", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// ① 正常投递一批：建立已发布投影，并让回收推进到该批末端。
	m.pollOnce()
	entry := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, logtypes.DeliveryRequestDone, entry.DeliveryState)
	tailStart := entry.Positions.Read + 1
	require.GreaterOrEqual(t, entry.Positions.Reclaim, entry.Positions.Read, "① 之后回收应已追上读位置")

	// ② 尾部批次：只进 WAL（不提交）——read 前进、durable 停在上一批末端，且没有任何分段覆盖它。
	tailEnd := entry.Positions.Read + 40
	tail := []logtypes.Event{logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"},
		logtypes.RecordRange{Start: tailStart, End: tailEnd},
		runtimeTestUTCDay()+"T12:00:30Z", runtimeTestUTCDay()+"T12:00:30Z", "INFO", "stdout", "静默前的最后一批",
	)}
	require.NoError(t, pipe.WAL().Append(tail...))
	require.NoError(t, pipe.ResolveUnknownThroughProjection(tail)) // 投递前缀追平读位置（无回收责任证明）
	entry = pipe.Ledger().Get(pipe.Key())
	require.GreaterOrEqual(t, entry.Positions.Delivery, entry.Positions.Read, "夹具：投递已追平读位置")
	require.Less(t, entry.Positions.Reclaim, entry.Positions.Read, "夹具：尾部无分段覆盖")
	require.LessOrEqual(t, entry.Positions.Reclaim, entry.Positions.Durable)

	// ③ 静默源出口：为「已全部投递、但无分段覆盖的尾部」补段并推进回收。
	require.NoError(t, m.ResolveCoveredGaps(context.Background()))
	entry = pipe.Ledger().Get(pipe.Key())
	require.Greater(t, entry.Positions.Reclaim, uint64(0))

	var manual *ledger.RecoveryRef
	for index := range entry.RecoveryRefs {
		if strings.HasPrefix(entry.RecoveryRefs[index].SegmentID, "manual-recovery-") {
			manual = &entry.RecoveryRefs[index]
			break
		}
	}
	require.NotNil(t, manual, "静默源出口必须补出一条恢复分段")
	require.Equal(t, silentExitNoCopyPath, manual.Path,
		"无副本段必须在路径上如实标注（不得写成看着像「已确认副本」的名字）")
	require.Contains(t, manual.Path, "no-copy")
	require.Equal(t, logtypes.ReleaseProjectionBacked, manual.ReleaseReason,
		"释放依据必须是 PROJECTION_BACKED：这段没有副本，凭据只有已发布投影 + 逐字段校验")
	require.NotEqual(t, logtypes.ReleaseNextCopyVerified, manual.ReleaseReason,
		"绝不能把无副本段登记成「下一份副本已验证」")
	require.Equal(t, silentExitNoCopyReceiver, manual.ResponsibilityReceiver)
	require.Contains(t, manual.ResponsibilityReceiver, "no-copy")
	require.Equal(t, logtypes.RecoveryCleaned, manual.State)
}
