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

	// 只闸新增物理副本，不永久封禁按源只读取证；首份seed已占一份，最多再新增两份。
	m.materializationLimits.WindowCopies = 3
	clock := time.Now()
	m.rebuilds.now = func() time.Time { return clock }
	rejected := 0
	writes := make([]int, 0, 2)
	for round := 1; round <= 6; round++ {
		before := fixture.insertAttemptCount()
		ok, skip := m.scheduleStartupRebuildTask(source, []string{"2026-09-22"}, startupReplayReasonMissing)
		require.True(t, ok, "退避外必须允许重新取证：%s", skip)
		require.Equal(t, 1, m.drainRebuilds(context.Background()))
		out, hasOut := m.RebuildLastResult(key)
		require.True(t, hasOut)
		require.Error(t, out.Err)
		require.Empty(t, out.Days, "失败不能伪报某日已完成")
		if round <= 2 {
			require.Greater(t, fixture.insertAttemptCount(), before, "前两轮必须真实写后校验失败")
			writes = append(writes, fixture.insertAttemptCount())
		} else {
			rejected++
			require.ErrorIs(t, out.Err, ErrMaterializationBudget)
			require.True(t, out.Pending)
			require.Equal(t, before, fixture.insertAttemptCount(), "预算用尽后零新增物理写")
		}
		okBackoff, skipBackoff := m.scheduleStartupRebuildTask(source, []string{"2026-09-22"}, startupReplayReasonMissing)
		require.False(t, okBackoff)
		require.Equal(t, "rebuild_backoff", skipBackoff)
		require.True(t, m.rebuildsPendingLeakFree(), "拒绝登记不能残留永远不会完成的键")
		state, ok := m.rebuilds.snapshot(key)
		require.True(t, ok)
		clock = state.nextAllowedAt.Add(time.Second)
	}
	require.Equal(t, 4, rejected, "同一稳定窗口后四轮必须均被持久物化预算拒绝新增副本")
	require.Len(t, writes, 2)
	require.Greater(t, writes[1], writes[0])
	_, abandoned := m.RebuildAbandonedReason(key)
	require.False(t, abandoned, "副本预算不能退化成永久停源")
	require.Zero(t, m.RebuildStats().Completed, "尚未真正恢复时不能冒称完成")
	require.Positive(t, m.RebuildStats().BackoffSkipped)
	require.False(t, m.CutoverReadiness().LedgerReady, "未完成的历史发布责任必须继续阻塞就绪")
	rows, err := m.index.ListMaterializations(context.Background(), key, "2026-09-22", "", 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 3, "原seed和两次未知物理写都须持久计费")
	require.Empty(t, m.StartupRecoveryStatus().ReplaysInFlight)
	require.True(t, m.rebuildsPendingLeakFree())

	// 外部恢复原已发布副本后，只读取证仍可成功，不新增任何物理副本，也不永久封禁来源。
	fixture.mu.Lock()
	fixture.silentDropInsert = false
	fixture.mu.Unlock()
	payload, err := projectionPayload(reconcileTestEvents()[:2], "projection-1")
	require.NoError(t, err)
	_, err = client.InsertJSONLines(context.Background(), payload)
	require.NoError(t, err)
	insertsBefore := fixture.insertAttemptCount()
	ok, skip := m.scheduleStartupRebuildTask(source, []string{"2026-09-22"}, startupReplayReasonMissing)
	require.True(t, ok, "%s", skip)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))
	out, ok := m.RebuildLastResult(key)
	require.True(t, ok)
	require.NoError(t, out.Err)
	require.True(t, out.ConfirmedOnly)
	require.Equal(t, insertsBefore, fixture.insertAttemptCount(), "预算用尽后的恢复必须只读确认，不再写全窗")
	require.False(t, savedSource(t, m, key).PublicationPending)
	require.True(t, m.rebuildsPendingLeakFree())
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

	// 注入大量缺口行造出「一次落库装不下」的差异（末端远高于当前回收水位 ⇒ 不会被任何剪枝带走）。
	//
	// 2026-10-29：原夹具注入的是 source_wal 行（断言索引里的 WAL 行数）。该表删除后改用
	// **缺口行**造差异——它同样走「按归属增量 + 切分续跑」这条路径，验证强度不变。
	//
	// 两个必须遵守的夹具前提（实测踩过）：
	//   - 必须经 `Ledger().RecordGap` 登记，不能直接往 `m.state` 里塞：`persist` 会从
	//     `pipe.Ledger().Snapshot()` **重建**该源，手塞的行会被重建覆盖掉（实测落库 0 行）；
	//   - 区间必须**不首尾相接**：相邻缺口会被 `gapMergeScanWindow` 当场合并成一条
	//     （实测 i*10..i*10+9 全部合并 ⇒ 只剩 1 行）。所以每条留 90 字节间隔。
	//
	// 行数上限是账本自己的 `DefaultMaxUnresolvedGapsPerSource`（64）——超过就按原因折叠。
	// 64 行 / 16 行预算 = 4 个提交单元，仍远多于 2，判别力足够；断言也改为与账本实际行数比对。
	const injected = 200
	for i := 0; i < injected; i++ {
		require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(),
			uint64(i*100), uint64(i*100+9), "PAUSED", fmt.Sprintf("stepwise %d", i)))
	}
	// 不再靠 Pause/Resume 推修订号：未解决缺口会让 `ResumeAcquire` 直接拒绝（默认拒绝是刻意的
	// 安全语义）。`RecordGap` 本身就是真实账本写入，修订号已经推进。
	ledgerGaps := len(pipe.Ledger().Get(pipe.Key()).Gaps)
	require.Greater(t, ledgerGaps, 16, "夹具前提：账本缺口行数必须超过单周期预算（否则分不了步）")

	// 周期预算已是 16 行（见上文 Options）：200 行差异不可能一步做完。
	samplesBefore := len(m.PersistSamples())
	require.NoError(t, m.persist())

	// ① 分步确实发生了（采样按提交单元记录 ⇒ 单元数随行数/预算增长）。
	require.Greater(t, len(m.PersistSamples())-samplesBefore, 2,
		"16 行/周期的预算下，200 行变更必须分成多个提交单元")
	require.False(t, m.LastPersistStats().Incomplete, "最后一轮必须把差异做完（否则 persist 不得返回）")

	// ② 全部变更都落库（逐行比对：索引缺口行数 == 账本缺口行数，且一条不少）。
	loaded, err := m.index.Load()
	require.NoError(t, err)
	indexed := 0
	for _, row := range loaded.Gaps {
		if row.Key == key {
			indexed++
		}
	}
	require.Equal(t, ledgerGaps, indexed, "分步续跑后索引必须与账本缺口一致（不得静默丢行）")
}

