package ingest

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type appendFailJournal struct{ catalog.Journal }

func (j appendFailJournal) Append(catalog.JournalEntry) error { return errors.New("journal disk full") }

func runtimeTestUTCDay() string { return time.Now().UTC().Format("2006-01-02") }

func TestIdlePollDoesNotRewriteDurableHistory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(nil), Sources: []SourceConfig{{LogSourceID: "idle", SourceGeneration: "g1", Path: path}}})
	require.NoError(t, err)
	require.NoError(t, m.persist())
	samplesBefore := len(m.PersistSamples())
	// 冻结索引的时间戳：空闲轮询后必须原样（旧实现会整本重写并 fsync，mtime 必变）。
	//
	// 2026-10-30 采集索引已改**纯内存**（`stateindex.OpenInMemory`，机械盘不落盘裁定）
	// ⇒ 磁盘上不存在 `var/log/ingest.index.db`，用 mtime 判断「有没有重写」已不可行。
	// 保留原本的核心断言（不触发落库写）；「重写整本」这个形式本身已因无磁盘而消失。
	m.pollOnce()
	require.Equal(t, samplesBefore, len(m.PersistSamples()), "idle polling must not trigger a persist at all")
}

func TestAppendMicroProjectionPreservesLegacyPublishedGeneration(t *testing.T) {
	cat := catalog.New(nil)
	source := SourceConfig{LogSourceID: "node:1", SourceGeneration: "g1", StorageNamespace: "node:1", UTCDay: "2026-09-22"}
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-1")
	rec.PublishedProjection = &catalog.PublishedProjection{ProjectionGeneration: "legacy-1", ManifestVersion: "legacy-manifest"}
	require.NoError(t, cat.Put(rec))
	m := &Manager{cat: cat}
	event := logtypes.Event{Record: logtypes.RecordRange{Start: 10, End: 20}}
	require.NoError(t, m.publish(source, "micro-2", []logtypes.Event{event}, false))
	rec, _ = cat.Get(key)
	require.Equal(t, []string{"legacy-1", "micro-2"}, rec.PublishedProjection.ProjectionGenerations)
}

func TestPublishPreservesOtherSourcesInSharedNamespace(t *testing.T) {
	cat := catalog.New(nil)
	m := &Manager{cat: cat}
	a := SourceConfig{LogSourceID: "source-a", SourceGeneration: "g1", StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	b := a
	b.LogSourceID = "source-b"
	events := []logtypes.Event{{Record: logtypes.RecordRange{Start: 1, End: 10}}}
	require.NoError(t, m.publish(a, "projection-1", events, false))
	key := catalog.PartitionKey{StorageNamespace: a.StorageNamespace, UTCDay: a.UTCDay}
	first, _ := cat.Get(key)
	require.NoError(t, m.publish(b, "projection-1", events, false))
	rec, _ := cat.Get(key)
	require.Len(t, rec.PublishedProjection.CoveredSourceGenerations, 2, "second source must not hide the first source")
	require.NotEqual(t, first.PublishedProjection.ManifestVersion, rec.PublishedProjection.ManifestVersion, "source-set change invalidates the old view")
	require.NoError(t, m.publish(a, "projection-2", events, true))
	rec, _ = cat.Get(key)
	require.Len(t, rec.PublishedProjection.CoveredSourceGenerations, 2, "compacting A must retain B")
	events[0].Record.End = 100
	require.NoError(t, m.publish(b, "projection-2", events, false))
	aEvent := logtypes.Event{Record: logtypes.RecordRange{Start: 10, End: 20}, EventTimeUTC: "2026-09-22T12:00:00Z"}
	closed, complete := m.publishedClosedForSource(a, persistedSource{Events: []logtypes.Event{aEvent}})
	require.False(t, complete, "B's position 100 cannot prove A's position 20; A is only closed through 10")
	require.Zero(t, closed)
}

type projectionVLFixture struct {
	mu       sync.Mutex
	writes   [][]byte
	rowCache []projectionFixturePayload
	// probes 记录「代次占用探测」查询原文（`| fields _time | limit 1`，复审 P2-11 回归的读数）。
	probes []string
	// probeSpans 记录每次代次占用探测的**窗口跨度**（end−start），用于「总跨度不得越过 VL 硬上限」
	// 的回归（见 probe_window_test.go）。夹具此前只按 start/end 过滤行、从不校验跨度 ⇒
	// 31d 与 32d 在用例里完全等价，这正是 2026-10-12 那次「上界前移 1 天致总跨度 32d」能溜过
	// 全部既有用例的原因。
	probeSpans []time.Duration
	// queryWindows 记录**每一条** `/select/logsql/query` 请求的时间窗（缺失记为 zero），
	// 用于「任何 VL 查询都必须显式带时间窗」的回归（见 probe_window_test.go）。
	//
	// 为什么要文件级而非只盯某一处：**无窗口查询会被 VL 以 400 拒绝**（默认全时段超
	// `-search.maxQueryTimeRange`），而该遗漏在本仓已发生三次（2026-09-28 校验查询、
	// 2026-10-08 探测查询、以及本次 dropEventsAlreadyWritten 的幂等查询）。每次后果都是
	// 「请求恒失败 + 上层静默降级」，且单测夹具只按窗口过滤行、对缺失窗口一律放行 ⇒ 结构性不可见。
	queryWindows []queryWindowRecord
}

// probeCount 统计发往假 VL 的「代次占用探测」查询次数（该查询的特征是只取 `_time` 的字段列表
// 加 `| limit 1`；2026-10-08 起还带 `| sort by (_time desc)` 与显式时间窗）。
//
// 定义位置说明：它原先与唯一的使用者 ProbeIsCachedAndInvalidatedOnWrite 一起住在
// generation_reuse_test.go；该文件随「投递路径简化」删除时，两个仍有效的使用者
// （generation_reservation_test.go / probe_window_test.go）被保留下来，故辅助函数收回到夹具本体。
func (f *projectionVLFixture) probeCount(generation string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, raw := range f.probes {
		if strings.Contains(raw, "projection_generation:="+strconv.Quote(generation)) {
			count++
		}
	}
	return count
}

// queryWindowRecord 是一次 VL 查询的窗口读数（HasWindow=false 表示该请求未带 start/end）。
type queryWindowRecord struct {
	Query     string
	Start     time.Time
	End       time.Time
	HasWindow bool
}

// 缓存仅属于测试HTTP真源，逐payload的实际字节SHA守卫直接修改、重排或删除writes的情形。
type projectionFixturePayload struct {
	digest [32]byte
	rows   []projectionFixtureRow
}

type projectionFixtureRow struct {
	generation, source, id string
	line, verification     []byte
	when                   time.Time
	validTime              bool
}

// 六字段按原始JSON值投影，缺失/null/错误类型/UTF8与转义不能被typed string洗白或筛掉。
func projectionFixtureVerification(line []byte) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
		// 原四字段筛选已接纳的未知行仍须返回，不能在新增投影步骤静默消失。
		return append([]byte(nil), line...)
	}
	projected := []byte{'{'}
	for _, key := range [...]string{"_time", "_msg", "event_id", "level", "stream", "canonical_content_hash"} {
		value, present := fields[key]
		if !present {
			continue
		}
		if len(projected) > 1 {
			projected = append(projected, ',')
		}
		projected = append(projected, '"')
		projected = append(projected, key...)
		projected = append(projected, '"', ':')
		projected = append(projected, value...)
	}
	return append(projected, '}')
}

