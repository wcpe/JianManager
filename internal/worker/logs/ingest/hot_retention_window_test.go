package ingest

// 本文件是「热层可发布窗口」的回归（2026-10-21 生产缺陷）。
//
// 现场背景（`worker-final2.log` 取证，已用真实 VL 实测复核）：
//   - VL 热层以 `-retentionPeriod=30d` 运行；
//   - VL 对**超出 retention 的数据点返回 HTTP 200 但静默丢弃**
//     （写 45 天前数据 → 200，随后查询 0 行；写当天数据 → 200，查询 1 行）；
//   - 于是重建重放 31 天前的那天时打 `verified:5781, status:200`（**假成功**），
//     随后逐字段校验恒 `rows:0` ⇒ 超时 ⇒ `PublicationPending` 永久为真 ⇒
//     重试到上限后「已停止自动重试」；而对账每轮又把该天判成 `observed < expected`
//     ⇒ 永远重新登记重建。
//
// 四条用例分别钉住：纯判定边界、重建面过滤（含「跨越边界不误伤」）、对账不算缺失、
// 以及「全部超窗口」的终态（清 pending + 缺口继续可见 + 不落成失败/放弃）。
//
// 每条用例都做过变异验证（把 `hotRetentionDayBeyondWindow` 改成恒 false / 去掉终态收口
// ⇒ 对应用例转红），详见交付报告。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// hotRetentionTestRetention 是回归用的热层 retention（与生产默认 30d 一致）。
const hotRetentionTestRetention = 30 * 24 * time.Hour

// hotRetentionTestDays 返回相对 now 的三个 UTC 天：
//
//	beyondDay   —— 边界前一整天：**整天都**早于 `now-retention` ⇒ 热层不可能可见；
//	boundaryDay —— **跨越边界**的当天（边界落在这一天之内）⇒ 必须仍按可发布处理；
//	inWindowDay —— 窗口内（昨天）。
//
// 为什么用「相对 now」而不是写死日期：判据本身以 `now - retention` 为界，写死日期会让
// 用例随时间推移自然过期（今天绿、一个月后红），那不是回归而是定时炸弹。
func hotRetentionTestDays(now time.Time, retention time.Duration) (beyondDay, boundaryDay, inWindowDay string) {
	boundary := hotRetentionBoundary(now, retention).UTC()
	boundaryDay = boundary.Format("2006-01-02")
	beyondDay = boundary.AddDate(0, 0, -1).Format("2006-01-02")
	inWindowDay = now.Add(-24 * time.Hour).UTC().Format("2006-01-02")
	return beyondDay, boundaryDay, inWindowDay
}

// hotRetentionTestSource 是回归用的源配置（Path 必须真实存在：FILE_PRIMARY 会建 FileTailer）。
func hotRetentionTestSource(t *testing.T, root string) SourceConfig {
	t.Helper()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o600))
	return SourceConfig{
		LogSourceID: "node:hotwindow", SourceGeneration: "g1",
		Path: logPath, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:hotwindow", UTCDay: runtimeTestUTCDay(),
	}
}

// hotRetentionTestEvent 造一条归属 utcDay 的权威事件。
func hotRetentionTestEvent(utcDay string, start, end uint64, message string) logtypes.Event {
	identity := logtypes.SourceIdentity{
		LogSourceID: "node:hotwindow", SourceGeneration: "g1", ParserVersion: "v1",
	}
	return logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: end},
		utcDay+"T01:00:00Z", utcDay+"T01:00:01Z", "INFO", "stdout", message)
}

// ── ① 窗口判定的边界语义（纯函数）──────────────────────────────────────────────

