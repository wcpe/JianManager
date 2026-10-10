package ingest

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是 2026-10-02 生产事故（启动路径"单线程、与规模成正比的持久化" ⇒ worker 20–30 分钟
// 不可达）的 ② 回归：**启动恢复不得阻塞 New**（生产语义 = StartupRecoveryBackground）、
// **落库分步可续且不丢行**、以及 ①水位化的**接线**（回收水位必须真的下发给索引层）。

// waitContext 返回带超时的 ctx（用例等待后台恢复用）。
func waitContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}

// TestStartupRecoveryDoesNotBlockNew：对账挂起时，New 必须立刻返回（恢复在后台），
// 就绪面在恢复未完成期间报 not-ready，恢复结束后自动转就绪。
//
// 现场形态（SIGQUIT 141 goroutine 取证）：`ingest.New` → applyStartupRecovery → releaseRecovery
// → persist → ApplyScoped → applyTableDeletes 单线程长跑，New 不返回 ⇒ main 起不到反向隧道 ⇒
// worker 全程不监听。本用例把「恢复慢」构造成确定性的（对账查询挂起），断言启动不再等它。
//
// 转红方式（实测）：让 startStartupRecovery 忽略 background 标志（退回改动前的同步全量）——
// New 会等满挂起的对账超时，本用例在「New 必须立刻返回」处变红。
func TestStartupRecoveryDoesNotBlockNew(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	// 对账查询挂起到客户端超时：给「恢复进行中」一个确定的时间窗（QueryTimeout = 3s）。
	fixture.setStallStats(true)

	started := time.Now()
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
		Reconcile:                 &ReconcileConfig{Enabled: true, Concurrency: 1, Timeout: 30 * time.Second, QueryTimeout: 3 * time.Second},
		StartupRecoveryBackground: true,
	})
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.Less(t, elapsed, 2*time.Second,
		"启动恢复在后台 ⇒ New 必须立刻返回（旧形态会等满整段恢复：对账挂起 3s + 重发）")

	status := m.StartupRecoveryStatus()
	require.True(t, status.InProgress, "此刻恢复仍在进行（New 没等它）")
	require.Positive(t, status.Total)
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady, "恢复未完成时必须报 not-ready")
	require.Contains(t, readiness.Reasons, "startup_recovery_in_progress",
		"就绪原因必须点名「启动恢复未完成」，不能只给一个笼统的 not-ready")

	// 恢复自行完成（对账超时 → 回退整窗重发 → 登记给重建 worker），就绪面随之转就绪。
	//
	// **两阶段等待（2026-10-10 第二项修复）**：恢复的契约由一段变两段——
	//   · `WaitStartupRecovery` 覆盖「判定 + 登记」（秒级，本驱动）；
	//   · 重发本身（分钟级）跑在重建 worker 上，生产由 `manager.Start` 起的
	//     `RunRebuildWorker` 承担。
	// 本用例只调 `New`、不调 `Start`（见 newTestManager），故重发会一直停在队列里——
	// 这不是缺陷，而是「登记 ≠ 执行」的直接体现；测试用确定性的 `drainRebuilds`
	// 在**同一套执行体**上把它跑完（不是另写一份实现）。
	// 缺了这一步就断言 LedgerReady 会让用例变成「谁先跑完」的竞态；而**不断言**就绪转真
	// 又是覆盖度下降。故这里把等待点写全——断言本身（"恢复彻底落定后可切换"）强度不变。
	require.NoError(t, m.WaitStartupRecovery(waitContext(t, 30*time.Second)))
	require.Equal(t, 1, m.drainRebuilds(waitContext(t, 30*time.Second)), "把已登记的重发跑完")
	require.NoError(t, m.WaitStartupReplayIdle(waitContext(t, 30*time.Second)))
	require.False(t, m.StartupRecoveryStatus().InProgress)
	require.Empty(t, m.StartupRecoveryStatus().ReplaysInFlight,
		"重发已出结论 ⇒ 未结论集合必须清空（否则就绪面会被永久阻塞）")
	require.True(t, m.CutoverReadiness().LedgerReady, "恢复完成后应回到可用状态")
}

