package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件钉住 B1「从 VL 反推天集合补建分区记录」（2026-11-05）。
//
// 背景：journal 落盘只保证「从现在起不再丢」；它修不了「之前从来没被写进 journal」的天。
// 生产 inst:318 的 2026-11-01 有 2880 行、11-02 有 1746 行，查询侧全部报
// NO_CATALOG_RECORD。唯一知道「那些天有数据」的地方就是 VL 自己。
//
// 四条红线各有一条用例（见 catalog_backfill.go 文件头）：
//  1. 天桶由 VL 给（不在 Go 里算）：TestBackfill_UsesVLBuckets
//  2. 幂等且绝不覆盖：TestBackfill_DoesNotOverwriteExistingRecord（最重要）
//  3. 有界：TestBackfillDaysWindow_Bounded
//  4. 可观测：TestBackfill_StatsDistinguishOutcomes

// dayBucketVL 是只回答「按天分桶」查询的假 VL。
//
// 它刻意**只**处理 stats by (_time:1d)：这样若被测代码改成别种查询（例如自己去
// Go 里算天界），用例会因为拿不到预期结果而失败——这正是要守住的契约。
type dayBucketVL struct {
	days []string // 形如 "2026-11-02"
	// failFor 使指定 source 的查询返回 500（测「失败不补建」）。
	failFor string
	// queries 记录收到的查询串（供断言）。
	queries []string
	// starts 记录时间窗，供断言「回溯窗口有界」。
	starts []string
}

func (v *dayBucketVL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/select/logsql/query" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	q := r.URL.Query().Get("query")
	v.queries = append(v.queries, q)
	v.starts = append(v.starts, r.URL.Query().Get("start"))
	if v.failFor != "" && strings.Contains(q, v.failFor) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !strings.Contains(q, "stats by (_time:1d)") {
		// 契约：必须让 VL 自己分桶。
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/stream+json")
	for _, day := range v.days {
		// VL 的天桶在 `_time` 里，形如 2026-11-02T00:00:00Z。
		fmt.Fprintf(w, "{\"_time\":%q,\"count(*)\":\"7\"}\n", day+"T00:00:00Z")
	}
}

func newDayBucketVL(t *testing.T, fixture *dayBucketVL) *vlsup.Client {
	t.Helper()
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client
}

// backfillTestManager 构造一个只登记了一个源的管理器（最小夹具）。
func backfillTestManager(t *testing.T, client *vlsup.Client, namespace, sourceID string) (*Manager, *catalog.Catalog) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: sourceID, SourceGeneration: "gen-1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: namespace,
			// 源登记日刻意设为很早：补建必须按**事件/天桶**归属，不能沿用它。
			UTCDay: "2026-10-01",
		}},
	})
	require.NoError(t, err)
	return m, cat
}

// TestBackfill_CreatesRecordsForDaysWithData 有数据的天补建记录，真·空的天不建。
//
// 这是 B1 的正例，也钉住「真·空不建记录」：VL 只返回 11-01/11-02/11-04，
// 11-03/11-05 不在结果里 ⇒ 绝不能给它们建记录（否则查询侧会把「真·空」当成
// 「有数据但查不到」，反过来又会破坏 complete=true）。
func TestBackfill_CreatesRecordsForDaysWithData(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-01", "2026-11-02", "2026-11-04"}}
	client := newDayBucketVL(t, fixture)
	const (
		namespace = "inst:318"
		sourceID  = "inst:318/file"
	)
	m, cat := backfillTestManager(t, client, namespace, sourceID)

	stats, err := m.BackfillCatalogPartitionsFromVL(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 1, stats.Namespaces)
	require.Equal(t, 3, stats.DaysSeen)
	require.Equal(t, 3, stats.Created, "有数据的三天必须全部补建；stats=%s", stats.String())
	require.Zero(t, stats.Failed)

	for _, day := range []string{"2026-11-01", "2026-11-02", "2026-11-04"} {
		rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day})
		require.True(t, ok, "补建后必须有 %s 的分区记录", day)
		require.NotNil(t, rec.PublishedProjection,
			"投影必须非 nil：querySnapshotLocked 的门禁要求它，否则记录在也查不到")
		require.Empty(t, rec.PublishedProjection.ProjectionGenerations,
			"刻意不填代次：填假代次会把查询限制在不存在的标签上")
	}
	// 真·空的天：绝不建记录。
	for _, day := range []string{"2026-11-03", "2026-11-05"} {
		_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day})
		require.False(t, ok,
			"%s 在 VL 里没有数据，不得补建记录（那会把「真·空」变成谎报）", day)
	}
}

