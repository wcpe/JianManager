package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件是 FR-497「重启增量对账（源 × UTC 天，条数级）」的四条回归
// （spec：docs/specs/log-startup-reconcile/spec.md §3）。
//
// 四条用例都必须能「转红」——即：把实现退回旧行为（无条件整窗重发）或去掉超时约束后，
// 断言必须失败。为此每条用例的断言都对准**投递量**（写了几次、写了哪几天、是否零投递），
// 而不是对准内部状态。

// reconcileVLRow 是内存 VL 中的一条记录（字段与 projectionPayload 对齐）。
type reconcileVLRow struct {
	ProjectionGeneration string `json:"projection_generation"`
	EventID              string `json:"event_id"`
	EventTimeUTC         string `json:"_time"`
	LogSourceID          string `json:"log_source_id"`
	SourceGeneration     string `json:"source_generation"`
	RecordEnd            uint64 `json:"record_end"`
	Message              string `json:"_msg"`
	Level                string `json:"level"`
	Stream               string `json:"stream"`
	CanonicalHash        string `json:"canonical_content_hash"`
}

// reconcileVLFixture 是「可对账的内存 VL」：除写入与内容查询外，还实现只读的 stats count
// 查询，并允许测试直接删/改某天的记录（模拟人为删除与内容缺失两种现场）。
type reconcileVLFixture struct {
	mu           sync.Mutex
	rows         []reconcileVLRow
	inserts      [][]byte
	statsQueries int
	// stallStats 为 true 时 stats 查询挂起直到请求被取消（模拟「对账不可信」）。
	stallStats bool
}

func (f *reconcileVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/insert/jsonline":
		payload, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.inserts = append(f.inserts, payload)
		f.rows = append(f.rows, parseReconcileRows(payload)...)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case "/select/logsql/query":
		f.serveContentQuery(w, r)
	case reconcileStatsPath:
		f.serveStats(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *reconcileVLFixture) serveContentQuery(w http.ResponseWriter, r *http.Request) {
	filter := parseReconcileSelector(r.URL.Query().Get("query"))
	from, to := reconcileWindow(r)
	f.mu.Lock()
	rows := append([]reconcileVLRow(nil), f.rows...)
	f.mu.Unlock()
	for _, row := range rows {
		if !filter.match(row) || !reconcileWithin(row.EventTimeUTC, from, to) {
			continue
		}
		line, err := json.Marshal(map[string]any{
			"_time": row.EventTimeUTC, "_msg": row.Message, "event_id": row.EventID,
			"level": row.Level, "stream": row.Stream, "canonical_content_hash": row.CanonicalHash,
		})
		if err != nil {
			continue
		}
		_, _ = w.Write(append(line, '\n'))
	}
}

func (f *reconcileVLFixture) serveStats(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	stall := f.stallStats
	f.statsQueries++
	f.mu.Unlock()
	if stall {
		// 挂起到请求被取消：验证对账侧的超时是有界的（客户端超时 → ctx 取消 → 回退整窗重发）。
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return
	}
	query := strings.TrimSuffix(r.URL.Query().Get("query"), " | stats count()")
	filter := parseReconcileSelector(query)
	from, to := reconcileWindow(r)
	f.mu.Lock()
	rows := append([]reconcileVLRow(nil), f.rows...)
	f.mu.Unlock()
	count := 0
	for _, row := range rows {
		if filter.match(row) && reconcileWithin(row.EventTimeUTC, from, to) {
			count++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,%q]}]}}`,
		strconv.Itoa(count))))
}

func (f *reconcileVLFixture) insertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inserts)
}

func (f *reconcileVLFixture) statsQueryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsQueries
}

// payloadsSince 返回第 index 次之后的全部插入载荷（用于断言「重启后到底写了什么」）。
func (f *reconcileVLFixture) payloadsSince(index int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index > len(f.inserts) {
		index = len(f.inserts)
	}
	out := make([]string, 0, len(f.inserts)-index)
	for _, payload := range f.inserts[index:] {
		out = append(out, string(payload))
	}
	return out
}

func (f *reconcileVLFixture) setStallStats(stall bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stallStats = stall
}

// deleteDay 删除某 UTC 天的全部记录（模拟「人为删掉 VL 某天数据」）。
func (f *reconcileVLFixture) deleteDay(day string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := make([]reconcileVLRow, 0, len(f.rows))
	for _, row := range f.rows {
		if reconcileRowDay(row) == day {
			continue
		}
		kept = append(kept, row)
	}
	f.rows = kept
}

// replaceMessage 改写某条记录的内容但保持条数不变（模拟「条数相同、内容缺失」的边界现场）。
func (f *reconcileVLFixture) replaceMessage(eventID, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.rows {
		if f.rows[index].EventID != eventID {
			continue
		}
		f.rows[index].Message = message
		f.rows[index].CanonicalHash = logtypes.CanonicalContentHash(
			f.rows[index].EventTimeUTC, f.rows[index].Level, f.rows[index].Stream, message)
	}
}

// reconcileSelectorFilter 是测试侧的选择器匹配器：只解析对账/校验用到的四个条件。
type reconcileSelectorFilter struct {
	logSourceID      string
	sourceGeneration string
	generations      []string
	recordEndMax     uint64
}

var (
	reconcileLogSourcePattern  = regexp.MustCompile(`log_source_id:="([^"]*)"`)
	reconcileSourceGenPattern  = regexp.MustCompile(`source_generation:="([^"]*)"`)
	reconcileGenerationPattern = regexp.MustCompile(`projection_generation:="([^"]*)"`)
	reconcileRecordEndPattern  = regexp.MustCompile(`record_end:<=([0-9]+)`)
)