// TestStartupRecoveryReplayIsRegisteredNotInlined：**启动恢复不得在自己的驱动 goroutine 上
// 跑完整窗重发**（2026-10-10 生产事故的直接对策，FR-497 资源侧）。
//
// 现场形态（只读统计 `worker.out`）：9 分钟内写出 2077 万事件、最大单次 573562 条，
// 5 个启动窗口共 4×1526 + 1×3359 条重复对账日志（同一 `(源, 缺失天)` 组合反复出现）。
// 根因是「判定」与「重发」被内联在同一驱动里：一旦某源出现确定性失败（写完 VL 却校验
// 不过），它会被下一趟、下一次重启反复整套重写，而该路径**没有任何上限**。
//
// 本用例断言的是**结构**（不依赖时长、可确定性复现）：
//
//	对账判定出「需要重发」之后，驱动内**不得**出现任何 VL 写入；重发必须已经登记到
//	调度器（`RebuildStats.Scheduled >= 1`），且该源此刻仍在「已登记未结论」集合里。
//
// 转红方式（变异 M-S1，见报告）：把 `startStartupRecovery(background)` 里的
// `asyncReplay` 传 false（退回内联重发）——重发会在 `WaitStartupRecovery` 期间跑完并写入
// VL，本用例在「驱动内不得写入」与「该源必须仍在未结论集合里」两处同时变红。
func TestStartupRecoveryReplayIsRegisteredNotInlined(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	// 造一个「必须重发」的确定缺口：删掉一整天的数据（对账会检出缺失）。
	fixture.deleteDay("2026-09-22")
	insertsBefore := fixture.insertCount()

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
		StartupRecoveryBackground: true,
		// 钉死重建 worker：本用例要观察的是「登记」而不受重发时序影响。
		// （测试不调 Start ⇒ worker 不会自己跑；pending 因此保持到用例结束。）
	})
	require.NoError(t, err)

	// 驱动只做判定 + 登记：本等待点在秒级返回（重发不在它身上）。
	require.NoError(t, m.WaitStartupRecovery(waitContext(t, 30*time.Second)))
	require.Zero(t, fixture.insertCount()-insertsBefore,
		"启动恢复驱动内不得写入 VL（重发必须已移出该 goroutine）")

	stats := m.RebuildStats()
	require.GreaterOrEqual(t, stats.Scheduled, int64(1), "重发必须已登记到重建调度器（否则就是被静默丢弃）")

	status := m.StartupRecoveryStatus()
	require.GreaterOrEqual(t, status.ReplaysScheduled, 1, "登记必须可观测（ReplaysScheduled）")
	require.Zero(t, status.ReplaysSkipped, "本用例的源应登记成功、无跳过")
	require.Contains(t, status.ReplaysInFlight, source.LogSourceID+"/"+source.SourceGeneration,
		"重发尚未出结论 ⇒ 必须在未结论集合里（就绪面据此不宣告完成）")

	// 就绪面此刻**不得**宣告可切换：重发还在路上，缺口可能仍未补上。
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady, "重发未出结论时不得宣告就绪")
	require.Contains(t, readiness.Reasons, "startup_replay_in_flight",
		"就绪原因必须点名「重发仍在路上」，不能只给一个笼统的 not-ready")

	// 显式排空（测试用确定性执行体）后，未结论集合清空、缺口补上、就绪面转真。
	require.Equal(t, 1, m.drainRebuilds(waitContext(t, 30*time.Second)))
	require.NotZero(t, fixture.insertCount()-insertsBefore, "重发必须真的写入了缺失天")
	require.Empty(t, m.StartupRecoveryStatus().ReplaysInFlight)
	require.True(t, m.CutoverReadiness().LedgerReady, "缺口补齐后应回到可用状态")
}

