package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:gap/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gap", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
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
	//
	// 2026-11-02 异步化：缺口消解已从「投递路径同步执行」改为「登记后由后台 worker 执行」
	// （见 gap_resolve_async.go）。因此这里不能再假设 pollOnce 返回即已消解——
	// 必须等后台 worker 真正跑完。**断言本身一条没放宽**：仍要求缺口必须清零、
	// 仍要求消解依据可审计；只是从"立即"改成"在合理时间内"。
	//
	// 同时必须**持续驱动采集轮**：简化投递路径后没有「整窗重放」，失败的那批只能靠
	// 自愈面 `selfHealPausedSource` 的 `DeliverPending` 重投产生证据。而该面按
	// `gapAutoResolveInterval`（10s/源）限频——生产上采集环会一直转，这里必须同样地
	// 持续转，否则测的是「没人调它时会不会自己变好」而不是被测语义。
	require.Eventually(t, func() bool {
		m.pollOnce()
		e := pipe.Ledger().Get(pipe.Key())
		return e != nil && ledger.UnresolvedGapCountOf(e) == 0
	}, 20*time.Second, 100*time.Millisecond,
		"投递恢复后缺口必须自动消解（自愈面重投 + 异步 worker 核对），不得再等人工")
	entry = pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.NotEmpty(t, entry.Gaps)
	require.True(t, entry.Gaps[0].Resolved)
	require.NotEmpty(t, entry.Gaps[0].Resolution, "消解依据必须可审计")
	// 就绪判据里的缺口阻塞必须随之消失（其它条目如投影覆盖不在本用例范围）。
	for _, reason := range m.CutoverReadiness().Reasons {
		require.NotContains(t, reason, ":unresolved_gaps")
	}
}

// TestPausedSourceSelfHealsEndToEnd 覆盖现场主路径：投递失败 + 源被暂停 → 投递恢复 →
// 存量外发 → 缺口自动消解 → 采集自动恢复（全程无人介入）。
//
// 现场（2026-10-01/02）：10 个源 paused=1、各带 5 万级未解决缺口、13+ 小时零新数据；
// 人工解算 13 个源后才恢复。暂停期间 FileTailer 不读新数据，若没有本用例覆盖的自愈链，
// 「投递 → 回收 → 恢复评估」在暂停源上没有任何触发点。
//
// 暂停成因（2026-10-29 起）：积压闸已改为「丢弃最旧」，本包**不再产生**积压暂停；
// 而本用例要守的是“已暂停的源靠投递恢复自愈”这条链，暂停从哪来无关紧要——
// 这里用旧二进制留下的**存量积压暂停**形态（legacyBacklogPauseReason），
// 它正是 maybeResumeBacklogLocked 唯一认的那一类，因而是本自愈链当前的真实入口。
// 容量/disk 类暂停不行：maybeResumePausedSources 的候选门只认 IsBacklogPauseReason。
func TestPausedSourceSelfHealsEndToEnd(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:paused/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:paused", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	// 自愈默认按源限频 10 秒（避免每轮重投失败存量）；回归用真实路径驱动，
	// 因此把间隔压到「两次轮询之间必然过期」的量级，而不是 sleep 或改默认值。
	m.SetSelfHealInterval(time.Nanosecond)
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 阶段一：投递失败 + 造出**存量积压暂停**（旧二进制写下的形态，生产库里仍在）。
	// 2026-10-29 起积压越界只丢最旧、不暂停（enforceBacklogLimitLocked），所以暂停必须显式造；
	// 手段是 maybeResumeBacklogLocked 唯一认的那一类原因，否则自愈链的入口就不存在。
	fixture.failWrites.Store(true)
	pipe.WAL().SetLimits(1, 0)
	m.pollOnce()
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))
	// 上限 1 下再进一批即越界 ⇒ 必须被裁（不是暂停）。
	appendLogLine(t, logPath, "[12:00:03] [Server thread/INFO]: third\n")
	m.pollOnce()
	if dropped, _ := pipe.WAL().DropStats(); dropped <= 0 {
		t.Fatalf("越界必须丢最旧（不背压），实测丢弃 %d 条", dropped)
	}
	require.False(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused,
		"越界不得暂停采集（2026-10-29 丢弃语义的红线）")
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), legacyBacklogPauseReason))
	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused, "前置：源必须处于存量积压暂停（自愈链的入口）")
	// 相邻投递失败必须合并为一条（有界的前提）。丢弃类缺口另计一条（它不属于可愈合类，
	// 也不会被任何自动消解路径抹掉）——所以这里按**原因**数，不数总数。
	require.Equal(t, 1, unresolvedGapsOfReason(entry, ledger.GapReasonDeliverError),
		"相邻投递失败必须合并为一条覆盖区间")

	// 阶段二：VL 恢复后，一次采集轮应当自愈（暂停期间 tailer 不读，只有自愈入口能推动）。
	fixture.failWrites.Store(false)
	m.pollOnce()
	entry = pipe.Ledger().Get(pipe.Key())
	require.Zero(t, unresolvedGapsOfReason(entry, ledger.GapReasonDeliverError),
		"投递恢复后可愈合的缺口必须自动消解")
	// 红线：丢弃类缺口**不得**被这次自愈销抹——那些字节真丢不回来，抹了就变成静默丢数据。
	// 但它已列为“不阻塞恢复”（ledger.gapReasonsNonBlocking）⇒ 不得因此把源钉在暂停上。
	require.Positive(t, unresolvedGapsOfReason(entry, ledger.GapReasonWALBacklogDropped),
		"丢弃类缺口必须仍然可见（不得被自动消解抹掉）")
	// 强度不降：剩下的**全部**未解决缺口必须就是那些丢弃类缺口（不得还留着别的原因）。
	require.Equal(t, unresolvedGapsOfReason(entry, ledger.GapReasonWALBacklogDropped),
		ledger.UnresolvedGapCountOf(entry),
		"除丢弃类外不得残留任何未解决缺口（否则“缺口必须自动消解”就没测到）")
	require.False(t, entry.AcquirePaused, "缺口不再阻塞 + 积压已裁后采集必须自动恢复")
	require.Empty(t, entry.PauseReason)
}