func parseReconcileSelector(selector string) reconcileSelectorFilter {
	filter := reconcileSelectorFilter{}
	if match := reconcileLogSourcePattern.FindStringSubmatch(selector); match != nil {
		filter.logSourceID = match[1]
	}
	if match := reconcileSourceGenPattern.FindStringSubmatch(selector); match != nil {
		filter.sourceGeneration = match[1]
	}
	for _, match := range reconcileGenerationPattern.FindAllStringSubmatch(selector, -1) {
		filter.generations = append(filter.generations, match[1])
	}
	if match := reconcileRecordEndPattern.FindStringSubmatch(selector); match != nil {
		filter.recordEndMax, _ = strconv.ParseUint(match[1], 10, 64)
	}
	return filter
}

func (f reconcileSelectorFilter) match(row reconcileVLRow) bool {
	if f.logSourceID != "" && row.LogSourceID != f.logSourceID {
		return false
	}
	if f.sourceGeneration != "" && row.SourceGeneration != f.sourceGeneration {
		return false
	}
	if len(f.generations) > 0 {
		matched := false
		for _, generation := range f.generations {
			if generation == row.ProjectionGeneration {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if f.recordEndMax > 0 && row.RecordEnd > f.recordEndMax {
		return false
	}
	return true
}

func parseReconcileRows(payload []byte) []reconcileVLRow {
	rows := make([]reconcileVLRow, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row reconcileVLRow
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func reconcileWindow(r *http.Request) (time.Time, time.Time) {
	from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
	to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
	return from, to
}

func reconcileWithin(eventTime string, from, to time.Time) bool {
	when, err := time.Parse(time.RFC3339Nano, eventTime)
	if err != nil {
		return false
	}
	if !from.IsZero() && when.Before(from) {
		return false
	}
	if !to.IsZero() && when.After(to) {
		return false
	}
	return true
}

func reconcileRowDay(row reconcileVLRow) string {
	when, err := time.Parse(time.RFC3339Nano, row.EventTimeUTC)
	if err != nil {
		return ""
	}
	return when.UTC().Format("2006-01-02")
}

func newReconcileVL(t *testing.T) (*vlsup.Client, *reconcileVLFixture, *httptest.Server) {
	t.Helper()
	fixture := &reconcileVLFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture, server
}

// reconcileTestSource 是回归用的源配置（两天的日志都落在过去，避免与「今天」耦合）。
//
// Path 必须真实存在：Register 会为 FILE_PRIMARY 源建立 FileTailer，pipeline 会校验路径。
// 回归不驱动 poll（只驱动启动恢复），故文件内容保持为空。
func reconcileTestSource(t *testing.T, root string) SourceConfig {
	t.Helper()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o600))
	return SourceConfig{
		LogSourceID: "node:reconcile", SourceGeneration: "g1",
		Path: logPath, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:reconcile", UTCDay: runtimeTestUTCDay(),
	}
}

// reconcileTestEvents 返回跨两个 UTC 日的三条权威事件（09-22 两条、09-23 一条）。
func reconcileTestEvents() []logtypes.Event {
	identity := logtypes.SourceIdentity{LogSourceID: "node:reconcile", SourceGeneration: "g1", ParserVersion: "v1"}
	return []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 10}, "2026-09-22T01:00:00Z", "2026-09-22T01:00:01Z", "INFO", "stdout", "day one alpha"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20}, "2026-09-22T02:00:00Z", "2026-09-22T02:00:02Z", "INFO", "stdout", "day one beta"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 20, End: 30}, "2026-09-23T01:00:00Z", "2026-09-23T01:00:03Z", "INFO", "stdout", "day two alpha"),
	}
}