// TestReconcileReportLoggingIsLinearNotQuadratic 固定**O(n²) 日志放大**这一现场放大器。
//
// 现场形态（只读统计 `worker.out`，见 `.tmp/logs-perf-20261009/startup-replay/timeline-148.txt`）：
// 启动恢复按批对账（`startupRecoveryBatch = 4`），而每批都把**累计**报告集交给同一个
// "既记读数又打日志"的方法 ⇒ 第 k 批重打前 k-1 批 ⇒ 总日志量 = O(批数²)。
// 5 个启动窗口打出 4×1526 + 1×3359 条、同一 `(源, 缺失天)` 组合反复出现，
// 直接后果是**第一次是谁触发的无法判读**（排障时被淹没）。
//
// **夹具必须跨多批**（否则被测不到）：`startupRecoveryBatch = 4`，故这里造 10 个源
// ⇒ 3 批。累计重打会打出 4 + 8 + 10 = 22 行（O(批数²) 的形态），修法后恰好 10 行。
//
// 本用例钉住修法后的**结构性**事实（不依赖时长、不采集日志文本以外的东西）：
//   - 读数面（`LastReconcileReports`）仍是**累计**集（语义不变：排障中途能看到跑了哪些源）；
//   - 日志面只输出**本批新增**（每份报告恰好一行 ⇒ 总量 = 报告数）。
//
// 转红方式（变异 M-S6，见报告）：把 `noteReconcileReports(reports)` 改回传 `allReports`
// （即恢复改动前的"累计集既记又打"）——本用例在「日志行数必须等于报告数」处变红。
func TestReconcileReportLoggingIsLinearNotQuadratic(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	// 10 个源（跨 3 批：4 + 4 + 2，`startupRecoveryBatch = 4`）。
	//
	// 关键：**每个源都必须有权威事件**——空源的报告不会进 `reconcileStartup` 的输出
	//（`canonicalRecoveryEvents` 返回空即 continue），那样批数虽然够，报告数却不够，
	// 用例就测不到"累计重打"。故这里用**同一个事件模板 + 每源独立的 identity/存储命名空间**。
	const sourceCount = 10
	sources := make([]SourceConfig, 0, sourceCount)
	eventsBySource := make(map[string][]logtypes.Event, sourceCount)
	for i := 0; i < sourceCount; i++ {
		id := fmt.Sprintf("node:many%d", i)
		logPath := filepath.Join(root, fmt.Sprintf("src-%d.log", i))
		require.NoError(t, os.WriteFile(logPath, nil, 0o600))
		sources = append(sources, SourceConfig{
			LogSourceID: id, SourceGeneration: "g1",
			Path: logPath, Mode: pipeline.ModeFilePrimary,
			StorageNamespace: id, UTCDay: runtimeTestUTCDay(),
		})
		identity := logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: "g1", ParserVersion: "v1"}
		eventsBySource[id] = []logtypes.Event{
			logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 10}, "2026-09-22T01:00:00Z", "2026-09-22T01:00:01Z", "INFO", "stdout", "alpha"),
			logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20}, "2026-09-23T01:00:00Z", "2026-09-23T01:00:03Z", "INFO", "stdout", "beta"),
		}
	}

	// seed 全部 10 个源（各自写投影 + 落段 + 发布 catalog），再在**干净重启**上跑恢复。
	// 用 seedProjection 同一形态但一次建好全部源：避免 10 次 New/Stop（每次都会跑一遍恢复）。
	seed, err := New(Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: sources,
		VerificationTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	for _, source := range sources {
		_, err = seed.writeProjection(source, eventsBySource[source.LogSourceID], "projection-1",
			eventsBySource[source.LogSourceID], nil, true)
		require.NoError(t, err)
	}
	require.NoError(t, seed.Stop())
	// 每个源都造出确定缺失 ⇒ 每个源恰好一份"发现缺失"报告。
	for _, source := range sources {
		fixture.deleteDay("2026-09-22")
		_ = source
	}

	var logs bytes.Buffer
	restore := captureSlogOutput(&logs)
	defer restore()

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: sources,
		StartupRecoveryBackground: true,
	})
	require.NoError(t, err)
	require.NoError(t, m.WaitStartupRecovery(waitContext(t, 60*time.Second)))

	// ① 读数面仍是累计集（修法没有把"增量可见"一起改掉）。
	reports := m.LastReconcileReports()
	require.Len(t, reports, sourceCount, "读数面必须仍持有全部源的结论（累计语义不变）")
	missing := 0
	for _, report := range reports {
		if len(report.MissingDays) > 0 {
			missing++
		}
	}
	require.Equal(t, sourceCount, missing,
		"10 个源都必须被判出缺失（否则本用例测的不是「每批都有报告」的形态）")

	// ② 日志面只输出本批新增：每份报告恰好一行"启动增量对账…"。
	lines := 0
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "启动增量对账") {
			lines++
		}
	}
	require.Equal(t, sourceCount, lines,
		"对账日志行数必须等于报告数（每份一行）；累计集重打会按批数平方放大（本夹具下为 4+8+10=22 行）")
	// 线性 vs 平方的判据：累计重打在 3 批下会打出 22 行 ≠ 10 行；这里再显式钉一次上界。
	require.Less(t, lines, sourceCount+startupRecoveryBatch,
		"日志行数必须贴住报告数、不得随批数增长——超出即 O(n²) 放大复发")
}

