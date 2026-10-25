package ingest

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本组回归由审查反例下沉，不连接外部 VL。
func TestIngestSixFixQueryFailureKeepsGap(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := hotRetentionTestSource(t, root)
	events := []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 10, 20, "本地仅存事件")}
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe, ok := m.pipeFor(key)
	require.True(t, ok)
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 10, 20, ledger.GapReasonDeliverError, "查询失败不得造证"))
	fixture.deleteDay(source.UTCDay)
	queries := atomic.Int64{}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer broken.Close()
	m.vl, err = vlsup.NewClient(vlsup.ClientOptions{BaseURL: broken.URL})
	require.NoError(t, err)
	_, err = m.deliver(source, events, false)
	require.Error(t, err, "查询不确定的本地重复不能返回204并产生真实ACK")
	require.Positive(t, queries.Load())
	require.Equal(t, 1, pipe.Ledger().UnresolvedGapCount(pipe.Key()), "503 且确实缺行，不能用本地段消解缺口")
	require.Empty(t, m.verifiedRuns(key), "失败查询不得生成逐字段凭据")
}

func TestIngestSixFixMissingClientsHaveNoEvidence(t *testing.T) {
	events := []logtypes.Event{hotRetentionTestEvent(runtimeTestUTCDay(), 10, 20, "无客户端")}
	for _, m := range []*Manager{nil, {}, {cat: catalog.New(catalog.NewMemJournal())}} {
		require.Empty(t, m.landedVisibleInVL(SourceConfig{}, events), "缺 Catalog 或客户端只能返回空证据")
	}
}

func TestIngestSixFixAutomaticResolveDefaultDeny(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("[12:00:01] [Server thread/INFO]: isolated\n"), 0600))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{LogSourceID: "six-deny", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary, StorageNamespace: "six-deny", UTCDay: runtimeTestUTCDay()}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	pipe := m.pipes[source.LogSourceID+"/g1"]
	events, err := pipe.Drain()
	require.NoError(t, err)
	require.NoError(t, m.releaseRecovery(source, events))
	entry := pipe.Ledger().Get(pipe.Key())
	require.Positive(t, entry.Positions.Reclaim)
	for _, reason := range []string{ledger.GapReasonWALBodyMissing, "SIX_UNKNOWN_REASON"} {
		require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, entry.Positions.Reclaim, reason, "允许名单外必须保留"))
	}
	_ = m.ResolveCoveredGaps(context.Background())
	for _, gap := range pipe.Ledger().Get(pipe.Key()).Gaps {
		if gap.Reason == ledger.GapReasonWALBodyMissing || gap.Reason == "SIX_UNKNOWN_REASON" {
			require.False(t, gap.Resolved, "未知原因不得自动消解：%s", gap.Reason)
		}
	}
}

func TestIngestSixFixHistoricalSuccessDoesNotPoisonNewDay(t *testing.T) {
	s := newRebuildScheduler()
	m := &Manager{rebuilds: s}
	key := "node:counter/g1"
	failure := fmt.Errorf("已写后校验超时: %w", errProjectionVerifyFailed)
	for day := 1; day <= 7; day++ {
		abandoned, _, _ := s.noteFailure(key, fmt.Sprintf("projection-%d", day), failure, m.classifyRebuildFailure(key, failure))
		require.False(t, abandoned)
		s.noteSuccess(key)
	}
	abandoned, _, _ := s.noteFailure(key, "projection-new-day", failure, m.classifyRebuildFailure(key, failure))
	require.False(t, abandoned, "已恢复历史不能毒化新一天首败")
}