// unresolvedGapsOfReason 统计一份条目副本里指定原因的**未解决**缺口数。
//
// 为何不直接用 UnresolvedGapCountOf（总数）：2026-10-29 起丢弃会多出一条永久可见的
// WAL_BACKLOG_DROPPED 缺口，它与“相邻同因失败必须合并”这条不变量无关，混在一起数就分不清
// 是合并失效还是多了另一个原因。
func unresolvedGapsOfReason(entry *ledger.Entry, reason string) int {
	count := 0
	for _, g := range entry.Gaps {
		if !g.Resolved && g.Reason == reason {
			count++
		}
	}
	return count
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
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:hull/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:hull", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
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
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:hole/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:hole", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: one\n[12:00:02] [Server thread/INFO]: two\n"), 0o644))
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
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:published/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:published", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	m.pollOnce()
	// 2026-11-02 简化：投递路径不再发布投影，「已发布投影」不再是夹具前置。
	// 本用例验的是**自动消解判据本身**（原因名单 + 连续覆盖），证据面已改为 VL 可见性，
	// 因此只需该源确实把事件写进了 VL；缺口区间改成覆盖真正写进去的那些字节。
	delivered := durableEvents(t, m, key)
	require.NotEmpty(t, delivered, "夹具：必须真的有已驻留事件")
	closed := uint64(0)
	for _, ev := range delivered {
		if ev.Record.End > closed {
			closed = ev.Record.End
		}
	}
	require.Greater(t, closed, uint64(0))
	// 确认这些事件真的在 VL 可见（新证据面的前置）。
	require.Len(t, m.landedVisibleInVL(source, delivered), len(delivered),
		"夹具：已投递事件必须能在 VL 里查到")

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

	// 子断言三：证据面必须是**真的要查得到**的字节。
	//
	// 2026-11-02 简化后不再依赖持久化的 `verifiedRuns` 凭据（那是「发布确认」链上的产物），
	// 而是直接向 VL 核对。但「不凭空消解」这条红线一字未变，只是换了表述形式：
	//   · 缺口跨到**从未投递的字节**上（超出真正写入的区间）⇒ 不可能全覆盖 ⇒ 不得消解；
	//   · 缺口回到真正写入的区间 ⇒ 必须消解（正向对照）。
	// 两者合起来钉住「覆盖判据按真实字节算，不按水位凸包算」。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, closed+5000,
		ledger.GapReasonDeliverError, "again"))
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"缺口末端超出真正写入的字节时不得消解（那些字节根本没进过 VL）")
	require.Equal(t, 2, pipe.Ledger().UnresolvedGapCount(pipe.Key()), "两条未消解缺口必须原样保留")
}

