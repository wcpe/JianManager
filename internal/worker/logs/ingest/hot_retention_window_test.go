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

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
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
	require.Equal(t, 1, m.drainRebuilds(context.Background())) // drain 数

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
	require.Equal(t, 1, m.drainRebuilds(context.Background())) // drain 数

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
		Sources:      []SourceConfig{source},
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

	// **关键夹具**：把 Delivery 顶到权威全量末端（=30），让 `releaseRecovery` 的
	// `positions.Delivery < to` 闸门**放行**。
	//
	// 为什么必须这样造：生产上受影响的源正是 `delivery == read`（这些天一个月前确实
	// 投递成功过），闸门在真实场景里**挡不住**。若不顶高 Delivery，本用例会因为闸门
	// 早退而「平凡通过」——那样的断言咬不住「为超窗口天建副本证明」这个真实缺陷。
	require.NoError(t, pipe.WAL().RecordHTTPResult(acquire.DeliveryResult{
		StartPos: 0, EndPos: 30, HTTPStatus: 200,
	}))
	positionsBefore, okPos := pipe.Ledger().Positions(pipe.Key())
	require.True(t, okPos)
	require.GreaterOrEqual(t, positionsBefore.Delivery, uint64(30),
		"夹具前提：Delivery 必须已达权威全量末端，否则 releaseRecovery 会因闸门早退（平凡通过）")
	reclaimBefore := positionsBefore.Reclaim

	insertsBefore := fixture.insertCount()
	okSchedule, reason := m.scheduleRebuildAsync(source, []string{beyondDay, boundaryDay, inWindowDay}, "mixed_window_test")
	require.True(t, okSchedule, "登记必须被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background())) // drain 数

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

// TestNonConfirmPathDoesNotClearPendingForBeyondWindowOnly 钉住「非确认路径不得清 pending」。
//
// 为什么必须单独一条：非确认路径（`reconcile_missing` 走 `scheduleRebuildAsync`，
// `confirmPending=false`）登记的是对账算出的缺失天，不经发布确认取证。若这种请求的天
// 恰好全落在超窗口内，就不能把 pending 归因给它们——pending 可能来自**窗口内某天的真实
// 写入失败**，清掉等于把「不确定」谎报成「确定」（数据诚实性红线）。
//
// 与 `TestRebuildAllDaysBeyondHotRetentionSettlesPending`（确认路径）互为对照：
// 两条路径对同一个「全部超窗口」形态必须有**不同**的 pending 处置。
func TestNonConfirmPathDoesNotClearPendingForBeyondWindowOnly(t *testing.T) {
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

	// pending 置真，模拟「窗口内某天的真实写入失败」留下的发布待定。
	fixture.deleteDay(beyondDay)
	m.mu.Lock()
	st := m.state.Sources[key]
	st.PublicationPending = true
	m.state.Sources[key] = st
	m.mu.Unlock()

	// **非确认路径**：scheduleRebuildAsync ⇒ confirmPending=false。
	okSchedule, reason := m.scheduleRebuildAsync(source, []string{beyondDay}, "reconcile_missing")
	require.True(t, okSchedule, "登记必须被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background())) // drain 数

	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pending,
		"非确认路径下，全部超窗口的天不得清 PublicationPending（可能掩盖窗口内天的真实失败）")

	// 如实记账仍然要有：跳过必须可见，不能静默。
	out, hasOut := m.RebuildLastResult(key)
	require.True(t, hasOut)
	require.NoError(t, out.Err, "跳过不是失败")
	require.Equal(t, []string{beyondDay}, out.BeyondHotRetention, "被跳过的天必须如实记录")
	require.Empty(t, out.Days, "超窗口的天绝不能被记成「已发布」")
}

// TestReplacePlanSkipsBeyondHotRetention 钉住**整窗重发写入面**（`writeProjectionPlan` 的
// `plan.Replace` 分支）同样跳过超窗口天。
//
// 这是部署后真机实测发现的残留：过滤此前只加在 `rebuildDaysInner`，而整窗重发走
// `writeProjectionPlan` 的 `plan.Replace`（原实现 `writeDays = days`，即权威全量），
// 于是超窗口天照写 ⇒ 校验恒 `not fully visible` ⇒ 末尾清 pending 的代码永远到不了
// ⇒ `PublicationPending` 永远清不掉。真机形态：inst:327 在重建侧已正确跳过，却在
// deliver 侧反复报 `projection-389 projection verify failed after write`。
//
// 为什么直接测 `writeProjectionPlan` 而不测 `deliver`：`deliver` 内部有「发布确认快路径」
// 与 tail 判定，夹具很难在不依赖内部状态的前提下稳定命中 Replace 分支；而**被本修复
// 改动的就是 Replace 分支本身**，直接测它既精确又不脆弱。
//
// 为什么它能转红：去掉 Replace 分支的窗口过滤 ⇒ 超窗口天被写入（payloads 出现
// "beyond window"）⇒ 第一条断言失败。
func TestReplacePlanSkipsBeyondHotRetention(t *testing.T) {
	now := time.Now()
	beyondDay, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

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
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)

	// 生产形态：三天在热层都查不到（超窗口天已被 VL 按 retention 丢弃）。
	fixture.deleteDay(beyondDay)
	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)
	insertsBefore := fixture.insertCount()

	// 整窗重发：Write 是权威全量（含超窗口天），Replace=true。
	_, _, err = m.writeProjectionPlan(source, events, "projection-replace-1",
		projectionWritePlan{Write: events, Replace: true})
	require.NoError(t, err, "整窗重发必须成功（超窗口天被跳过，不应因校验失败而报错）")

	joined := strings.Join(fixture.payloadsSince(insertsBefore), "\n")
	require.NotContains(t, joined, "beyond window",
		"整窗重发绝不能再写超窗口天（VL 返回 200 却静默丢弃 ⇒ 校验恒失败 ⇒ pending 永远清不掉）")
	require.Contains(t, joined, "straddling boundary", "跨越边界当天必须仍按可发布处理")
	require.Contains(t, joined, "inside window", "窗口内的天必须照常写入")
}