// seedProjection 复现「重启前现场」：把权威事件按天写进 VL、发布 catalog、落段存储并持久化。
//
// 这里手动 New + Stop（不走 newTestManager 的 t.Cleanup）：seed 出来的进程必须在重启前
// **完全关闭**（索引归并 + 事件段句柄释放），否则 Cleanup 阶段会用旧内存状态再持久化一次，
// 把重启后的索引状态覆盖回旧代次，污染「重启」语义。
func seedProjection(t *testing.T, client *vlsup.Client, root string, journal catalog.Journal, cat *catalog.Catalog, source SourceConfig, events []logtypes.Event) {
	t.Helper()
	manager, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source},
		VerificationTimeout: 200 * time.Millisecond,
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

// 回归 ①：人为删掉 VL 某天数据 → 启动对账检出并**只补该天**。
//
// 为什么能转红：旧实现（无条件整窗重发）会把两天的数据都重写一遍，
// 于是 payloadsSince(...) 的长度为 2 且包含 "day two alpha"，本用例的三条断言全部失败。
func TestStartupReconcileReplaysOnlyDeletedUTCDay(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	require.Equal(t, 2, fixture.insertCount(), "首次写入按 UTC 天分两次插入")
	require.Zero(t, fixture.statsQueryCount(), "首次写入（无恢复事件）不触发对账查询")

	fixture.deleteDay("2026-09-22")
	insertsBefore := fixture.insertCount()

	restartedCatalog := catalog.New(journal)
	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: restartedCatalog, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)

	payloads := fixture.payloadsSince(insertsBefore)
	require.Len(t, payloads, 1, "只应重发被删掉的那一天")
	require.Contains(t, payloads[0], "day one alpha")
	require.NotContains(t, payloads[0], "day two alpha", "未缺失的天不得被重发")

	reports := restarted.LastReconcileReports()
	require.Len(t, reports, 1)
	require.False(t, reports[0].Fallback, "对账可用时不得回退整窗重发")
	require.Equal(t, []string{"2026-09-22"}, reports[0].MissingDays)
	require.Equal(t, reconcileBasisCountOnly, reports[0].Basis)
	for _, day := range reports[0].Days {
		if day.UTCDay == "2026-09-22" {
			require.True(t, day.Missing)
			require.Equal(t, 2, day.Expected)
			require.Zero(t, day.Observed)
		}
	}

	// 缺失天推进到新代；未缺失天保持原代（不动 catalog）。
	repaired, ok := restartedCatalog.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: "2026-09-22"})
	require.True(t, ok)
	require.Equal(t, []string{"projection-2"}, repaired.PublishedProjection.ProjectionGenerations)
	intact, ok := restartedCatalog.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: "2026-09-23"})
	require.True(t, ok)
	require.Equal(t, []string{"projection-1"}, intact.PublishedProjection.ProjectionGenerations)
}