// TestStdioRawWriteFailureGapIsNeverAutoResolved 守住语义红线：
// 「原始字节写入失败」无法由任何投影/重投证据证明已落库，自动路径必须放手，交人工确认。
//
// 转红：若自动消解不带原因排除，本用例会在断言处红（该缺口被错误地标记为已解决）。
func TestStdioRawWriteFailureGapIsNeverAutoResolved(t *testing.T) {
	client, fixture := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:raw/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:raw", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	pipe := m.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	require.NotNil(t, pipe)

	// 正常投递一轮：把这批事件真的写进 VL（新证据面的来源）。
	//
	// 2026-11-02 简化前这里是「建立已发布投影」；不再有代次/发布这层中间态，
	// 证据就是**事件在不在 VL 里**，所以只需正常投递即可。
	m.pollOnce()
	require.Greater(t, fixture.writeCount(), 0, "夹具：必须真的写过 VL")

	// 缺口区间必须真的覆盖已投递的事件（新证据面是「这批事件在不在 VL 里」）。
	// 旧用例随手写了 [0,20]，那是靠「已发布水位 ≤ closed」判消解的产物；
	// 现在候选集来自「落在缺口内的事件」，区间写小了就永远没有候选。
	require.NotEmpty(t, durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration))
	gapEnd := uint64(0)
	for _, ev := range durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration) {
		if ev.Record.End > gapEnd {
			gapEnd = ev.Record.End
		}
	}
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, gapEnd,
		ledger.GapReasonStdioRawWriteFailed, "raw bytes may never reach disk"))
	// 该区间确实已落库（VL 里查得到），但该原因必须被排除。
	require.False(t, m.autoResolveGapsFromPublished(source, pipe),
		"不可证明的原因不得被自动消解")
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()))

	// 同一区间上的普通投递失败缺口则可以自动消解：证明排除是按原因而非按区间。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, gapEnd, "DELIVER_ERROR", "transport down"))
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

// TestSilentSourceExitAdvancesReclaimOnDeliveryEvidence（2026-10-29 改写）：
// 静默源（不再有新批次）的回收必须能在**没有新输入**的情况下被推进。
//
// ## 旧用例为何失去对象
//
// 旧用例（TestSilentSourceExitIsMarkedAsNoCopy）验的是「静默源出口补出的恢复分段必须
// 如实标注为 no-copy（路径 manual://delivered-no-copy、依据 PROJECTION_BACKED）——
// 因为它没有任何物理副本，不得包装成「已验证副本」」。
// 补段链与 RecoveryRef 已随受管恢复分段机制删除 ⇒ 那个分段不复存在，
// 「标注得对不对」也就无从谈起（机制失去对象）。
//
// ## 新语义下的同一关切
//
// 真正不能丢的是「**静默源不会因为“没人再投递”而永久卡在回收推不动上**」。
// 新实现（见 resolveOneSourceGaps）在 `delivery >= read > reclaim` 时直接调一次 `TryReclaim`——
// 安全性由账本保证：`reclaim = min(REQUEST_DONE 覆盖末端, max(read,durable))`，
// 只有全部确认投递的字节才会被跨过。
//
// 本用例逐条钉住：
//   - ① 负控：只有 UNKNOWN 结论、没有真实 ACK 时，reclaim 必须**不动**（不得冒充已确认副本）；
//   - ② 真实 ACK + 逐字段校验后，`ResolveCoveredGaps` 必须把 reclaim 推进到尾部末端；
//   - ③ 且**绝不**伪造任何恢复分段（旧机制已删，不得以别的形式复活）。
func TestSilentSourceExitAdvancesReclaimOnDeliveryEvidence(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:silent/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:silent", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 登记完成（EOF = 0）后写入正式内容。
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o644))
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// ① 正常投递一批：建立已发布投影，并让回收推进到该批末端。
	m.pollOnce()
	entry := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, logtypes.DeliveryRequestDone, entry.DeliveryState)
	tailStart := entry.Positions.Read
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

	// ③ 仅有UNKNOWN恢复结论而无真实ACK/当前VL行时，不能冒充已确认副本。
	require.NoError(t, m.ResolveCoveredGaps(context.Background()))
	entry = pipe.Ledger().Get(pipe.Key())
	require.Equal(t, tailStart, entry.Positions.Reclaim, "缺证据负控必须保留尾部责任")
	// 补齐真实物理写、逐字段校验、发布与独立HTTP ACK，但不调用常规恢复释放口。
	_, err = m.deliver(source, tail, false)
	require.NoError(t, err)
	require.NoError(t, pipe.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: tailStart, EndPos: tailEnd, HTTPStatus: 200}))
	require.Len(t, m.landedVisibleInVL(source, tail), 1)
	require.NoError(t, m.ResolveCoveredGaps(context.Background()))
	entry = pipe.Ledger().Get(pipe.Key())
	require.Equal(t, tailEnd, entry.Positions.Reclaim)

	// ③ 静默源出口不得以任何形式伪造恢复分段（旧机制已删，也不得借别的手段复活）。
	require.NotContains(t, fmt.Sprint(entry), "manual://delivered-no-copy",
		"补段链已删：静默源出口不再建段，也不得留一个“看起来像段”的残留")
	// 对照：再来一次也必须幂等（已追平 ⇒ 不动、不报错）。
	require.NoError(t, m.ResolveCoveredGaps(context.Background()))
	require.Equal(t, tailEnd, pipe.Ledger().Get(pipe.Key()).Positions.Reclaim)
}