// TestDeliverEvidenceFaceExcludesUnwrittenDays 钉住「`deliver` 自动消解缺口时，证据面必须等于
// **真正写进去的**事件」，而不是「计划写入面（权威全量）」。
//
// 现场缺陷（2026-10-21 真机，本轮最严重的一处）：`inst:317` 的权威全量跨 10-12～10-21
// （`record_end` 到 7585609），热层过滤后实际只写了 09-21 一天（5492 条、`record_end` 到 ~955K）。
// 旧代码用 `evidence = expected`（权威全量）做消解证据，于是区间 `[7583541, 7585609)`
// （10-21 那 2068 字节）被 `CoveredByPositionRanges` 判为「已覆盖」，缺口被写成
// `resolution="delivery confirmed for this range"` —— 而 VL 里该源最新只到 7583541（实测为空）。
// 随后 `reclaim` 被推到 `read_pos`，这笔欠账再没有任何信号。
//
// 更值得记住的是：它是**修热层窗口时引入的**。修之前 09-20 超窗口 ⇒ 校验失败 ⇒
// `writeProjectionPlan` 返回 err ⇒ 走不到消解（响亮的失败）；修之后该天被跳过、其余天写成功
// ⇒ 走到消解，反而把「投递失败」改写成「已确认投递」（静默的成功）。
//
// **为什么必须走 `deliver` 而不是直连 `writeProjectionPlan`**：证据面是在 `deliver` 里构造的。
// 直连写入函数再自己调 `resolveGapsLandedByDelivery` 测的是「我传什么就被消解什么」，
// 完全绕过了被测逻辑 —— 这正是本用例第一版「变异不转红」的原因。
func TestDeliverEvidenceFaceExcludesUnwrittenDays(t *testing.T) {
	now := time.Now()
	beyondDay, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

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

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 生产形态：数据在热层查不到 + 账本写着「发布待定」⇒ 走整窗重发（replace=true）。
	fixture.deleteDay(beyondDay)
	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)
	m.mu.Lock()
	st := m.state.Sources[key]
	st.PublicationPending = true
	m.state.Sources[key] = st
	m.mu.Unlock()

	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pipe.Ledger().Ensure(pipe.Key(), logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	})
	// **缺口放在窗口内的天**（区间 10..20 = boundaryDay）：这正是生产的真实形态。
	// 之前的用例把缺口放在超窗口天（1..10），只能证明「过期天不被消解」，
	// 而生产缺失的那天（inst:317 的 2026-10-21）**在窗口内** —— 判别维度必须是
	// 「VL 查不查得到」，不是「这天是否过期」。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 10, 20, ledger.GapReasonDeliverError, "窗口内天缺口夹具"))
	gapsBefore := pipe.Ledger().UnresolvedGapCount(pipe.Key())
	require.Equal(t, 1, gapsBefore, "夹具前提：缺口必须存在且未解决")

	result, err := m.deliver(source, events, false)
	require.Error(t, err, "本地重复但当前VL全部缺失时，必须拒绝虚假204并交预算化恢复")
	require.NotEqual(t, 204, result.HTTPStatus)
	require.Positive(t, m.RebuildStats().Scheduled)

	// 核心断言：未实际写入的窗口不能由本地正文消解，超窗口天也不例外。
	require.Equal(t, gapsBefore, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"窗口内天若在 VL 查不到，其缺口绝不能被「本地段已有」的证据面消解")
}

