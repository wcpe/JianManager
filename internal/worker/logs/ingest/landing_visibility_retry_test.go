package ingest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「缺口核对必须等得到 VL 的写后可见性」这条契约（2026-11-02 真机缺陷）。
//
// 事故形态（FR-498 长稳 + 干净单源实验双重实测，逐字来自日志）：
//
//	05:34:52 投影写入结束 gen=projection-1 events=6 verified=6 status=200   ← 写成功
//	05:34:52 投影校验查询 events=6 window=[21:34:50Z, 21:34:53Z]
//	05:34:52 WARN 本地段有、VL 查不到 missingCount=6 visibleCount=0         ← 查空
//	事后直查同一 event_id：查到 9 条（projection-1/3/5/7/9/11/13/15/17）
//	且它在 05:34:52 → 05:35:23 的 31 秒内被反复报缺失 9 次
//
// ⇒ 数据确实写进去了，只是「写后约 1 秒才可见」（实测 1.028~1.057s，与 `_time` 新旧无关）。
// 旧实现（landedVisibleInVL）只查一次 ⇒ 每次都落在 1 秒窗口内 ⇒ 100% 查空
// ⇒ 缺口永不消解 ⇒ WAL 不回收 ⇒ 积压丢最旧 ⇒ 产生更多缺口（正反馈，正是第 1 轮长稳
// Unresolved 4→127 的供给侧）。
//
// 同一动作在投递路径（verifyProjectionChunk）一直有 nextVerifyBackoff 退避环，
// 注释里写明「封顶 500ms 后探测点变为 0/0.2/0.6/1.1 s，数据一可见就命中并返回」。
// 本组用例把缺口核对路径也钉到同一标准上。

// gapVisibilityEvents 造若干条**身份与 source 一致**的事件。
//
// 为什么必须自建而不是用 hotRetentionTestEvent：后者的 identity 是 `node:hotwindow`，
// 而校验选择器取的是 `events[0].Source.LogSourceID`（见 verifyProjectionQueryInWindowCore
// 里那段「选择器以事件自身的 source 标识为准」的历史注释）——身份不一致时查询恒 0 条。
func gapVisibilityEvents(source SourceConfig, day string, spans [][2]uint64, message string) []logtypes.Event {
	identity := logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	}
	out := make([]logtypes.Event, 0, len(spans))
	for i, span := range spans {
		out = append(out, logtypes.BuildEvent(identity, logtypes.RecordRange{Start: span[0], End: span[1]},
			day+"T01:00:00Z", day+"T01:00:01Z", "INFO", "stdout", fmt.Sprintf("%s-%d", message, i)))
	}
	return out
}

// gapVisibility_TestSource 造一个可在 catalog 里登记已发布投影的单源环境。
func gapVisibility_TestSource(t *testing.T) (SourceConfig, string) {
	t.Helper()
	root := t.TempDir()
	day := "2026-11-01"
	return SourceConfig{
		LogSourceID:      "inst:gapvis/file",
		SourceGeneration: "g1",
		Path:             root + "/logs/latest.log",
		UTCDay:           day,
		StorageNamespace: "inst:gapvis",
	}, root
}

// publishGapVisibilityDay 把某天的已发布投影登记进 catalog，使 generationsFor 能取到白名单。
func publishGapVisibilityDay(t *testing.T, cat *catalog.Catalog, source SourceConfig, events []logtypes.Event, day string) {
	t.Helper()
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day}
	rec := catalog.NewStableRecord(key, "hot", 1, "hot-projection-1")
	maxEnd := uint64(0)
	for _, ev := range events {
		if ev.Record.End > maxEnd {
			maxEnd = ev.Record.End
		}
	}
	rec.PublishedProjection = &catalog.PublishedProjection{
		ManifestVersion:   "manifest-test",
		CoverageComplete:  true,
		QueryLocationDirID: "hot-projection-1",
		QueryGeneration:   1,
		ProjectionGeneration: "projection-1",
		ProjectionGenerations: []string{"projection-1"},
		SourceProjections: []catalog.SourceProjection{{
			SourceGenerationRef: catalog.SourceGenerationRef{
				LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
			},
			ProjectionGenerations: []string{"projection-1"},
			ClosedVisibleSeq:      maxEnd,
		}},
		ClosedVisibleSeq: map[string]uint64{key.String(): maxEnd, "default": maxEnd},
	}
	require.NoError(t, cat.PublishProjection(nil, rec))
}