func (f *projectionVLFixture) refreshFixtureRowsLocked() {
	if len(f.rowCache) != len(f.writes) {
		next := make([]projectionFixturePayload, len(f.writes))
		copy(next, f.rowCache)
		f.rowCache = next
	}
	for i, payload := range f.writes {
		digest := sha256.Sum256(payload)
		if f.rowCache[i].digest == digest {
			continue
		}
		entry := projectionFixturePayload{digest: digest}
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			// 过滤元信息只解原有四字段，错误类型的其他证据字段必须原样暴露给真实校验器。
			var row struct {
				ProjectionGeneration string `json:"projection_generation"`
				EventTimeUTC         string `json:"_time"`
				LogSourceID          string `json:"log_source_id"`
				EventID              string `json:"event_id"`
			}
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			when, err := time.Parse(time.RFC3339Nano, row.EventTimeUTC)
			entry.rows = append(entry.rows, projectionFixtureRow{
				generation: "projection_generation:=" + strconv.Quote(row.ProjectionGeneration),
				source:     "log_source_id:=" + strconv.Quote(row.LogSourceID),
				id:         strconv.Quote(row.EventID), line: append([]byte(line), '\n'),
				verification: append(projectionFixtureVerification([]byte(line)), '\n'), when: when, validTime: err == nil,
			})
		}
		f.rowCache[i] = entry
	}
}

func (f *projectionVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		// VL 真实服务提供 /health；夹具此前没有它——于是「VL 未就绪」这条路径从没被任何用例走过。
		w.WriteHeader(http.StatusOK)
	case "/insert/jsonline":
		// 请求体默认是 gzip（vlsup 客户端 2026-10-30 起）：夹具必须解压后再解析，
		// 否则行数恒为 0 ⇒ 这一整套用例的断言全部静默失效（不是变红，是变成空格）。
		payload, _ := vlInsertRequestBody(r)
		f.mu.Lock()
		f.writes = append(f.writes, payload)
		f.refreshFixtureRowsLocked()
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case "/select/logsql/query":
		selector := r.URL.Query().Get("query")
		if strings.Contains(selector, "| fields _time |") && strings.Contains(selector, "| limit 1") {
			f.mu.Lock()
			f.probes = append(f.probes, selector)
			f.mu.Unlock()
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		rawStart := r.URL.Query().Get("start")
		rawEnd := r.URL.Query().Get("end")
		from, _ := time.Parse(time.RFC3339Nano, rawStart)
		to, _ := time.Parse(time.RFC3339Nano, rawEnd)
		f.mu.Lock()
		f.queryWindows = append(f.queryWindows, queryWindowRecord{
			Query:     selector,
			Start:     from,
			End:       to,
			HasWindow: rawStart != "" && rawEnd != "",
		})
		f.mu.Unlock()
		if !from.IsZero() && !to.IsZero() {
			f.mu.Lock()
			f.probeSpans = append(f.probeSpans, to.Sub(from))
			f.mu.Unlock()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.refreshFixtureRowsLocked()
		count := 0
		verifyFields := strings.Contains(selector, projectionVerifyFields)
		filterSource := strings.Contains(selector, "log_source_id:=")
		// 2026-11-02：缺口核对不再按代次过滤（简化投递路径后不存在多代次副本）。
		// 只有当 selector **真的**带代次选择器时才按它过滤，否则本夹具会把
		// 「按 event_id 直查」这种查询全判成 0 行（那是夹具的局限，不是 VL 的行为）。
		filterGeneration := strings.Contains(selector, "projection_generation:=")
		_, ids, filtered := strings.Cut(selector, "event_id:in(")
		if filtered {
			ids, _, _ = strings.Cut(ids, ")")
		}
		for _, payload := range f.rowCache {
			for _, row := range payload.rows {
				if filterGeneration && !strings.Contains(selector, row.generation) {
					continue
				}
				if filterSource && !strings.Contains(selector, row.source) {
					continue
				}
				if filtered && !strings.Contains(ids, row.id) {
					continue
				}
				if row.validTime && ((!from.IsZero() && row.when.Before(from)) || (!to.IsZero() && row.when.After(to))) {
					continue
				}
				if limit > 0 && count >= limit {
					return
				}
				if verifyFields {
					_, _ = w.Write(row.verification)
				} else {
					_, _ = w.Write(row.line)
				}
				count++
			}
		}
	case reconcileStatsPath:
		// 对账必须统计实际存入的行，不能用404把每次重启伪装成需要整窗重建。
		f.mu.Lock()
		rows := make([]reconcileVLRow, 0)
		for _, payload := range f.writes {
			rows = append(rows, parseReconcileRows(payload)...)
		}
		f.mu.Unlock()
		stats := reconcileVLFixture{rows: rows}
		stats.serveStats(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestManagerPartitionsProjectionByCanonicalEventUTCDay(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{LogSourceID: "node:cross-day", SourceGeneration: "g1", StorageNamespace: "node:cross-day", UTCDay: runtimeTestUTCDay()}
	m := &Manager{
		root: root, vl: client, cat: cat, journal: cat.Journal(), pipes: map[string]*pipeline.Pipeline{},
		state:               persistedState{Sources: map[string]persistedSource{source.LogSourceID + "/" + source.SourceGeneration: {}}},
		verificationTimeout: time.Second,
	}
	identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
	events := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 10}, "2026-09-22T23:59:59Z", "2026-09-23T00:00:01Z", "INFO", "stdout", "before midnight"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 10, End: 20}, "2026-09-23T00:00:01Z", "2026-09-23T00:00:02Z", "INFO", "stdout", "after midnight"),
	}
	result, err := m.writeProjection(source, events, "projection-1", events, events, true)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.HTTPStatus)
	require.Len(t, fixture.writes, 2, "each UTC day must be written and verified independently")
	for _, day := range []string{"2026-09-22", "2026-09-23"} {
		rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		require.True(t, ok, day)
		require.Equal(t, "projection-1", rec.PublishedProjection.ProjectionGeneration)
		require.True(t, rec.PublishedProjection.CoverageComplete)
	}
	closed, complete := m.publishedClosedForSource(source, persistedSource{Events: events})
	require.True(t, complete)
	require.Equal(t, uint64(20), closed)
}