// TestPersistReclaimWatermarkPrunesQueueAndPositions：回收水位推进时，**内存队列**必须真的被剪掉
// 那一段，且落库把新的 reclaim 水位持久化下来（重启后 rollbackReadToReclaim 依赖它决定重读起点）。
//
// 2026-10-29：本用例原为 `TestPersistRoutesReclaimWatermarkToRangePrune`（①水位化谓词范围删除的
// 接线回归）。WAL 不落盘后「索引侧把整段 WAL 差异用一条语句删掉」这件事已不存在（没有 WAL 行），
// 所以这里改为钉真正还需要的两条：
//   - 内存剪枝真的发生（队列里回收水位之内的已耐久条目被丢掉）；
//   - 新水位真的落进了 position 表（这是新可靠性模型的唯一持久凭据）。
//
// 转红方式：让 persist 不推进 position（或让 TryReclaim 不剪队列）⇒ 对应断言直接变红。
func TestPersistReclaimWatermarkPrunesQueueAndPositions(t *testing.T) {
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

	// 注入 12 条「已耐久且落在回收水位之内」的条目 —— 它们应被内存剪枝丢掉。
	identity := logtypes.SourceIdentity{LogSourceID: "inst:prune", SourceGeneration: "g1", ParserVersion: "v1"}
	events := make([]logtypes.Event, 0, 12)
	for i := 0; i < 12; i++ {
		end := uint64(i*10 + 10)
		events = append(events, logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
			"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("seed %d", i)))
	}
	seedWAL(t, pipe.WAL(), events...)
	before := len(pipe.WAL().Snapshot())
	require.Equal(t, 12, before, "夹具前提：12 条已耐久条目必须都在队列里")

	// 推进回收：生产上这一步由 releaseRecovery 在投递确认后调 TryReclaim 完成。
	//
	// 2026-10-29：旧夹具靠「绑定恢复段 + 推进到 RELEASED」让账本放行；那套机制已删。
	// 新语义下回收只认「REQUEST_DONE 连续覆盖 + max(Read,Durable)」——把读入量与认证覆盖
	// 都推到注入末端（120）即可，语义与旧夹具等价（都要求「这一段已被确认投递」）。
	require.NoError(t, pipe.Ledger().AdvanceRead(pipe.Key(), 120))
	require.NoError(t, pipe.Ledger().RecordDelivery(pipe.Key(), pipe.Ledger().Get(pipe.Key()).Positions.Reclaim, 120, logtypes.DeliveryRequestDone))
	_, err = pipe.TryReclaim()
	require.NoError(t, err, "凭据齐备后回收必须放行（否则夹具没造出「剪枝」这一步）")
	surviving := len(pipe.WAL().Snapshot())
	require.Less(t, surviving, before, "回收水位之内的已耐久条目必须被内存剪枝丢掉")

	// 落库必须把推进后的水位持久化到 position 表（重启后重读的唯一凭据）。
	require.NoError(t, m.persist())
	reclaim := pipe.Ledger().Get(pipe.Key()).Positions.Reclaim
	loaded, err := m.index.Load()
	require.NoError(t, err)
	persisted := uint64(0)
	for _, row := range loaded.Positions {
		if row.Key == key {
			persisted = row.ReclaimPos
		}
	}
	require.Equal(t, reclaim, persisted, "回收水位必须真的落进 position 表（重启重读的起点）")
	require.Positive(t, reclaim, "夹具前提：回收水位应已推进")
}