// TestLandingVisibilityRetriesUntilVisible 是本修复的**核心验收**：
//「写入后前几次查询查不到、之后可见」时，缺口核对必须重试到看得见为止，
// 而不是把「此刻查不到」当成「永远没有」。
//
// 转红方式（实测）：
//   - 把 landedVisibleInVLCtx 的退避环去掉（回退成单次查询）⇒ 本用例必红；
//   - 把 gapVisibilityBudget 压到 1ms ⇒ 本用例必红。
func TestLandingVisibilityRetriesUntilVisible(t *testing.T) {
	source, root := gapVisibility_TestSource(t)
	day := source.UTCDay

	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	events := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}, {100, 200}}, "first")
	publishGapVisibilityDay(t, cat, source, events, day)
	// 让这两条先真的进 VL（模拟「已投递成功」）。
	seedProjection(t, client, root, journal, cat, source, events)

	// **关键夹具**：写进去之后的前 3 次内容查询里它们都不可见（复刻 1s 可见延迟）。
	fixture.mu.Lock()
	fixture.delayedVisibilityQueries = 3
	fixture.querySeq = 0
	fixture.mu.Unlock()

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	// 缩短退避，让用例跑得快；语义不变（仍是 nextVerifyBackoff 的指数序列）。
	m.verifyBackoffMin = 5 * time.Millisecond
	m.verifyBackoffMax = 20 * time.Millisecond

	visible := m.landedVisibleInVL(source, events)
	require.Len(t, visible, len(events),
		"可见延迟内的前几次查询应当触发退避重试，最终必须拿到全部可见证据；"+
			"只查一次的实现会在这里拿到空集（这就是生产上缺口永不消解的直接原因）")

	fixture.mu.Lock()
	probes := fixture.contentQueries
	fixture.mu.Unlock()
	require.Greater(t, probes, 1, "必须发生多次探测（单次查询无法越过可见延迟窗口）")
}

// TestLandingVisibilityNeverLiesWhenTrulyMissing 钉住反方向：
// 数据**真的**不在 VL 里时，重试不得把缺口消解掉（「查不到就不作证据」方向不变）。
//
// 转红方式：让重试在预算耗尽后返回「全部事件」（即谎报可见）⇒ 本用例必红。
func TestLandingVisibilityNeverLiesWhenTrulyMissing(t *testing.T) {
	source, root := gapVisibility_TestSource(t)
	day := source.UTCDay

	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	events := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}}, "never written")
	// 只登记已发布投影，**不写入** VL ⇒ 恒不可见。
	publishGapVisibilityDay(t, cat, source, events, day)

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	m.verifyBackoffMin = 5 * time.Millisecond
	m.verifyBackoffMax = 10 * time.Millisecond
	// 收紧预算，避免用例等满 6 秒。
	savedBudget := gapVisibilityBudgetForTest
	gapVisibilityBudgetForTest = 150 * time.Millisecond
	t.Cleanup(func() { gapVisibilityBudgetForTest = savedBudget })

	visible := m.landedVisibleInVL(source, events)
	require.Empty(t, visible,
		"真的没进 VL 的事件绝不能被重试「磨」成可见证据——那等于把欠账静默抹平")

	fixture.mu.Lock()
	probes := fixture.contentQueries
	fixture.mu.Unlock()
	require.Greater(t, probes, 1, "预算内仍应多次探测（只是最终如实放弃）")
}