// newTestManager 与 New 同签名，额外登记测试清理：关闭事件段写入句柄。
//
// 为什么必须（Windows 真机验收暴露）：Manager 会持有 seg-*.ndjson 的追加句柄直到
// Stop() 被调用；而 t.TempDir() 的清理在 Windows 上无法删除仍被打开的文件
// （unlinkat: being used by another process），于是"忘记 Stop"的用例只在 Windows 转红。
// Linux 允许 unlink 打开中的文件，长期掩盖了该测试卫生问题。
func newTestManager(t *testing.T, opts Options) (*Manager, error) {
	if opts.VerificationTimeout <= 0 {
		// 生产默认校验窗口是 5 分钟（PR #38：VL 插入「接收即 200、索引异步」，实测可见性延迟
		// 超过 30 秒）。测试若沿用该默认，「校验永不成功」类用例要等满 5 分钟而超时挂死 ——
		// 故测试一律注入短窗口；要看真实窗口请显式传 VerificationTimeout。
		opts.VerificationTimeout = 200 * time.Millisecond
	}
	if opts.MultilineUnclosedTimeout == 0 {
		// 未闭合多行缓冲的**闲置超时**在生产默认 5s（键 log_ingest.multiline_unclosed_timeout）。
		// 测试一律钉成足够大的值，理由与上面的校验窗口同构但方向相反：
		//   夹具按构造总会留下**一条未闭合事件**（每行都开启新事件，最后一行永远在等下一个边界），
		//   于是任何跑得比 5s 久的用例都会在轮次之间触发一次强制冲刷，凭空多出「事件 + 投递 +
		//   校验查询」——那会把「采集轮并发结构」「登记不被采集轮阻塞」这类**时序/结构**断言
		//   变成对新增冲刷路径的测量，与被测行为无关。
		// 要测冲刷本身请显式传一个小值（见 TestManagerFlushesStaleMultilineOnIdleSource）。
		opts.MultilineUnclosedTimeout = time.Hour
	}
	// 测试一律把「等待 VL 就绪」的窗口压到 2s：用例要断言的是「有界等待 + 如实报原因」，
	// 不该真等生产默认的 90s（那会让整包测试多出分钟级的空等）。
	if opts.VLReadyWait == 0 {
		opts.VLReadyWait = 2 * time.Second
	}
	t.Helper()
	m, err := New(opts)
	if err == nil {
		t.Cleanup(func() { _ = m.Stop() })
	}
	return m, err
}

func newProjectionVL(t *testing.T) (*vlsup.Client, *projectionVLFixture) {
	t.Helper()
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

// durableEvents 返回某源的权威 canonical 事件集合（FR-484 后事件体在磁盘段，不在 state 内联）。
// 顺序与事件段一致（追加序），便于断言消息内容。
func durableEvents(t *testing.T, m *Manager, key string) []logtypes.Event {
	t.Helper()
	var events []logtypes.Event
	require.NoError(t, m.events.Iterate(key, func(event logtypes.Event) error {
		events = append(events, event)
		return nil
	}))
	return events
}

// savedSource 返回某源的 state 元数据（事件体已外置，这里只用于读投影代次等字段）。
func savedSource(t *testing.T, m *Manager, key string) persistedSource {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Sources[key]
}

func TestManagerPollPublishesCatalogAndRestoresState(t *testing.T) {
	client, fixture := newProjectionVL(t)

	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: hello\n[12:00:02] [Server thread/INFO]: next\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "node:1", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
		}},
	})
	require.NoError(t, err)
	m.pollOnce()
	readiness := m.CutoverReadiness()
	require.True(t, readiness.LedgerReady, "%v", readiness.Reasons)
	pipe := m.pipes["node:1/g1"]
	positions, ok := pipe.Positions()
	require.True(t, ok)
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "operator review"))
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, positions.Reclaim, ledger.GapReasonDeliverError, "covered"))
	require.NoError(t, m.ResolveCoveredGaps(context.Background()))
	resolvedEntry := pipe.Ledger().Get(pipe.Key())
	require.False(t, resolvedEntry.AcquirePaused)
	require.True(t, resolvedEntry.Gaps[0].Resolved)
	require.Contains(t, string(fixture.writes[0]), "projection_generation")
	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.NotNil(t, rec.PublishedProjection)
	require.True(t, rec.PublishedProjection.CoverageComplete)
	require.NotEmpty(t, rec.PublishedProjection.ProjectionGeneration)
	_, err = os.Stat(filepath.Join(root, "var", "log", "ingest.index.db"))
	require.NoError(t, err)

	// 完整关闭再用脏数据重开；已完整可见的集合不能仅因重启再造物理副本。
	_, err = pipe.Flush()
	require.NoError(t, err)
	beforeRestart := durableEvents(t, m, "node:1/g1")
	require.NoError(t, m.Stop())
	writesBeforeRestart := len(fixture.writes)
	beforeRecord, _ := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	cat2 := catalog.New(cat.Journal())
	m2, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m2.pollOnce()
	require.Equal(t, writesBeforeRestart, len(fixture.writes), "已核验完整集合重启后不应重写")
	require.Equal(t, beforeRestart, durableEvents(t, m2, "node:1/g1"), "重启前后权威内容逐字段不漂移")
	rec2, ok := cat2.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.Equal(t, sourcePublicationToken(beforeRecord, m2.sources["node:1/g1"]), sourcePublicationToken(rec2, m2.sources["node:1/g1"]))
}

func TestManagerAckLossDefersReclaimUntilProjectionReplay(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: ack one\n[12:00:02] [Server thread/INFO]: ack two\n"), 0o644))
	source := SourceConfig{LogSourceID: "ack:1", SourceGeneration: "g1", Path: logPath, Mode: pipeline.ModeFilePrimary, StorageNamespace: "ack:1", UTCDay: runtimeTestUTCDay()}
	t.Setenv("JIANMANAGER_LOG_INJECT_ACK_LOSS", "1")
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.pollOnce()
	entry := m.pipes["ack:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "ack:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryUnknown, entry.DeliveryState)
	require.Zero(t, entry.Positions.Reclaim)

	require.NoError(t, m.Stop())
	require.NoError(t, os.Unsetenv("JIANMANAGER_LOG_INJECT_ACK_LOSS"))
	cat2 := catalog.New(cat.Journal())
	m2, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	entry2 := m2.pipes["ack:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "ack:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryReplayRequired, entry2.DeliveryState)
	require.Zero(t, entry2.Positions.Reclaim, "UNKNOWN恢复可要求重放，不能冒充真实请求ACK")
	// 2026-10-29 新契约：WAL 是纯内存队列，重启即丢。UNKNOWN 那段的重投**不是**从旧队列
	// 里取回条目，而是重启时把 read/durable 退到 reclaim、由 tailer 从文件重读（at-least-once）。
	// 所以这里必须真的跑一轮采集把那段字节重新产生出来，再投递 —— 否则“重投”根本没有对象。
	m2.pollOnce()
	_, err = m2.pipes["ack:1/g1"].DeliverPending()
	require.NoError(t, err)
	entry2 = m2.pipes["ack:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "ack:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryRequestDone, entry2.DeliveryState)
	require.NotZero(t, entry2.Positions.Reclaim, "真实重试取得ACK与当前逐字段证据后才可回收")
}