// TestHotRetentionDayBeyondWindow 固定「热层可发布窗口」的判定口径。
//
// **为什么它能转红**：把判据改宽（例如用「该天开始早于边界」而不是「该天结束早于边界」），
// 「跨越边界的当天」就会变成超窗口 ⇒ 边界用例转红；改窄（例如不比较日末）则「边界前一天」
// 转红；把零值当作失效（默认 30d 或直接判 true）则零值用例转红。
func TestHotRetentionDayBeyondWindow(t *testing.T) {
	// 固定 now，使边界落在当天的 15:30（不是整点，覆盖「边界在一天中途」的常态）。
	now := time.Date(2026, 10, 21, 15, 30, 0, 0, time.UTC)
	// 边界 = 2026-09-21T15:30:00Z。
	cases := []struct {
		name      string
		day       string
		retention time.Duration
		want      bool
	}{
		{name: "边界前一天（整天都在边界之前）", day: "2026-09-20", retention: hotRetentionTestRetention, want: true},
		{name: "跨越边界的当天（部分数据仍在窗口内）", day: "2026-09-21", retention: hotRetentionTestRetention, want: false},
		{name: "窗口内", day: "2026-09-22", retention: hotRetentionTestRetention, want: false},
		{name: "今天", day: "2026-10-21", retention: hotRetentionTestRetention, want: false},
		{name: "未来天", day: "2026-10-22", retention: hotRetentionTestRetention, want: false},
		{name: "很早以前", day: "1990-01-01", retention: hotRetentionTestRetention, want: true},
		{name: "retention 为零（未知/未启用过滤）", day: "1990-01-01", retention: 0, want: false},
		{name: "retention 为负（配置异常）", day: "1990-01-01", retention: -time.Hour, want: false},
		{name: "天不可解析", day: "not-a-day", retention: hotRetentionTestRetention, want: false},
		{name: "天为空", day: "", retention: hotRetentionTestRetention, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, hotRetentionDayBeyondWindow(tc.day, now, tc.retention))
		})
	}

	// 边界恰好落在 UTC 整点：前一天必须以**含最后一纳秒的日末**判定，否则会漏掉一天
	// （用「次日零点 <= 边界」的写法时这里必须仍是 true）。
	midnight := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) // 边界 = 2026-09-21T00:00:00Z
	require.True(t, hotRetentionDayBeyondWindow("2026-09-20", midnight, hotRetentionTestRetention),
		"边界整点时，前一天整天都在边界之前 ⇒ 必须判为超窗口")
	require.False(t, hotRetentionDayBeyondWindow("2026-09-21", midnight, hotRetentionTestRetention),
		"边界整点时，边界所在的当天仍跨在窗口上 ⇒ 不得判为超窗口（不能误伤）")

	// retention 大于整个数据历史：任何天都不超窗口（保守方向）。
	huge := 100 * 365 * 24 * time.Hour
	require.False(t, hotRetentionDayBeyondWindow("1970-01-01", now, huge))
}

// ── ② 重建面：超窗口的天不写、不校验、不算已发布（跨越边界的天必须照写）────────────

