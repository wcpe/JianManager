package ingest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	indexPath := m.indexPath()
	stamp := time.Unix(1700000000, 0)
	require.NoError(t, os.Chtimes(indexPath, stamp, stamp))
	m.pollOnce()
	// 空闲轮询不得重写历史（FR-496 spec §2.3）：源无任何变化时不得触发落库写。
	require.Equal(t, samplesBefore, len(m.PersistSamples()), "idle polling must not trigger a persist at all")
	stat, err := os.Stat(indexPath)
	require.NoError(t, err)
	require.Equal(t, stamp.Unix(), stat.ModTime().Unix(), "idle polling must not rewrite and fsync the durable index")
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
	mu     sync.Mutex
	writes [][]byte
	// probes 记录「代次占用探测」查询原文（`| fields _time | limit 1`，复审 P2-11 回归的读数）。
	probes []string
}

func (f *projectionVLFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/insert/jsonline":
		payload, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.writes = append(f.writes, payload)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case "/select/logsql/query":
		selector := r.URL.Query().Get("query")
		if strings.Contains(selector, "| fields _time | limit 1") {
			f.mu.Lock()
			f.probes = append(f.probes, selector)
			f.mu.Unlock()
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
		f.mu.Lock()
		defer f.mu.Unlock()
		count := 0
		for _, payload := range f.writes {
			for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
				var row struct {
					ProjectionGeneration string `json:"projection_generation"`
					EventTimeUTC         string `json:"_time"`
					LogSourceID          string `json:"log_source_id"`
				}
				if json.Unmarshal([]byte(line), &row) != nil || !strings.Contains(selector, "projection_generation:="+strconv.Quote(row.ProjectionGeneration)) {
					continue
				}
				if strings.Contains(selector, "log_source_id:=") && !strings.Contains(selector, "log_source_id:="+strconv.Quote(row.LogSourceID)) {
					continue
				}
				when, timeErr := time.Parse(time.RFC3339Nano, row.EventTimeUTC)
				if timeErr == nil && ((!from.IsZero() && when.Before(from)) || (!to.IsZero() && when.After(to))) {
					continue
				}
				if limit > 0 && count >= limit {
					return
				}
				_, _ = w.Write(append([]byte(line), '\n'))
				count++
			}
		}
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
		state:     persistedState{Sources: map[string]persistedSource{source.LogSourceID + "/" + source.SourceGeneration: {}}},
		statePath: filepath.Join(root, "var", "log", "ingest.state.json"), verificationTimeout: time.Second,
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
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, positions.Reclaim, "TEST", "covered"))
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

	// 新 Manager 从索引库恢复同一 source，在新 projection generation 中重建事件。
	cat2 := catalog.New(cat.Journal())
	m2, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(), Sources: []SourceConfig{{
		LogSourceID: "node:1", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m2.pollOnce()
	require.Contains(t, string(fixture.writes[len(fixture.writes)-1]), "projection-2")
	rec2, ok := cat2.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.Equal(t, "projection-2", rec2.PublishedProjection.ProjectionGeneration)
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

	os.Unsetenv("JIANMANAGER_LOG_INJECT_ACK_LOSS")
	cat2 := catalog.New(cat.Journal())
	m2, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	entry2 := m2.pipes["ack:1/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "ack:1", SourceGeneration: "g1"})
	require.Equal(t, logtypes.DeliveryReplayRequired, entry2.DeliveryState)
	require.NotZero(t, entry2.Positions.Reclaim)
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
				payload, _ := io.ReadAll(r.Body)
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
	require.Equal(t, "projection-2", manager.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].ProjectionGeneration)
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:05] [Server thread/INFO]: following batch\n[12:00:06] [Server thread/INFO]: following boundary\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	manager.pollOnce()
	continued, _ := cat.Get(key)
	require.Equal(t, []string{"projection-3"}, continued.PublishedProjection.ProjectionGenerations, "retry without restart must replace any partially published attempt")
	fixture.mu.Lock()
	lastPayload := string(fixture.writes[len(fixture.writes)-1])
	fixture.mu.Unlock()
	require.Contains(t, lastPayload, "uncertain", "must recover prior durable events before advancing their closed prefix")

	recoveredCatalog := catalog.New(journal)
	recovered, err := newTestManager(t, Options{Root: root, VL: client, Catalog: recoveredCatalog, Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	require.Equal(t, "projection-4", recovered.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].ProjectionGeneration)
	rec, ok := recoveredCatalog.Get(key)
	require.True(t, ok)
	require.Equal(t, []string{"projection-4"}, rec.PublishedProjection.ProjectionGenerations)
	require.NotContains(t, rec.PublishedProjection.ProjectionGenerations, "projection-2")
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
	require.Len(t, durableEvents(t, recovered, "node:1/g1"), 1)
	_, ok = recoveredCatalog.Get(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
}

func TestOwnerChangeDuringVerificationCannotPublishToNewOwner(t *testing.T) {
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "node:race", UTCDay: runtimeTestUTCDay()}
	fixture := &projectionVLFixture{}
	switched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 注入「验证期间换主」的时点必须锚定在**校验查询**上：代次占用探测（见
		// nextFreeProjectionGeneration）走的是同一个端点但没有时间窗，它不是验证。
		if r.URL.Path == "/select/logsql/query" && r.URL.Query().Get("start") != "" && !switched {
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
			// 只读直查索引库：WAL 条目必须已 durable 落库，才允许发 VL 插入请求。
			store, err := stateindex.OpenReadOnly(indexPath)
			if err != nil {
				t.Errorf("WAL snapshot missing before VL insert: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			rows, err := store.Load()
			_ = store.Close()
			if err != nil {
				t.Errorf("WAL snapshot corrupt before VL insert: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var durable int
			for _, row := range rows.WAL {
				if row.Key == "node:1/g1" && row.Durable {
					durable++
				}
			}
			if durable == 0 {
				t.Error("WAL event must be durable on disk before VL insert")
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
	require.Equal(t, "projection-2", second.PublishedProjection.ProjectionGeneration)
	require.Equal(t, []string{"projection-1", "projection-2"}, second.PublishedProjection.ProjectionGenerations)
	require.Equal(t, query.ErrCodeViewStale, planner.Plan(query.PlanRequest{ViewRef: &query.ViewRef{ViewID: old.ViewID}}).Err.Code)
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

func TestManagerRecoveryHoldSurvivesRestartAndReleasesAfterConstraintClears(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: held\n[12:00:02] [Server thread/INFO]: boundary\n"), 0o600))
	source := SourceConfig{LogSourceID: "node:hold", SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:hold", UTCDay: "2026-09-23"}
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	held, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: journal, Sources: []SourceConfig{source},
		RecoveryHold: func(SourceConfig, string) (bool, string) { return true, "active query lease" }})
	require.NoError(t, err)
	held.pollOnce()
	entry := held.pipes["node:hold/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:hold", SourceGeneration: "g1"})
	require.Zero(t, entry.Positions.Reclaim)
	require.Len(t, entry.RecoveryRefs, 1)
	require.Equal(t, "projection-recovery-projection-1-"+strconv.FormatUint(entry.Positions.Durable, 10), entry.RecoveryRefs[0].SegmentID)
	require.Equal(t, logtypes.RecoveryWALResponsibilityXfer, entry.RecoveryRefs[0].State)
	require.True(t, entry.RecoveryRefs[0].HasHold)
	require.Empty(t, entry.RecoveryRefs[0].ReleaseReason)
	require.Equal(t, "projection:projection-1", entry.RecoveryRefs[0].ResponsibilityReceiver)

	var resumedGeneration string
	recovered, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source}, RecoveryHold: func(_ SourceConfig, generation string) (bool, string) {
			resumedGeneration = generation
			return false, ""
		}})
	require.NoError(t, err)
	require.Equal(t, "projection-2", resumedGeneration)
	require.Equal(t, "projection-2", recovered.state.Sources["node:hold/g1"].ProjectionGeneration)
	entry = recovered.pipes["node:hold/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:hold", SourceGeneration: "g1"})
	require.Equal(t, entry.Positions.Durable, entry.Positions.Reclaim)
	require.Empty(t, entry.Gaps)
	require.Len(t, entry.RecoveryRefs, 1, "restart resumes the existing held responsibility record")
	for _, ref := range entry.RecoveryRefs {
		require.Contains(t, ref.SegmentID, "projection-1")
		require.Equal(t, logtypes.RecoveryCleaned, ref.State)
		require.False(t, ref.HasHold)
		require.Equal(t, logtypes.ReleaseProjectionBacked, ref.ReleaseReason)
		require.Equal(t, "projection:projection-2", ref.ResponsibilityReceiver)
	}
	recovered.pollOnce()
	entry = recovered.pipes["node:hold/g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "node:hold", SourceGeneration: "g1"})
	require.Empty(t, entry.Gaps, "replaying the pending line after restart must not manufacture a zero-width event")
}

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
			manager.pollOnce()
			require.Empty(t, hotFixture.writes)
			require.Len(t, coldFixture.writes, 1)
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

func TestManagerAutoImportsHistoricalGzipBeforeCurrentFile(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	archivePath := filepath.Join(logDir, "2026-09-22-1.log.gz")
	writeRuntimeGzip(t, archivePath,
		"[10:00:00] [Server thread/INFO]: historical one\n"+
			"[10:00:01] [Server thread/ERROR]: historical two\n")
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[10:00:02] [Server thread/INFO]: current one\n"+
			"[10:00:03] [Server thread/INFO]: current boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:archive/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:archive", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	saved := durableEvents(t, m, "inst:archive/file/holder-g1")
	require.Len(t, saved, 3, "two finite archive events and the first closed live event")
	require.Contains(t, saved[0].Message, "historical one")
	require.Contains(t, saved[1].Message, "historical two")
	require.Contains(t, saved[2].Message, "current one")
	entry := m.pipes["inst:archive/file/holder-g1"].Ledger().Get(ledger.SourceKey{LogSourceID: "inst:archive/file", SourceGeneration: "holder-g1"})
	require.NotNil(t, entry)
	imported := false
	for _, segment := range entry.Segments {
		if segment.Path == archivePath {
			imported = segment.Imported
			require.Equal(t, uint64(0), segment.StartPos)
		}
	}
	require.True(t, imported)
	before := len(saved)
	m.pollOnce()
	require.Len(t, durableEvents(t, m, "inst:archive/file/holder-g1"), before, "imported gzip must not replay")
	require.NoError(t, m.Stop())
	require.Len(t, durableEvents(t, m, "inst:archive/file/holder-g1"), before+1,
		"graceful stop closes the one pending live event")
	restartedCatalog := catalog.New(cat.Journal())
	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: restartedCatalog, Journal: restartedCatalog.Journal()})
	require.NoError(t, err)
	restarted.pollOnce()
	require.Len(t, durableEvents(t, restarted, "inst:archive/file/holder-g1"), before+1,
		"restart must resume the live file from its segment-local cursor")
	ready := restarted.PrepareCutoverReadiness()
	require.True(t, ready.LedgerReady, "%v", ready.Reasons)
	require.Len(t, durableEvents(t, restarted, "inst:archive/file/holder-g1"), before+1,
		"the closed live boundary and imported gzip must not replay")
}

// 跨轮转的「堆栈打一半」必须在数据里留下显式未闭合痕迹（2026-10-02 补）。
//
// 形态：一条堆栈的前半段写在旧 latest.log、后半段随轮转落进归档，新 latest.log 换上新内容。
// 轮转处必须闭合 pending（否则 gz 的 skipBytes 对不上已读前缀，重导入会重复喂行），
// 但闭合**不能静默标成 OK**——否则事后无法从数据里看出这条事件被切过。
//
// 转红方式（实测）：把 FlushClosedSegment 改回 FlushComplete（旧行为），
// 第一条断言立即红（标记消失、parse_status 变回 OK）。
func TestManagerRotationMarksCrossRotationUnclosedMultiline(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	livePath := filepath.Join(logDir, "latest.log")

	readLine := "[11:00:00] [Server thread/INFO]: already read\n"
	// 堆栈前半段：事件头 + 一条续行（尚未闭合）。
	stackHead := "[11:00:01] [Server thread/ERROR]: boom\n"
	stackCont := "\tat com.example.Foo(Foo.java:1)\n"
	require.NoError(t, os.WriteFile(livePath, []byte(readLine+stackHead+stackCont), 0o600))

	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:rotate-stack/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:rotate-stack", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	// 前半段仍在缓冲：只落了「already read」这一条完整事件。
	require.Len(t, durableEvents(t, m, "inst:rotate-stack/file/holder-g1"), 1,
		"未闭合堆栈不得在闭合前落库")

	// 轮转：归档里是完整前半段 + 堆栈余下续行；新 latest.log 是替换文件。
	archivePath := filepath.Join(logDir, "2026-09-23-1.log.gz")
	writeRuntimeGzip(t, archivePath, readLine+stackHead+stackCont+
		"\tat com.example.Bar(Bar.java:2)\n")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[11:00:03] [Server thread/INFO]: replacement current\n"), 0o600))
	m.pollOnce()

	saved := durableEvents(t, m, "inst:rotate-stack/file/holder-g1")
	var stackEvents []logtypes.Event
	for _, ev := range saved {
		if strings.Contains(ev.Message, "boom") {
			stackEvents = append(stackEvents, ev)
		}
	}
	require.Len(t, stackEvents, 1, "堆栈前半段应恰好落一条事件，实测 %d 条", len(stackEvents))
	got := stackEvents[0]
	require.Equal(t, normalize.UnclosedReasonCrossRotation, got.Fields[normalize.FieldMultilineUnclosed],
		"跨轮转闭合必须留下显式未闭合原因（否则切分点在数据里不可见）")
	require.Equal(t, string(normalize.StatusPartial), got.Fields[normalize.FieldParseStatus],
		"跨轮转闭合的悬挂记录不得伪装成完整事件")

	// 对面：同一批里自然闭合的完整记录不得被误标（避免每次轮转全体误报）。
	for _, ev := range saved {
		if strings.Contains(ev.Message, "already read") {
			_, marked := ev.Fields[normalize.FieldMultilineUnclosed]
			require.False(t, marked, "自然闭合的完整记录不得带未闭合标记")
		}
	}
	// 既有不变量：轮转不得产生重复事件身份。
	ids := make(map[string]bool)
	for _, ev := range saved {
		require.False(t, ids[ev.EventID], "轮转不得重复事件身份")
		ids[ev.EventID] = true
	}
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

// 归属执行体端到端（**补账语义**，2026-10-02 定案）：同一份归档由 g1 导入后，
// g2 不得把它按自己的代次导入；而应**记回 g1 的待投递账目**（补账），且重复轮次幂等。
//
// 为什么是补账而不是简单拒绝：拒绝会让这份数据永远进不来（人工无法指定一个"原代次"的导入通道）；
// 补账把它记回**它自己的账**上——旧代次将来再次活跃时，其既有 backlog 路径会自然外发（不丢）；
// 以 g2 的 event_id 重复入 VL 的路径被封死（不重）；g2 名下永无归属（不归）。
//
// 转红方式（实测）：把 Register 里的 p.SetArchiveBackfill(...) 去掉，
// 「g2 不得产出事件」会因退回旧的"拒绝"路径而仍然成立，但「g1 的账目必须增加」立即红
// （证明补账真的发生了，而不是只把文件跳过）。
func TestManagerBackfillsArchiveUnderOriginalGeneration(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	archivePath := filepath.Join(logDir, "2026-09-22-1.log.gz")
	writeRuntimeGzip(t, archivePath, "[10:00:00] [Server thread/INFO]: owned by g1\n")
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[10:00:02] [Server thread/INFO]: live one\n[10:00:03] [Server thread/INFO]: boundary\n"), 0o600))

	// 代次 g1：首次见到该归档 ⇒ 导入并登记绑定（历史归档能力）。
	cat1 := catalog.New(catalog.NewMemJournal())
	m1, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat1, Journal: cat1.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:owner/file", SourceGeneration: "g1", Path: livePath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:owner", UTCDay: runtimeTestUTCDay(),
		}}})
	require.NoError(t, err)
	m1.pollOnce()
	require.NotEmpty(t, durableEvents(t, m1, "inst:owner/file/g1"))
	require.NoError(t, m1.Stop(), "Stop 会落盘，确保 g1 的账与绑定已持久化")
	g1WAL := len(m1.state.Sources["inst:owner/file/g1"].WAL)

	// 代次 g2：同一份归档必须走**补账**，不得按 g2 导入。
	cat2 := catalog.New(catalog.NewMemJournal())
	m2, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat2, Journal: cat2.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:owner/file", SourceGeneration: "g2", Path: livePath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:owner", UTCDay: runtimeTestUTCDay(),
		}}})
	require.NoError(t, err)
	m2.pollOnce()

	g2Key := ledger.SourceKey{LogSourceID: "inst:owner/file", SourceGeneration: "g2"}
	pipe2, ok := m2.pipeFor("inst:owner/file/g2")
	require.True(t, ok)
	entry2 := pipe2.Ledger().Get(g2Key)
	require.NotNil(t, entry2)
	// ① 不重：g2 名下不得有任何来源为该归档的事件，也不得把它登记为自己已导入。
	for _, seg := range entry2.Segments {
		if seg.Path == archivePath {
			require.False(t, seg.Imported, "g2 不得把属于 g1 的归档登记为自己已导入")
		}
	}
	for _, gap := range entry2.Gaps {
		require.NotEqual(t, "ARCHIVE_FOREIGN_GENERATION", gap.Reason,
			"补账成功时不应再记跨代次拒绝缺口（那条只在补账不可行时出现）")
	}
	// ② 不重：g2 名下的落库事件只能来自它自己的 live 文件，不得含归档那一条。
	g2Events := durableEvents(t, m2, "inst:owner/file/g2")
	for _, ev := range g2Events {
		require.NotContainsf(t, ev.Message, "owned by g1",
			"归档的数据必须以 g1 的身份存在，绝不得以 g2 的 event_id 再进一次（静默重复）")
		require.Equal(t, "g2", ev.Source.SourceGeneration,
			"g2 名下的事件必须带 g2 身份")
	}

	// ③ 不丢 + 幂等：原代次的账目不得被改动（该归档在 g1 那边**早已导入完成**，
	// 补账因此是一条正确的 no-op），且重复轮次零新增。
	//
	// 为什么这里是"零新增"而不是"WAL 增加"：补账是否会搬动数据，取决于**原代次是否已经导入过**。
	//   - 原代次已导入（本夹具）⇒ 数据早在它的账上、也早已投递 ⇒ 正确动作是幂等 no-op；
	//   - 原代次尚未导入（换代恰好发生在归档入库之前）⇒ 内层导入会真的把事件写进原代次的
	//     待投递账目，那条路径由 acquire 层的 TestImportGzipBackfillsInsteadOfImportingToCurrentGeneration
	//     用注入的补账回调直接覆盖（断言回调被以正确的 owner/objectID 调用）。
	m2.mu.Lock()
	g1State := m2.state.Sources["inst:owner/file/g1"]
	m2.mu.Unlock()
	require.Equal(t, g1WAL, len(g1State.WAL),
		"原代次已导入过的归档：补账必须是 no-op（重复把同一份数据记进旧账就是另一种重复）")
	var g1Imported bool
	for _, e := range g1State.Ledger {
		for _, s := range e.Segments {
			if s.Kind == ledger.SegmentGzip && s.Imported {
				g1Imported = true
			}
		}
	}
	require.True(t, g1Imported, "该归档必须仍在 g1 账上登记为已导入（补账不得把它改坏）")

	// ③ 幂等：再采一轮不得重复补账。
	m2.pollOnce()
	m2.mu.Lock()
	g1After := len(m2.state.Sources["inst:owner/file/g1"].WAL)
	m2.mu.Unlock()
	require.Equal(t, len(g1State.WAL), g1After, "重复轮次不得重复补账（与旧账幂等零新增）")
}