// TestNoteReconcileReportsEmitsPerBatchOnly 是上一个用例的**纯单元补充**：直接断言
// `noteReconcileReports` 的行为是"给多少打多少"（不累积、不去重），
// 从而把"日志量 = 传入条数"这一契约钉在最小粒度上。
//
// 为什么还要这一条：集成用例依赖"恢复驱动恰好分批"这一事实；若将来批次大小改变，
// 上一条用例会跟着漂移。本单元用例与批次大小无关（它测的是方法的**契约**本身）。
func TestNoteReconcileReportsEmitsPerBatchOnly(t *testing.T) {
	var logs bytes.Buffer
	restore := captureSlogOutput(&logs)
	defer restore()

	m := &Manager{}
	m.noteReconcileReports([]ReconcileReport{
		{Source: "a/g1", Fallback: true, FallbackReason: "count_query_failed"},
		{Source: "b/g1", MissingDays: []string{"2026-09-22"}},
		{Source: "c/g1"},
	})
	got := strings.Count(logs.String(), "启动增量对账")
	require.Equal(t, 3, got, "三份报告必须恰好三行（不多打、不合并、不静默）")
	// 三支各一：不信任（WARN）/ 发现缺失（Info）/ 一致（Info）。
	require.Contains(t, logs.String(), "回退整窗重发")
	require.Contains(t, logs.String(), "发现缺失，仅重发缺失天")
	require.Contains(t, logs.String(), "一致，跳过重发")
}