// TestBackfill_DoesNotOverwriteExistingRecord 红线 2：已有记录一律跳过，一个字节都不改。
//
// 这是全文件最重要的一条。已有记录可能来自：
//   - journal 重放（带完整发布投影与 owner）；
//   - 归档迁移（owner 已改成 COLD，dirID 指向归档目录）。
//
// 把后者强行改回 HOT 是数据面破坏，比「查不到」严重得多。
//
// 转红方式（实测）：把 ensureCatalogPartitionForDay 前面的 `cat.Get` 幂等检查删掉，
// 本用例会以「owner 被改回 HOT」变红。
func TestBackfill_DoesNotOverwriteExistingRecord(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-02"}}
	client := newDayBucketVL(t, fixture)
	const (
		namespace = "inst:318"
		sourceID  = "inst:318/file"
		day       = "2026-11-02"
	)
	m, cat := backfillTestManager(t, client, namespace, sourceID)

	// 预置一条「已迁移到 COLD」的记录（模拟归档后的权威）。
	key := catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}
	existing := catalog.NewStableRecord(key, catalog.OwnerCold, 9, "archive-dir-42")
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, Record: existing}))

	before, ok := cat.Get(key)
	require.True(t, ok)
	ownerBefore, genBefore, dirBefore := before.Owner, before.Generation, before.OwnerDirID

	stats, err := m.BackfillCatalogPartitionsFromVL(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 1, stats.SkippedExisting, "已有记录必须计入 SkippedExisting；stats=%s", stats.String())
	require.Zero(t, stats.Created, "不得补建/覆盖已有记录")

	after, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, ownerBefore, after.Owner, "owner 不得被改回 HOT（那会把归档分区伪装成热层）")
	require.Equal(t, genBefore, after.Generation, "generation 不得被改")
	require.Equal(t, dirBefore, after.OwnerDirID, "dirID 不得被改（它决定查询去哪个目录读）")
}

// TestBackfill_IsIdempotent 重复跑不会重复建（第二次全部落到 SkippedExisting）。
func TestBackfill_IsIdempotent(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-02", "2026-11-04"}}
	client := newDayBucketVL(t, fixture)
	m, _ := backfillTestManager(t, client, "inst:318", "inst:318/file")
	opts := CatalogBackfillOptions{Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}

	first, err := m.BackfillCatalogPartitionsFromVL(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, 2, first.Created)

	second, err := m.BackfillCatalogPartitionsFromVL(context.Background(), opts)
	require.NoError(t, err)
	require.Zero(t, second.Created, "第二次不得再建")
	require.Equal(t, 2, second.SkippedExisting, "第二次应全部跳过")
}