func TestIngestSixFixSweepAdmissionDeterministic(t *testing.T) {
	const callers = 6
	m := &Manager{sweepInflightCap: 2}
	arrived := make(chan struct{}, callers)
	releaseAdmission := make(chan struct{})
	releaseWork := make(chan struct{})
	m.sweepAdmissionHook = func() { arrived <- struct{}{}; <-releaseAdmission }
	var finished sync.WaitGroup
	finished.Add(callers)
	var started atomic.Int64
	for i := 0; i < callers; i++ {
		go func() {
			defer finished.Done()
			m.runSweepStepBounded(time.Millisecond, func() { started.Add(1); <-releaseWork })
		}()
	}
	for i := 0; i < callers; i++ {
		select {
		case <-arrived:
		case <-time.After(3 * time.Second):
			t.Fatal("准入读数屏障未到达")
		}
	}
	close(releaseAdmission)
	finished.Wait()
	defer close(releaseWork)
	require.LessOrEqual(t, started.Load(), int64(2), "同一旧读数不能重复占额")
	require.LessOrEqual(t, m.sweepInflight.Load(), int64(2), "超时不能提前归还真正后台占额")
}

// 此反例走真实入队、worker 和退避，不直接调用任务体或清除退避字段。
func TestIngestSixFixSchedulerAccountsStalePhysicalWrites(t *testing.T) {
	fixture := &reconcileVLFixture{}
	var m *Manager
	var source SourceConfig
	var armed atomic.Bool
	var liveSeq atomic.Uint64
	var callbackErr atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.ServeHTTP(w, r)
		if m != nil && r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "| fields _time, _msg") && armed.CompareAndSwap(true, false) {
			n := liveSeq.Add(1)
			event := hotRetentionTestEvent(source.UTCDay, 100+n*10, 110+n*10, fmt.Sprintf("并发主线%d", n))
			if _, err := m.deliver(source, []logtypes.Event{event}, false); err != nil {
				callbackErr.Store(err)
			}
		}
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source = hotRetentionTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 10, 20, "旧事件")})
	m, err = newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m.rebuilds.now = func() time.Time { return time.Unix(0, clock.Load()) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-done }()
	for round := 0; round < 10; round++ {
		armed.Store(true)
		started := m.rebuilds.stats.Started.Load()
		require.True(t, m.rebuilds.schedule(&rebuildRequest{key: key, source: source, days: []string{source.UTCDay}}, "并发发布失效"))
		require.Eventually(t, func() bool {
			m.rebuilds.mu.Lock()
			defer m.rebuilds.mu.Unlock()
			_, pending := m.rebuilds.pending[key]
			_, inflight := m.rebuilds.inflight[key]
			return m.rebuilds.stats.Started.Load() > started && !pending && !inflight
		}, 10*time.Second, time.Millisecond)
		if value := callbackErr.Load(); value != nil {
			t.Fatalf("主线不得被重建阻断：%v", value)
		}
		state, ok := m.rebuilds.snapshot(key)
		if ok && !state.nextAllowedAt.IsZero() {
			require.False(t, m.rebuilds.schedule(&rebuildRequest{key: key, source: source, days: []string{source.UTCDay}}, "退避内应拒绝"))
			clock.Store(state.nextAllowedAt.Add(time.Second).UnixNano())
		}
	}
	rows, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	require.NotEmpty(t, rows, "CAS失效前已物理写入的每次尝试必须持久记账")
	require.LessOrEqual(t, len(rows), 8, "持续新内容也不能绕过源×日小时频率预算")
	require.Positive(t, liveSeq.Load(), "真实并发发布主线必须继续")
}