// TestRebuildDaysSkipsBeyondHotRetentionWindow 固定「重建只写窗口内的天」。
//
// **为什么它能转红**：把窗口判定去掉（恒 false）⇒ 超窗口的天被写进 VL（`payloads` 里出现
// "beyond window"、`out.Days` 含该天、`out.BeyondHotRetention` 为空）⇒ 三条断言同时失败。
// 反过来，若判据过宽（把跨越边界的天也算成超窗口）⇒ 少写一天、"straddling boundary" 缺失 ⇒ 转红。
func TestRebuildDaysSkipsBeyondHotRetentionWindow(t *testing.T) {
	now := time.Now()
	beyondDay, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)
	require.NotEqual(t, boundaryDay, inWindowDay)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	events := []logtypes.Event{
		hotRetentionTestEvent(beyondDay, 1, 10, "beyond window"),
		hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary"),
		hotRetentionTestEvent(inWindowDay, 20, 30, "inside window"),
	}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	require.Equal(t, 3, fixture.insertCount(), "首次写入按 UTC 天分三次插入")

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
		// 本用例要断言「三天里究竟写了哪几天」，故把按天切片预算放大到一轮跑完；
		// 默认 1 天/轮会让第二天留到下一轮，测到的就不是「过滤」而是「切片」。
		ReplayBudget: ReplayBudget{MaxDays: 8},
		HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 模拟生产形态：这三天在热层都查不到（超保留期天已被 VL 丢弃，其余按「需重建」处理）。
	fixture.deleteDay(beyondDay)
	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)
	insertsBefore := fixture.insertCount()

	ok, reason := m.scheduleRebuildAsync(source, []string{beyondDay, boundaryDay, inWindowDay}, "hot_window_test")
	require.True(t, ok, "登记必须被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))

	// ① 只有窗口内的两天被写入（超窗口的天一次都没写）。
	payloads := fixture.payloadsSince(insertsBefore)
	require.Len(t, payloads, 2, "只应重建窗口内的两天（超窗口的天不写、不校验）")
	joined := strings.Join(payloads, "\n")
	require.NotContains(t, joined, "beyond window",
		"超出热层保留期的天绝不能被写入：VL 会返回 200 却静默丢弃（纯浪费 + 假成功）")
	require.Contains(t, joined, "straddling boundary",
		"跨越保留边界的当天必须仍按可发布处理（判据不得误伤部分可见的天）")
	require.Contains(t, joined, "inside window")

	// ② 结论如实：超窗口的天进 BeyondHotRetention，绝不进 Days（那等于宣称已发布）。
	out, hasOut := m.RebuildLastResult(key)
	require.True(t, hasOut)
	require.NoError(t, out.Err)
	require.False(t, out.Pending, "本轮全部窗口内天都已发布，不应是「有界未完成」")
	require.Equal(t, []string{beyondDay}, out.BeyondHotRetention, "被跳过的天必须如实记录")
	require.Equal(t, []string{boundaryDay, inWindowDay}, out.Days, "只有窗口内的天算已发布")
	require.Zero(t, m.RebuildStats().Failed, "跳过不等于失败")
}

// ── ③ 对账：超窗口的天不得计入缺失天（否则每轮都重新登记重建）────────────────────

// TestReconcileExcludesBeyondHotRetentionDays 固定「对账不把超窗口的天判成缺失」。
//
// **为什么它能转红**：把窗口判定去掉（恒 false）⇒ 超窗口那天照旧查询，`observed(0) <
// expected(1)` ⇒ 进入 `MissingDays` ⇒ 断言失败（这正是「缺失天永远存在 ⇒ 永远重新登记重建」）。
func TestReconcileExcludesBeyondHotRetentionDays(t *testing.T) {
	now := time.Now()
	beyondDay, _, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	events := []logtypes.Event{
		hotRetentionTestEvent(beyondDay, 1, 10, "beyond window"),
		hotRetentionTestEvent(inWindowDay, 10, 20, "inside window"),
	}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 超窗口的天在热层「查不到」（VL 已按 retention 丢弃）；窗口内的天完好可见。
	fixture.deleteDay(beyondDay)
	insertsBefore := fixture.insertCount()

	expectation := reconcileExpectation{
		Days:   []string{beyondDay, inWindowDay},
		Counts: map[string]int{beyondDay: 1, inWindowDay: 1},
	}
	report := m.reconcileWithExpectation(context.Background(), source, key, expectation, m.reconcile)

	require.False(t, report.Fallback, "对账可用时不得回退整窗重发：%s", report.FallbackReason)
	require.Empty(t, report.MissingDays,
		"超窗口的天不得计入缺失天（否则每轮对账都重新登记重建，永远追不上）")
	require.Equal(t, []string{beyondDay}, report.BeyondHotRetention,
		"被跳过的天必须在报告上如实体现（不能静默丢弃，更不能当成「一致」）")
	require.Equal(t, 1, report.Queries, "超窗口的天不应再发注定 0 行的 count 查询")
	require.Equal(t, insertsBefore, fixture.insertCount(), "对账本身不写 VL")
}

// ── ④ 终态：全部超窗口 ⇒ 清 pending、缺口继续可见、不落成失败/放弃 ──────────────────