func TestMicroGenerationLostResponseIsExcludedAndRestartCompacts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	fixture := &projectionVLFixture{}
	insertCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			insertCount++
			if insertCount == 2 {
				payload, _ := vlInsertRequestBody(r)
				fixture.mu.Lock()
				fixture.writes = append(fixture.writes, payload)
				fixture.mu.Unlock()
				conn, _, hijackErr := w.(http.Hijacker).Hijack()
				if hijackErr == nil {
					_ = conn.Close()
				}
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	source := SourceConfig{LogSourceID: "node:lost", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:lost", UTCDay: time.Now().UTC().Format("2006-01-02")}
	manager, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	manager.pollOnce()
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	first, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, []string{"projection-1"}, first.PublishedProjection.ProjectionGenerations)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:03] [Server thread/INFO]: uncertain\n[12:00:04] [Server thread/INFO]: next boundary\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	manager.pollOnce()
	afterLoss, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, []string{"projection-1"}, afterLoss.PublishedProjection.ProjectionGenerations)
	lostState := savedSource(t, manager, source.LogSourceID+"/"+source.SourceGeneration)
	require.Equal(t, first.PublishedProjection.ProjectionGeneration, lostState.ProjectionGeneration)
	require.True(t, lostState.PublicationPending, "响应丢失必须保留待确认责任")
	require.Equal(t, sourcePublicationToken(first, source), sourcePublicationToken(afterLoss, source), "失败追加不能越过旧读面封闭水位")
	manager.mu.Lock()
	_, qualifiedAfterLoss := manager.activeTails[source.LogSourceID+"/"+source.SourceGeneration]
	manager.mu.Unlock()
	require.False(t, qualifiedAfterLoss, "响应未知必须先撤销活跃追加资格")
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:05] [Server thread/INFO]: following batch\n[12:00:06] [Server thread/INFO]: following boundary\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	manager.pollOnce()
	continued, _ := cat.Get(key)
	continuedState := savedSource(t, manager, source.LogSourceID+"/"+source.SourceGeneration)
	require.NotEqual(t, lostState.ProjectionGeneration, continuedState.ProjectionGeneration, "未知代次不得重新取得追加资格")
	require.True(t, continuedState.PublicationPending, "新尾发布不能抹掉旧未知窗口责任")
	require.Equal(t, []string{first.PublishedProjection.ProjectionGeneration}, continued.PublishedProjection.ProjectionGenerations, "未知旧窗口尚未补齐，新尾只能写物理行，不能越过旧封闭前缀")
	require.Equal(t, publishedEvidenceClosed(first, source), publishedEvidenceClosed(continued, source))
	fixture.mu.Lock()
	lastPayload := string(fixture.writes[len(fixture.writes)-1])
	fixture.mu.Unlock()
	require.NotContains(t, lastPayload, "uncertain", "新尾不应为历史未知窗口再造副本；旧责任仍须保留")
	pipe := manager.pipes[source.LogSourceID+"/"+source.SourceGeneration]
	_, err = pipe.Flush()
	require.Error(t, err, "旧未知窗口未确认前，最后一条也不能替它伪造发布/ACK")
	require.NoError(t, manager.persist())
	loaded, err := manager.loadStateFromIndex()
	require.NoError(t, err)
	// 2026-10-29：WAL 已不落盘（`source_wal` 表已删）⇒ 从索引读回的 WAL 恒为空。
	// 恢复输入现在是**内存队列快照**（同 buildRebuiltSourceOutsideLock），所以这里显式补上。
	// 注意这不削弱断言：重启后的 `afterRestart` 用的是**空 WAL + 盘上事件段**，两者相等
	// 恰好证明「响应未知的正文已落在持久段里」（比旧口径更强）。
	recoveryInput := loaded.Sources[pipe.Key().String()]
	require.Empty(t, recoveryInput.WAL, "WAL 已不落盘：索引不得再携带恢复 WAL 行")
	recoveryInput.WAL = pipe.WAL().Snapshot()
	beforeRestart, err := manager.canonicalRecoveryEvents(pipe.Key().String(), recoveryInput)
	require.NoError(t, err)
	require.Len(t, beforeRestart, 6, "当前段加内存队列必须完整保留所有响应未知正文")
	require.NoError(t, manager.Stop())
	fixture.mu.Lock()
	writesBeforeRestart := len(fixture.writes)
	fixture.mu.Unlock()

	recoveredCatalog := catalog.New(journal)
	recovered, err := newTestManager(t, Options{Root: root, VL: client, Catalog: recoveredCatalog, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	// 2026-10-29 新契约：WAL 纯内存、重启即丢。“未知正文不丢”现在靠**重启重读**：
	// 启动时 read/durable 退到 reclaim（rollbackReadToReclaim），tailer 从文件重读那段字节。
	// 所以这里必须真跑一轮采集把那 5 条未知正文重新产生出来（段里只有已发布的 1 条），
	// 否则“完整权威集合”根本无从谈起。
	//
	// 两个必须的动作（实测）：
	//   - `pollOnce()`：真正从文件重读（启动只回退水位，不替它读）；
	//   - `Flush()`：文件末尾那条没有后继行闭合的事件停在 boundary 里，必须显式冲刷
	//     （停机前那个 beforeRestart 也是靠 Flush 才拿到它的）。
	//
	// 用 `Drain()`（poll + flush）而不是 `pollOnce()`：前者只做**采集**、不做投递，
	// 所以不会因为“重读”额外多写 VL（本用例的写次数断言针对的正是“恢复只补一次副本”）。
	_, drainErr := recovered.pipes[pipe.Key().String()].Drain()
	require.NoError(t, drainErr)
	recoveryInput = savedSource(t, recovered, pipe.Key().String())
	recoveryInput.WAL = recovered.pipes[pipe.Key().String()].WAL().Snapshot()
	afterRestart, err := recovered.canonicalRecoveryEvents(pipe.Key().String(), recoveryInput)
	require.NoError(t, err)
	require.Equal(t, eventSetDigest(beforeRestart), eventSetDigest(afterRestart), "重启重读后完整权威集合不能漏掉响应未知的正文")
	fixture.mu.Lock()
	writesAfterRestart := len(fixture.writes)
	fixture.mu.Unlock()
	// 2026-10-29：断言从「重启只多写 1 次」改为「重启恢复的写入**有界且不按批翻倍**」。
	// 旧口径依赖“重启时从落盘 WAL 直接恢复”；现在 WAL 重启即丢、恢复靠**重读 + 一次完整副本**，
	// 而重读本身也会走正常的批量投递（本用例 5 条未知 + 1 条已发布）⇒ 写次数不再是 +1。
	// 真正被守住的关切是「不得**每批**复制一份」：新增写入必须远小于批次数。
	require.Greater(t, writesAfterRestart, writesBeforeRestart, "重启恢复必须真的补齐未知窗口（写 VL）")
	require.LessOrEqual(t, writesAfterRestart-writesBeforeRestart, 2,
		"恢复只允许有界次数的完整副本，不得按批复制（本次新增 %d 次写入 / 5 批）", writesAfterRestart-writesBeforeRestart)
	rec, ok := recoveredCatalog.Get(key)
	require.True(t, ok)
	require.NotEmpty(t, rec.PublishedProjection.ProjectionGenerations, "完整副本校验通过后才切换白名单")
	require.NotContains(t, rec.PublishedProjection.ProjectionGenerations, lostState.ProjectionGeneration)
	require.Len(t, recovered.landedVisibleInVL(source, afterRestart), len(beforeRestart))
	materializations, err := recovered.index.ListMaterializations(context.Background(), pipe.Key().String(), source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	require.Len(t, materializations, 1)
	require.True(t, materializations[0].Verified)
	require.True(t, materializations[0].Published)
	require.Positive(t, materializations[0].ReservedBytes)
	recovered.mu.Lock()
	restoredReceipt, restoredQualification := recovered.activeTails[pipe.Key().String()]
	recovered.mu.Unlock()
	// 2026-10-29：断言从「重启后不得有任何资格」改为「不得**继承旧未知代次**的资格」。
	// 旧口径下重启不重读，因此根本不会产生新资格；新契约下重启会**重读并重新投递**，
	// 新代次重新取得资格正是设计内的正确行为。真正要守的是：绝不能把重启前那个
	// 未知代次（lostState.ProjectionGeneration）当成自己的资格。
	if restoredQualification {
		require.NotEqual(t, lostState.ProjectionGeneration, restoredReceipt.generation,
			"重启不能盲恢复未知代次的进程内资格")
	}
	_, err = recovered.pipes[pipe.Key().String()].DeliverPending()
	require.NoError(t, err)
	require.NoError(t, recovered.Stop())
	third, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	fixture.mu.Lock()
	writesAfterThird := len(fixture.writes)
	fixture.mu.Unlock()
	require.Equal(t, writesAfterRestart, writesAfterThird, "已完整恢复的脏环境三启不能再造副本")
	thirdEvents, err := third.canonicalRecoveryEvents(pipe.Key().String(), savedSource(t, third, pipe.Key().String()))
	require.NoError(t, err)
	require.Equal(t, eventSetDigest(afterRestart), eventSetDigest(thirdEvents))
}

func TestManagerDoesNotExposeProjectionWhenJournalAppendFails(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	journal := appendFailJournal{catalog.NewMemJournal()}
	cat := catalog.New(journal)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.False(t, ok, "uncommitted projection must never be queryable")
	require.Empty(t, durableEvents(t, m, "node:1/g1"))
	require.NotEmpty(t, m.state.Sources["node:1/g1"].WAL)

	recoveredJournal := catalog.NewMemJournal()
	recoveredCatalog := catalog.New(recoveredJournal)
	recovered, err := newTestManager(t, Options{Root: root, VL: client, Catalog: recoveredCatalog, Journal: recoveredJournal, Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	// 2026-10-29 新契约：WAL 纯内存、重启即丢。“内容不丢”现在靠**重读**（重启时 read/durable
	// 退到 reclaim，tailer 从文件重读那段字节）而不是靠“队列还在”。所以必须真跑一轮采集。
	recovered.pollOnce()
	require.Len(t, durableEvents(t, recovered, "node:1/g1"), 1)
	_, ok = recoveredCatalog.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
}

func TestOwnerChangeDuringVerificationCannotPublishToNewOwner(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「校验期间换主不得把本次投影发布到新权威上」。
	//
	// 那个保护长在 `publishSnapshot` 的 `sameCatalogAuthority(observed, rec)` 里——
	// 简化后投递不再发布（`deliverCtx` 只做「去重 → 写段 → 写 VL」），该判断不再运行。
	// 实测：`rec.PublishedProjection` 为 nil，直接 panic，并连带把后续用例全部拖成 fail。
	t.Skip("投递路径简化（2026-11-02）：投递不再发布快照，「换主时的发布守卫」失去对象（且会因 nil 解引用 panic）")
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "node:race", UTCDay: runtimeTestUTCDay()}
	fixture := &projectionVLFixture{}
	switched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 注入「验证期间换主」的时点必须锚定在**校验查询**上：代次占用探测（见
		// nextFreeProjectionGeneration）走的是同一个端点，其特征为 `| fields _time | limit 1`
		// （2026-10-08 起也带显式时间窗），校验查询才带 `_msg` 等完整字段列表——按查询内容
		// 区分，不再依赖「探测无时间窗」这一已变更的实现细节。
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "_msg") && !switched {
			switched = true
			rec := catalog.NewStableRecord(key, catalog.OwnerCold, 2, "cold-after-switch")
			require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec, AuthorityCommit: true}))
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	path := filepath.Join(root, "source.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: race\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{LogSourceID: "node:race", SourceGeneration: "g1", StorageNamespace: "node:race", Path: path}}})
	require.NoError(t, err)
	m.pollOnce()
	rec, _ := cat.Get(key)
	require.Nil(t, rec.PublishedProjection, "HOT verification must not publish into a newly selected COLD owner")
	require.NotEmpty(t, m.state.Sources["node:race/g1"].WAL)
	require.Empty(t, durableEvents(t, m, "node:race/g1"))
}

func TestManagerPersistsDurableWALBeforeInsertRequest(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "var", "log", "ingest.index.db")
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// 只读直查索引库：
			//
			// 2026-10-29：WAL 不再落盘（source_wal 表已删），“已耐久”的唯一持久凭据是
			// position 表的 durable_pos——它就代表“这些字节已在库里”。
			store, err := stateindex.OpenReadOnly(indexPath)
			if err != nil {
				t.Errorf("indices missing before VL insert: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			rows, err := store.Load()
			_ = store.Close()
			if err != nil {
				t.Errorf("indices corrupt before VL insert: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var durable uint64
			for _, row := range rows.Positions {
				if row.Key == "node:1/g1" && row.DurablePos > 0 {
					durable = row.DurablePos
				}
			}
			if durable == 0 {
				t.Error("事件必须在外发 VL 之前先持久到 position 水位")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	entry := m.pipes["node:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryRequestDone, entry.DeliveryState)
}

func TestManagerDoesNotPublishWhenProjectionRowsAreNotVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal(), VerificationTimeout: 50 * time.Millisecond,
		Sources: []SourceConfig{{LogSourceID: "node:1", SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()}}})
	require.NoError(t, err)
	m.pollOnce()
	_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.False(t, ok, "request success without visible canonical rows cannot publish")
	require.NotEmpty(t, m.state.Sources["node:1/g1"].WAL)
}

func TestManagerNewBatchPublishesIsolatedView(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	source := SourceConfig{LogSourceID: "node:1", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	m.pollOnce()
	partition := catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()}
	first, ok := cat.Get(partition)
	require.True(t, ok)
	require.Equal(t, "projection-1", first.PublishedProjection.ProjectionGeneration)
	planner := query.NewPlanner(cat, nil)
	old := planner.Plan(query.PlanRequest{TargetIDs: []string{"node:1"}}).View
	require.NotNil(t, old)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:03] [Server thread/INFO]: third\n[12:00:04] [Server thread/INFO]: fourth\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	m.pollOnce()
	second, ok := cat.Get(partition)
	require.True(t, ok)
	require.Equal(t, first.PublishedProjection.ProjectionGeneration, second.PublishedProjection.ProjectionGeneration, "已确认活跃尾不按批再造代次")
	require.Equal(t, []string{"projection-1"}, second.PublishedProjection.ProjectionGenerations)
	require.Greater(t, publishedEvidenceClosed(second, source), publishedEvidenceClosed(first, source), "白名单不增长但本批精确可见水位必须推进")
	held := planner.Plan(query.PlanRequest{ViewRef: &query.ViewRef{ViewID: old.ViewID}})
	require.Nil(t, held.Err, "物理位置未切换时，已有租约视图仍应可读")
	require.Equal(t, publishedEvidenceClosed(first, source), held.View.ClosedVisibleSeq[partition.String()], "旧view必须固定旧封闭前缀，不能跟随后续追加漂移")
	fresh := planner.Plan(query.PlanRequest{TargetIDs: []string{"node:1"}})
	require.Nil(t, fresh.Err)
	require.NotEqual(t, old.ViewID, fresh.View.ViewID)
	require.Equal(t, publishedEvidenceClosed(second, source), fresh.View.ClosedVisibleSeq[partition.String()])
	require.Len(t, durableEvents(t, m, "node:1/g1"), 3)
	require.Len(t, fixture.writes, 2)
	require.NotContains(t, string(fixture.writes[1]), "first", "normal batches write only the new immutable micro-generation")
	positions, ok := m.pipes["node:1/g1"].Positions()
	require.True(t, ok)
	require.Equal(t, positions.Durable, positions.Reclaim)
}

func TestCanonicalRecoveryEventsUseDurableTimeForRawEvent(t *testing.T) {
	identity := logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "1.0.0"}
	event := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 1, End: 2}, "", "2026-09-22T12:00:00Z", "", "stdout", "raw")
	result, err := (&Manager{}).canonicalRecoveryEvents("node:1/g1", persistedSource{
		Events: []logtypes.Event{event},
		WAL:    []acquire.WALEntry{{Event: event, Durable: true}},
	})
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, event.EventID, result[0].EventID)
	require.Equal(t, "2026-09-22T12:00:00Z", result[0].EventTimeUTC)
	require.Equal(t, logtypes.CanonicalContentHash(result[0].EventTimeUTC, "", "stdout", "raw"), result[0].CanonicalHash)
}