// 批次来自真实60源短验暴露的617条ID查询，按实际query字节硬限拒绝，不调高批准引擎默认。
func TestIngestSixFixScopedQueryActualByteLimit(t *testing.T) {
	source := SourceConfig{LogSourceID: "node:query-byte-limit", SourceGeneration: "g1", UTCDay: runtimeTestUTCDay()}
	events := make([]logtypes.Event, 0, 617)
	for i := 0; i < 617; i++ {
		events = append(events, sixFixEvent(source, source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("正文%d", i)))
	}
	var largest atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		for n := largest.Load(); int64(len(query)) > n; n = largest.Load() {
			if largest.CompareAndSwap(n, int64(len(query))) {
				break
			}
		}
		if len(query) > 16384 {
			http.Error(w, "query too long", http.StatusBadRequest)
			return
		}
		selected := make([]logtypes.Event, 0, len(events))
		for _, event := range events {
			if strings.Contains(query, strconv.Quote(event.EventID)) {
				selected = append(selected, event)
			}
		}
		payload, err := projectionPayload(selected, "projection-1")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m := &Manager{}
	visible, err := m.visibleEventsInVL(context.Background(), client, source, []string{"projection-1"}, events)
	require.NoError(t, err, "617条ID不可生成超过真实16KiB上限的query")
	require.Len(t, visible, len(events), "切片不得降低逐字段证明覆盖")
	require.LessOrEqual(t, largest.Load(), int64(12<<10), "保留fixed clauses和转义余量")
}

// 字节切片只拆ID，不得缩小原簇取证时间窗，否则跨片时间的同ID坏副本会被两边同时漏掉。
func TestIngestSixFixScopedQuerySplitRetainsOriginalTimeWindow(t *testing.T) {
	fixture := &reconcileVLFixture{}
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	base, err := time.Parse(time.RFC3339Nano, source.UTCDay+"T12:00:00Z")
	require.NoError(t, err)
	events := make([]logtypes.Event, 0, 200)
	for i := 0; i < 200; i++ {
		event := hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("跨片时间%d", i))
		event.EventTimeUTC = base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		events = append(events, event)
	}
	end, err := scopedProjectionChunkEnd(source, []string{"projection-1"}, events, 0)
	require.NoError(t, err)
	require.Less(t, end, len(events), "夹具必须真实跨过12KiB切片界")
	var injected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.ServeHTTP(w, r)
		if r.URL.Path == "/insert/jsonline" && injected.CompareAndSwap(false, true) {
			fixture.mu.Lock()
			bad := fixture.rows[0]
			bad.EventTimeUTC = events[len(events)-1].EventTimeUTC
			bad.CanonicalHash = strings.Repeat("f", 64)
			bad.Message = "同ID异hash副本落在另一ID片的时间段"
			fixture.rows = append(fixture.rows, bad)
			fixture.mu.Unlock()
		}
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, VerificationTimeout: time.Second})
	require.NoError(t, err)
	_, err = m.deliver(source, events, false)
	require.Error(t, err, "原簇窗内的同ID坏副本必须阻止发布，不能被ID切片与子时间窗组合隐藏")
	require.True(t, injected.Load())
	if err != nil {
		require.Contains(t, err.Error(), "duplicate projection event_id")
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	require.Empty(t, m.verifiedRuns(key), "失败分片不能产生VerifiedRuns")
	rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if rec != nil {
		require.Nil(t, rec.PublishedProjection, "坏副本不能被任何子片的局部成功发布")
	}
}

func TestIngestSixFixSnapshotSplitKeepsMiddleBody(t *testing.T) {
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal()), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	p := m.pipes[key]
	events := []logtypes.Event{
		hotRetentionTestEvent(source.UTCDay, 0, 10, "已存前段"),
		hotRetentionTestEvent(source.UTCDay, 10, 20, "只有WAL的中间正文"),
		hotRetentionTestEvent(source.UTCDay, 20, 30, "已存后段"),
	}
	for _, ev := range events {
		appendErr := p.WAL().Append(ev)
		require.NoError(t, appendErr)
	}
	require.NoError(t, m.events.Append(key, []logtypes.Event{events[0], events[2]}))
	rebuilt := m.buildRebuiltSourceOutsideLock(rebuildTask{key: key, pipe: p, eventsStored: true, through: 30})
	found := false
	for _, item := range rebuilt.WAL {
		if item.Event.EventID == events[1].EventID {
			found = true
		}
	}
	require.True(t, found, "最大末端不能证明中间正文可取，必须保留内联正文")
}

