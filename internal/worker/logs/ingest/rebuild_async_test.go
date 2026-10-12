package ingest

// 本文件是「投影重建异步化 + 限速 + 防循环」的回归（2026-10-09）。
//
// 现场背景（真实日志取证，`worker.out.20261009-2103`）：源 `inst:220/file` 的同一份
// **573562 事件 / 同一 UTC 天**被写成 `projection-115…125…`（≥70 代次，每 ~1 小时一轮），
// 每次 `投影写入结束 status=200` 后 `投影校验完成` 报
// `not fully visible before deadline: context deadline exceeded` ⇒ 代次不推进 ⇒
// 下一轮又判缺失 ⇒ **再写 573562 条**。后果：VL 累积 281GB；重建与采集争算力与 VL 容量，
// 大批源 `acquire: tailer paused (wal backlog limit exceeded)`（40 源卡死 13 小时）。
//
// 本文件钉住四条性质（缺一不可，每条都能转红）：
//   ① TestRebuildDoesNotStallCollection —— 重建进行时采集仍推进（主线不被占用）；
//   ② TestRebuildRetryBoundedAndVisible  —— 同源重建有上限 + 有退避，放弃时计数与原因可见；
//   ③ TestRebuildThrottlesPerBatch      —— 单批有界 + 批间让出（限速生效）；
//   ④ TestRebuildFailureNeverMasksAsSuccess —— 失败绝不被伪装成成功、不静默丢数据。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// ── ① 重建不占主线 ──────────────────────────────────────────────────────────────

// TestRebuildDoesNotStallCollection 固定「重建进行时采集轮照常推进」。
//
// **为什么它能转红**（本用例的判别力来自"受控阻塞点"，不是时长阈值）：
// 用 `RebuildRunNotify` 钩子在**重建执行体**里卡住（等一个只由采集断言打开的 channel），
// 于是「重建是否在飞」是确定的；此时采一轮 `pollOnce` 并断言：
//   - 它**能返回**（把重建放回对账轮内联执行 ⇒ 采集轮拿不到 cycleMu/被同 goroutine 串行 ⇒ 死锁超时）；
//   - 它真的**消费了新写入的日志**（读水位前进），而不是空转。
//
// 与既有 `TestReplayDoesNotStallCollection` 的差别：后者用 700ms 插入延时拉开窗口（时长口径），
// 本用例用**同步阻塞点**（结构口径），因此不依赖调度时序，也不会因机器快慢假绿/假红。
func TestRebuildDoesNotStallCollection(t *testing.T) {
	client, fixture := newSlowInsertVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	// 参与采集的文件源（与重建同进程、争用同一套锁与门）。
	logPath := filepath.Join(t.TempDir(), "busy.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: warmup\n"), 0o600))
	busy := SourceConfig{
		LogSourceID: "node:busy", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:busy", UTCDay: runtimeTestUTCDay(),
	}

	// 受控阻塞点：重建一进来就卡住，直到采集断言完成。
	rebuildEntered := make(chan struct{})
	releaseRebuild := make(chan struct{})
	var enteredOnce bool
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source, busy},
		RebuildRunNotify: func(string) {
			if enteredOnce {
				return
			}
			enteredOnce = true
			close(rebuildEntered)
			<-releaseRebuild // 卡住重建，制造「重建在飞且正在跑」的确定窗口
		},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 造一天缺失 ⇒ 对账会登记重建（否则无缺失即零重发，本用例会测成空流程）。
	fixture.deleteDay("2026-09-22")
	// 先采一轮建立基线（把 warmup 行读掉），再追加新行，用于断言「重建期间采集仍在消费」。
	m.pollOnce()
	pipe := m.pipes["node:busy/g1"]
	require.NotNil(t, pipe)
	before, ok := pipe.Positions()
	require.True(t, ok)
	appendLogLines(t, logPath, 10)

	// 在独立 goroutine 上驱动「对账 + 执行重建」（模拟生产 worker），它会卡在 hook 上。
	// 对账轮返回后立刻记录**增量**插入数：**对账轮自己不得写 VL**（登记必须是 O(1) 的）。
	insertsBaseline := fixture.insertCount()
	insertsAtRoundEnd := make(chan int, 1)
	roundDone := make(chan struct{})
	go func() {
		defer close(roundDone)
		m.reconcileOneCandidate(context.Background(),
			reconcileCandidate{key: key, source: source}, m.reconcile, &ReconcileRoundResult{})
		insertsAtRoundEnd <- fixture.insertCount() - insertsBaseline
		m.drainRebuilds(context.Background())
	}()

	select {
	case <-rebuildEntered:
	case <-time.After(5 * time.Second):
		close(releaseRebuild)
		t.Fatal("重建未在窗口内进入「重建执行体」：可能（a）夹具没造出重建，" +
			"或（b）重建被内联执行在别处（那样就不会经由 RunRebuildWorker 的执行体，本钩子不会触发）")
	}
	// **断言 A（「不占主线」的形式判据）**：对账轮只做 O(1) 登记，重建的 VL 写入必须发生在
	// 登记之后 —— 若把重建放回对账轮内联执行（生产事故形态），写入会在对账轮内就发生，此处转红。
	require.Zero(t, <-insertsAtRoundEnd,
		"对账轮不得内联执行重建（登记必须是 O(1) 的：写入只能发生在重建执行体里）")

	// **断言 B（并发推进的行为判据）**：重建正卡在执行体中，此刻采集轮必须照常推进读水位。
	pollDone := make(chan struct{})
	var afterRead uint64
	go func() {
		defer close(pollDone)
		m.pollOnce()
		if after, ok := pipe.Positions(); ok {
			afterRead = after.Read
		}
	}()
	select {
	case <-pollDone:
	case <-time.After(10 * time.Second):
		close(releaseRebuild)
		t.Fatal("重建进行期间采集轮被卡住：重建占用了主线")
	}
	require.Greater(t, afterRead, before.Read,
		"重建进行期间采集轮必须照常消费新日志（读水位前进）")

	close(releaseRebuild)
	select {
	case <-roundDone:
	case <-time.After(10 * time.Second):
		t.Fatal("释放阻塞后重建未结束")
	}
	require.Positive(t, m.RebuildStats().Started, "重建必须真的被执行过（否则本用例测的是空流程）")
}