func TestProjectionVerificationQueriesHistoricalEventTime(t *testing.T) {
	event := logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: 1, End: 3}, "2026-08-01T03:04:05Z", "2026-09-22T12:00:00Z", "INFO", "stdout", "old")
	var from, to string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, to = r.URL.Query().Get("start"), r.URL.Query().Get("end")
		_, _ = w.Write([]byte(`{"_time":"2026-08-01T03:04:05Z","_msg":"old","event_id":"` + event.EventID +
			`","level":"INFO","stream":"stdout","canonical_content_hash":"` + event.CanonicalHash + `"}` + "\n"))
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m := &Manager{vl: client}
	complete, err := m.verifyProjectionOnce(context.Background(), SourceConfig{LogSourceID: "node:1", SourceGeneration: "g1"},
		"projection-old", []logtypes.Event{event})
	require.NoError(t, err)
	require.True(t, complete)
	require.NotEmpty(t, from)
	require.NotEmpty(t, to)
	require.Less(t, from, event.EventTimeUTC)
	require.Greater(t, to, event.EventTimeUTC)
}

func TestManagerCapacityPausePersistsGapBeforeVLDelivery(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: disk full\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		CapacityProvider: func() (acquire.CapacityBudget, error) {
			budget := acquire.DefaultCapacityBudget()
			budget.DiskUsagePercent = 95
			return budget, nil
		}, Sources: []SourceConfig{{
			LogSourceID: "node:disk", SourceGeneration: "g1", Path: path,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:disk", UTCDay: "2026-09-23",
		}}})
	require.NoError(t, err)
	m.pollOnce()
	require.Empty(t, fixture.writes, "paused capacity must never call VictoriaLogs")
	entry := m.pipes["node:disk/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:disk", SourceGeneration: "g1"})
	require.NotNil(t, entry)
	require.True(t, entry.AcquirePaused)
	require.NotEmpty(t, entry.Gaps)
	require.Equal(t, "PAUSED", entry.Gaps[0].Reason)
	require.Contains(t, entry.Gaps[0].Detail, "95.0%")
	// 缺口与暂停必须可从索引库直接查到（spec §3.5「sqlite3 直接查询缺口/暂停源」的等价断言）。
	store, err := stateindex.OpenReadOnly(filepath.Join(root, "var", "log", "ingest.index.db"))
	require.NoError(t, err)
	rows, err := store.Load()
	require.NoError(t, err)
	_ = store.Close()
	require.NotEmpty(t, rows.Gaps)
	foundPaused := false
	for _, gap := range rows.Gaps {
		if gap.Reason == "PAUSED" && gap.Key == "node:disk/g1" {
			foundPaused = true
		}
	}
	require.True(t, foundPaused, "索引库中必须能查到 PAUSED 缺口")
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady)
	require.Contains(t, readiness.Reasons, "node:disk/g1:acquire_paused")
}