// TestEvidenceFaceEqualsActuallyWrittenEvents 钉住「自动消解缺口的证据面必须等于**真正写进去的**
// 事件」，而不是「计划写入面」。
//
// 现场缺陷（2026-10-21 真机，最严重的一处）：`inst:317` 的权威全量跨 10-12～10-21
// （`record_end` 到 7585609），热层过滤后实际只写了 09-21 一天（5492 条、`record_end` 到 ~955K）。
// 旧代码用 `evidence = expected`（权威全量）做消解证据，于是区间
// `[7583541, 7585609)`（10-21 那 2068 字节）被 `CoveredByPositionRanges` 判为「已覆盖」，
// 缺口被写成 `resolution="delivery confirmed for this range"`；而 VL 里该源最新只到 7583541。
// 随后 `reclaim` 被推到 `read_pos`，这笔欠账再没有任何信号。
//
// 更值得记住的是：这个缺陷是**修热层窗口时引入的**。修之前 09-20 超窗口 ⇒ 校验失败 ⇒
// `writeProjectionPlan` 返回 err ⇒ 走不到消解那一步（响亮的失败）；修之后该天被跳过、
// 剩下的天写成功 ⇒ 走到消解，反而把「投递失败」的缺口改写成了「已确认投递」（静默的成功）。
//
// 判据：写入面只含某天时，落在**其他天**区间上的缺口绝不能被消解。
func TestEvidenceFaceEqualsActuallyWrittenEvents(t *testing.T) {
	now := time.Now()
	beyondDay, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	// 三天：超窗口天排最前（位置 1..10），窗口内两天在后（10..30）。
	events := []logtypes.Event{
		hotRetentionTestEvent(beyondDay, 1, 10, "beyond window"),
		hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary"),
		hotRetentionTestEvent(inWindowDay, 20, 30, "inside window"),
	}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pipe.Ledger().Ensure(pipe.Key(), logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	})
	// 在**超窗口天**的区间 (1,10) 上放一个投递错误缺口。该区间本次不会被写入
	// ⇒ 绝不能被消解（它从未落库，VL 里不可能有）。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 1, 10, ledger.GapReasonDeliverError, "超窗口天缺口夹具"))
	gapsBefore := pipe.Ledger().UnresolvedGapCount(pipe.Key())
	require.Equal(t, 1, gapsBefore, "夹具前提：缺口必须存在且未解决")

	fixture.deleteDay(beyondDay)
	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)

	// 直接驱动写入计划（Replace=true 会让 Write 扩展为权威全量，正好复刻整窗重发形态）。
	plan := projectionWritePlan{Write: events, Replace: true}
	result, written, err := m.writeProjectionPlan(source, events, "projection-evidence-1", plan)
	require.NoError(t, err)
	require.NotZero(t, result.HTTPStatus)

	// ① `Written` 必须只含**真正写入的天**（超窗口天被跳过）。
	writtenDays := make(map[string]struct{}, 3)
	for _, ev := range written {
		day, derr := eventUTCDay(source, ev)
		require.NoError(t, derr)
		writtenDays[day] = struct{}{}
	}
	require.NotContains(t, writtenDays, beyondDay,
		"超窗口天不得出现在「真正写入」集合里（它被跳过、一次都没写）")
	require.Contains(t, writtenDays, inWindowDay, "窗口内的天必须被写入")

	// ② 红线：用这个证据面消解时，超窗口天的缺口必须仍然可见。
	m.resolveGapsLandedByDelivery(source, written)
	require.Equal(t, gapsBefore, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"窗口内天若在 VL 查不到，其缺口绝不能被「本地段已有」的证据面消解")

	// ③ 下游现在还会独立核对当前VL；即使调用方误传权威全量，也不能洗掉未写区间。
	m.resolveGapsLandedByDelivery(source, events)
	require.Equal(t, gapsBefore, pipe.Ledger().UnresolvedGapCount(pipe.Key()), "权威正文不是逐字段可见性凭据")
	// ④ 真正补回该天并发布后才允许消解，避免把保守拒绝实现成永远不解。
	m.hotRetention = 0
	_, restored, err := m.writeProjectionPlan(source, events, "projection-evidence-2", projectionWritePlan{Write: events[:1]})
	require.NoError(t, err)
	require.Len(t, restored, 1)
	m.resolveGapsLandedByDelivery(source, restored)
	require.Zero(t, pipe.Ledger().UnresolvedGapCount(pipe.Key()), "真实写入、逐字段校验并发布后才取得消解资格")
}