// ── ② 防循环重建：有界重试 + 退避 + 放弃可见 ─────────────────────────────────────

// TestRebuildRetryBoundedAndVisible 固定「同源重建不得无界重复」，并把放弃做成可见事实。
//
// **现场形态（必须精确复现）**：`inst:220/file` 的写入**自称成功**（`status=200`），
// 但数据永远不可见 —— 随后 `投影校验完成` 恒报
// `not fully visible before deadline: context deadline exceeded`，于是「换新代次再写一遍」
// 无限循环（≥70 代次，每轮 ~1 小时，VL 累积 281GB）。
// 夹具的 `silentDropInsert` 正是这个形态：返回 200 但不落行。
//
// 断言：
//   - 每次失败都可见（Failed 计数 + `RebuildLastResult` 带错误）；
//   - 达确定性失败上限后**放弃自动重试**，且 `RebuildAbandonedReason` 给出可读原因
//     （含已尝试代次与最后失败原因）——放弃不是静默；
//   - 放弃后**不再有任何新写入**（这正是 281GB 的止血点）。
//
// **为什么它能转红**：把该形态误判成"外部条件"（例如按 `context.DeadlineExceeded` 归类，
// 或去掉上限判定）⇒ 永远不放弃 ⇒ 本用例在「放弃后零新写入」处变红。
func TestRebuildRetryBoundedAndVisible(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
		// 上限压到 3：让"有界"这件事在用例里可数（生产默认 4，同类语义）。
		RebuildTuning: &RebuildTuning{MaxAttempts: 3, Throttle: time.Millisecond},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// **生产形态**：写入自称成功、数据永远不可见。
	fixture.silentDropInsert = true
	fixture.deleteDay("2026-09-22")

	writesSeen := make([]int, 0, 3)
	for attempt := 1; attempt <= 3; attempt++ {
		ok, reason := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_loop")
		require.True(t, ok, "第 %d 次登记应被接受（上限是 3）：%s", attempt, reason)
		require.Equal(t, 1, m.drainRebuilds(context.Background()),
			"第 %d 次登记必须被执行", attempt)
		stats := m.RebuildStats()
		require.Equal(t, int64(attempt), stats.Failed, "第 %d 次必须是可观测的失败", attempt)
		require.Zero(t, stats.Completed, "失败绝不得被记成完成")
		out, hasOut := m.RebuildLastResult(key)
		require.True(t, hasOut)
		require.Error(t, out.Err, "第 %d 次的失败必须可取证", attempt)
		require.Empty(t, out.Days, "失败绝不产生「已完成的天」")
		writesSeen = append(writesSeen, fixture.insertAttemptCount())
		// 清退避窗口，让"下一次登记"测的是上限而不是计时。
		m.rebuilds.mu.Lock()
		if st := m.rebuilds.attempts[key]; st != nil {
			st.nextAllowedAt = time.Time{}
		}
		m.rebuilds.mu.Unlock()
	}
	// 循环形态确认：这三次都真的又写了一遍（否则测的不是"写完却校验不过"的循环）。
	for i := 1; i < len(writesSeen); i++ {
		require.Greater(t, writesSeen[i], writesSeen[i-1],
			"每次失败前都确实又写了一遍（这正是循环重写的形态）")
	}

	// 第 3 次失败后：必须**放弃**，且原因可读（含已尝试代次与最后失败原因）。
	reason, abandoned := m.RebuildAbandonedReason(key)
	require.True(t, abandoned, "连续确定性失败达上限后必须放弃自动重试（否则就是生产那个无界循环）")
	require.Contains(t, reason, "连续 3 次重建失败后停止自动重试", "放弃原因必须可读：%s", reason)
	require.Contains(t, reason, "projection-", "放弃原因必须点名已尝试的代次（现场最难判的一点）")

	// 放弃后：登记一律被拒（且原因可归类），**不再产生任何新写入**。
	insertsBefore := fixture.insertCount()
	ok, skip := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_after_abandon")
	require.False(t, ok, "已放弃后不得再登记")
	require.Equal(t, "rebuild_abandoned", skip, "跳过原因必须可归类")
	require.Zero(t, m.drainRebuilds(context.Background()), "已放弃后不得再有任务可执行")
	require.Equal(t, insertsBefore, fixture.insertCount(),
		"放弃后不得再有新写入（这正是 281GB 的止血点）")
	require.Positive(t, m.RebuildStats().Abandoned, "放弃次数必须进读数")
	require.Positive(t, m.RebuildStats().AbandonedSkips, "被拒的登记也必须进读数")
}