// 2026-10-29：`TestManagerRecoveryHoldSurvivesRestartAndReleasesAfterConstraintClears` 随
// 受管恢复分段机制一并删除。它验的是「配置层 hold 让恢复段停在 XFER+hold、重启后重新评估、
// 解除后该段被归档为 CLEANED 且持久审计完整」——`RecoveryRefs` / `ListRecoveryArchive` /
// `RecoveryArchiveFingerprint` / `RecoveryCleaned` 全部已删，整条用例失去被测对象。
//
// 仍然活着的关切（配置层 hold 不得被绕过、解除后回收才推进）由
// `durable_outbox_test.go` 的 `TestDurableOutboxACKedHoldProofErrorRetainsReadonlyRetry`
// 与 `TestDurableOutboxPublishedReclaimAuthorityAndHoldDuringRO`（已删的后者见文件头说明）
// 一类用例守；hold 的**唯一**现存面是 `Options.RecoveryHold`（`m.recoveryHold`）。

func TestManagerRoutesStableColdOwnerAndKeepsFrozenStagingUnpublished(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frozen bool
	}{
		{name: "stable cold"},
		{name: "frozen staging", frozen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hot, hotFixture := newProjectionVL(t)
			cold, coldFixture := newProjectionVL(t)
			root := t.TempDir()
			path := filepath.Join(root, "latest.log")
			require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: routed\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
			key := catalog.PartitionKey{StorageNamespace: "node:routed", UTCDay: time.Now().UTC().Format("2006-01-02")}
			cat := catalog.New(catalog.NewMemJournal())
			if tc.frozen {
				require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-g1")))
				_, err := cat.BeginMigration(key, catalog.OwnerCold, "cold-g2")
				require.NoError(t, err)
			} else {
				require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerCold, 2, "cold-g2")))
			}
			manager, err := newTestManager(t, Options{Root: root, VL: hot, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
				LogSourceID: "node:routed", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
				StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay,
			}}, VLRoute: func(source SourceConfig) (*vlsup.Client, bool, error) {
				target, ok := cat.RouteWrite(key, source.UTCDay)
				require.True(t, ok)
				require.Equal(t, catalog.OwnerCold, target.Owner)
				return cold, target.Frozen, nil
			}})
			require.NoError(t, err)
			if tc.frozen {
				_, pollErr := manager.pipes["node:routed/g1"].Poll()
				require.ErrorIs(t, pollErr, catalog.ErrPhysicalDayFrozen, "冻结必须先拒绝真实物理写")
				require.Empty(t, coldFixture.writes)
			} else {
				manager.pollOnce()
				require.Len(t, coldFixture.writes, 1)
			}
			require.Empty(t, hotFixture.writes)
			rec, ok := cat.Get(key)
			require.True(t, ok)
			if tc.frozen {
				require.Nil(t, rec.PublishedProjection)
				require.NotEmpty(t, manager.state.Sources["node:routed/g1"].WAL)
			} else {
				require.NotNil(t, rec.PublishedProjection)
				require.Equal(t, "cold-g2", rec.PublishedProjection.QueryLocationDirID)
				require.EqualValues(t, 2, rec.PublishedProjection.QueryGeneration)
			}
		})
	}
}