// captureSlogOutput 把默认 slog 输出重定向到 buf（返回还原函数）。
//
// 为什么要重定向而不是用 hook：本用例要测的正是**日志行数**这一可观测量，
// 而 slog 无内置的"按前缀计数"钩子；重定向是唯一能直接数到行的做法。
func captureSlogOutput(buf *bytes.Buffer) func() {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}
// 「同一来源、同一天」不得被无界重复重发。
//
// 现场形态（`.tmp/logs-perf-20261009/startup-replay/timeline-148.txt`）：inst:148/file 的
// `2026-10-05` 被反复判定缺失、反复整窗重发，5 个启动窗口内同一 `(源, 缺失天)` 组合重复出现
// 407 次；每次重发都写完一整代次却仍"查不到"，因为校验查询用的代次白名单与写入面不一致。
// 内联路径下**没有任何上限**——重启一次就重来一轮。
//
// 本用例断言「有界 + 可见」两件事（用**确定性**手法，不依赖时长）：
//
//	① 同源同天的第 N 次登记在确定性失败达上限后被拒（`rebuild_abandoned`），不再产生新写入；
//	② 拒绝不是静默：`RebuildAbandonedReason` 可读、`Abandoned`/`AbandonedSkips` 有读数、
//	   且启动恢复的**登记入口**把它记成失败（就绪面因此保持 not-ready，而不是假装完成）。
//
// 转红方式（变异 M-S2，见报告）：去掉 `scheduleStartupRebuildTask` 里对
// `scheduleStartup` 返回值的判断（登记失败也当成成功）——本用例在「源必须未恢复」处变红；
// 或把 `rebuildFailureDeterministic` 归类去掉（按外部条件永不放弃）——在「上限后被拒」处变红。
func TestStartupReplayBoundedForSameSourceDay(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	// **生产形态**：写入自称成功（200），数据永远不可见 ⇒ 校验恒失败 ⇒ 换新代次再写一遍。
	// 这正是 inst:220/file 的 573562 条 / inst:148 的 407 轮循环形态。
	// 注意注入时机在下方 New 之后（见那里的说明）。
	key := source.LogSourceID + "/" + source.SourceGeneration

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
		// 上限压到 2：让"有界"在用例里可数（生产默认 4，同类语义）。
		RebuildTuning: &RebuildTuning{MaxAttempts: 2, Throttle: time.Millisecond},
	})
	require.NoError(t, err)
	// **故障在构造之后注入**：本用例测的是「反复启动恢复登记」的有界性，而不是 New 的失败面。
	// （同步模式下 New 会内联跑一次恢复并让确定性失败变成 New 的错误——那是另一条既有回归的
	// 领域；这里先用完好的数据把 Manager 建起来，再制造循环形态。）
	fixture.silentDropInsert = true
	fixture.deleteDay("2026-09-22")

	// 反复走**启动恢复的登记入口**（复刻"重启一次又来一轮"的循环）。
	rejected := 0
	writes := make([]int, 0, 3)
	backoffChecked := false
	for round := 1; round <= 6; round++ {
		ok, skip := m.scheduleStartupRebuildTask(source, []string{"2026-09-22"}, startupReplayReasonMissing)
		if !ok {
			rejected++
			require.Equal(t, "rebuild_abandoned", skip,
				"第 %d 轮必须被「已放弃」拒绝（这才是有界）；实际原因 %s", round, skip)
			continue
		}
		require.Equal(t, 1, m.drainRebuilds(context.Background()), "第 %d 轮登记必须被执行", round)
		out, hasOut := m.RebuildLastResult(key)
		require.True(t, hasOut)
		require.Error(t, out.Err, "第 %d 轮的失败必须可取证", round)
		writes = append(writes, fixture.insertAttemptCount())
		// **退避拒绝路径**（`scheduleLocked` 返回 false，而不是 `scheduleStartup` 的提前返回）：
		// 这是最容易漏掉的一条——它必须同样「不记 pending」，否则该键会永远等不到结论。
		// 只查一次（第一次失败后必然在退避窗口内），避免把用例变成对计时器的测量。
		if !backoffChecked {
			backoffChecked = true
			okBackoff, skipBackoff := m.scheduleStartupRebuildTask(source, []string{"2026-09-22"}, startupReplayReasonMissing)
			require.False(t, okBackoff)
			require.Equal(t, "rebuild_backoff", skipBackoff, "退避窗口内必须被可解释地拒绝")
			require.True(t, m.rebuildsPendingLeakFree(),
				"退避拒绝不得在未结论台账里留下永远不会出结论的键（否则就绪面永久 not-ready）")
		}
		// 清退避窗口：本用例测的是**上限**，不是计时。
		m.rebuilds.mu.Lock()
		if st := m.rebuilds.attempts[key]; st != nil {
			st.nextAllowedAt = time.Time{}
		}
		m.rebuilds.mu.Unlock()
	}
	require.GreaterOrEqual(t, rejected, 4,
		"上限 2 之后的所有轮次都必须被拒（6 轮里至少 4 轮）——否则就是生产那个无界重写循环")
	require.LessOrEqual(t, len(writes), 2, "写入次数必须受上限约束（这正是 VL 体积的止血点）")
	// 前两轮确实各写了一遍（确认夹具造出了"写完却校验不过"的循环形态，而不是空流程）。
	for i := 1; i < len(writes); i++ {
		require.Greater(t, writes[i], writes[i-1], "每轮失败前都确实又写了一遍")
	}

	// **有界必须可见**：放弃原因可读（含已尝试代次），且读数留痕。
	reason, abandoned := m.RebuildAbandonedReason(key)
	require.True(t, abandoned)
	require.Contains(t, reason, "projection-", "放弃原因必须点名已尝试的代次：%s", reason)
	require.Positive(t, m.RebuildStats().Abandoned, "放弃次数必须进读数")
	require.Positive(t, m.RebuildStats().AbandonedSkips, "被拒的登记也必须进读数")

	// **不静默丢数据**：放弃与拒绝绝不能被当成「已恢复」。
	// 走一次完整的启动恢复登记，确认它把"已放弃"记成失败（而不是跳过或成功）。
	m.mu.Lock()
	m.startupRecovery = startupRecoveryState{}
	m.mu.Unlock()
	summary := m.scheduleStartupReplays(
		[]startupSource{{source: source, key: key, events: reconcileTestEvents()}},
		[]ReconcileReport{{Fallback: false, MissingDays: []string{"2026-09-22"}}},
	)
	require.Zero(t, summary.Scheduled, "已放弃的源不得被登记成「已在重发」")
	require.Equal(t, 1, summary.Skipped)
	require.Contains(t, summary.SkipReasons[0], "rebuild_abandoned")
	status := m.StartupRecoveryStatus()
	require.Positive(t, status.Failed, "放弃必须记入启动恢复失败（就绪面据此保持 not-ready）")
	require.Contains(t, strings.Join(status.Reasons, " "), "已放弃自动重试",
		"失败原因必须点名「已放弃」，而不是一句笼统的失败：%v", status.Reasons)
	require.False(t, m.CutoverReadiness().LedgerReady,
		"源未真正恢复 ⇒ 不得宣告就绪（缺口由数据面照实呈现）")

	// 决定性一步：放弃之后**不再有任何新写入**。
	insertsBefore := fixture.insertCount()
	ok, skip := m.scheduleStartupRebuildTask(source, []string{"2026-09-22"}, startupReplayReasonMissing)
	require.False(t, ok)
	require.Equal(t, "rebuild_abandoned", skip)
	require.Zero(t, m.drainRebuilds(context.Background()), "已放弃后不得再有任务可执行")
	require.Equal(t, insertsBefore, fixture.insertCount(), "放弃后不得再有新写入")

	// **未被登记的源绝不能进入「未结论」台账**（否则就绪面会被永久阻塞）。
	//
	// 为什么必须单独断言：`startupPending` 是「登记成功」与「就绪可判」的耦合点。若在登记被拒时
	// 也把它记进去（例如"先记 pending 再排队"的写法），该源就永远等不到一个不会到来的结论 ⇒
	// `startup_replay_in_flight` 永久置位 ⇒ 平台永远不可切换。这是比"重发没做"更严重的静默故障。
	// 转红方式（变异 M-S5，见报告）：把 `scheduleStartup` 里的 pending 登记提到
	// `scheduleLocked` 之前（登记失败也记 pending）——本断言即刻变红。
	require.Empty(t, m.StartupRecoveryStatus().ReplaysInFlight,
		"被拒的登记不得进入未结论台账（否则就绪面永久 not-ready）")
	require.True(t, m.rebuildsPendingLeakFree(), "未结论台账不得残留任何永远不会出结论的键")
}