// TestRebuildExternalFailureNeverAbandons 固定**反面**：外部条件失败（VL 未就绪/插入 5xx）
// 不得把源判死 —— 那会让「VL 抖一次 ⇒ 该源永远不再重建」，是可用性回归。
//
// 断言：连续 10 次外部失败（远超上限 3）后仍**未放弃**、仍可登记、退避窗口外仍能执行。
//
// **为什么它能转红**：把外部失败也计入上限（即所有失败一视同仁）⇒ 第 3 次后就 abandoned ⇒
// 本用例在「仍可登记」处变红。
func TestRebuildExternalFailureNeverAbandons(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources:       []SourceConfig{source},
		RebuildTuning: &RebuildTuning{MaxAttempts: 3, Throttle: time.Millisecond},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	fixture.failInsert = true // 插入 500 = 外部条件失败（重试可自愈）
	fixture.deleteDay("2026-09-22")
	for attempt := 1; attempt <= 10; attempt++ {
		ok, reason := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_external")
		require.True(t, ok, "第 %d 次外部失败后仍必须允许登记：%s", attempt, reason)
		require.Equal(t, 1, m.drainRebuilds(context.Background()))
		m.rebuilds.mu.Lock()
		if st := m.rebuilds.attempts[key]; st != nil {
			st.nextAllowedAt = time.Time{}
		}
		m.rebuilds.mu.Unlock()
	}
	_, abandoned := m.RebuildAbandonedReason(key)
	require.False(t, abandoned,
		"外部条件失败（VL 不可用）不得把源判死：那会让该源永远不再重建（可用性回归）")
	require.Zero(t, m.RebuildStats().Abandoned)
	require.Equal(t, int64(10), m.RebuildStats().Failed, "但仍必须每次可见地留痕")
}

// TestRebuildBackoffSkipsWithinWindow 固定「退避窗口内的登记被跳过且可见」。
//
// 与上一条互补：上一条测「上限」，本条目测「上限之前的退避」。转红方式同型
// （去掉退避计算 ⇒ 窗口内登记仍被接受）。
func TestRebuildBackoffSkipsWithinWindow(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources:       []SourceConfig{source},
		RebuildTuning: &RebuildTuning{MaxAttempts: 8, Throttle: time.Millisecond},
	})
	require.NoError(t, err)

	fixture.failInsert = true
	fixture.deleteDay("2026-09-22")
	ok, _ := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_backoff")
	require.True(t, ok)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))
	require.Equal(t, int64(1), m.RebuildStats().Failed)

	// 退避窗口内：登记必须被跳过（不排队、不执行），且计数可读。
	insertsBefore := fixture.insertCount()
	ok, skip := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_backoff")
	require.False(t, ok, "退避窗口内不得再登记")
	require.Equal(t, "rebuild_backoff", skip)
	require.Zero(t, m.drainRebuilds(context.Background()))
	require.Equal(t, insertsBefore, fixture.insertCount(), "退避期间不得产生任何写入")
	require.Positive(t, m.RebuildStats().BackoffSkipped, "退避跳过必须进读数")
}