// TestDeliverEvidenceFaceResolvesWhenDataIsActuallyInVL 是上一条用例的**正向配对**。
//
// 为什么必须有这一条：只测「查不到 ⇒ 不消解」，无法区分「判定正确」与「永远不消解」。
// 若 `landedVisibleInVL` 因任何原因（时间窗夹错天、代次白名单取错、过度保守）
// 恒返回空集，反向用例依然全绿，而生产上缺口会永久滞留 ⇒ 阻塞 `ResumeAcquire`
// ⇒ 源被暂停（这正是 `gap_autoresolve.go` 文件头描述的 13 小时零数据形态）。
//
// 本用例让缺口事件**确实存在于 VL**（不删 fixture 数据），断言缺口被正常消解。
// 它同时钉住一个具体缺陷：`verifyProjectionQuery` 会用 `utcDayBounds(source.UTCDay)`
// 把时间窗夹在「源当前天」内，而缺口常属**别的天** ⇒ 若不把 `UTCDay` 设成事件所属天，
// 时间窗就落在错误的一天、恒查不到 ⇒ 本用例转红。
func TestDeliverEvidenceFaceResolvesWhenDataIsActuallyInVL(t *testing.T) {
	now := time.Now()
	_, boundaryDay, _ := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	// 注意：源的 UTCDay 是**今天**，而事件属于 boundaryDay（30 天前）——
	// 两者不同正是本用例要覆盖的形态（生产上缺口也几乎总在历史天）。
	require.NotEqual(t, source.UTCDay, boundaryDay, "夹具前提：源当前天必须与事件所属天不同")
	events := []logtypes.Event{hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary")}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pipe.Ledger().Ensure(pipe.Key(), logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	})
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 10, 20, ledger.GapReasonDeliverError, "可见数据缺口夹具"))
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()), "夹具前提：缺口存在且未解决")

	// **不删 fixture**：数据确实在 VL 里 ⇒ 该缺口应被正常消解。
	_, err = m.deliver(source, events, false)
	require.NoError(t, err)

	require.Equal(t, 0, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"数据确实在 VL 里时，缺口必须被消解（否则缺口永久滞留 ⇒ 阻塞采集恢复）")
	require.Greater(t, fixture.insertCount(), 0, "夹具前提：VL 里必须有数据")
}

// TestWriteFaceEntirelyBeyondRetentionMustFail 钉住「写入面被过滤为空时**不得**宣称成功」。
//
// 为什么这是红线（2026-10-21 复审发现的回归）：`writeDays` 被过滤为空后，按天循环一次
// 都不跑（`written == nil`），而函数末尾仍会把 `PublicationPending` 清成 false 并返回
// nil 错误。于是 `deliver` 认为投递成功 ⇒ `acquire.Pipeline` 调 `RecordHTTPResult`
// 推进 `Delivery` 水位 ⇒ `releaseRecovery` 的 `Delivery >= to` 闸门放行 ⇒ 为**从未写入的
// 区间**建副本证明 ⇒ `reclaim` 越过它 ⇒ `WAL.pruneReclaimed` 删条目 ⇒ 数据彻底消失，
// 而账本写着「有副本」。
//
// 可达形态：源暂停 ≥ retention(30d) 后 `DeliverPending` 重放一个月前的 durable 条目，
// 这批旧天会被整段过滤为空。加过滤之前它会去写入并校验失败（响亮的失败），
// 加了过滤却没加这道闸，就变成「静默的成功」。
//
// 变异验证：去掉 `len(writeDays)==0 && len(events)>0` 那段闸 ⇒ 本用例转红
// （err 变 nil、`PublicationPending` 被清成 false）。
func TestWriteFaceEntirelyBeyondRetentionMustFail(t *testing.T) {
	now := time.Now()
	beyondDay, _, _ := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	// 只造**超窗口天**的事件：整批都会被过滤掉。
	events := []logtypes.Event{hotRetentionTestEvent(beyondDay, 1, 10, "beyond window only")}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	fixture.deleteDay(beyondDay)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	insertsBefore := fixture.insertCount()
	_, written, err := m.writeProjectionPlan(source, events, "projection-allbeyond-1",
		projectionWritePlan{Write: events, Replace: true})

	require.ErrorIs(t, err, errProjectionBeyondHotRetention,
		"写入面全部超窗口时必须返回哨兵错误，绝不能返回 nil（nil 会让上游推进投递水位）")
	require.Empty(t, written, "没有写入任何事件")
	require.Equal(t, insertsBefore, fixture.insertCount(), "确实一次都没写")

	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pending,
		"必须保持 PublicationPending=true：什么都没写就不能宣称已发布")
}