type reservedBudgetRequest struct {
	query, start, end string
	at                time.Time
	deadline          time.Time
}

type reservedBudgetFixture struct {
	events              []logtypes.Event
	started             time.Time
	visibleAfter        time.Duration
	missingLast         bool
	missingLastOnce     bool
	crossPieceDuplicate bool
	badFields           bool
	calls               atomic.Int64
	lastPieceCalls      atomic.Int64
	mu                  sync.Mutex
	requests            []reservedBudgetRequest
}

func reservedBudgetEvents(t *testing.T, count int) (SourceConfig, []logtypes.Event) {
	t.Helper()
	source := SourceConfig{LogSourceID: "node:1/bench-000", SourceGeneration: "fixture-1", UTCDay: runtimeTestUTCDay()}
	events := make([]logtypes.Event, 0, count)
	for i := 0; i < count; i++ {
		events = append(events, sixFixEvent(source, source.UTCDay, uint64(i*156), uint64((i+1)*156), fmt.Sprintf("正文%d", i)))
	}
	return source, events
}

func reservedBudgetManager() *Manager {
	return &Manager{verificationTimeout: 3 * time.Second, verifyBackoffMin: 200 * time.Millisecond, verifyBackoffMax: 500 * time.Millisecond, verifyMaxQueriesPerChunk: 12, verifyChunkEvents: 2000, verifyChunkConcurrency: 4, verifyRoundBudget: 3 * time.Second}
}

type reservedBudgetTransport struct {
	next    http.RoundTripper
	fixture *reservedBudgetFixture
}

func (tr reservedBudgetTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	deadline, _ := r.Context().Deadline()
	tr.fixture.mu.Lock()
	tr.fixture.requests = append(tr.fixture.requests, reservedBudgetRequest{r.URL.Query().Get("query"), r.URL.Query().Get("start"), r.URL.Query().Get("end"), time.Now(), deadline})
	tr.fixture.mu.Unlock()
	return tr.next.RoundTrip(r)
}

func reservedBudgetClient(t *testing.T, fixture *reservedBudgetFixture) *vlsup.Client {
	t.Helper()
	// 静态真源在启动HTTP夹具时编码一次；不把重复造同一JSON的测试成本吃进真实120ms父截止。
	payloads := make([][]byte, len(fixture.events))
	quotedIDs := make([]string, len(fixture.events))
	for i, event := range fixture.events {
		body, err := projectionPayload([]logtypes.Event{event}, "projection-1")
		require.NoError(t, err)
		payloads[i] = body
		quotedIDs[i] = strconv.Quote(event.EventID)
	}
	last := fixture.events[len(fixture.events)-1]
	badLast := last
	badLast.Message = "坏副本正文"
	badLastPayload, err := projectionPayload([]logtypes.Event{badLast}, "projection-1")
	require.NoError(t, err)
	fixture.started = time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		if time.Since(fixture.started) < fixture.visibleAfter {
			return
		}
		query := r.URL.Query().Get("query")
		last := fixture.events[len(fixture.events)-1]
		containsLast := strings.Contains(query, strconv.Quote(last.EventID))
		lastCall := int64(0)
		if containsLast {
			lastCall = fixture.lastPieceCalls.Add(1)
		}
		var body bytes.Buffer
		for i, event := range fixture.events {
			if !strings.Contains(query, quotedIDs[i]) {
				continue
			}
			if event.EventID == last.EventID && (fixture.missingLast || fixture.missingLastOnce && lastCall == 1) {
				continue
			}
			if fixture.badFields && event.EventID == last.EventID {
				body.Write(badLastPayload)
			} else {
				body.Write(payloads[i])
			}
		}
		if fixture.crossPieceDuplicate && strings.Contains(query, quotedIDs[0]) && !containsLast {
			body.Write(payloads[len(payloads)-1])
		}
		_, _ = w.Write(body.Bytes())
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL, HTTPClient: &http.Client{Transport: reservedBudgetTransport{next: http.DefaultTransport, fixture: fixture}}})
	require.NoError(t, err)
	return client
}