// ── ③ 限速：单批有界 + 批间让出 ─────────────────────────────────────────────────

// rebuildSeedVerifyTimeout 是本文件「大夹具」使用的校验窗口。
//
// 为什么必须显式给出（而不是沿用 `seedProjection` / `newTestManager` 的 200ms 默认）：
// 本用例的夹具刻意造 1200 条事件（> 2 × `insertBatchMaxEvents`，否则切不出多批、就测不到
// 批间让出）。在 `-race` 构建下竞态检测器把每条事件的处理放大 10–20×，200ms 窗口会在
// **夹具准备阶段**（`seedProjection` 的投影写入 + 校验）就耗尽 ⇒ 用例在「与用例性质无关的
// 前置」上失败（实测错误：`not fully visible before deadline`）。
//
// 口径不放松：本用例断言的是「单批有界 + 批间让出 + 让出不改写入面」，
// 与校验窗口长短无关；窗口只作为「足够大以致不成为瓶颈」的前置条件而存在。
// （同型取舍见 `resolve_gaps_lock_test.go` 的 `primeCompleteProjections` 对 race 的说明。）
const rebuildSeedVerifyTimeout = 2 * time.Minute

// seedProjectionWithTimeout 与 `seedProjection` 同构，但允许显式指定校验窗口（供大夹具使用）。
func seedProjectionWithTimeout(t *testing.T, client *vlsup.Client, root string, journal catalog.Journal, cat *catalog.Catalog, source SourceConfig, events []logtypes.Event, verifyTimeout time.Duration) {
	t.Helper()
	manager, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source},
		VerificationTimeout: verifyTimeout,
	})
	require.NoError(t, err)
	stopped := false
	defer func() {
		if !stopped {
			_ = manager.Stop()
		}
	}()
	_, err = manager.writeProjection(source, events, "projection-1", events, nil, true)
	require.NoError(t, err)
	require.NoError(t, manager.Stop())
	stopped = true
}