// TestConfirmVerdictDistinguishesInconclusiveFromMeasured 钉住三态语义可分。
//
// **为什么这个区分是重建饥饿的根治点**（2026-10-21）：旧实现是两态 `(bool, error)`，
// 把「取证没跑完」与「无需确认」混成同一个 false；上层因此退回「登记时的全量天清单」
// （升序）+ 默认预算 1 天/轮 ⇒ **每轮重写最老那一天**。若最老那天跨热层保留边界
// （判据按整天算、仍被当成可发布），它早于边界的事件永远写不进 VL ⇒ 永远校验失败
// ⇒ 永久饥饿（生产实证 `published=['2026-09-21'] remaining_n=15` 连续六轮不变）。
//
// 本用例不制造在飞写入（那会让 `drainRebuilds` 等待而超时），而是直接验证
// 「无待确认发布」与「已完整确认」这两种常见情形不会退化成 inconclusive，
// 以及三态枚举本身互不相等——保证上层能据此分支。
func TestConfirmVerdictDistinguishesInconclusiveFromMeasured(t *testing.T) {
	require.NotEqual(t, confirmConfirmed, confirmMissingMeasured)
	require.NotEqual(t, confirmConfirmed, confirmInconclusive)
	require.NotEqual(t, confirmMissingMeasured, confirmInconclusive,
		"三态必须互不相等：否则「取证未完成」会被上层误当「已测出缺失」或「无需确认」")

	now := time.Now()
	_, boundaryDay, _ := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	events := []logtypes.Event{hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary")}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 情形一：本就无需确认（pending=false）⇒ 不得报 inconclusive。
	verdict, err := m.tryConfirmPendingPublicationVerdict(context.Background(), source, key,
		m.state.Sources[key], nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, confirmInconclusive, verdict,
		"没有待确认发布时不得报「取证未完成」（那会让上层空转）")
}

// TestReleaseRecoveryBlockedWhenAuthoritativeHasBeyondWindowDays 钉住「护栏判据必须对
// 权威全量求，不能只看 req.days」。
//
// 漏洞形态（2026-10-21 复审发现）：`beyondWindow` 是从 `req.days` 推的，而对账**早已把
// 超窗口天剔出缺失面**。于是一个「有超窗口天（从未发布）+ 有窗口内天（真缺失）」的源：
//
//	· `req.days` 只剩窗口内天 ⇒ `beyondWindow` 为空 ⇒ 护栏不触发；
//	· 而 `releaseRecovery` 拿的是**权威全量**，其 `to = 权威全量末端`、`from = Reclaim`，
//	  建的是**单个连续区间** ⇒ 超窗口天（位置更早）必然落在区间内
//	  ⇒ 为「从未在热层发布」的区间建副本证明 ⇒ reclaim 越过后 WAL 条目被剪 ⇒ 数据消失。
//
// 本用例：权威全量含 beyondDay 与 inWindowDay，但只登记 inWindowDay 重建。
// 断言 `Reclaim` 不得越过 beyondDay 的区间上界。
//
// 变异验证：把护栏判据改回只看 `beyondWindow` ⇒ 本用例转红（Reclaim 越界）。
func TestReleaseRecoveryBlockedWhenAuthoritativeHasBeyondWindowDays(t *testing.T) {
	now := time.Now()
	beyondDay, _, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	// 权威全量：超窗口天在前（位置 1..10），窗口内天在后（20..30）。
	events := []logtypes.Event{
		hotRetentionTestEvent(beyondDay, 1, 10, "beyond window"),
		hotRetentionTestEvent(inWindowDay, 20, 30, "inside window"),
	}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources:      []SourceConfig{source},
		ReplayBudget: ReplayBudget{MaxDays: 8},
		HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	// 夹具前提：把 Delivery 顶到权威全量末端（=30），让 releaseRecovery 的
	// `positions.Delivery < to` 闸门**放行**。生产上受影响的源正是 delivery == read，
	// 闸门在真实场景里挡不住；不顶高就会「平凡通过」（被闸门早退，测不到本缺陷）。
	require.NoError(t, pipe.WAL().RecordHTTPResult(acquire.DeliveryResult{
		StartPos: 0, EndPos: 30, HTTPStatus: 200,
	}))
	positionsBefore, okPos := pipe.Ledger().Positions(pipe.Key())
	require.True(t, okPos)
	require.GreaterOrEqual(t, positionsBefore.Delivery, uint64(30),
		"夹具前提：Delivery 必须已达权威全量末端，否则闸门会早退（平凡通过）")

	fixture.deleteDay(beyondDay)
	fixture.deleteDay(inWindowDay)

	// **只登记窗口内那天**（复刻「对账已剔除超窗口天」的真实形态）。
	okSchedule, reason := m.scheduleRebuildAsync(source, []string{inWindowDay}, "reconcile_missing")
	require.True(t, okSchedule, "登记必须被接受：%s", reason)
	require.Equal(t, 1, m.drainRebuilds(context.Background())) // drain 数

	// 红线：回收不得越过从未在热层发布的超窗口天区间。
	positionsAfter, okPos2 := pipe.Ledger().Positions(pipe.Key())
	require.True(t, okPos2)
	require.LessOrEqual(t, positionsAfter.Reclaim, uint64(10),
		"权威全量含超窗口天时，reclaim 不得越过它（reclaim %d → %d）",
		positionsBefore.Reclaim, positionsAfter.Reclaim)
}

// TestDeliverEvidenceFaceTruncatedToWrittenWhenReplaceSpansBeyondWindow 钉住
// 「replace（整窗重发）路径下的证据面必须截断到**实际写入**的天，不能直接用权威全量」。
//
// 为什么必须单独测这条（2026-10-22 测试有效性复审 P0）：已有的
// `TestDeliverEvidenceFaceExcludesUnwrittenDays` 声称覆盖 `evidence = written`，
// 但实测它走的是 `deliver` 里 `len(toWrite)==0` 的**早退快路径**（本批事件已全在本地段里，
// 直接由 `landedVisibleInVL` 判可见性），根本没走到赋值点
// (`runtime.go` 的 `evidence = written`)：
//
//	· 把 `evidence = written` 删掉（退回 `evidence = expected`），它**依然通过**；
//	· ingest 包内所有 Gap/Evidence/Deliver/HotRetention/Rebuild/Projection 用例全绿。
//
// 即这条最严重真机缺陷的修复当时**零覆盖**。本用例补的就是那个缺口。
//
// 夹具关键：必须同时满足三件事，缺一不可。
//  1. `replace=true` —— 否则初始 evidence 是本批 toWrite，与 written 恰好相等，测不出差异。
//     （`PublicationPending=true` 会强制 replace，见 deliver 里的 `replace = replace || saved.PublicationPending`。）
//  2. `toWrite > 0` —— 否则早退快路径接管，赋值点根本执行不到。故这里**不**预置本地段，
//     让 `canonicalRecoveryEvents`/段比对判定为「需要写」。
//  3. 权威全量**跨出热层保留期** —— 只有这样 `writeProjectionPlan` 才会真的少写一天，
//     使 `written ⊊ expected`，两者才可区分。
//
// 断言口径：缺口落在**超窗口天**上。写入面不含该天 ⇒ 该缺口必须仍未被消解。
// 若把 `evidence = written` 改回 `evidence = expected`，超窗口天的区间会落进证据面
// ⇒ 缺口被 `delivery confirmed` 消解 ⇒ 本用例转红。
func TestDeliverEvidenceFaceTruncatedToWrittenWhenReplaceSpansBeyondWindow(t *testing.T) {
	now := time.Now()
	beyondDay, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	// 超窗口天排最前（位置 1..10）；窗口内两天在后（10..30）。
	events := []logtypes.Event{
		hotRetentionTestEvent(beyondDay, 1, 10, "beyond window"),
		hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary"),
		hotRetentionTestEvent(inWindowDay, 20, 30, "inside window"),
	}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 生产形态：VL 里查不到，**且本地段也判不出「已落库」** ⇒ 本批必须重写。
	// 只删 VL 不够：段存储仍持有这些事件，`deliver` 会认为无需重写而走
	// `len(toWrite)==0` 的早退快路径，那里的证据面由 `landedVisibleInVL` 决定，
	// 根本执行不到 `evidence = written` 这个赋值点（见用例头注释第 2 条）。
	fixture.deleteDay(beyondDay)
	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)
	// 清空该源的 WAL/段账 ⇒ `canonicalRecoveryEvents` 判不出「已落库」⇒ 本批全部进入 toWrite。
	// 不清的话 `deliver` 会认为事件已在本地段里而走 `len(toWrite)==0` 快路径。
	m.mu.Lock()
	cleared := m.state.Sources[key]
	cleared.WAL = nil
	cleared.Events = nil
	cleared.EventsStored = false
	cleared.EventsStoredThrough = 0
	m.state.Sources[key] = cleared
	m.mu.Unlock()
	m.mu.Lock()
	st := m.state.Sources[key]
	st.PublicationPending = true // 强制 replace（整窗重发）
	m.state.Sources[key] = st
	m.mu.Unlock()

	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	pipe.Ledger().Ensure(pipe.Key(), logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	})
	// 缺口落在**超窗口天**（1..10 = beyondDay）——写入面必然不含该天。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 1, 10, ledger.GapReasonDeliverError, "超窗口天缺口夹具"))
	gapsBefore := pipe.Ledger().UnresolvedGapCount(pipe.Key())
	require.Equal(t, 1, gapsBefore, "夹具前提：缺口必须存在且未解决")

	insertsBefore := fixture.insertCount()
	_, err = m.deliver(source, events, false)
	require.NoError(t, err, "整窗重发必须成功（超窗口天被跳过，其余天写入）")

	// **夹具前提守卫**：必须真的发生了写入，否则本用例会退化成 toWrite==0 的快路径，
	// 那就又变成「名字对、覆盖错」的假绿（正是被复审抓到的那条用例的形态）。
	require.Greater(t, fixture.insertCount(), insertsBefore,
		"夹具前提：本批必须真的写入 VL（>0），否则走 toWrite==0 快路径 ⇒ 覆盖不到 evidence=written")

	// 核心断言：超窗口天从未写入 ⇒ 它的缺口必须仍然可见。
	require.Equal(t, gapsBefore, pipe.Ledger().UnresolvedGapCount(pipe.Key()),
		"证据面必须截断到实际写入的天；用权威全量做证据会把从未落库的超窗口天标成已消解")
}