func reservedBudgetAllowed(events []logtypes.Event) map[string]logtypes.Event {
	allowed := map[string]logtypes.Event{}
	for _, event := range events {
		allowed[event.EventID] = event
	}
	return allowed
}

func reservedBudgetParts(t *testing.T, source SourceConfig, events []logtypes.Event) int {
	t.Helper()
	count := 0
	for start := 0; start < len(events); {
		end, err := scopedProjectionChunkEnd(source, []string{"projection-1"}, events, start)
		require.NoError(t, err)
		start = end
		count++
	}
	return count
}

// 真实1001长ID会拆六片，旧实现只容两次全片扫描，尚未可见就耗尽预算。
func TestReservedBudgetCandidateVisibilityKeepsFinalEvidence(t *testing.T) {
	source, events := reservedBudgetEvents(t, 1001)
	require.Equal(t, 6, reservedBudgetParts(t, source, events))
	fixture := &reservedBudgetFixture{events: events, visibleAfter: time.Second}
	client := reservedBudgetClient(t, fixture)
	m := reservedBudgetManager()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stats, err := m.verifyProjectionChunk(ctx, client, source, "projection-1", events, reservedBudgetAllowed(events), []string{"projection-1"})
	require.NoError(t, err, "候选只花单片探测额度，必须保留全六片证据的最终机会")
	require.Equal(t, int64(stats.Queries), fixture.calls.Load())
	require.LessOrEqual(t, stats.Queries, 12, "实际请求硬界不因字节子片放大")
	require.GreaterOrEqual(t, stats.Queries, 7, "候选探测不能冒充完整六片证据")
	window, err := projectionQueryWindowFor(source, events)
	require.NoError(t, err)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, request := range fixture.requests {
		require.LessOrEqual(t, len(request.query), scopedProjectionQueryMaxBytes)
		require.Equal(t, window.start.UTC().Format(time.RFC3339Nano), request.start)
		require.Equal(t, window.end.UTC().Format(time.RFC3339Nano), request.end)
		require.Contains(t, request.query, projectionVerifyFields)
		deadline, _ := ctx.Deadline()
		require.True(t, request.deadline.Equal(deadline), "所有探测和证据请求都必须沿用父绝对截止")
	}
	t.Logf("parts=6 queries=%d actual=%d elapsed=%s budget=12", stats.Queries, fixture.calls.Load(), time.Since(fixture.started))
}

func TestReservedBudgetCandidateMissingLastDoesNotVerifyRuns(t *testing.T) {
	source, events := reservedBudgetEvents(t, 1001)
	fixture := &reservedBudgetFixture{events: events, missingLast: true}
	client := reservedBudgetClient(t, fixture)
	m := reservedBudgetManager()
	runs, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil, []string{"projection-1"})
	require.Error(t, err)
	require.Empty(t, runs, "候选可见但全片不完整时绝不能授予VerifiedRuns")
	require.Equal(t, int64(7), fixture.calls.Load(), "1次候选+6次全证据失败后只剩5，必须保守结束而非补齐预算")
}

// 跨响应注入为防御性反例，不假定真实VL会忽略ID选择器。
func TestReservedBudgetCandidateRejectsCrossPieceDuplicate(t *testing.T) {
	source, events := reservedBudgetEvents(t, 1001)
	fixture := &reservedBudgetFixture{events: events, crossPieceDuplicate: true}
	client := reservedBudgetClient(t, fixture)
	m := reservedBudgetManager()
	runs, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil, []string{"projection-1"})
	require.ErrorContains(t, err, "unexpected or duplicate projection event_id")
	require.Empty(t, runs)
	require.LessOrEqual(t, fixture.calls.Load(), int64(12))
}