func TestManagerRotationImportsOnlyUnreadTailBeforeReplacementFile(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	livePath := filepath.Join(logDir, "latest.log")
	first := "[11:00:00] [Server thread/INFO]: already read\n"
	pending := "[11:00:01] [Server thread/INFO]: closed by rotation\n"
	require.NoError(t, os.WriteFile(livePath, []byte(first+pending), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:rotate/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:rotate", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	require.Len(t, durableEvents(t, m, "inst:rotate/file/holder-g1"), 1)

	archivePath := filepath.Join(logDir, "2026-09-23-1.log.gz")
	writeRuntimeGzip(t, archivePath, first+pending+"[11:00:02] [Server thread/WARN]: unread rotated tail\n")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[11:00:03] [Server thread/INFO]: replacement current\n"+
			"[11:00:04] [Server thread/INFO]: replacement boundary\n"), 0o600))
	m.pollOnce()
	saved := durableEvents(t, m, "inst:rotate/file/holder-g1")
	require.Len(t, saved, 4)
	require.Contains(t, saved[0].Message, "already read")
	require.Contains(t, saved[1].Message, "closed by rotation")
	require.Contains(t, saved[2].Message, "unread rotated tail")
	require.Contains(t, saved[3].Message, "replacement current")
	ids := make(map[string]bool)
	for _, event := range saved {
		require.False(t, ids[event.EventID], "logical rotation must not duplicate an event identity")
		ids[event.EventID] = true
	}
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