// 回归 ②：两侧一致 → **零重发**（断言没有多余投递）。
//
// 为什么能转红：旧实现无条件整窗重发，重启后 fixture.insertCount() 必然增加（+2），
// 「零重发」断言失败。
func TestStartupReconcileSkipsReplayWhenCountsMatch(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	insertsBefore := fixture.insertCount()
	queriesBefore := fixture.statsQueryCount()

	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)

	require.Equal(t, insertsBefore, fixture.insertCount(), "两侧一致时必须零重发")
	require.Equal(t, 2, fixture.statsQueryCount()-queriesBefore, "每天一次只读 count 查询")
	reports := restarted.LastReconcileReports()
	require.Len(t, reports, 1)
	require.False(t, reports[0].Fallback)
	require.Empty(t, reports[0].MissingDays)
	require.Len(t, reports[0].Days, 2, "两个 UTC 天各有一条结论")
	for _, day := range reports[0].Days {
		require.False(t, day.Missing, day.UTCDay)
		require.Equal(t, day.Expected, day.Observed, day.UTCDay)
		require.Equal(t, reconcileBasisCountOnly, day.Basis)
	}
}

// 回归 ③：VL 不可达 / 对账查询挂起 → 回退整窗重发且不卡死（有超时）。
//
// 为什么能转红：若实现「对账失败即放弃重发」，子用例 A 的整窗重发断言（2 次插入）失败；
// 若实现不带超时，子用例 A 会挂死到测试超时（默认 10 分钟）。
func TestStartupReconcileFallsBackToFullReplayWhenVLUnreliable(t *testing.T) {
	t.Run("对账查询挂起", func(t *testing.T) {
		client, fixture, _ := newReconcileVL(t)
		root := t.TempDir()
		journal := catalog.NewMemJournal()
		source := reconcileTestSource(t, root)
		seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
		fixture.setStallStats(true)
		insertsBefore := fixture.insertCount()

		started := time.Now()
		// Timeout 故意放宽到 30s：本子用例验证的是**单次查询超时**（QueryTimeout）真的生效——
		// 若实现忽略它（退回默认 10s），下面的 elapsed 断言必然失败。
		restarted, err := newTestManager(t, Options{
			Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
			Reconcile: &ReconcileConfig{Enabled: true, Concurrency: 1, Timeout: 30 * time.Second, QueryTimeout: 150 * time.Millisecond},
		})
		elapsed := time.Since(started)
		require.NoError(t, err, "回退整窗重发必须成功，不得因对账失败而放弃")
		require.Less(t, elapsed, 3*time.Second, "对账超时必须快速回退，不得卡死启动")
		require.Equal(t, 2, len(fixture.payloadsSince(insertsBefore)), "回退必须整窗重发（两天各一次）")

		reports := restarted.LastReconcileReports()
		require.Len(t, reports, 1)
		require.True(t, reports[0].Fallback)
		require.Equal(t, "count_query_failed", reports[0].FallbackReason)
		require.Empty(t, reports[0].MissingDays, "回退路径不按天裁剪")
	})

	t.Run("VL 完全不可达", func(t *testing.T) {
		client, fixture, server := newReconcileVL(t)
		root := t.TempDir()
		journal := catalog.NewMemJournal()
		source := reconcileTestSource(t, root)
		seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
		server.Close()
		insertsBefore := fixture.insertCount()

		started := time.Now()
		_, err := newTestManager(t, Options{
			Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
			Reconcile: &ReconcileConfig{Enabled: true, Concurrency: 1, Timeout: time.Second, QueryTimeout: 150 * time.Millisecond},
		})
		elapsed := time.Since(started)
		require.Error(t, err, "VL 不可达时整窗重发也会失败（与现状一致），但必须是有界的快速失败")
		require.Less(t, elapsed, 3*time.Second, "不得卡死")
		require.Contains(t, err.Error(), "insert", "失败必须来自投递路径（证明走的是整窗重发而不是卡在对账）")
		require.Equal(t, insertsBefore, fixture.insertCount(), "不可达时不会产生成功插入")
	})
}