// rebuildsPendingLeakFree 报告「未结论台账里没有任何既不在飞、也不在待办的键」。
//
// 不变量的准确表述：`startupPending ⊆ pending ∪ inflight`。任何不满足它的键都意味着
// **一次永远不会到来的结论**（排队被拒/被合并却记了账），后果是就绪面永久 not-ready。
// 这个不变量在别处无法观察（`startupPending` 是内部台账），故以白盒断言显式钉住。
func (m *Manager) rebuildsPendingLeakFree() bool {
	if m == nil || m.rebuilds == nil {
		return true
	}
	s := m.rebuilds
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.startupPending {
		if _, queued := s.pending[key]; queued {
			continue
		}
		if _, busy := s.inflight[key]; busy {
			continue
		}
		return false
	}
	return true
}

// TestPersistStepwiseSurvivesTinyCycleBudget：每周期总预算压到极小（16 行）时，一次 persist
// 仍必须把**全部**变更落库（分步续跑），既不多写也不少写。
//
// 转红方式（实测）：在 persistSnapshotStep 里让「未完成」也推进 persistedRev/persistedCover
// —— 下一步会把这些源当作未变更而跳过，索引静默少行，本用例在「索引行数 == 注入行数」处变红。
func TestPersistStepwiseSurvivesTinyCycleBudget(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: seed one\n[12:00:02] [Server thread/INFO]: seed two\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:step", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:step", UTCDay: runtimeTestUTCDay(),
		}},
		// 周期预算在**打开索引时**生效（键 log_index.persist.* 的同一条接线），故必须经 Options 下发。
		IndexCommit: &stateindex.CommitBudget{CycleMaxRows: 16},
	})
	require.NoError(t, err)
	m.pollOnce()
	const key = "inst:step/g1"
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 注入 200 条待落库的 WAL 行（末端远高于当前读/回收水位 ⇒ 不会被剪枝带走，必须全部落库）。
	identity := logtypes.SourceIdentity{LogSourceID: "inst:step", SourceGeneration: "g1", ParserVersion: "v1"}
	const injected = 200
	seed := make([]acquire.WALEntry, 0, injected)
	for i := 0; i < injected; i++ {
		end := uint64(1_000_000 + i*10)
		seed = append(seed, acquire.WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("step %d", i)),
			Appended: true, Durable: true,
		})
	}
	require.NoError(t, pipe.WAL().Restore(seed))
	// 制造一次账本修订（真实路径下 WAL 变化总伴随账本变化，落库的「变更源」判据是修订号）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "seed"))
	require.NoError(t, pipe.Ledger().ResumeAcquire(pipe.Key()))

	// 周期预算已是 16 行（见上文 Options）：200 行差异不可能一步做完。
	samplesBefore := len(m.PersistSamples())
	require.NoError(t, m.persist())

	// ① 分步确实发生了（采样按提交单元记录 ⇒ 单元数随行数/预算增长）。
	require.Greater(t, len(m.PersistSamples())-samplesBefore, 2,
		"16 行/周期的预算下，200 行变更必须分成多个提交单元")
	require.False(t, m.LastPersistStats().Incomplete, "最后一轮必须把差异做完（否则 persist 不得返回）")

	// ② 全部变更都落库（逐行比对：索引行数 == 管道 WAL 条目数，且一条不少）。
	loaded, err := m.index.Load()
	require.NoError(t, err)
	indexed := 0
	for _, row := range loaded.WAL {
		if row.Key == key {
			indexed++
		}
	}
	require.Equal(t, injected, indexed, "分步续跑后索引必须与管道 WAL 一致（不得静默丢行）")
	require.Len(t, pipe.WAL().Snapshot(), injected)
}