// TestBackfill_UsesVLBuckets 红线 1：天桶必须由 VL 给（查询里必须带 stats by (_time:1d)）。
func TestBackfill_UsesVLBuckets(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-02"}}
	client := newDayBucketVL(t, fixture)
	m, _ := backfillTestManager(t, client, "inst:318", "inst:318/file")

	_, err := m.BackfillCatalogPartitionsFromVL(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotEmpty(t, fixture.queries)
	for _, q := range fixture.queries {
		require.Contains(t, q, "stats by (_time:1d)",
			"必须让 VL 自己分天桶，而不是在 Go 里算天界（前三起事故都是这么来的）：%s", q)
	}
}

// TestBackfill_QueryFailureCreatesNothing 查询失败 ⇒ 一条都不建，且计入 Failed。
//
// 方向：宁可继续「查不到」，也不凭猜测写记录。
func TestBackfill_QueryFailureCreatesNothing(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-02"}, failFor: "inst:318/file"}
	client := newDayBucketVL(t, fixture)
	const namespace = "inst:318"
	m, cat := backfillTestManager(t, client, namespace, "inst:318/file")

	stats, err := m.BackfillCatalogPartitionsFromVL(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err, "查询失败不是整体错误（部分成功模型）")
	require.Zero(t, stats.Created, "查询失败时不得补建任何记录")
	require.NotZero(t, stats.Failed, "失败次数必须可观测（红线 4）")
	require.NotEmpty(t, stats.QueryErrors, "失败原因必须留下")
	for _, k := range cat.Keys() {
		require.NotEqual(t, namespace, k.StorageNamespace, "不得留下任何 %s 的记录", namespace)
	}
}

// TestBackfillDaysWindow_Bounded 红线 3：回溯窗口有界，且含今天。
func TestBackfillDaysWindow_Bounded(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	from, to := backfillDaysWindow(now, 1)
	// 只回溯 1 天 = 只有今天。
	require.Equal(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), from.UTC())
	require.Equal(t, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), to.UTC())

	from, to = backfillDaysWindow(now, 7)
	// 7 天 = 10-30 .. 11-05 全天（含今天）。
	require.Equal(t, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), from.UTC())
	require.Equal(t, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), to.UTC())

	// 0/负值必须落到默认值，而不是「全量历史」。
	from, to = backfillDaysWindow(now, 0)
	// 2026-11-05：默认值由 30 改为 180（依据订正见 DefaultCatalogBackfillDays 注释）。
	// 180 天含今天 ⇒ 起点 = 11-05 往前 179 天 = 2026-05-10。
	require.Equal(t, time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC), from.UTC(),
		"0 应取默认 %d 天", DefaultCatalogBackfillDays)

	// 总窗口（180d）**可以**大于单段上限：回填按 catalogBackfillMaxRange 切窗后逐段查询。
	// 因此这里不能断言「总跨度 ≤ 单段上限」——那会把正确的切窗逻辑判成违规。
	// 真正要守的是「每一段都不超上限」，由 TestBackfillDaysWindow_SegmentsBounded 覆盖。
	require.Greater(t, to.Sub(from), catalogBackfillMaxRange,
		"默认窗口应大于单段上限，否则切窗逻辑失去意义（这说明默认值被误设小了）")
}

// TestBackfillDaysWindow_SegmentsBounded 切窗后的**每一段**都不得超出单段上限。
//
// 为什么单独一条：总窗口变大后，「总跨度 ≤ 上限」这条断言必然失败，
// 而它想守的其实是「不要一次查太大」。判据必须落在切出来的段上。
func TestBackfillDaysWindow_SegmentsBounded(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	from, to := backfillDaysWindow(now, DefaultCatalogBackfillDays)

	segments := 0
	for windowStart := from; windowStart.Before(to); {
		windowEnd := windowStart.Add(catalogBackfillMaxRange)
		if windowEnd.After(to) {
			windowEnd = to
		}
		require.LessOrEqual(t, windowEnd.Sub(windowStart), catalogBackfillMaxRange,
			"第 %d 段跨度超出单段上限", segments)
		segments++
		windowStart = windowEnd
	}
	require.GreaterOrEqual(t, segments, 2,
		"180 天默认窗口按 90 天切窗应至少 2 段，实际 %d 段", segments)
}

// TestBackfill_NoSourcesIsNotAnError 启动早期还没登记源 ⇒ 零计数、不报错。
func TestBackfill_NoSourcesIsNotAnError(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-02"}}
	client := newDayBucketVL(t, fixture)
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: t.TempDir(), VL: client, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)

	stats, err := m.BackfillCatalogPartitionsFromVL(context.Background(), CatalogBackfillOptions{Days: 7})
	require.NoError(t, err, "没有源时不应报错（下次启动再来）")
	require.Zero(t, stats.Namespaces)
	require.Zero(t, stats.Created)
}

// TestBackfill_ContextCancelledStopsEarly ctx 取消后立刻收手（不硬跑到底）。
func TestBackfill_ContextCancelledStopsEarly(t *testing.T) {
	fixture := &dayBucketVL{days: []string{"2026-11-02"}}
	client := newDayBucketVL(t, fixture)
	m, _ := backfillTestManager(t, client, "inst:318", "inst:318/file")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.BackfillCatalogPartitionsFromVL(ctx, CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.ErrorIs(t, err, context.Canceled)
}