// TestRebuildAllDaysBeyondHotRetentionSettlesPending 固定「全部天超窗口」的终态。
//
// 三种错误实现都会让它转红：
//   - 不做窗口过滤（恒 false）⇒ 该天被写、校验必然失败 ⇒ `out.Err` 非空且 pending 不被收口；
//   - 过滤了但不清 pending ⇒ `PublicationPending` 仍为 true（永久待定，状态机卡死）；
//   - 过滤后用「整窗完成」的尾部收口（resolveGaps/releaseRecovery）⇒ 缺口被消解 ⇒ 缺口断言转红。
func TestRebuildAllDaysBeyondHotRetentionSettlesPending(t *testing.T) {
	now := time.Now()
	beyondDay, _, _ := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	events := []logtypes.Event{hotRetentionTestEvent(beyondDay, 1, 10, "lost beyond window")}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 生产形态：该天在热层查不到（VL 已按 retention 丢弃），账本却登记着「发布待定」。
	fixture.deleteDay(beyondDay)
	m.mu.Lock()
	pendingState := m.state.Sources[key]
	pendingState.PublicationPending = true
	m.state.Sources[key] = pendingState
	m.mu.Unlock()

	// 该天范围内放一个「可被投递证明消解」的缺口：若收口走了 resolveGaps/releaseRecovery，
	// 它会被当成本次写入已覆盖而消解掉 —— 那正是数据诚实性红线。
	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pipe.Ledger().Ensure(pipe.Key(), logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	})
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 1, 10, ledger.GapReasonDeliverError, "超窗口天的缺口夹具"))
	gapsBefore := pipe.Ledger().UnresolvedGapCount(pipe.Key())
	require.Equal(t, 1, gapsBefore)

	insertsBefore := fixture.insertCount()
	okSchedule, reason := m.scheduleRebuildConfirmPendingAsync(source, []string{beyondDay}, "pending_unconfirmed")
	require.True(t, okSchedule, "登记必须被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))

	// ① 零写入：超窗口的天一次都不能写（写了也只有「200 + 静默丢弃」的假成功）。
	require.Equal(t, insertsBefore, fixture.insertCount(),
		"超窗口的天绝不能被写入")

	// ② PublicationPending 必须收口（否则永久为真 ⇒ 重建被无限重排）。
	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.False(t, pending, "全部天超窗口时必须清掉 PublicationPending，否则状态机卡死")

	// ③ 结论如实：既不宣称已发布，也不把「热层永远看不见」当成失败。
	out, hasOut := m.RebuildLastResult(key)
	require.True(t, hasOut)
	require.NoError(t, out.Err, "跳过不是失败：不得把「热层永远看不见」当成重建失败")
	require.False(t, out.Pending)
	require.Empty(t, out.Days, "超窗口的天绝不能被记成「已发布」")
	require.Equal(t, []string{beyondDay}, out.BeyondHotRetention, "被跳过的天必须如实记录")

	// ④ 缺口继续对用户可见：绝不 resolveGaps / releaseRecovery。
	require.Equal(t, gapsBefore, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"超窗口天的缺口必须继续保留在账本中（不得当作已解决）")

	// ⑤ 不落成失败/放弃：否则又会走到「已停止自动重试」那条刷屏路径。
	require.Zero(t, m.RebuildStats().Failed, "跳过不得计入失败台账")
	require.Equal(t, int64(1), m.RebuildStats().Completed)
	_, abandoned := m.RebuildAbandonedReason(key)
	require.False(t, abandoned, "跳过不得让源进入「已放弃自动重试」")
}