// TestLandingVisibilityReturnsImmediatelyWhenAlreadyVisible 钉住热路径：
// 数据本来就可见时**不得**引入任何额外等待（否则每次投递都被拖慢）。
//
// 转红方式：把「全部可见即返回」的判断去掉、无条件睡首轮退避 ⇒ 本用例的耗时断言转红。
//
// 夹具注意：不能走 newTestManager 那条「启动即恢复 + 扫描」的路径——它会在本次调用**之前**
// 再写一份同样的投影（于是 VL 里同一 event_id 出现两份，逐字段校验按「重复」拒绝）。
// 这里手工装配最小 Manager：只给 catalog / 客户端 / 退避参数，直接验被测函数。
func TestLandingVisibilityReturnsImmediatelyWhenAlreadyVisible(t *testing.T) {
	source, root := gapVisibility_TestSource(t)
	day := source.UTCDay
	_ = root

	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	events := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}}, "already there")
	publishGapVisibilityDay(t, cat, source, events, day)

	// 直接向夹具落**唯一一份**行（不经过 Manager 的恢复/扫描路径，避免同一 event_id 写两份）。
	fixture.mu.Lock()
	for _, ev := range events {
		fixture.rows = append(fixture.rows, reconcileVLRow{
			ProjectionGeneration: "projection-1",
			EventID:              ev.EventID,
			EventTimeUTC:         ev.EventTimeUTC,
			LogSourceID:          source.LogSourceID,
			SourceGeneration:     source.SourceGeneration,
			RecordEnd:            ev.Record.End,
			Message:              ev.Message,
			Level:                ev.Level,
			Stream:               ev.Stream,
			CanonicalHash:        ev.CanonicalHash,
		})
	}
	fixture.mu.Unlock()

	m := &Manager{
		cat:              cat,
		vl:               client,
		verifyBackoffMin: 200 * time.Millisecond,
		verifyBackoffMax: 500 * time.Millisecond,
	}

	started := time.Now()
	visible := m.landedVisibleInVL(source, events)
	elapsed := time.Since(started)

	require.Len(t, visible, len(events))
	require.Less(t, elapsed, 150*time.Millisecond,
		"数据本来就可见时必须在第一次探测就返回，不得白等一轮退避（投递热路径零额外延迟）")

	fixture.mu.Lock()
	probes := fixture.contentQueries
	fixture.mu.Unlock()
	require.Equal(t, 1, probes, "已可见路径只应有一次查询")
}

// TestVisibleEventsInVLQueriesSlicesConcurrently 钉住「切片必须并发查」（2026-11-02 修）。
//
// 事故形态（60 源 × 15 行/s 实测）：缺口核对的 selector 被 12KB 上限切成 180 条/片，
// 一次核对待查 3778 条 ⇒ **21 片**。原实现串行逐片查询 ⇒ 一轮核对 21 个 HTTP 往返，
// 任何预算都会被单次核对的串行往返吃掉（实测 154 次 deadline exceeded）。
//
// 判据：让夹具对每次内容查询固定睡一小段，然后比较「一次多片查询」的总耗时。
// 串行 ≈ 片数 × 延迟；并发（度 c）≈ ceil(片数/c) × 延迟。
//
// 转红方式（实测）：把 visibleEventsInVL 的并发段改回串行 ⇒ 本用例必红。
func TestVisibleEventsInVLQueriesSlicesConcurrently(t *testing.T) {
	source, _ := gapVisibility_TestSource(t)
	day := source.UTCDay

	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	// 900 条事件 ⇒ batch=512 ⇒ 2 个外层片；而每个外层片内部还会被 12KB selector 上限
	// 再切成约 180 条/子片（≈3 个子片）。故串行往返数 ≈ 6，并发(4) ≈ 2 轮。
	const total = 900
	spans := make([][2]uint64, 0, total)
	for i := 0; i < total; i++ {
		spans = append(spans, [2]uint64{uint64(i * 10), uint64(i*10 + 5)})
	}
	events := gapVisibilityEvents(source, day, spans, "slice")
	publishGapVisibilityDay(t, cat, source, events, day)

	// 每一片查询固定耗时，便于用总耗时区分串行/并发。
	const perQueryDelay = 60 * time.Millisecond
	fixture.mu.Lock()
	fixture.contentQueryDelay = perQueryDelay
	for _, ev := range events {
		fixture.rows = append(fixture.rows, reconcileVLRow{
			ProjectionGeneration: "projection-1",
			EventID:              ev.EventID,
			EventTimeUTC:         ev.EventTimeUTC,
			LogSourceID:          source.LogSourceID,
			SourceGeneration:     source.SourceGeneration,
			RecordEnd:            ev.Record.End,
			Message:              ev.Message,
			Level:                ev.Level,
			Stream:               ev.Stream,
			CanonicalHash:        ev.CanonicalHash,
		})
	}
	fixture.mu.Unlock()

	// 先量「串行基线」：并发度 1 时同样的查询要多久。
	serial := &Manager{cat: cat, vl: client, verifyChunkConcurrency: 1}
	started := time.Now()
	serialVisible, sErr := serial.visibleEventsInVL(context.Background(), client, source, []string{"projection-1"}, events)
	serialElapsed := time.Since(started)
	require.NoError(t, sErr)
	require.Len(t, serialVisible, total, "串行路径也须拿回全部可见事件")

	// 再量并发版。
	concurrent := &Manager{cat: cat, vl: client, verifyChunkConcurrency: 4}
	started = time.Now()
	visible, vErr := concurrent.visibleEventsInVL(context.Background(), client, source, []string{"projection-1"}, events)
	elapsed := time.Since(started)
	require.NoError(t, vErr)
	require.Len(t, visible, total, "并发合并后必须拿回全部可见事件（不能丢片）")

	// **判据取「相对」而非绝对值**：不同片数下绝对耗时不可预期，
	// 但并发必须明显快于串行。要求至少快 30%（实测约快 50%~60%）。
	require.Less(t, float64(elapsed), float64(serialElapsed)*0.7,
		"多片查询必须并发执行（串行=%v 并发=%v）；串行会让任何超时预算被单次核对吃掉",
		serialElapsed, elapsed)
}