// TestPersistRoutesReclaimWatermarkToRangePrune：①水位化的**接线**回归——采集侧回收水位必须真的
// 作为剪枝提示下发到索引层，且索引层用它把整段差异**一条语句**删掉。
//
// 夹具复刻现场形态：先让索引里存在「已耐久、且落在回收水位之内」的 WAL 行（生产上由积压期产生），
// 再让回收推进把它们判为可剪（生产上由启动恢复的整窗重发触发）。此后落库必须走谓词范围删除。
//
// 转红方式（实测）：把 persistSnapshotStep 里的 prunes 提示改成 nil —— 本用例在
// 「必须走谓词范围删除 / 12 行必须由范围删除清掉」处变红。
func TestPersistRoutesReclaimWatermarkToRangePrune(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	// 三行：末行永远处于「未闭合」状态（多行缓冲在等下一个边界），前两行才会成为事件并完成投递。
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: seed one\n"+
			"[12:00:02] [Server thread/INFO]: seed two\n"+
			"[12:00:03] [Server thread/INFO]: seed boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:prune", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:prune", UTCDay: runtimeTestUTCDay(),
		}},
	})
	require.NoError(t, err)
	const key = "inst:prune/g1"
	pipe := m.pipes[key]
	require.NotNil(t, pipe)
	// 推起回收水位：投递 + 逐字段校验 + 责任转移（可能跨若干轮，故用 Eventually）。
	require.Eventually(t, func() bool {
		m.pollOnce()
		entry := pipe.Ledger().Get(pipe.Key())
		return entry != nil && entry.Positions.Reclaim > 0
	}, 20*time.Second, 20*time.Millisecond, "夹具前提：先把回收水位推起来")

	// 注入 12 条「已耐久且落在回收水位之内」的 WAL 行 —— 它们既应被内存剪枝丢掉，
	// 也应被索引层用**一条谓词删除**清掉。
	identity := logtypes.SourceIdentity{LogSourceID: "inst:prune", SourceGeneration: "g1", ParserVersion: "v1"}
	seed := make([]acquire.WALEntry, 0, 12)
	for i := 0; i < 12; i++ {
		end := uint64(i*10 + 10)
		seed = append(seed, acquire.WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("seed %d", i)),
			Appended: true, Durable: true,
		})
	}
	require.NoError(t, pipe.WAL().Restore(seed))
	// 真实路径下 WAL 变化总伴随账本变化（读/耐久/回收位置推进），而落库的「变更源」判据是
	// 账本修订号；这里注入的是纯 WAL 行，故显式制造一次账本修订（暂停→恢复，语义上是空操作）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "seed"))
	require.NoError(t, pipe.Ledger().ResumeAcquire(pipe.Key()))
	require.NoError(t, m.persist(), "先把这批 WAL 行落进索引（复刻积压期形态）")
	loaded, err := m.index.Load()
	require.NoError(t, err)
	seeded := 0
	for _, row := range loaded.WAL {
		if row.Key == key {
			seeded++
		}
	}
	require.Equal(t, 12, seeded, "索引里应存在 12 条待剪的 WAL 行")

	// 推进回收即「内存剪枝」发生的那一刻：生产上它的调用点是 releaseRecovery 的尾步
	// （启动恢复整窗重发之后），稳态则是每轮采集的投递之后——两处都是 TryReclaim。
	// 推进回收：生产上这一步由 releaseRecovery 走完责任转移链后调 TryReclaim 完成（起点是启动
	// 恢复的整窗重发）。这里按**同一条链**绑定并释放一个覆盖 [reclaim, 注入末端] 的恢复分段，
	// 使账本放行（默认拒绝正是保证「回收必须有凭据」的守卫）。
	from := pipe.Ledger().Get(pipe.Key()).Positions.Reclaim
	const segID = "projection-recovery-test-120"
	require.NoError(t, pipe.BindRecoverySegment(segID, "projection://test", from, 120))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", "test"))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryReleased, logtypes.ReleaseProjectionBacked, "test"))
	_, err = pipe.TryReclaim()
	require.NoError(t, err, "凭据齐备后回收必须放行（否则夹具没造出「剪枝」这一步）")
	surviving := len(pipe.WAL().Snapshot())
	require.Less(t, surviving, 12, "回收水位之内的已耐久条目必须被内存剪枝丢掉")

	// 紧接着的落库必须把这段差异表达成**一条谓词范围删除**，而不是逐行 DELETE。
	require.NoError(t, m.persist())
	stats := m.LastPersistStats()
	require.Positive(t, stats.RangePrunes, "必须走谓词范围删除（①水位化接线生效）")
	require.Equal(t, 12-surviving, stats.RangePruned, "被剪掉的行必须由范围删除清掉")
	require.LessOrEqual(t, stats.Statements, 4, "删除必须批量（不得退化成逐行 DELETE）")

	// 等价性：索引里该源的 WAL 行数必须与内存 WAL 完全一致（范围删除不得多删或少删）。
	loaded, err = m.index.Load()
	require.NoError(t, err)
	indexed := 0
	for _, row := range loaded.WAL {
		if row.Key == key {
			indexed++
		}
	}
	require.Equal(t, surviving, indexed, "范围删除后索引必须与内存 WAL 逐行一致")
}