// TestMixedRebuildKeepsBeyondWindowGapsVisible 钉住「部分跳过」时的尾部收口语义。
//
// 为什么必须单独一条：全部天超窗口时走的是提前收尾分支（`finishRebuildBeyondHotRetention`），
// 它压根到不了函数尾部；而**混合请求**（窗口内 + 超窗口）会继续往下跑到尾部，那里有
// `resolveGapsLandedByDelivery` + `releaseRecovery`。这正是本次修复**唯一改变**的语义边界：
// 改动前，混合请求会在超窗口天校验失败处早退，永远到不了尾部；改动后能到。
//
// 红线（releaseRecovery 注释里的历史教训）：`releaseRecovery` 会把 WAL 责任转交给投影，
// 随后 `tryReclaimLocked` 按段状态推进 Reclaim ⇒ `pruneReclaimed` 丢掉 `Record.End <= Reclaim`
// 的条目。若为**从未在热层发布**的超窗口天建段，等于宣称「已有副本」，将来 eventstore 一旦
// 轮转/清理，那段数据就真没了。
//
// 本用例的判据：超窗口天的缺口必须仍然可见（不能被本次「部分写入」捎带消解），
// 且超窗口天绝不进 out.Days（那等于宣称已发布）。
func TestMixedRebuildKeepsBeyondWindowGapsVisible(t *testing.T) {
	now := time.Now()
	beyondDay, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	// 位置区间刻意让超窗口天**排在最前**（1..10），窗口内天在后（10..30）：
	// 这样 `events[len-1].Record.End`（=releaseRecovery 的 to）落在窗口内天，
	// 若实现不区分天就建段，超窗口天会被一并纳入 [from, to] 覆盖。
	beyondEvent := hotRetentionTestEvent(beyondDay, 1, 10, "lost beyond window")
	boundaryEvent := hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary")
	inWindowEvent := hotRetentionTestEvent(inWindowDay, 20, 30, "inside window")
	events := []logtypes.Event{beyondEvent, boundaryEvent, inWindowEvent}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
		ReplayBudget: ReplayBudget{MaxDays: 8},
		HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 生产形态：三天在热层都查不到 ⇒ 对账会判三天全缺失。
	fixture.deleteDay(beyondDay)
	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)

	// 给**超窗口天**的区间 (1,10) 放一个投递错误缺口：它只能靠「该区间确实投递成功」消解。
	// 若尾部收口把本次写入面误认成覆盖了超窗口天，这个缺口就会被消解 ⇒ 断言转红。
	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pipe.Ledger().Ensure(pipe.Key(), logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	})
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 1, 10, ledger.GapReasonDeliverError, "超窗口天缺口夹具"))
	gapsBefore := pipe.Ledger().UnresolvedGapCount(pipe.Key())
	require.Equal(t, 1, gapsBefore)
	positionsBefore, okPos := pipe.Ledger().Positions(pipe.Key())
	require.True(t, okPos)
	reclaimBefore := positionsBefore.Reclaim

	insertsBefore := fixture.insertCount()
	okSchedule, reason := m.scheduleRebuildAsync(source, []string{beyondDay, boundaryDay, inWindowDay}, "mixed_window_test")
	require.True(t, okSchedule, "登记必须被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))

	// ① 窗口内的两天被写入，超窗口天一次都没写。
	joined := strings.Join(fixture.payloadsSince(insertsBefore), "\n")
	require.NotContains(t, joined, "lost beyond window",
		"超窗口天绝不能被写入（200 + 静默丢弃 = 假成功）")
	require.Contains(t, joined, "straddling boundary", "跨越边界当天必须仍按可发布处理")

	// ② 结论如实分流：超窗口天只进 BeyondHotRetention。
	out, hasOut := m.RebuildLastResult(key)
	require.True(t, hasOut)
	require.NoError(t, out.Err)
	require.Equal(t, []string{beyondDay}, out.BeyondHotRetention)
	require.NotContains(t, out.Days, beyondDay, "超窗口天绝不能被记成「已发布」")
	require.Contains(t, out.Days, boundaryDay)
	require.Contains(t, out.Days, inWindowDay)

	// ③ 红线：超窗口天的缺口必须仍然可见 —— 部分写入不得捎带消解它。
	require.Equal(t, gapsBefore, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"混合请求下，超窗口天的缺口绝不能被本次「部分写入」消解")

	// ④ 红线：回收责任不得越过「从未在热层发布」的区间。
	//
	// 判据取 Reclaim 是否前移。超窗口天的区间 (1,10) 从未发布，故 Reclaim 不得越过 10。
	// （若实现为超窗口天也建 recovery 段，`tryReclaimLocked` 会把它推到覆盖末端 ⇒ 转红。）
	positionsAfter, okPos2 := pipe.Ledger().Positions(pipe.Key())
	require.True(t, okPos2)
	reclaimAfter := positionsAfter.Reclaim
	require.LessOrEqual(t, reclaimAfter, uint64(10),
		"回收不得越过从未在热层发布的超窗口天区间（reclaim %d → %d）", reclaimBefore, reclaimAfter)
}