// TestLandingVisibilityProbesDaysConcurrently 钉住「跨天也必须并发」（2026-11-02 第二处修复）。
//
// 事故形态（60 源 × 15 行/s 长稳 r1 实测）：
//
//	WARN 缺口消解前核对 VL 可见性失败 probes=1 error="context deadline exceeded"
//	WARN 缺口消解前核对 VL 可见性失败 probes=2 error="context deadline exceeded"
//
// **probes=1 就超时 ⇒ 首次探测本身把 6s 预算耗光。** 原因是一次探测要按天**串行**
// 遍历 order 里的每一天，而单片查询实测可达 1.5s（`投影校验慢查询 elapsedMs=1578`）。
//
// 判据：让夹具每次查询固定耗时，然后比较「跨 3 天各查一次」的总耗时。
// 串行 ≈ 3 × delay；并发 ≈ 1 × delay。
//
// 转红方式（实测）：把 landedVisibleProbeOnce 的跨天循环改回串行 ⇒ 本用例必红。
func TestLandingVisibilityProbesDaysConcurrently(t *testing.T) {
	source, _ := gapVisibility_TestSource(t)
	const dayA, dayB, dayC = "2026-11-01", "2026-11-02", "2026-11-03"
	days := []string{dayA, dayB, dayC}

	client, fixture, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	events := make([]logtypes.Event, 0, len(days))
	for _, day := range days {
		one := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}}, "day-"+day)
		events = append(events, one...)
		publishGapVisibilityDay(t, cat, source, one, day)
	}

	const perQueryDelay = 80 * time.Millisecond
	fixture.mu.Lock()
	fixture.contentQueryDelay = perQueryDelay
	for _, ev := range events {
		// 取该事件所属天（与 eventUTCDay 同口径：优先 EventTimeUTC）。
		day := ev.EventTimeUTC[:10]
		fixture.rows = append(fixture.rows, reconcileVLRow{
			ProjectionGeneration: "projection-1",
			EventID:              ev.EventID,
			EventTimeUTC:         ev.EventTimeUTC,
			LogSourceID:          source.LogSourceID,
			SourceGeneration:     source.SourceGeneration,
			RecordEnd:            ev.Record.End,
			Message:              ev.Message,
			Level:                ev.Level,
			Stream:               ev.Stream,
			CanonicalHash:        ev.CanonicalHash,
		})
		_ = day
	}
	fixture.mu.Unlock()

	m := &Manager{cat: cat, vl: client, verifyChunkConcurrency: 4}

	started := time.Now()
	visible := m.landedVisibleInVL(source, events)
	elapsed := time.Since(started)

	require.Len(t, visible, len(events), "跨天并发合并后必须拿回全部可见事件")
	// 串行需 3×80=240ms；并发(4) 只需 ~80ms。阈值取 180ms：串行必超，并发余量充足。
	require.Less(t, elapsed, 180*time.Millisecond,
		"跨天探测必须并发；串行会让首次探测就吃掉整个预算（实测 %v）", elapsed)
}