// 回归 ④：条数相同、内容缺失 —— 条数级对账判「一致」（口径显式），内容级路径可检出。
//
// 口径与检出方式（spec §2.4）：条数级对账**不比较内容**，此边界列入「不做」；
// 内容一致性由写入路径的逐条校验（verifyProjection*）承接，人工全量重建可兜底。
//
// 为什么能转红：若实现把「条数相同」误当成内容一致而隐藏了依据标记（Basis），断言失败；
// 若实现改做内容级对账（会重发），零重发断言失败。
func TestStartupReconcileCountsOnlyAcceptsEqualCountWithMissingContent(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	// 只用一天两条事件，聚焦「同数不同内容」这一条边界。
	events := reconcileTestEvents()[:2]
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)

	fixture.replaceMessage(events[0].EventID, "tampered content")
	insertsBefore := fixture.insertCount()

	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	require.Equal(t, insertsBefore, fixture.insertCount(), "条数级对账不检出内容缺失 → 零重发")

	reports := restarted.LastReconcileReports()
	require.Len(t, reports, 1)
	require.Len(t, reports[0].Days, 1)
	day := reports[0].Days[0]
	require.False(t, day.Missing)
	require.Equal(t, 2, day.Expected)
	require.Equal(t, 2, day.Observed)
	require.Equal(t, reconcileBasisCountOnly, day.Basis, "结论必须显式标注只做了条数级比较")
	require.False(t, reports[0].Fallback)

	// 检出方式：内容级校验能抓到这个缺失（证明「口径边界」不是「无能力」）。
	key := source.LogSourceID + "/" + source.SourceGeneration
	restarted.mu.Lock()
	saved := restarted.state.Sources[key]
	restarted.mu.Unlock()
	want, err := restarted.canonicalRecoveryEvents(key, saved)
	require.NoError(t, err)
	require.Len(t, want, 2)
	verifySource := source
	verifySource.UTCDay = "2026-09-22"
	complete, verifyErr := restarted.verifyProjectionOnceWithClient(context.Background(), client, verifySource, "projection-1", want)
	require.False(t, complete)
	require.Error(t, verifyErr, "内容级校验必须检出被改写的内容")
	require.Contains(t, verifyErr.Error(), "projection content mismatch")
}

// 覆盖配置归一化与「显式关闭对账」的逃生口：关闭后行为必须回到现状（整窗重发）。
func TestStartupReconcileDisabledFallsBackToFullReplay(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	insertsBefore := fixture.insertCount()
	queriesBefore := fixture.statsQueryCount()

	restarted, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
		Reconcile: &ReconcileConfig{Enabled: false},
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(fixture.payloadsSince(insertsBefore)), "关闭对账 = 现状整窗重发")
	require.Equal(t, queriesBefore, fixture.statsQueryCount(), "关闭对账不得发出任何 count 查询")
	reports := restarted.LastReconcileReports()
	require.Len(t, reports, 1)
	require.True(t, reports[0].Fallback)
	require.Equal(t, "reconcile_disabled", reports[0].FallbackReason)
}

// parseStatsCount 必须兼容两种已知返回形态，并对「看不懂」硬失败（绝不当作 0 漏发）。
func TestParseStatsCountShapes(t *testing.T) {
	value, err := parseStatsCount([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"42"]}]}}`))
	require.NoError(t, err)
	require.Equal(t, 42, value)

	value, err = parseStatsCount([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[1700000000,42]}]}}`))
	require.NoError(t, err)
	require.Equal(t, 42, value)

	value, err = parseStatsCount([]byte(`{"status":"success","data":{"result":[{"values":[{"field":"count()","value":"7"}]}]}}`))
	require.NoError(t, err)
	require.Equal(t, 7, value)

	value, err = parseStatsCount([]byte(`{"status":"success","data":{"result":[]}}`))
	require.NoError(t, err)
	require.Zero(t, value)

	_, err = parseStatsCount([]byte(`{"status":"success","data":{"result":[{"metric":{}}]}}`))
	require.Error(t, err, "无可识别计数必须硬失败（调用方据此回退整窗重发）")

	_, err = parseStatsCount([]byte(`not-json`))
	require.Error(t, err)

	_, err = parseStatsCount([]byte(`{"status":"error","error":"boom"}`))
	require.Error(t, err)
}

// 增量写入面与「计划天」错位必须硬失败（防「标记为已发布却没写数据」的静默丢数据）。
func TestSameUTCDaysRejectsMismatch(t *testing.T) {
	require.NoError(t, sameUTCDays([]string{"2026-09-22", "2026-09-23"}, []string{"2026-09-23", "2026-09-22"}))
	require.Error(t, sameUTCDays([]string{"2026-09-22"}, []string{"2026-09-22", "2026-09-23"}))
	require.Error(t, sameUTCDays([]string{"2026-09-22", "2026-09-24"}, []string{"2026-09-22", "2026-09-23"}))
}