func TestReservedBudgetCandidateRejectsFieldMismatch(t *testing.T) {
	source, events := reservedBudgetEvents(t, 1001)
	fixture := &reservedBudgetFixture{events: events, badFields: true}
	client := reservedBudgetClient(t, fixture)
	m := reservedBudgetManager()
	runs, err := m.verifyProjectionCtxYieldRuns(context.Background(), client, source, "projection-1", events, nil, []string{"projection-1"})
	require.ErrorContains(t, err, "projection content mismatch")
	require.Empty(t, runs)
	require.LessOrEqual(t, fixture.calls.Load(), int64(12))
}

func TestReservedBudgetCandidateSharesDeadlineAndLimiter(t *testing.T) {
	source, events := reservedBudgetEvents(t, 1001)
	fixture := &reservedBudgetFixture{events: events}
	client := reservedBudgetClient(t, fixture)
	m := reservedBudgetManager()
	m.verifyLimiter = newVerifyLimiter(VerifyBudget{MaxInFlight: 1, MaxPerSecond: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	started := time.Now()
	stats, err := m.verifyProjectionChunk(ctx, client, source, "projection-1", events, reservedBudgetAllowed(events), []string{"projection-1"})
	require.Error(t, err)
	require.Less(t, time.Since(started), 400*time.Millisecond, "子片不可延长父截止或绕过许可排队")
	require.LessOrEqual(t, fixture.calls.Load(), int64(2), "速率桶初始仅2个许可，父截止前不能补发")
	require.Equal(t, int64(stats.Queries), fixture.calls.Load())
	observed := m.VerifyBudgetStats()
	require.Equal(t, uint64(fixture.calls.Load()), observed.Acquired)
	require.Equal(t, observed.Acquired, observed.Completed)
	require.Equal(t, 0, observed.InFlight)
	require.Equal(t, 1, observed.MaxInFlight)
	require.Equal(t, 2, observed.MaxPerSecond)
	require.Positive(t, observed.Rejected)
}

func TestReservedBudgetCandidateInsufficientBudgetConservative(t *testing.T) {
	for _, test := range []struct {
		name                 string
		count, parts, budget int
		wantCalls            int64
	}{
		{"片数等于预算", 2160, 12, 12, 12},
		{"片数超过十二", 2161, 13, 12, 0},
		{"预算不足六片", 1001, 6, 5, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, events := reservedBudgetEvents(t, test.count)
			require.Equal(t, test.parts, reservedBudgetParts(t, source, events))
			fixture := &reservedBudgetFixture{events: events, visibleAfter: time.Hour}
			client := reservedBudgetClient(t, fixture)
			m := reservedBudgetManager()
			m.verifyMaxQueriesPerChunk = test.budget
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stats, err := m.verifyProjectionChunk(ctx, client, source, "projection-1", events, reservedBudgetAllowed(events), []string{"projection-1"})
			require.Error(t, err)
			require.Equal(t, test.wantCalls, fixture.calls.Load())
			require.LessOrEqual(t, stats.Queries, test.budget)
		})
	}
}

func TestReservedBudgetCandidateFreshEvidenceOnRetry(t *testing.T) {
	source, events := reservedBudgetEvents(t, 181)
	require.Equal(t, 2, reservedBudgetParts(t, source, events))
	fixture := &reservedBudgetFixture{events: events, missingLastOnce: true}
	client := reservedBudgetClient(t, fixture)
	m := reservedBudgetManager()
	m.verifyBackoffMin = 20 * time.Millisecond
	m.verifyBackoffMax = 20 * time.Millisecond
	stats, err := m.verifyProjectionChunk(context.Background(), client, source, "projection-1", events, reservedBudgetAllowed(events), []string{"projection-1"})
	require.NoError(t, err)
	require.Equal(t, 6, stats.Queries, "第一完整证明失败后必须另起候选和完整两片，不拼接旧轮成功片")
	require.Equal(t, int64(2), fixture.lastPieceCalls.Load())
	require.LessOrEqual(t, stats.Queries, 12)
}