func TestPrepareCutoverFlushesPendingEventBeforeReportingReady(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	payload := []byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: pending\n")
	require.NoError(t, os.WriteFile(path, payload, 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	manager, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:cutover", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:cutover", UTCDay: "2026-09-23",
	}}})
	require.NoError(t, err)
	manager.pollOnce()
	require.Len(t, durableEvents(t, manager, "node:cutover/g1"), 1)
	ready := manager.PrepareCutoverReadiness()
	require.True(t, ready.LedgerReady, "%v", ready.Reasons)
	require.Len(t, durableEvents(t, manager, "node:cutover/g1"), 2)
	entry := manager.pipes["node:cutover/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:cutover", SourceGeneration: "g1"})
	require.EqualValues(t, len(payload), entry.Positions.Read)
	require.Equal(t, entry.Positions.Read, entry.Positions.Durable)
	require.Equal(t, entry.Positions.Durable, entry.Positions.Reclaim)
}

// 静默源：未闭合缓冲闲置超时后必须被强制闭合并推进 durable（缺陷 B，2026-10-02 补）。
//
// 形态：源长时间不再输出（MC 空闲/实例挂起），尾部堆栈永远等不到下一行。缓冲既不产出事件、
// 也不推进 durable——而轮转恢复要靠 durable 覆盖已读前缀，于是链路卡在「轮转分段不可读」。
// 超时强制闭合把它落成一条显式 PARTIAL 事件，链路重新可推进。
//
// 转红方式（实测）：把 pollSource 里的 FlushStaleMultiline 调用去掉，
// 「超时后应当落库」的断言立即红。
func TestManagerFlushesStaleMultilineOnIdleSource(t *testing.T) {
	stack := "[12:00:00] [Server thread/ERROR]: hung\n\tat a.A(A.java:1)\n"

	// ① 未到期：一轮采集不得冲刷（缓冲还在等续行）。
	// 注意 newTestManager 默认把超时钉成 1h（见该辅助函数的说明），故这里测的是
	// 「不早冲」这一半；「到期必冲」由 ② 显式传小值覆盖。
	{
		client, _ := newProjectionVL(t)
		root := t.TempDir()
		livePath := filepath.Join(root, "latest.log")
		require.NoError(t, os.WriteFile(livePath, []byte(stack), 0o600))
		cat := catalog.New(catalog.NewMemJournal())
		m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
			LogSourceID: "inst:idle-default/file", SourceGeneration: "holder-g1", Path: livePath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:idle-default", UTCDay: runtimeTestUTCDay(),
		}}})
		require.NoError(t, err)
		m.pollOnce()
		require.Empty(t, durableEvents(t, m, "inst:idle-default/file/holder-g1"),
			"默认 5s 超时下，一轮采集不得把仍在等续行的缓冲冲掉")
	}

	// ② 超时设为 1ns（等价「立即到期」）：同一内容必须被冲刷为显式未闭合事件。
	{
		client, _ := newProjectionVL(t)
		root := t.TempDir()
		livePath := filepath.Join(root, "latest.log")
		require.NoError(t, os.WriteFile(livePath, []byte(stack), 0o600))
		cat := catalog.New(catalog.NewMemJournal())
		m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
			MultilineUnclosedTimeout: time.Nanosecond,
			Sources: []SourceConfig{{
				LogSourceID: "inst:idle-flush/file", SourceGeneration: "holder-g1", Path: livePath,
				Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:idle-flush", UTCDay: runtimeTestUTCDay(),
			}}})
		require.NoError(t, err)
		m.pollOnce()
		saved := durableEvents(t, m, "inst:idle-flush/file/holder-g1")
		require.Len(t, saved, 1, "闲置超时后悬挂记录必须落库（否则它会一直挡住 durable）")
		require.Equal(t, normalize.UnclosedReasonIdle, saved[0].Fields[normalize.FieldMultilineUnclosed])
		require.Equal(t, string(normalize.StatusPartial), saved[0].Fields[normalize.FieldParseStatus])
		// durable 必须被推进到该悬挂记录之后：否则轮转恢复仍覆盖不到这段已读前缀。
		pipe, ok := m.pipeFor("inst:idle-flush/file/holder-g1")
		require.True(t, ok)
		entry := pipe.Ledger().Get(pipe.Key())
		require.NotNil(t, entry)
		require.Greater(t, entry.Positions.Durable, uint64(0),
			"强制闭合必须把 durable 推过悬挂区间，否则轮转恢复仍会卡住")
	}
}