// 配置归一化：越界/零值必须回退默认（配置误写不得让对账退化为无超时或高并发）。
func TestReconcileConfigNormalization(t *testing.T) {
	defaults := reconcileConfigOf(nil)
	require.True(t, defaults.Enabled)
	require.Equal(t, reconcileDefaultConcurrency, defaults.Concurrency)
	require.Equal(t, reconcileDefaultTimeout, defaults.Timeout)
	require.Equal(t, reconcileDefaultQueryTimeout, defaults.QueryTimeout)

	normalized := reconcileConfigOf(&ReconcileConfig{Enabled: true, Concurrency: 999, Timeout: -time.Second})
	require.Equal(t, reconcileMaxConcurrency, normalized.Concurrency)
	require.Equal(t, reconcileDefaultTimeout, normalized.Timeout)
	require.Equal(t, reconcileDefaultQueryTimeout, normalized.QueryTimeout)

	off := reconcileConfigOf(&ReconcileConfig{Enabled: false})
	require.False(t, off.Enabled)
}

// 复审 P2-10 回归：启动对账必须有**整批总预算**，且预算到期时在途查询要立刻收手。
//
// 现场（修复前）：对账在 ingest.New 里同步执行，只有「单源 30s + 并发 4」两道约束——
// 60 源最坏 ≈ ceil(60/4)×30s = 7.5 分钟，启动恢复被拖成分钟级，实例在此期间不可用。
//
// 转红：把 Budget 的 ctx 派生去掉（直接用传入 ctx）即红——本用例的 elapsed 断言会落在
// QueryTimeout（4s）上而不是预算（200ms）。
func TestStartupReconcileHonorsTotalBudget(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	// 对账查询挂起：只有预算/超时能把它收回来。
	fixture.setStallStats(true)
	insertsBefore := fixture.insertCount()

	started := time.Now()
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
		Reconcile: &ReconcileConfig{
			Enabled: true, Concurrency: 1,
			Timeout:      4 * time.Second, // 单源超时远大于预算：判别值必须来自预算本身
			QueryTimeout: 4 * time.Second, // 单次查询超时同样远大于预算
			Budget:       200 * time.Millisecond,
		},
	})
	elapsed := time.Since(started)
	require.NoError(t, err, "回退整窗重发必须成功：预算只影响「重发多少」，不影响「要不要重发」")
	require.Less(t, elapsed, 1500*time.Millisecond,
		"总预算必须把整批对账收回（实测 %s；预算 200ms，单源/单查询超时都是 4s）", elapsed)
	require.Equal(t, 2, len(fixture.payloadsSince(insertsBefore)),
		"预算耗尽的源必须回退整窗重发（两天各一次），绝不因「没来得及对账」而少发")

	reports := m.LastReconcileReports()
	require.Len(t, reports, 1)
	require.True(t, reports[0].Fallback, "预算到期 ⇒ 该源按最保守方向回退")
	require.NotEmpty(t, reports[0].FallbackReason)
	require.Empty(t, reports[0].MissingDays, "回退路径不按天裁剪")
}

// TestCloseReconcileBudgetNeverSkipsReplay 守住安全网：预算内**没来得及出结论**的源
// （Basis 为空，即 reconcileSource 从未跑到过）必须显式回退整窗重发，
// 绝不能按零值报告处理——那等于「零缺失天 → 零重发」，会把没对账的数据静默漏掉。
func TestCloseReconcileBudgetNeverSkipsReplay(t *testing.T) {
	m := &Manager{reconcile: DefaultReconcileConfig()}
	items := []startupSource{
		{key: "node:a/g1"}, // 未完成：Basis 为空
		{key: "node:b/g1"}, // 已完成：不带重发
	}
	reports := []ReconcileReport{
		{},
		{Source: "node:b/g1", Basis: reconcileBasisCountOnly},
	}
	closed := m.closeReconcileBudget(items, reports)
	require.True(t, closed[0].Fallback, "未完成的源必须回退整窗重发")
	require.Equal(t, reconcileBudgetExhausted, closed[0].FallbackReason)
	require.Equal(t, "node:a/g1", closed[0].Source)
	require.True(t, closed[0].needsReplay(), "回退必须真的触发重发")
	require.False(t, closed[1].Fallback, "已完成判定的源不得被改写")
	require.Equal(t, reconcileBasisCountOnly, closed[1].Basis)
}