// 关于 `confirmInconclusive` 的分支覆盖：**这里刻意不放一个「看起来覆盖了」的用例**。
//
// 历史（2026-10-22 复审 P1）：曾有一个 TestConfirmVerdictInconclusiveBranches，文档写着
// 「用最小可控手段触发 ①②」，但函数体只剩一个 `pending=false ⇒ confirmed` 子用例 ——
// 那正是 `tryConfirmPendingPublicationVerdict` 的第一个早退分支，与 inflight /
// canonicalRecoveryEvents-空 **毫无关系**。把 8 个 `return confirmInconclusive` 中
// 任意一个改成 `confirmConfirmed`，它照样全绿。也就是说：声称覆盖，实际零覆盖，
// 而且因为名字和文档都在说「覆盖了」，比没有这个用例更危险（下一位复审者会跳过它）。
//
// 为什么不在单元层硬造这五条分支：
//   · `inflight != 0`：把 `publicationInflight[key]` 置 1 会让后台发布路径永久等待
//     （实测 600s 超时），该计数与真实写入生命周期绑定，不是可以单独篡改的纯状态；
//   · `canonicalRecoveryEvents` 为空 / `groupEventsByUTCDay` 失败：权威来源是**磁盘段
//     存储**（`storedCount > 0` 时以段为准，这是 2026-09-29 生产事故后刻意定下的），
//     要让它真空就得删数据文件，已超出单元测试能安全表达的范围。
//
// 取而代之，这里守的是**上层可观测的行为契约**：取证没跑完时一天都不写、
// 并如实返回 pending。见 TestRebuildWritesNothingWhenConfirmationInconclusive
// （它直接调 rebuildDaysInner，并已实测「去掉 if inconclusive 分支 ⇒ 转红」）。