func TestManagerQuarantinesUnchangedCorruptGzipWithoutRepeatingGap(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logDir := filepath.Join(root, "server", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o700))
	badPath := filepath.Join(logDir, "2026-09-22-1.log.gz")
	require.NoError(t, os.WriteFile(badPath, []byte("not-gzip"), 0o600))
	livePath := filepath.Join(logDir, "latest.log")
	require.NoError(t, os.WriteFile(livePath, []byte(
		"[12:00:00] [Server thread/INFO]: live\n"+
			"[12:00:01] [Server thread/INFO]: boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{{
		LogSourceID: "inst:bad/file", SourceGeneration: "holder-g1", Path: livePath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:bad", UTCDay: runtimeTestUTCDay(),
	}}})
	require.NoError(t, err)
	m.pollOnce()
	key := ledger.SourceKey{LogSourceID: "inst:bad/file", SourceGeneration: "holder-g1"}
	entry := m.pipes[key.String()].Ledger().Get(key)
	require.Len(t, entry.Gaps, 1)
	require.Contains(t, entry.Gaps[0].Reason, "ARCHIVE_GZIP_CORRUPT")
	quarantined := false
	for _, segment := range entry.Segments {
		if segment.Path == badPath {
			quarantined = segment.ImportError != "" && !segment.Imported
		}
	}
	require.True(t, quarantined)
	m.pollOnce()
	entry = m.pipes[key.String()].Ledger().Get(key)
	require.Len(t, entry.Gaps, 1, "an unchanged quarantined object must not add one gap every 250ms")
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