// TestRebuildThrottlesPerBatch 固定「重建按批推进、批间主动让出」。
//
// 生产形态：单天 573562 事件 = 1147 个 500 条插入批 + 287 个校验簇，原文一次连续跑完。
// 本用例用一条「远超单批量」的天造多批，断言：
//   - **单批有界**：每次插入载荷的事件数 ≤ `insertBatchMaxEvents`（让出语义的前提；
//     若某天被整份塞进一次请求，批间让出就无从谈起）；
//   - **批间让出**：`ThrottledYields` 读数随批数增长（每批一次），且让出次数 ≥ 批数-1；
//   - **让出不改变写入面**：全部权威事件仍被完整写入（限速只是节奏，不是裁剪）。
//
// **为什么它能转红**：把 `insertInBatchesCtxYield` 换回原 `insertInBatchesCtx`（不传 yield）⇒
// 让出读数恒为 0 ⇒ 本用例在「让出次数」处变红。
func TestRebuildThrottlesPerBatch(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)

	// 造一天里 1200 条权威事件（> 2 × insertBatchMaxEvents）⇒ 至少 3 个插入批。
	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	events := make([]logtypes.Event, 0, 1200)
	for i := 0; i < 1200; i++ {
		start := uint64(i * 10)
		events = append(events, logtypes.BuildEvent(identity,
			logtypes.RecordRange{Start: start, End: start + 9},
			"2026-09-22T01:00:00Z", "2026-09-22T01:00:01Z", "INFO", "stdout",
			strings.Repeat("x", 8)+string(rune('a'+i%26))))
	}
	seedProjectionWithTimeout(t, client, root, journal, catalog.New(journal), source, events, rebuildSeedVerifyTimeout)

	yields := 0
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
		// 让出时长压到 1ms：本用例断言的是「让出**被调用**且次数正确」，不是等待时长本身
		// （真按生产默认 2ms × 每批一次也只是毫秒级，但没必要让用例白等）。
		// 注意 Throttle<=0 会被归一成生产默认值，故这里显式给一个小的正数。
		RebuildTuning:      &RebuildTuning{Throttle: time.Millisecond},
		RebuildYieldNotify: func() { yields++ },
		// 同一夹具口径：1200 条在 `-race` 下需要更宽的校验窗口（见 rebuildSeedVerifyTimeout）。
		VerificationTimeout: rebuildSeedVerifyTimeout,
	})
	require.NoError(t, err)

	fixture.deleteDay("2026-09-22")
	insertsBefore := fixture.insertCount()
	ok, reason := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_throttle")
	require.True(t, ok, "登记应被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))
	require.Zero(t, m.RebuildStats().Failed, "本用例不得有失败（否则测不到让出路径）")

	payloads := fixture.payloadsSince(insertsBefore)
	require.GreaterOrEqual(t, len(payloads), 3, "1200 条 / 每批 500 条必须切出多批")

	// ① 单批有界：逐载荷数事件（NDJSON 每条一个 event_id）。
	for i, payload := range payloads {
		count := strings.Count(payload, `"event_id"`)
		require.LessOrEqual(t, count, insertBatchMaxEvents,
			"第 %d 批的事件数必须 ≤ %d（单批有界是让出语义的前提）", i, insertBatchMaxEvents)
	}
	// ② 批间让出：每批之前一次（首批发之前不算，故 ≥ 批数-1）。
	require.GreaterOrEqual(t, yields, len(payloads)-1,
		"每个插入批之前必须让出一次（限速生效的直接证据）")
	require.Equal(t, int64(yields), m.RebuildStats().ThrottledYields,
		"让出次数必须进读数（可观测）")
	// ③ 让出不裁剪数据：全部权威事件都写在载荷里。
	joined := strings.Join(payloads, "\n")
	for _, event := range events {
		require.Contains(t, joined, event.EventID, "限速不得改变写入面：事件 %s 必须仍被写入", event.EventID)
	}
}

// ── ④ 失败绝不伪装成成功 / 不静默丢数据 ─────────────────────────────────────────

// TestRebuildFailureNeverMasksAsSuccess 固定红线：重建失败时，状态必须仍然表达「未发布」，
// 且不得推进回收责任 / 不得消解缺口（否则就是把失败说成成功、并放行回收 = 丢数据）。
//
// **为什么它能转红**：让重建失败后仍清 `PublicationPending`（或仍推进 reclaim 水位）⇒
// 本用例在「pending 必须为真」「reclaim 不得前进」处变红。
func TestRebuildFailureNeverMasksAsSuccess(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources:       []SourceConfig{source},
		RebuildTuning: &RebuildTuning{MaxAttempts: 8, Throttle: time.Millisecond},
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 让写入必然失败（VL 插入 500）。
	fixture.failInsert = true
	fixture.deleteDay("2026-09-22")

	m.mu.Lock()
	reclaimBefore := m.state.Sources[key].ProjectionGeneration // 代次基线（失败不得推进）
	m.mu.Unlock()

	ok, _ := m.scheduleRebuildAsync(source, []string{"2026-09-22"}, "test_fail_visible")
	require.True(t, ok)
	m.drainRebuilds(context.Background())
	out, hasOut := m.RebuildLastResult(key)
	require.True(t, hasOut, "最近结论必须可取证（否则失败无从判读）")
	require.Error(t, out.Err, "写入失败必须如实返回错误（不得静默）")
	require.Empty(t, out.Days, "失败绝不产生「已完成的天」")

	stats := m.RebuildStats()
	require.Equal(t, int64(1), stats.Failed, "失败必须进读数")
	require.Zero(t, stats.Completed, "失败绝不得被记成完成")

	// 发布状态必须仍表达「未发布」：要么 pending 为真，要么代次未被推进到新值之上。
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	require.True(t, saved.PublicationPending || saved.ProjectionGeneration == reclaimBefore,
		"失败后状态必须仍表达「未发布」（pending 或代次未推进），绝不得伪装成成功")

	// 缺口不得被失败路径消解（消解等于宣称数据已落库）。
	entry := m.pipes[key].Ledger().Get(m.pipes[key].Key())
	require.NotNil(t, entry)
	for _, gap := range entry.Gaps {
		require.False(t, gap.Resolved,
			"重建失败绝不得消解缺口（那等于宣称数据已可见）")
	}
}