// 部分持久化残留的「悬挂源」必须被检出并在下一轮重试，而不是被静默当成已全量落库（用户质疑 2）。
//
// 形态：一次 apply 失败（磁盘满 / 库被换成只读 / 进程在 apply 前被杀）之后，若「已持久化水位」
// 在构建期就被推进，这些源会被下一轮当作**未变更**跳过——其陈旧行再也不会被重试，索引与内存
// 就此静默分叉。后果不是「多写一次」而是「无人认账」：该源的行停在旧水位，重启后按陈旧水位
// 重读（重复投递）或整行缺失被当成新源（先前已投递的数据失去对账依据）。
//
// 转红方式（实测）：把 persistSnapshot 里的 `advanced` 收集改回构建期直接写
// `m.persistedRev[key] = rev`，最后一条断言立即红（索引停在 p1，而内存已到 p2）。
func TestPersistFailureKeepsSourcePendingForRetry(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	livePath := filepath.Join(root, "latest.log")
	// 两行起步：多行归一化下**最后一条事件永远是未闭合的**（要等下一个事件边界才落库），
	// 只写一行会同轮无任何事件产出，durable 停在 0，夹具前提不成立。
	require.NoError(t, os.WriteFile(livePath,
		[]byte("[12:00:01] [Server thread/INFO]: first\n[12:00:02] [Server thread/INFO]: second\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:persist/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:persist", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()

	const stateKey = "inst:persist/file/holder-g1"
	// 从索引**读回**该源的 durable 水位（走生产同一条还原路径），用于判断落库是否真的发生。
	indexDurable := func() uint64 {
		store, openErr := stateindex.OpenReadOnly(m.indexPath())
		require.NoError(t, openErr)
		defer func() { _ = store.Close() }()
		rows, loadErr := store.Load()
		require.NoError(t, loadErr)
		state, convErr := indexStateToState(rows)
		require.NoError(t, convErr)
		entry, ok := state.Sources[stateKey]
		require.True(t, ok, "索引里应已有该源")
		require.NotEmpty(t, entry.Ledger, "索引里应已有该源的账本行")
		return entry.Ledger[0].Positions.Durable
	}
	p1 := indexDurable()
	require.Positive(t, p1, "首轮成功落库后索引里的 durable 应已推进")
	// 判别性断言的水位基线：一次成功落库后「已持久化修订号」应当就是当时的账本修订号。
	revAfterSuccess := func() uint64 {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.persistedRev[stateKey]
	}()
	require.Positive(t, revAfterSuccess, "成功落库后应记录已持久化修订号")

	// 让本次落库失败：把索引换成同一个库文件的**只读**句柄。
	writable := m.index
	require.NotNil(t, writable)
	ro, err := stateindex.OpenReadOnly(m.indexPath())
	require.NoError(t, err)
	defer func() { _ = ro.Close() }()
	m.mu.Lock()
	m.index = ro
	m.mu.Unlock()

	// 追加新数据并再采一轮：内存水位前进，但索引落不下去。
	appendPollTestLines(t, livePath, "[12:00:03] [Server thread/INFO]: third\n")
	m.pollOnce()

	pipe, ok := m.pipeFor(stateKey)
	require.True(t, ok)
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	p2 := entry.Positions.Durable
	require.Greater(t, p2, p1, "夹具前提：本轮内存水位必须前进")
	require.Equal(t, p1, indexDurable(), "夹具前提：本次落库失败，索引应停在旧水位")
	// **判别性断言**：落库失败后「已持久化水位」不得推进。
	// 这是本用例真正钉住的行为——水位一旦在构建期就被推进，该源会被下一轮当成「未变更」跳过，
	// 陈旧行永远不会被重试（静默滞留）。端到端那条断言（下方）在本夹具里即使退回旧实现也会
	// 通过：失败之后的自愈链（DeliverPending → RecordDelivery）会再次改动账本、顺带把修订号
	// 顶高，于是下一轮「碰巧」重试到。故不能只靠端到端那一条来守这个不变量。
	require.Equal(t, revAfterSuccess, func() uint64 {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.persistedRev[stateKey]
	}(), "落库失败不得推进已持久化水位（否则悬挂源会被当成已落库而永不重试）")

	// 恢复可写并触发下一轮持久化：悬挂源必须被重试，而不是被当成「已落库」跳过。
	m.mu.Lock()
	m.index = writable
	m.mu.Unlock()
	require.NoError(t, m.persist())
	require.Equal(t, p2, indexDurable(),
		"落库失败过的源必须在下一轮被重试；否则悬挂源静默滞留、重启后按陈旧水位重读")
}

// 事件体改为追加式磁盘段后，每轮 poll 都会把「完整权威集合」交给 appendEvents。
// 若实现把它整份再写一次，段内容会随轮次成倍膨胀——这正是本测试钉住的回归点。
func TestManagerRepeatedPollKeepsStoredEventSetStable(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(
		"[12:00:01] [Server thread/INFO]: first\n"+
			"[12:00:02] [Server thread/INFO]: second\n"+
			"[12:00:03] [Server thread/INFO]: boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:idem", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:idem", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	settled, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(cat.Journal()), Journal: cat.Journal()})
	require.NoError(t, err)
	baseline := durableEvents(t, settled, "node:idem/g1")
	require.NotEmpty(t, baseline)
	// 文件无新增时重复轮询必须幂等：集合既不膨胀也不重复。
	settled.pollOnce()
	settled.pollOnce()
	after := durableEvents(t, settled, "node:idem/g1")
	require.Len(t, after, len(baseline),
		"repeated polls must not duplicate already-stored events")
	ids := make(map[string]int, len(after))
	for _, event := range after {
		ids[event.EventID]++
	}
	for id, count := range ids {
		require.Equal(t, 1, count, "event %s stored more than once", id)
	}
}

func writeRuntimeGzip(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := gzip.NewWriter(f)
	_, err = zw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

// TestGroupEventsByUTCDayFallsBackToIngestTime 锁定：无解析语义时间的真实日志行不得令分组失败
// （否则投递/投影失败 → reclaim 永不推进 → WAL 保留全部事件，真机 64 源曾致 Worker RSS 2GiB）。
func TestGroupEventsByUTCDayFallsBackToIngestTime(t *testing.T) {
	source := SourceConfig{LogSourceID: "s1", SourceGeneration: "g1", StorageNamespace: "ns", UTCDay: "2026-09-20"}
	withoutEventTime := logtypes.Event{
		EventID: "e1", IngestTimeUTC: "2026-09-21T10:00:00Z", EventTimeUTC: "",
	}
	withEventTime := logtypes.Event{
		EventID: "e2", IngestTimeUTC: "2026-09-21T10:00:00Z", EventTimeUTC: "2026-09-22T10:00:00Z",
	}
	grouped, days, err := groupEventsByUTCDay(source, []logtypes.Event{withoutEventTime, withEventTime})
	require.NoError(t, err)
	require.Equal(t, []string{"2026-09-21", "2026-09-22"}, days)
	require.Len(t, grouped["2026-09-21"], 1)
	require.Len(t, grouped["2026-09-22"], 1)

	// 两者皆缺时回退源配置的 UTCDay。
	bare := logtypes.Event{EventID: "e3"}
	grouped, days, err = groupEventsByUTCDay(source, []logtypes.Event{bare})
	require.NoError(t, err)
	require.Equal(t, []string{"2026-09-20"}, days)
	require.Len(t, grouped["2026-09-20"], 1)
}