// TestRebuildWritesNothingWhenConfirmationInconclusive 钉住「取证未完成 ⇒ 本轮一天都不写」。
//
// 这是「重建饥饿」根治点的上层行为（2026-10-21 修复，2026-10-22 复审确认 HEAD 上零覆盖）。
//
// 缺陷形态：旧实现的两态返回在「算出 missing 后、三道证据稳定性校验失败」时直接
// `return false, nil` —— **丢弃了已经算出的 missing**。上层于是退回「登记全量」，
// 而登记面按升序排、默认切片预算只有 1 天 ⇒ 每轮重写**最老那一天**；若那天跨热层
// 保留边界（判据按整天算，仍被当成可发布），它早于边界的事件永远写不进 VL ⇒ 永远
// 校验失败 ⇒ 永久饥饿（生产实证 remaining_n=15 连续六轮不变）。
//
// 三态化后：`inconclusive` 是**常态而非异常**，此时必须如实返回 pending 并**零写入**。
//
// **为什么直接调 `rebuildDaysInner`**：它是重建路径真正做决策的地方。走
// `scheduleRebuildAsync` 会先经过登记层的窗口过滤，等轮到 `rebuildDaysInner` 时
// 请求面往往已被清空 —— 那样无论有没有 `if inconclusive` 分支都是零写入，
// 变异测不出来（本用例第一版就是这么假绿的）。
//
// 变异验证：把 `if inconclusive { return rebuildOutcome{Pending: true} }` 去掉
// （退回「拿不到 missing 就按登记全量重写」）⇒ 本用例在「零写入」处转红。
func TestRebuildWritesNothingWhenConfirmationInconclusive(t *testing.T) {
	now := time.Now()
	_, boundaryDay, inWindowDay := hotRetentionTestDays(now, hotRetentionTestRetention)

	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	events := []logtypes.Event{
		hotRetentionTestEvent(boundaryDay, 10, 20, "straddling boundary"),
		hotRetentionTestEvent(inWindowDay, 20, 30, "inside window"),
	}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, ReplayBudget: ReplayBudget{MaxDays: 8},
		HotRetention: hotRetentionTestRetention,
	})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration

	fixture.deleteDay(boundaryDay)
	fixture.deleteDay(inWindowDay)
	m.mu.Lock()
	st := m.state.Sources[key]
	st.PublicationPending = true
	m.state.Sources[key] = st
	m.mu.Unlock()

	// 让取证走 inconclusive：把权威事件来源（磁盘段存储 + WAL + 内联）一起清空
	// ⇒ `canonicalRecoveryEvents` 返回空集合 ⇒ `tryConfirmPendingPublicationVerdict`
	// 走 inconclusive。这条分支**不会**死锁：它只影响取证结论，不参与任何等待。
	emptyStore, serr := eventstore.Open(t.TempDir())
	require.NoError(t, serr)
	m.mu.Lock()
	m.events = emptyStore
	m.eventsStoredRows = map[string]int{}
	m.eventsStoredEpoch = map[string]uint64{}
	cleared := m.state.Sources[key]
	cleared.WAL = nil
	cleared.Events = nil
	cleared.EventsStored = false
	cleared.EventsStoredThrough = 0
	m.state.Sources[key] = cleared
	m.mu.Unlock()

	// 直接构造确认路径的请求（confirmPending=true 才会先取证再决定重建面）。
	// days 取**真实的事件天**：若取别的天，窗口过滤会在取证之前就把请求面清空，
	// 于是无论有没有 inconclusive 分支都是零写入 —— 测不出被测行为（第一版的假绿）。
	req := &rebuildRequest{
		key: key, source: source,
		days:           []string{boundaryDay, inWindowDay},
		confirmPending: true,
		reason:         "inconclusive_guard_test",
	}
	insertsBefore := fixture.insertCount()
	out := m.rebuildDaysInner(context.Background(), req, func() {})

	require.True(t, out.Pending,
		"取证未完成 ⇒ 必须如实返回 pending（待下一轮重新取证），不得宣称完成")
	require.Equal(t, insertsBefore, fixture.insertCount(),
		"取证未完成时本轮必须零写入；若按登记全量重写就会退回「每轮重写最老一天」的饥饿循环")
}
