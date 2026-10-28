package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func outboxTestManager(t *testing.T, client *vlsup.Client, configure ...func(*Options)) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	source := SourceConfig{LogSourceID: "outbox-source", SourceGeneration: "g1", StorageNamespace: "outbox", UTCDay: runtimeTestUTCDay(), Path: path, Mode: pipeline.ModeFilePrimary}
	journal := catalog.NewMemJournal()
	opts := Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}, DeferredDelivery: true, VerificationTimeout: 2 * time.Second}
	for _, apply := range configure {
		apply(&opts)
	}
	m, err := newTestManager(t, opts)
	require.NoError(t, err)
	p, ok := m.pipeFor(source.LogSourceID + "/" + source.SourceGeneration)
	require.True(t, ok)
	return m, source, p
}

func appendOutboxLines(t *testing.T, path string, start, count int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	var body strings.Builder
	for n := start; n < start+count; n++ {
		fmt.Fprintf(&body, "[12:00:01] [Server thread/INFO]: outbox-%06d\n", n)
	}
	_, err = file.WriteString(body.String())
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

type outboxBlockingVerifyTransport struct {
	next       http.RoundTripper
	entered    chan struct{}
	release    <-chan struct{}
	once       *sync.Once
	attemptCtx chan context.Context
}

func (tr outboxBlockingVerifyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := tr.next.RoundTrip(request)
	if response != nil && request.URL.Path == "/select/logsql/query" && strings.Contains(request.URL.Query().Get("query"), "canonical_content_hash") {
		first := false
		tr.once.Do(func() { first = true; tr.attemptCtx <- request.Context(); close(tr.entered) })
		// 仅钉住真实HTTP返回后的调用完成点；原请求ctx/查询预算不变，不能把race耗时当1s性能门。
		if first {
			<-tr.release
		}
	}
	return response, err
}

func TestDurableOutboxBlockedVerifyAllowsNext1000RealSQLiteCommit(t *testing.T) {
	fixture := &projectionVLFixture{}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	attemptCtx := make(chan context.Context, 1)
	server := httptest.NewServer(fixture)
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL, HTTPClient: &http.Client{Transport: outboxBlockingVerifyTransport{next: http.DefaultTransport, entered: entered, release: release, once: &once, attemptCtx: attemptCtx}}})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	appendOutboxLines(t, source.Path, 0, 1000)
	first, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, first, 1000)
	drainDone := make(chan struct{})
	go func() { m.drainScheduledReplay(); close(drainDone) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("第一批未进入真实HTTP校验屏障")
	}
	// 先用纯状态入口识别旧锁耦合；不拿带race的SQLite墙钟代替编译版Worker性能验收。
	stateReady := make(chan struct{})
	go func() { p.SetRotateTarget(source.RotateTo); close(stateReady) }()
	select {
	case <-stateReady:
	case <-time.After(100 * time.Millisecond):
		close(release)
		<-stateReady
		select {
		case <-drainDone:
		case <-time.After(5 * time.Second):
			t.Error("解除负控后投递未退出")
		}
		t.Fatal("真实verify调用持有采集状态锁")
	}
	appendOutboxLines(t, source.Path, 1000, 1000)
	type result struct {
		events []logtypes.Event
		err    error
	}
	next := make(chan result, 1)
	go func() { events, err := p.Drain(); next <- result{events, err} }()
	got := <-next
	require.NoError(t, got.err)
	second := got.events
	require.Len(t, second, 1000)
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	var durable, rows uint64
	require.NoError(t, reader.QueryRow("SELECT MAX(durable_pos) FROM position").Scan(&durable))
	require.GreaterOrEqual(t, durable, second[len(second)-1].Record.End, "必须由独立mode=ro SQLite实际COMMIT证明，不接受内存水位")
	// 2026-10-29：WAL 不再落盘（source_wal 表已删），”已耐久”的持久化凭据只有 position 水位；
	// 条目数量由内存队列自身提供断言。
	require.Equal(t, 2000, len(p.WAL().Snapshot()), "队列必须完整保留两批共 2000 条")
	m.replayMu.Lock()
	_, ownerStillInflight := m.replayInflight[source.LogSourceID+"/"+source.SourceGeneration]
	m.replayMu.Unlock()
	require.True(t, ownerStillInflight, "第二批真实SQL提交时首批worker仍应在飞，不能靠首批超时退出解锁")
	firstAttemptCtx := <-attemptCtx
	require.True(t, p.OwnsDeliveryGuard(firstAttemptCtx), "真实投递/verify的同一guard owner必须仍有效，不接受仅server handler挂起")
	// sweep的有界等待者可先返回；真正owner是否仍在飞由上面的guard及replayInflight证明。
	require.Equal(t, uint64(0), p.Ledger().Get(p.Key()).Positions.Reclaim)
	close(release)
	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("解除屏障后排空器未退出")
	}
	require.Eventually(t, func() bool { return !p.OwnsDeliveryGuard(firstAttemptCtx) }, 5*time.Second, 10*time.Millisecond, "必须join解除屏障后的真实owner，不能只等有界等待者")
	t.Logf("blocked_verify_next_batch=1000 real_sqlite_durable=%d wal_rows=%d", durable, rows)
}

func TestDurableOutboxActualPlanReceiptDoesNotBorrowReplacedVariant(t *testing.T) {
	m, source, p := outboxTestManager(t, nil)
	event := sixFixEvent(source, source.UTCDay, 0, 20, "同一物理正文")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	id, err := acquire.CommitIdentityOf(event)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.rebuildSourceBuildHook.Store(func() { once.Do(func() { close(entered); <-release }) })
	done := make(chan error, 1)
	go func() {
		done <- m.persistCommittedBeforeDelivery(context.Background(), source.LogSourceID+"/"+source.SourceGeneration, p, []acquire.CommitIdentity{id})
	}()
	<-entered
	changed := event
	changed.EventTimeUTC = source.UTCDay + "T12:01:00Z"
	changed.CanonicalHash = logtypes.CanonicalContentHash(changed.EventTimeUTC, changed.Level, changed.Stream, changed.Message)
	require.NoError(t, p.WAL().AppendReplay(changed))
	require.NoError(t, p.WAL().Commit())
	close(release)
	require.ErrorIs(t, <-done, ErrPersistDeferred, "完成计划不包含原捕获正文，generic nil/covered不得授证")
	m.rebuildSourceBuildHook.Store((func())(nil))
	require.False(t, m.committedContains(source.LogSourceID+"/"+source.SourceGeneration, p, []acquire.CommitIdentity{id}))
	changedID, err := acquire.CommitIdentityOf(changed)
	require.NoError(t, err)
	require.NoError(t, m.persistCommittedBeforeDelivery(context.Background(), source.LogSourceID+"/"+source.SourceGeneration, p, []acquire.CommitIdentity{changedID}))
}

func TestDurableOutboxActualCompletedOldPlanRemainsQualified(t *testing.T) {
	m, source, p := outboxTestManager(t, nil)
	event := sixFixEvent(source, source.UTCDay, 0, 20, "同一物理正文")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	id, err := acquire.CommitIdentityOf(event)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.rebuildSourceBuildHook.Store(func() { once.Do(func() { close(entered); <-release }) })
	done := make(chan error, 1)
	go func() {
		done <- m.persistCommittedBeforeDelivery(context.Background(), source.LogSourceID+"/"+source.SourceGeneration, p, []acquire.CommitIdentity{id})
	}()
	<-entered
	changed := event
	changed.EventTimeUTC = source.UTCDay + "T12:01:00Z"
	changed.CanonicalHash = logtypes.CanonicalContentHash(changed.EventTimeUTC, changed.Level, changed.Stream, changed.Message)
	require.NoError(t, p.WAL().AppendReplay(changed))
	require.NoError(t, p.WAL().Commit())
	close(release)
	require.NoError(t, <-done, "实际完成计划包含旧正文时，不能因后续Append把整个真实回执作废")
	m.rebuildSourceBuildHook.Store((func())(nil))
	require.False(t, m.committedContains(source.LogSourceID+"/"+source.SourceGeneration, p, []acquire.CommitIdentity{id}), "不在保留WAL的旧摘要不能常驻缓存")
}

func TestDurableOutboxStopJoinsTimedOutLegacySweep(t *testing.T) {
	m, _, _ := outboxTestManager(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	require.False(t, m.runSweepStepBounded(time.Millisecond, func() { close(entered); <-release }))
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop() }()
	select {
	case err := <-stopped:
		t.Fatalf("旧sweep仍在真实运行却先关库: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	require.Equal(t, int64(1), m.sweepInflight.Load())
	close(release)
	require.NoError(t, <-stopped)
	require.Zero(t, m.sweepInflight.Load())
	var ran atomic.Bool
	require.False(t, m.runSweepStepBounded(time.Millisecond, func() { ran.Store(true) }))
	require.False(t, ran.Load(), "Stop后禁止WG.Add或后台重新开库")
	require.NoError(t, m.Stop())
}

func TestDurableOutboxAttemptBudgetNotExtendedByPoll(t *testing.T) {
	m, source, _ := outboxTestManager(t, nil)
	ctx, release, err := m.acquireSourceDelivery(context.Background(), source)
	require.NoError(t, err)
	defer release()
	attempt := ctx.Value(verifyAttemptKey{}).(*verifyAttemptBudget)
	attempt.state.deadlineNanos.Store(time.Now().Add(-time.Second).UnixNano())
	attempt.state.progressed.Store(1)
	before := attempt.state.deadlineNanos.Load()
	m.pollOnce()
	require.Equal(t, before, attempt.state.deadlineNanos.Load())
	_, err = m.verifyBudgetForContext(ctx, source, "projection-1")
	require.ErrorIs(t, err, ErrVerifyDeferred)
}

func TestDurableOutboxAllWriteEntrypointsShareSingleOwner(t *testing.T) {
	fixture := &projectionVLFixture{}
	entered, releaseHTTP := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			writes.Add(1)
			once.Do(func() { close(entered) })
			select {
			case <-releaseHTTP:
			case <-r.Context().Done():
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-releaseHTTP:
		default:
			close(releaseHTTP)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	appendOutboxLines(t, source.Path, 0, 1)
	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 1)
	ownerDone := make(chan struct{})
	go func() { m.drainScheduledReplay(); close(ownerDone) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实首写未进入屏障")
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	checks := []struct {
		name string
		call func(context.Context) error
	}{
		{"普通投递", func(ctx context.Context) error { _, err := m.deliverCtx(ctx, source, events, false); return err }},
		{"有界WAL排空", func(ctx context.Context) error { _, err := p.DeliverPendingContext(ctx); return err }},
		{"发布计划", func(ctx context.Context) error {
			_, _, err := m.writeProjectionPlan(source, events, "other", projectionWritePlan{Write: events, Snapshot: m.capturePublicationSnapshot(source, ctx)})
			return err
		}},
		{"重建及确认", func(ctx context.Context) error {
			return m.rebuildDaysInner(ctx, &rebuildRequest{key: key, source: source, days: []string{source.UTCDay}, confirmPending: true}, nil).Err
		}},
		{"启动重放", func(ctx context.Context) error {
			_, err := m.runReplayWindow(ctx, startupSource{key: key, source: source, events: events, snapshot: m.capturePublicationSnapshot(source, ctx)}, &replayWindow{generation: "other", days: []string{source.UTCDay}})
			return err
		}},
		{"发布确认", func(ctx context.Context) error {
			m.mu.Lock()
			saved := m.state.Sources[key]
			m.mu.Unlock()
			_, err := m.tryConfirmPendingPublicationVerdict(ctx, source, key, saved, nil, nil)
			return err
		}},
	}
	var wg sync.WaitGroup
	for _, check := range checks {
		check := check
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			err := check.call(ctx)
			if err != context.DeadlineExceeded && !strings.Contains(fmt.Sprint(err), context.DeadlineExceeded.Error()) {
				t.Errorf("%s未等待同源owner: %v", check.name, err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), writes.Load(), "等待许可的维护入口不能物理并发外发")
	directDone := make(chan struct{})
	go func() { m.directDeliverResolvedReReadGap(p, source); close(directDone) }()
	select {
	case <-directDone:
		t.Fatal("破环入口绕过源owner")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseHTTP)
	select {
	case <-ownerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("首个owner未结束")
	}
	select {
	case <-directDone:
	case <-time.After(3 * time.Second):
		t.Fatal("破环入口嵌套自锁")
	}
}

func TestDurableOutboxStopCancelsActualHTTPBeforeClosingIndex(t *testing.T) {
	entered, releaseHTTP := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			once.Do(func() { close(entered) })
			select {
			case <-r.Context().Done():
				return
			case <-releaseHTTP:
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-releaseHTTP:
		default:
			close(releaseHTTP)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	appendOutboxLines(t, source.Path, 0, 1)
	_, err = p.Drain()
	require.NoError(t, err)
	go m.drainScheduledReplay()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实HTTP未开始")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop() }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop没有取消并join实际HTTP")
	}
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()
	require.Nil(t, index)
	require.Zero(t, m.sweepInflight.Load())
	require.Empty(t, p.Ledger().Get(p.Key()).RequestDoneRanges, "取消不能伪ACK或释放WAL")
	require.NotEmpty(t, p.WAL().Snapshot())
}

func TestDurableOutboxReadOnlyVerificationClassifiesWithoutChargingCopies(t *testing.T) {
	for _, kind := range []string{"semantic", "400", "503"} {
		t.Run(kind, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var phase atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") {
					if phase.Load() == 0 || (kind == "503" && phase.Load() == 1) {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if kind == "400" {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if kind == "503" {
						fixture.ServeHTTP(w, r)
						return
					}
					recorder := httptest.NewRecorder()
					fixture.ServeHTTP(recorder, r)
					w.WriteHeader(recorder.Code)
					_, err := w.Write([]byte(strings.ReplaceAll(recorder.Body.String(), "readonly-original", "tampered-message")))
					if err != nil {
						t.Errorf("夹具响应失败: %v", err)
					}
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			event := sixFixEvent(source, source.UTCDay, 0, 10, "readonly-original")
			require.NoError(t, p.WAL().Append(event))
			require.NoError(t, p.WAL().Commit())
			_, err = p.DeliverPendingContext(context.Background())
			require.Error(t, err)
			phase.Store(1)
			key := source.LogSourceID + "/" + source.SourceGeneration
			out := m.rebuildDays(context.Background(), &rebuildRequest{key: key, source: source, days: []string{source.UTCDay}, confirmPending: true, reason: "真实只读失败分类"})
			if kind == "503" {
				require.NoError(t, out.Err)
				require.True(t, out.Pending)
			} else {
				require.Error(t, out.Err, "确定性语义/永久失败不能被吞成普通inconclusive")
				require.Equal(t, rebuildFailureDeterministic, m.classifyRebuildFailure(key, out.Err))
				scheduler := newRebuildScheduler()
				for i := 0; i < 2; i++ {
					scheduler.noteFailure(key, "", out.Err, m.classifyRebuildFailure(key, out.Err))
				}
				state, ok := scheduler.snapshot(key)
				require.True(t, ok)
				require.Equal(t, 2, state.attempts)
				require.Zero(t, state.totalRebuilds, "只读错误不能借本轮写入marker虚计物理copies")
			}
			fixture.mu.Lock()
			writes := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 1, writes)
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
			require.Len(t, p.WAL().Snapshot(), 1)
			if kind == "503" {
				phase.Store(2)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { m.RunRebuildWorker(ctx); close(done) }()
				defer func() { cancel(); <-done }()
				require.Eventually(t, func() bool { return len(p.WAL().Snapshot()) == 0 }, 3*time.Second, 10*time.Millisecond, "无新输入时由既有worker只读收敛")
				cancel()
				<-done
				require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 0, 10))
				fixture.mu.Lock()
				writes = len(fixture.writes)
				fixture.mu.Unlock()
				require.Equal(t, 1, writes)
			}
		})
	}
}

func TestDurableOutboxFirstWrittenUnknownOnlyReadsUntilVisible(t *testing.T) {
	for _, failure := range []string{"invisible", "503"} {
		t.Run(failure, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var unavailable atomic.Bool
			unavailable.Store(true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
					if failure == "503" {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			event := sixFixEvent(source, source.UTCDay, 0, 10, "首次实际200但校验未知")
			require.NoError(t, p.WAL().Append(event))
			require.NoError(t, p.WAL().Commit())
			_, err = p.DeliverPendingContext(context.Background())
			require.Error(t, err)
			fixture.mu.Lock()
			first := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 1, first)
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
			_, err = p.DeliverPendingContext(context.Background())
			require.Error(t, err)
			fixture.mu.Lock()
			repeated := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 1, repeated, "未发布的真实written窗口只能只读确认，查询失败/未可见不得再换代插入")
			require.Len(t, p.WAL().Snapshot(), 1)
			unavailable.Store(false)
			_, err = p.DeliverPendingContext(context.Background())
			require.NoError(t, err)
			fixture.mu.Lock()
			final := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 1, final)
			require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 0, 10))
			require.Empty(t, p.WAL().Snapshot())
			m.mu.Lock()
			pending := m.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].PublicationPending
			m.mu.Unlock()
			require.False(t, pending)
		})
	}
}

func TestDurableOutboxPostACKVariantDuringRecoveryQueryCannotTransfer(t *testing.T) {
	fixture := &projectionVLFixture{}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var current atomic.Pointer[pipeline.Pipeline]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := current.Load(); p != nil && r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && len(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key()))) > 0 {
			once.Do(func() { close(entered); <-release })
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	current.Store(p)
	event := sixFixEvent(source, source.UTCDay, 0, 10, "ACK后网络期间变体")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	done := make(chan error, 1)
	go func() { _, deliveryErr := p.DeliverPendingContext(context.Background()); done <- deliveryErr }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("未进入真实ACK后的责任取证网络屏障")
	}
	variant := logtypes.BuildEvent(event.Source, event.Record, source.UTCDay+"T12:00:00.1Z", event.IngestTimeUTC, event.Level, event.Stream, event.Message)
	require.NoError(t, p.WAL().AppendReplay(variant))
	require.NoError(t, p.WAL().Commit())
	close(release)
	require.ErrorIs(t, <-done, acquire.ErrWALWindowChanged)
	require.Zero(t, p.Ledger().Get(p.Key()).Positions.Reclaim)
	for _, entry := range p.WAL().Snapshot() {
		require.Equal(t, variant.CanonicalHash, entry.Event.CanonicalHash)
	}
	require.NotEmpty(t, p.WAL().Snapshot())
	_, err = p.TryReclaim()
	require.Error(t, err)
	require.Zero(t, p.Ledger().Get(p.Key()).Positions.Reclaim)
	m.mu.Lock()
	hint := m.writtenAttempts[source.LogSourceID+"/"+source.SourceGeneration]
	m.mu.Unlock()
	require.Nil(t, hint)
}

type outboxBlockingJournalStore struct {
	next    catalog.Store
	entered chan struct{}
	release <-chan struct{}
	enabled atomic.Bool
	once    sync.Once
}

func (store *outboxBlockingJournalStore) AppendEntry(entry catalog.JournalEntry) error {
	if store.enabled.Load() {
		store.once.Do(func() { close(store.entered); <-store.release })
	}
	return store.next.AppendEntry(entry)
}
func (store *outboxBlockingJournalStore) LoadEntries() ([]catalog.JournalEntry, error) {
	return store.next.LoadEntries()
}

func TestDurableOutboxFinalPublicationLeaseSerializesEarlierDebtButNotNewTail(t *testing.T) {
	client, _ := newProjectionVL(t)
	fileStore, err := catalog.NewJSONLFileStore(filepath.Join(t.TempDir(), "catalog.jsonl"))
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	store := &outboxBlockingJournalStore{next: fileStore, entered: entered, release: release}
	journal, err := catalog.NewJournalWithStore(store)
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client, func(opts *Options) { opts.Catalog = catalog.New(journal); opts.Journal = journal })
	event := sixFixEvent(source, source.UTCDay, 100, 110, "候选封闭前缀尾A")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	store.enabled.Store(true)
	done := make(chan error, 1)
	go func() { _, deliveryErr := p.DeliverPendingContext(context.Background()); done <- deliveryErr }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实Catalog发布未进入journal屏障")
	}
	older := sixFixEvent(source, source.UTCDay, 0, 100, "不能先COMMIT后被本次closed越过的旧正文B")
	oldDone := make(chan error, 1)
	go func() {
		err := p.WAL().AppendReplay(older)
		if err == nil {
			err = p.WAL().Commit()
		}
		oldDone <- err
	}()
	select {
	case err := <-oldDone:
		require.ErrorIs(t, err, acquire.ErrWALWindowBusy, "旧前缀必须明确背压，不能先COMMIT被closed越过或持outer锁等IO")
	case <-time.After(time.Second):
		t.Fatal("旧前缀追加不应跨journal等待")
	}
	newTail := sixFixEvent(source, source.UTCDay, 110, 120, "恰在候选边界之外的新尾")
	require.NoError(t, p.WAL().Append(newTail))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	require.Equal(t, 2, len(p.WAL().Snapshot()))
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, p.WAL().AppendReplay(older))
	require.NoError(t, p.WAL().Commit())
	require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 0, 100))
}

// 同一真实脏根供生产后台与默认同步两种重启契约复用；原输入、归档屏障和1502断言不变。
func outboxArchiveBusyRestartSeed(t *testing.T, client *vlsup.Client) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	fileStore, err := catalog.NewJSONLFileStore(filepath.Join(t.TempDir(), "catalog.jsonl"))
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	store := &outboxBlockingJournalStore{next: fileStore, entered: entered, release: release}
	journal, err := catalog.NewJournalWithStore(store)
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client, func(opts *Options) { opts.Catalog = catalog.New(journal); opts.Journal = journal })
	path := filepath.Join(filepath.Dir(source.Path), "latest.log.1")
	raw := "[12:00:01] [Server thread/INFO]: outbox-000000\n[12:00:01] [Server thread/INFO]: outbox-000001\n"
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	hash, size, err := acquire.HashFile(path)
	require.NoError(t, err)
	objectID := acquire.ArchiveObjectID(path, size, hash)
	require.NoError(t, p.Ledger().RegisterSegment(p.Key(), ledger.Segment{Path: path, Kind: ledger.SegmentRotatedText, StartPos: 0, EndPos: 0, PhysicalMappingKnown: true, ArchiveObjectID: objectID}))
	p.SetArchiveOwnerRegistry(func(string, string, string) (string, bool) { return source.SourceGeneration, true })
	appendOutboxLines(t, source.Path, 0, 1000)
	_, err = p.Drain()
	require.NoError(t, err)
	store.enabled.Store(true)
	delivery := make(chan error, 1)
	go func() { _, err := p.DeliverPendingContext(context.Background()); delivery <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实发布journal未进入屏障")
	}
	imported := make(chan error, 1)
	go func() { _, err := p.ImportArchive(path); imported <- err }()
	select {
	case err := <-imported:
		require.ErrorIs(t, err, acquire.ErrWALWindowBusy)
	case <-time.After(time.Second):
		t.Fatal("真实outer归档不应持采集锁等待prefix lease/journal")
	}
	for _, segment := range p.Ledger().Get(p.Key()).Segments {
		if segment.Path == path {
			require.False(t, segment.Imported)
			require.Zero(t, segment.EndPos, "失败chunk游标必须退回未接收起点，不得下轮EOF漏尾")
			require.Equal(t, objectID, segment.ArchiveObjectID)
		}
	}
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, string(bytes))
	require.NotEmpty(t, p.Gaps(), "受管原物与精确未接收责任必须都可解释")
	appendOutboxLines(t, source.Path, 1000, 1000)
	next := make(chan error, 1)
	go func() { _, err := p.Drain(); next <- err }()
	select {
	case err := <-next:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		close(release)
		<-delivery
		t.Fatal("旧outer归档背压后新尾1000仍被journal拖住")
	}
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	require.Equal(t, 2000, len(p.WAL().Snapshot()))
	require.True(t, m.SyncArchiveScan(25*time.Millisecond))
	defer m.StopArchiveScan()
	close(release)
	require.NoError(t, <-delivery)
	require.Eventually(t, func() bool {
		for _, segment := range p.Ledger().Get(p.Key()).Segments {
			if segment.Path == path {
				return segment.Imported
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "无新输入时必须由生产SyncArchiveScan自动重试而非测试手调导入")
	require.Len(t, p.WAL().Snapshot(), 1502, "原500在真实ACK/证明后正常回收，未确认1500和重导2条均真实耐久")
	m.StopArchiveScan()
	require.NoError(t, m.Stop())
	require.Len(t, p.WAL().Snapshot(), 1502)
	return m, source, p
}

// 2026-10-29：`source_wal` 表已删除、WAL 不再落盘，因此「重启后的队列内容」不再由库提供。
// 原 `outboxRestartSQLWAL`（直接查 source_wal）与 `outboxRestartDiskWAL`（source_wal +
// canonical 段水合正文）两个 helper 随之删除。重启责任改由下面两条断言承担：
//   - `position` 水位（read/durable/reclaim）必须一字不动地持久化在库里——它是新可靠性模型
//     的唯一持久凭据（配合 rollbackReadToReclaim 重读）；
//   - 账本里的恢复引用/审计凭据必须完整（见 outboxRestartRecoveryRefsError）。
//
// 队列内容不再参与跨重启断言：它本来就该为空（MemoryQueue 语义）。
func outboxAssertRestartResponsibility(t *testing.T, m *Manager, p *pipeline.Pipeline, want []acquire.WALEntry, before *ledger.Entry) {
	t.Helper()
	// 队列：重启后允许为空（内存队列重启即丢）。本函数只核对**若仍有条目，其身份必须精确**。
	actual := p.WAL().Snapshot()
	type walKey struct {
		from, to uint64
		id       string
	}
	identityKey := func(entry acquire.WALEntry) walKey {
		return walKey{entry.Event.Record.Start, entry.Event.Record.End, entry.Event.EventID}
	}
	wantByKey := make(map[walKey]acquire.WALEntry, len(want))
	for _, entry := range want {
		wantByKey[identityKey(entry)] = entry
	}
	for _, entry := range actual {
		expected, present := wantByKey[identityKey(entry)]
		require.True(t, present, "队列里不得出现原物集合之外的条目：id=%s", entry.Event.EventID)
		identity, err := acquire.CommitIdentityOf(expected.Event)
		require.NoError(t, err)
		got, err := acquire.CommitIdentityOf(entry.Event)
		require.NoError(t, err)
		require.Equal(t, identity, got, "原物完整正文身份不得被相同计数替代，id=%s", entry.Event.EventID)
		require.Equal(t, expected.Replay, entry.Replay)
		require.True(t, entry.Appended && entry.Durable)
	}
	// 持久凭据：position 水位必须能从库里逐字段读回（这正是重启后重读的起点）。
	var read, durable, reclaim uint64
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	require.NoError(t, reader.QueryRow("SELECT read_pos,durable_pos,reclaim_pos FROM position WHERE key=?",
		p.Key().LogSourceID+"/"+p.Key().SourceGeneration).Scan(&read, &durable, &reclaim))
	require.NoError(t, reader.Close())
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, before.Positions, entry.Positions, "未发生本批ACK时任何位置水位不得借旧资格推进")
	require.Equal(t, ledger.RequestDoneRangesOf(before), ledger.RequestDoneRangesOf(entry))
	require.NoError(t, outboxRestartRecoveryRefsError(m.indexPath(), before, entry), "未确认原物的副本责任不得提前释放；旧CLEANED消失必须有同身份完整持久审计")
	require.NoError(t, p.WAL().ReservationError())
}

// 只允许已解除且被原R覆盖的旧历史引用以独立SQLite审计替代；活引用和新引用仍逐字段比较。
func outboxRestartRecoveryRefsError(path string, before, current *ledger.Entry) error {
	if before.Key != current.Key || before.Positions.Reclaim != current.Positions.Reclaim {
		return fmt.Errorf("重启责任的源身份或原回收位置已变")
	}
	present := make(map[string]bool, len(current.RecoveryRefs))
	for _, ref := range current.RecoveryRefs {
		present[ref.SegmentID] = true
	}
	var retained []ledger.RecoveryRef
	for _, ref := range before.RecoveryRefs {
		if present[ref.SegmentID] {
			retained = append(retained, ref)
			continue
		}
		expected := ledger.RecoveryArchiveRecord{Key: before.Key, Ref: ref, Fingerprint: ledger.RecoveryArchiveFingerprint(before.Key, ref)}
		if err := ledger.ValidateRecoveryArchiveRecord(expected); err != nil {
			return fmt.Errorf("未解除或带hold的恢复引用消失：%s：%w", ref.SegmentID, err)
		}
		if ref.CoversTo > before.Positions.Reclaim {
			return fmt.Errorf("原回收位置未覆盖消失的恢复引用：%s", ref.SegmentID)
		}
		record, receipt, err := outboxReadRestartRecoveryAudit(path, before.Key, ref.SegmentID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(expected, record) || receipt.Key != before.Key || receipt.SegmentID != ref.SegmentID || receipt.Fingerprint != expected.Fingerprint || receipt.ArchiveID == "" {
			return fmt.Errorf("消失的恢复引用没有同源同全Ref的完整持久审计：%s", ref.SegmentID)
		}
	}
	if !reflect.DeepEqual(retained, current.RecoveryRefs) {
		return fmt.Errorf("仍活恢复引用发生变化或出现新引用：原样=%+v，实际=%+v", retained, current.RecoveryRefs)
	}
	return nil
}

// 直接打开独立只读SQL连接；完整正文SHA、领域指纹和回执均验证，不能借活动镜像或条数授证。
func outboxReadRestartRecoveryAudit(path string, key ledger.SourceKey, segmentID string) (ledger.RecoveryArchiveRecord, ledger.RecoveryArchiveReceipt, error) {
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return ledger.RecoveryArchiveRecord{}, ledger.RecoveryArchiveReceipt{}, err
	}
	defer func() { _ = reader.Close() }()
	var row stateindex.RecoveryAuditRow
	var checksum string
	err = reader.QueryRow("SELECT source_key,ref_key,fingerprint,payload,payload_sha,archive_id FROM recovery_audit WHERE source_key=? AND ref_key=?", key.String(), segmentID).Scan(&row.SourceKey, &row.RefKey, &row.Fingerprint, &row.Payload, &checksum, &row.ArchiveID)
	if err != nil {
		return ledger.RecoveryArchiveRecord{}, ledger.RecoveryArchiveReceipt{}, fmt.Errorf("缺失恢复引用的独立持久审计读取失败：%s：%w", segmentID, err)
	}
	if checksum != fmt.Sprintf("%x", sha256.Sum256(row.Payload)) {
		return ledger.RecoveryArchiveRecord{}, ledger.RecoveryArchiveReceipt{}, fmt.Errorf("恢复审计正文SHA不匹配：%s", segmentID)
	}
	return decodeRecoveryArchive(row)
}

// 只读捕获真实ACK/回收后的归档前快照；审计写入及摘引用仍完整委托原仓储和领域实现。
type outboxRestartAuditWitnessStore struct {
	next   *Manager
	pipe   *pipeline.Pipeline
	before chan *ledger.Entry
}

func (store *outboxRestartAuditWitnessStore) ArchiveRecovery(ctx context.Context, records []ledger.RecoveryArchiveRecord) ([]ledger.RecoveryArchiveReceipt, error) {
	select {
	case store.before <- store.pipe.Ledger().Get(store.pipe.Key()):
	default:
	}
	return store.next.ArchiveRecovery(ctx, records)
}

func (store *outboxRestartAuditWitnessStore) LookupRecovery(ctx context.Context, key ledger.SourceKey, segmentID string) (ledger.RecoveryArchiveRecord, ledger.RecoveryArchiveReceipt, bool, error) {
	return store.next.LookupRecovery(ctx, key, segmentID)
}

func TestDurableOutboxRestartRecoveryRefsRequireExactDurableAudit(t *testing.T) {
	client, _ := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	store := &outboxRestartAuditWitnessStore{next: m, pipe: p, before: make(chan *ledger.Entry, 1)}
	p.Ledger().SetRecoveryArchiveStore(store)
	appendOutboxLines(t, source.Path, 0, 3)
	_, err := p.Drain()
	require.NoError(t, err)
	ctx, release, err := m.acquireSourceDelivery(context.Background(), source)
	require.NoError(t, err)
	_, err = p.DeliverPendingContext(ctx)
	release()
	require.NoError(t, err)
	var before *ledger.Entry
	select {
	case before = <-store.before:
	case <-time.After(3 * time.Second):
		t.Fatal("真实ACK/回收未到达原审计仓储，不能手造CLEANED替代证人")
	}
	current := p.Ledger().Get(p.Key())
	require.Len(t, before.RecoveryRefs, 1)
	require.Empty(t, current.RecoveryRefs)
	require.Equal(t, before.Positions, current.Positions)
	ref := before.RecoveryRefs[0]
	record, receipt, err := outboxReadRestartRecoveryAudit(m.indexPath(), p.Key(), ref.SegmentID)
	require.NoError(t, err)
	require.Equal(t, ref, record.Ref)
	require.Equal(t, ledger.RecoveryArchiveFingerprint(p.Key(), ref), receipt.Fingerprint)
	require.NotEmpty(t, receipt.ArchiveID)
	require.NoError(t, outboxRestartRecoveryRefsError(m.indexPath(), before, current))
	// 消费者/epoch没有公开读数；实际摘除走原锁内复验，不把本测试的状态字面当其数值采样。
	t.Logf("真实ACK后全Ref审计替代成立：原R=%d record=%+v receipt=%+v；consumer/epoch仅原领域锁内门证据", before.Positions.Reclaim, record, receipt)
	writer, err := sql.Open("sqlite", "file:"+m.indexPath())
	require.NoError(t, err)
	defer func() { _ = writer.Close() }()
	var saved stateindex.RecoveryAuditRow
	var checksum string
	require.NoError(t, writer.QueryRow("SELECT source_key,ref_key,fingerprint,payload,payload_sha,archive_id FROM recovery_audit WHERE source_key=? AND ref_key=?", p.Key().String(), ref.SegmentID).Scan(&saved.SourceKey, &saved.RefKey, &saved.Fingerprint, &saved.Payload, &checksum, &saved.ArchiveID))
	for _, kind := range []string{"active", "hold", "uncovered", "changed_active", "extra_ref", "missing_audit", "bad_payload", "bad_fingerprint", "different_full_ref", "empty_receipt"} {
		t.Run(kind, func(t *testing.T) {
			original, actual := *before, *current
			original.RecoveryRefs = append([]ledger.RecoveryRef(nil), before.RecoveryRefs...)
			defer func() {
				_, restoreErr := writer.Exec("INSERT INTO recovery_audit(source_key,ref_key,fingerprint,payload,payload_sha,archive_id) VALUES(?,?,?,?,?,?) ON CONFLICT(source_key,ref_key) DO UPDATE SET fingerprint=excluded.fingerprint,payload=excluded.payload,payload_sha=excluded.payload_sha,archive_id=excluded.archive_id", saved.SourceKey, saved.RefKey, saved.Fingerprint, saved.Payload, checksum, saved.ArchiveID)
				require.NoError(t, restoreErr)
			}()
			switch kind {
			case "active":
				original.RecoveryRefs[0].State = logtypes.RecoveryStaged
			case "hold":
				original.RecoveryRefs[0].HasHold = true
			case "uncovered":
				original.Positions.Reclaim = ref.CoversTo - 1
				actual.Positions.Reclaim = original.Positions.Reclaim
			case "changed_active":
				actual.RecoveryRefs = append([]ledger.RecoveryRef(nil), before.RecoveryRefs...)
				actual.RecoveryRefs[0].Path += "-不可借同ID换正文"
			case "extra_ref":
				actual.RecoveryRefs = append([]ledger.RecoveryRef(nil), before.RecoveryRefs...)
				actual.RecoveryRefs[0].SegmentID += "-新引用"
			case "missing_audit":
				_, err = writer.Exec("DELETE FROM recovery_audit WHERE source_key=? AND ref_key=?", saved.SourceKey, saved.RefKey)
				require.NoError(t, err)
			case "bad_payload":
				_, err = writer.Exec("UPDATE recovery_audit SET payload=? WHERE source_key=? AND ref_key=?", append(append([]byte(nil), saved.Payload...), ' '), saved.SourceKey, saved.RefKey)
				require.NoError(t, err)
			case "bad_fingerprint":
				_, err = writer.Exec("UPDATE recovery_audit SET fingerprint=? WHERE source_key=? AND ref_key=?", saved.Fingerprint+"-坏指纹", saved.SourceKey, saved.RefKey)
				require.NoError(t, err)
			case "different_full_ref":
				other := record
				other.Ref.RetentionDeadlineUnixSec++
				other.Fingerprint = ledger.RecoveryArchiveFingerprint(other.Key, other.Ref)
				body, encodeErr := json.Marshal(other)
				require.NoError(t, encodeErr)
				_, err = writer.Exec("UPDATE recovery_audit SET fingerprint=?,payload=?,payload_sha=? WHERE source_key=? AND ref_key=?", other.Fingerprint, body, fmt.Sprintf("%x", sha256.Sum256(body)), saved.SourceKey, saved.RefKey)
				require.NoError(t, err)
			case "empty_receipt":
				_, err = writer.Exec("UPDATE recovery_audit SET archive_id='' WHERE source_key=? AND ref_key=?", saved.SourceKey, saved.RefKey)
				require.NoError(t, err)
			}
			require.Error(t, outboxRestartRecoveryRefsError(m.indexPath(), &original, &actual), "非法缺失/变更必须真失败：%s", kind)
		})
	}
}

// 仅启动生产真实执行体，不启动采集轮或容量排空来改变本用例的未ACK集合。
func outboxStartRestartWorker(t *testing.T, m *Manager) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(m.lifecycleContext())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("真实重建worker取消后未join")
		}
	})
	return cancel, done
}

// 两个Wait仅表示出结论；只有明确成功或有分类的临时失败才分别核其责任，不接受任意错误。
func outboxJoinRestartOutcome(t *testing.T, m *Manager, source SourceConfig, fixture *projectionVLFixture, cancel context.CancelFunc, done <-chan struct{}) rebuildOutcome {
	t.Helper()
	key := source.LogSourceID + "/" + source.SourceGeneration
	require.NoError(t, m.WaitStartupReplayIdle(waitContext(t, 30*time.Second)))
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("正常释放网络屏障后真实重建worker未join")
	}
	outcome, ok := m.RebuildLastResult(key)
	require.True(t, ok, "不能以driver登记或Wait返回冒充真实执行结论")
	require.Positive(t, m.RebuildStats().Started)
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	switch {
	case outcome.Err != nil:
		require.ErrorIs(t, outcome.Err, context.DeadlineExceeded, "仅原校验窗临时超时可走责任保留分支，语义错误必须真红")
		require.ErrorIs(t, outcome.Err, errProjectionVerifyFailed)
		require.True(t, outcome.Pending)
		require.Positive(t, m.RebuildStats().Failed)
		require.True(t, saved.PublicationPending)
		require.False(t, m.CutoverReadiness().LedgerReady)
		require.Contains(t, m.CutoverReadiness().Reasons, key+":publication_pending")
		t.Logf("生产后台恢复实际超时未成功，原200ms不变且未确认责任全留: %v", outcome.Err)
	case outcome.Pending:
		require.Positive(t, m.RebuildStats().Partial)
		require.True(t, saved.PublicationPending)
		require.False(t, m.CutoverReadiness().LedgerReady)
		t.Log("生产后台只读取证明确延期，未宣告恢复成功，未确认责任全留")
	default:
		require.Positive(t, m.RebuildStats().Completed)
		require.Equal(t, []string{source.UTCDay}, outcome.Days)
		require.Empty(t, outcome.BeyondHotRetention)
		require.False(t, saved.PublicationPending)
		record, exists := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
		require.True(t, exists)
		require.NotNil(t, record.PublishedProjection)
		require.True(t, record.PublishedProjection.CoverageComplete)
		fixture.mu.Lock()
		payloads := append([][]byte(nil), fixture.writes...)
		fixture.mu.Unlock()
		seen := make(map[string]struct{}, 2000)
		for _, payload := range payloads {
			for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
				var row struct {
					Generation string `json:"projection_generation"`
					EventID    string `json:"event_id"`
				}
				require.NoError(t, json.Unmarshal([]byte(line), &row))
				if row.Generation != saved.ProjectionGeneration {
					continue
				}
				_, duplicate := seen[row.EventID]
				require.False(t, duplicate, "实际2000写入面不能借重复物理行凑数")
				seen[row.EventID] = struct{}{}
			}
		}
		require.Len(t, seen, 2000, "原2000条必须真实落到本次已验证发布的物理代次")
		t.Logf("生产后台真实G已校验发布，未运行容量ACK排空，原未确认WAL责任仍独立保留: generation=%s", saved.ProjectionGeneration)
	}
	return outcome
}

// 真实raw失败的单源下沉：167热身后900条，500真ACK回收后余400不能因旧索引取模跳过首100。
func TestDurableOutboxPrunedReplayCursorDrainsRemaining400WithoutHintDeadlock(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	appendLines := func(start, count int) {
		t.Helper()
		file, err := os.OpenFile(source.Path, os.O_APPEND|os.O_WRONLY, 0600)
		require.NoError(t, err)
		var body strings.Builder
		for n := start; n < start+count; n++ {
			line := fmt.Sprintf("[12:00:01] [Server thread/INFO]: cursor-%06d ", n)
			line += strings.Repeat("x", 155-len(line)) + "\n"
			require.Len(t, line, 156)
			body.WriteString(line)
		}
		_, err = file.WriteString(body.String())
		require.NoError(t, err)
		require.NoError(t, file.Close())
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	appendLines(0, 167)
	warmup, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, warmup, 167)
	require.NoError(t, m.persist())
	delivered, err := p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.True(t, delivered)
	require.Empty(t, p.WAL().Snapshot())
	appendLines(167, 900)
	input, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, input, 900)
	require.NoError(t, m.persist())
	delivered, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.True(t, delivered)
	require.NoError(t, m.persist())
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, uint64(104052), entry.Positions.Delivery)
	require.Equal(t, uint64(104052), entry.Positions.Reclaim, "必须由真实HTTP逐字段确认与回收证明删前500，不手动剪WAL")
	require.Len(t, p.WAL().Snapshot(), 400, "删前 500 后应剩 400 条待投递")
	delivered, err = p.DeliverPendingContext(context.Background())
	if err != nil {
		m.mu.Lock()
		hint := m.writtenAttempts[key]
		if hint != nil {
			t.Logf("真实失败hint count=%d from=%d to=%d generation=%s", hint.count, hint.from, hint.to, hint.generation)
		}
		m.mu.Unlock()
		t.Logf("真实失败verified=%v remaining_wal=%d", m.verifiedRuns(key), len(p.WAL().Snapshot()))
	}
	require.NoError(t, err, "900先回收500后必须继续首个剩余身份，不能先发后300再被首100债务与written hint永久互锁")
	require.True(t, delivered)
	require.NoError(t, m.persist())
	entry = p.Ledger().Get(p.Key())
	require.Equal(t, uint64(166452), entry.Positions.Durable)
	require.Equal(t, uint64(166452), entry.Positions.Delivery)
	require.Equal(t, uint64(166452), entry.Positions.Reclaim)
	require.Empty(t, p.WAL().Snapshot(), "全部回收后队列必须为空")
	require.False(t, needReconfirmPending(m, key))
	fixture.mu.Lock()
	payloads := append([][]byte(nil), fixture.writes...)
	fixture.mu.Unlock()
	seen := make(map[string]struct{}, 1067)
	for _, payload := range payloads {
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			var row struct {
				ID string `json:"event_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(line), &row))
			_, duplicate := seen[row.ID]
			require.False(t, duplicate, "修游标不能靠重复插入未知正文解除死环")
			seen[row.ID] = struct{}{}
		}
	}
	require.Len(t, seen, 1067)
}

// 真实HTTP成功、完整发布与精确ACK之后，单独让责任确认读取失败；不注入水位或伪造ACK。
func outboxReclaimDebtFixture(t *testing.T) (*Manager, SourceConfig, *pipeline.Pipeline, *projectionVLFixture, *atomic.Bool, *atomic.Int64, uint64, uint64) {
	t.Helper()
	fixture := &projectionVLFixture{}
	var pipe atomic.Pointer[pipeline.Pipeline]
	blocked, failed := &atomic.Bool{}, &atomic.Int64{}
	var warmupEnd atomic.Uint64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && blocked.Load() {
			if p := pipe.Load(); p != nil {
				if pos, ok := p.Ledger().Positions(p.Key()); ok && pos.Delivery > warmupEnd.Load() {
					failed.Add(1)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	pipe.Store(p)
	appendOutboxLines(t, source.Path, 0, 1)
	warmup, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, warmup, 1)
	delivered, err := p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.True(t, delivered)
	before := p.Ledger().Get(p.Key()).Positions.Reclaim
	require.Equal(t, warmup[0].Record.End, before)
	warmupEnd.Store(before)
	appendOutboxLines(t, source.Path, 1, 3)
	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 3)
	through := events[len(events)-1].Record.End
	blocked.Store(true)
	return m, source, p, fixture, blocked, failed, before, through
}

// 只读SQLite证明真正COMMIT；不得拿内存回收、后台交办或Stop的最终落库充数。
func outboxReclaimSQLCaughtUp(t *testing.T, m *Manager, key string, through uint64) bool {
	t.Helper()
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	if err != nil {
		return false
	}
	defer reader.Close()
	// 2026-10-29：`source_wal` 表已删除，「剩余未回收行数」改由调用方用内存队列自行断言
	// （本 helper 只管库侧），这里只读三个水位与 projection.pending。
	var read, durable, reclaim, pending uint64
	err = reader.QueryRow(`SELECT p.read_pos,p.durable_pos,p.reclaim_pos,pr.pending FROM position p JOIN projection pr ON pr.key=p.key WHERE p.key=?`, key).Scan(&read, &durable, &reclaim, &pending)
	return err == nil && read == through && durable == through && reclaim == through && pending == 0
}

func TestDurableOutboxACKedProofDebtRetriesReadonlyWithoutNewInput(t *testing.T) {
	m, source, p, fixture, blocked, failed, before, through := outboxReclaimDebtFixture(t)
	key := source.LogSourceID + "/" + source.SourceGeneration
	input, err := os.ReadFile(source.Path)
	require.NoError(t, err)
	claimed, ok := m.takeScheduledReplay()
	require.True(t, ok)
	require.Equal(t, key, claimed)
	m.runDurableDelivery(key, p)
	m.finishScheduledReplay(key)
	require.Positive(t, failed.Load(), "故障必须发生在真实ACK之后的HTTP逐字段责任确认，不能挡在外发前")
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, through, entry.Positions.Read)
	require.Equal(t, through, entry.Positions.Durable)
	require.Equal(t, through, entry.Positions.Delivery)
	require.Equal(t, before, entry.Positions.Reclaim)
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), before, through))
	require.False(t, needReconfirmPending(m, key), "发布已完成，欠的是后置责任证明而非重新写入")
	pending, err := p.HasPendingDelivery(context.Background())
	require.NoError(t, err)
	require.False(t, pending, "精确ACK集合已完整，不能靠扩大未ACK集合重发正文")
	m.replayMu.Lock()
	_, retry := m.replayPending[key]
	due := m.replayDue[key]
	m.replayMu.Unlock()
	require.True(t, retry, "ACK已全时仍须保留后置责任只读续做，不能等待新输入或周期补段")
	require.False(t, due.IsZero(), "确认暂不可读必须沿原按源退避，不得无HTTP进展热循环")
	blocked.Store(false)
	// 调用既有单源worker的一步，模拟原退避到期后的认领；不改间隔、不追加输入、不启Stop。
	m.runDurableDelivery(key, p)
	require.Equal(t, through, p.Ledger().Get(p.Key()).Positions.Reclaim)
	require.Eventually(t, func() bool { return outboxReclaimSQLCaughtUp(t, m, key, through) }, 2*time.Second, 5*time.Millisecond,
		"真实后置确认/回收必须在运行期登记落库，不能只在Stop时SQL才追平")
	fixture.mu.Lock()
	payloads := append([][]byte(nil), fixture.writes...)
	fixture.mu.Unlock()
	require.Len(t, payloads, 2, "后置债务只能只读取证，不能再插已ACK正文")
	seen := map[string]bool{}
	for _, payload := range payloads {
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			var row struct {
				ID string `json:"event_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(line), &row))
			require.False(t, seen[row.ID], "不能用重复物理行换取回收收敛")
			seen[row.ID] = true
		}
	}
	require.Len(t, seen, 4)
	after, err := os.ReadFile(source.Path)
	require.NoError(t, err)
	require.Equal(t, input, after)
	require.False(t, m.managerStopped())
}

func TestDurableOutboxACKedHoldProofErrorRetainsReadonlyRetry(t *testing.T) {
	m, source, p, fixture, blocked, _, before, through := outboxReclaimDebtFixture(t)
	key := source.LogSourceID + "/" + source.SourceGeneration
	blocked.Store(false)
	var holds atomic.Int64
	m.recoveryHold = func(SourceConfig, string) (bool, string) {
		holds.Add(1)
		return true, "真实ACK后的运维保留约束"
	}
	claimed, ok := m.takeScheduledReplay()
	require.True(t, ok)
	require.Equal(t, key, claimed)
	m.runDurableDelivery(key, p)
	m.finishScheduledReplay(key)
	require.Positive(t, holds.Load(), "必须真实走到ACK之后的hold/proofErr分支")
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, through, entry.Positions.Delivery)
	require.Equal(t, before, entry.Positions.Reclaim)
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), before, through))
	pending, err := p.HasPendingDelivery(context.Background())
	require.NoError(t, err)
	require.False(t, pending)
	m.replayMu.Lock()
	_, retry := m.replayPending[key]
	due := m.replayDue[key]
	m.replayMu.Unlock()
	require.True(t, retry, "delivered=true加后置proofErr不能因没有未ACK正文就丢弃责任续做")
	require.False(t, due.IsZero())
	// 运维约束未解除时再运行原worker一步，回收和物理写入都必须保持。
	m.runDurableDelivery(key, p)
	require.Equal(t, before, p.Ledger().Get(p.Key()).Positions.Reclaim)
	fixture.mu.Lock()
	writes := len(fixture.writes)
	fixture.mu.Unlock()
	require.Equal(t, 2, writes)
	m.recoveryHold = nil
	cleared := 0
	for _, ref := range p.Ledger().Get(p.Key()).RecoveryRefs {
		if ref.HasHold {
			require.NoError(t, p.SetRecoveryHold(ref.SegmentID, false), "通过原领域运维入口解除hold，不改水位或ACK")
			cleared++
		}
	}
	require.Equal(t, 1, cleared)
	m.runDurableDelivery(key, p)
	require.Eventually(t, func() bool { return outboxReclaimSQLCaughtUp(t, m, key, through) }, 2*time.Second, 5*time.Millisecond)
	fixture.mu.Lock()
	require.Len(t, fixture.writes, 2, "解除约束后的后置责任仍只能只读确认，不能重发已ACK正文")
	fixture.mu.Unlock()
}

func TestDurableOutboxRollingReclaimProgressPersistsBeforeStop(t *testing.T) {
	m, source, p, _, blocked, failed, before, through := outboxReclaimDebtFixture(t)
	key := source.LogSourceID + "/" + source.SourceGeneration
	delivered, err := p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.True(t, delivered)
	require.Positive(t, failed.Load())
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, through, entry.Positions.Delivery)
	require.Equal(t, before, entry.Positions.Reclaim)
	require.NoError(t, m.persist(), "先把真实ACK与旧R落SQL，形成与run3相同的已发布但回收未跟状态")
	require.False(t, outboxReclaimSQLCaughtUp(t, m, key, through))
	blocked.Store(false)
	m.reclaimStep0RollingOnly(p, source)
	require.Equal(t, through, p.Ledger().Get(p.Key()).Positions.Reclaim, "既有完整PP/body/ACK助手必须真实推进内存回收")
	require.Eventually(t, func() bool { return outboxReclaimSQLCaughtUp(t, m, key, through) }, 2*time.Second, 5*time.Millisecond,
		"维护面已真实推进R后必须唤醒既有持久化消费者，不能靠下一笔输入或Stop代落")
	require.False(t, m.managerStopped())
}

func TestDurableOutboxOuterArchiveBusyRewindsAndScannerRecoversWithoutInput(t *testing.T) {
	fixture := &projectionVLFixture{}
	var armed atomic.Bool
	entered, release, handlerDone := make(chan []byte, 1), make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first := false
		if armed.Load() && r.URL.Path == "/insert/jsonline" {
			once.Do(func() { first = true })
		}
		if first {
			defer close(handlerDone)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// 只记录真实请求字节，未交给VL夹具、未回复状态，绝不以睡眠猜测未ACK时点。
			entered <- body
			select {
			case <-release:
				r.Body = io.NopCloser(strings.NewReader(string(body)))
			case <-r.Context().Done():
				return
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	old, source, previous := outboxArchiveBusyRestartSeed(t, client)
	key := source.LogSourceID + "/" + source.SourceGeneration
	want, before := previous.WAL().Snapshot(), previous.Ledger().Get(previous.Key())
	armed.Store(true)
	restarted, err := newTestManager(t, Options{Root: old.root, VL: client, Catalog: catalog.New(old.journal), Journal: old.journal, Sources: []SourceConfig{source}, DeferredDelivery: true, StartupRecoveryBackground: true})
	require.NoError(t, err)
	require.Equal(t, 200*time.Millisecond, restarted.verificationTimeout)
	require.NoError(t, restarted.WaitStartupRecovery(waitContext(t, 30*time.Second)))
	require.Contains(t, restarted.StartupRecoveryStatus().ReplaysInFlight, key)
	restored, ok := restarted.pipeFor(key)
	require.True(t, ok)
	outboxAssertRestartResponsibility(t, restarted, restored, want, before)
	cancel, workerDone := outboxStartRestartWorker(t, restarted)
	var payload []byte
	select {
	case payload = <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("生产后台重建没有到达真实insert响应前屏障")
	}
	require.NotEmpty(t, payload)
	require.Positive(t, restarted.RebuildStats().Started)
	require.Contains(t, restarted.StartupRecoveryStatus().ReplaysInFlight, key)
	require.Contains(t, restarted.CutoverReadiness().Reasons, "startup_replay_in_flight")
	outboxAssertRestartResponsibility(t, restarted, restored, want, before)
	oldID, err := acquire.CommitIdentityOf(want[0].Event)
	require.NoError(t, err)
	require.False(t, restarted.committedContains(key, previous, []acquire.CommitIdentity{oldID}), "旧进程pipe的回执不能授权新进程")
	close(release)
	outboxJoinRestartOutcome(t, restarted, source, fixture, cancel, workerDone)
	<-handlerDone
	outboxAssertRestartResponsibility(t, restarted, restored, want, before)
	require.NoError(t, restarted.Stop())
}

// 真实HTTP已经返回响应头后仍不消费校验正文，直到本次原ctx超时；父生命周期保持有效。
type outboxRestartVerifyTimeoutTransport struct {
	next       http.RoundTripper
	armed      *atomic.Bool
	once       *sync.Once
	attemptCtx chan context.Context
}

func (tr outboxRestartVerifyTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := tr.next.RoundTrip(request)
	first := false
	query := request.URL.Query().Get("query")
	if tr.armed.Load() && response != nil && request.URL.Path == "/select/logsql/query" && strings.Contains(query, `projection_generation:="projection-2"`) && strings.Contains(query, projectionVerifyFields) {
		tr.once.Do(func() { first = true })
	}
	if first {
		tr.attemptCtx <- request.Context()
		<-request.Context().Done()
		_ = response.Body.Close()
		return nil, request.Context().Err()
	}
	return response, err
}

func TestDurableOutboxSynchronousRestartTimeoutPreservesExactResponsibility(t *testing.T) {
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	var armed atomic.Bool
	var once sync.Once
	attempt := make(chan context.Context, 1)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL, HTTPClient: &http.Client{Timeout: 5 * time.Second, Transport: outboxRestartVerifyTimeoutTransport{next: http.DefaultTransport, armed: &armed, once: &once, attemptCtx: attempt}}})
	require.NoError(t, err)
	old, source, previous := outboxArchiveBusyRestartSeed(t, client)
	key := source.LogSourceID + "/" + source.SourceGeneration
	want, before := previous.WAL().Snapshot(), previous.Ledger().Get(previous.Key())
	armed.Store(true)
	failed, err := newTestManager(t, Options{Root: old.root, VL: client, Catalog: catalog.New(old.journal), Journal: old.journal, Sources: []SourceConfig{source}, DeferredDelivery: true})
	require.Nil(t, failed, "默认同步New不能把校验失败伪装成创建成功")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, errProjectionVerifyFailed)
	require.ErrorContains(t, err, "启动恢复重发失败")
	var attemptCtx context.Context
	select {
	case attemptCtx = <-attempt:
	default:
		t.Fatal("同步超时必须由原ctx内的真实HTTP校验响应屏障触发")
	}
	owner, ok := attemptCtx.Value(managerDeliveryKey{}).(*managerDeliveryOwner)
	require.True(t, ok)
	failedOwner := owner.manager
	var cleanOnce sync.Once
	cleanupFailedOwner := func() {
		cleanOnce.Do(func() {
			failedOwner.lifecycleCancel()
			require.NoError(t, failedOwner.WaitStartupRecovery(waitContext(t, 30*time.Second)))
			<-failedOwner.persistWorkerDone
			require.NoError(t, failedOwner.CloseIndex())
		})
	}
	t.Cleanup(cleanupFailedOwner)
	require.NoError(t, failedOwner.lifecycleContext().Err(), "父生命周期未取消，不能把关停错误冒充校验窗到期")
	require.ErrorIs(t, attemptCtx.Err(), context.DeadlineExceeded)
	_, hasAttemptDeadline := attemptCtx.Deadline()
	_, hasParentDeadline := failedOwner.lifecycleContext().Deadline()
	require.True(t, hasAttemptDeadline)
	require.False(t, hasParentDeadline)
	require.Equal(t, 200*time.Millisecond, failedOwner.verificationTimeout)
	restored, ok := failedOwner.pipeFor(key)
	require.True(t, ok)
	outboxAssertRestartResponsibility(t, failedOwner, restored, want, before)
	failedOwner.mu.Lock()
	pending := failedOwner.state.Sources[key].PublicationPending
	failedOwner.mu.Unlock()
	require.True(t, pending)
	require.False(t, failedOwner.CutoverReadiness().LedgerReady)
	// 失败型New不把Manager交给调用方；只清理真实请求带出的同一实例，避免测试遗留持久化环。
	cleanupFailedOwner()
	armed.Store(false)
	third, err := newTestManager(t, Options{Root: old.root, VL: client, Catalog: catalog.New(old.journal), Journal: old.journal, Sources: []SourceConfig{source}, DeferredDelivery: true, StartupRecoveryBackground: true})
	require.NoError(t, err)
	require.Equal(t, 200*time.Millisecond, third.verificationTimeout)
	require.NoError(t, third.WaitStartupRecovery(waitContext(t, 30*time.Second)))
	thirdPipe, ok := third.pipeFor(key)
	require.True(t, ok)
	outboxAssertRestartResponsibility(t, third, thirdPipe, want, before)
	cancel, done := outboxStartRestartWorker(t, third)
	outboxJoinRestartOutcome(t, third, source, fixture, cancel, done)
	outboxAssertRestartResponsibility(t, third, thirdPipe, want, before)
	require.NoError(t, third.Stop())
}

// 用真实物化台账留下跨日已写副本与源级待确认责任；仅清进程内hint以覆盖无hint复用入口。
func outboxReusePendingSeed(t *testing.T, client *vlsup.Client) (*Manager, SourceConfig, *pipeline.Pipeline, []logtypes.Event) {
	t.Helper()
	m, source, p := outboxTestManager(t, client, func(opts *Options) { opts.VerificationTimeout = 200 * time.Millisecond })
	day, err := time.Parse("2006-01-02", source.UTCDay)
	require.NoError(t, err)
	events := []logtypes.Event{
		sixFixEvent(source, day.AddDate(0, 0, -1).Format("2006-01-02"), 0, 10, "前一天必须独立确认的责任"),
		sixFixEvent(source, source.UTCDay, 10, 20, "当天可复用的已写副本"),
	}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	_, _, err = m.writeProjectionPlan(source, events, "projection-1", projectionWritePlan{Write: events, Replace: true, KeepPending: true})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	hint := m.writtenAttempts[key]
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pending)
	m.clearWrittenAttempt(key, hint)
	require.NoError(t, m.persist())
	return m, source, p, events
}

func outboxRunRegularReuse(t *testing.T, m *Manager, source SourceConfig) rebuildOutcome {
	t.Helper()
	key := source.LogSourceID + "/" + source.SourceGeneration
	ok, reason := m.scheduleRebuildAsync(source, []string{source.UTCDay}, "普通缺失天复用待确认责任")
	require.True(t, ok, reason)
	m.rebuilds.mu.Lock()
	req := m.rebuilds.pickLocked()
	m.rebuilds.mu.Unlock()
	require.NotNil(t, req)
	require.False(t, req.confirmPending, "必须覆盖普通入口，不能由测试把它改成确认请求")
	m.runRebuildRequest(m.lifecycleContext(), req)
	outcome, exists := m.RebuildLastResult(key)
	require.True(t, exists)
	return outcome
}

func outboxFixtureWriteCount(fixture *projectionVLFixture) int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return len(fixture.writes)
}

func TestDurableOutboxRegularReuseRequeuesQuietSourceWithoutCopies(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p, events := outboxReusePendingSeed(t, client)
	key := source.LogSourceID + "/" + source.SourceGeneration
	before, writes := p.Ledger().Get(p.Key()), outboxFixtureWriteCount(fixture)
	outcome := outboxRunRegularReuse(t, m, source)
	require.NoError(t, outcome.Err)
	require.True(t, outcome.Pending, "单日复用不能伪报源级Completed，跨日责任仍待原CAS确认")
	require.Equal(t, int64(0), m.RebuildStats().Completed)
	require.Equal(t, int64(1), m.RebuildStats().Partial)
	require.True(t, needReconfirmPending(m, key))
	require.Equal(t, before.Positions, p.Ledger().Get(p.Key()).Positions)
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
	require.Equal(t, writes, outboxFixtureWriteCount(fixture), "复用当轮零新物理写")
	m.rebuilds.mu.Lock()
	timers := len(m.rebuilds.confirmTimers)
	m.rebuilds.mu.Unlock()
	require.Equal(t, 1, timers, "普通请求退出也必须保留唯一真实续排唤醒")
	cancel, done := outboxStartRestartWorker(t, m)
	// 使用生产原5秒退避；静默期间没有新尾、没有测试手动登记或排空。
	require.Eventually(t, func() bool { return !needReconfirmPending(m, key) }, 15*time.Second, 10*time.Millisecond, "普通quiet复用必须由原timer与真实worker只读确认全部天")
	cancel()
	<-done
	last, exists := m.RebuildLastResult(key)
	require.True(t, exists)
	require.NoError(t, last.Err)
	require.False(t, last.Pending)
	require.True(t, last.ConfirmedOnly)
	require.Equal(t, writes, outboxFixtureWriteCount(fixture), "只读续排收口不能新增insert或副本")
	require.Equal(t, before.Positions, p.Ledger().Get(p.Key()).Positions)
	require.Len(t, p.WAL().Snapshot(), 2, "Catalog确认不等于WAL真实ACK，不放未确认责任")
	for _, event := range events {
		rows, err := m.index.ListMaterializations(context.Background(), key, event.EventTimeUTC[:10], canonicalWindowKey([]logtypes.Event{event}), 0, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
	}
}

func TestDurableOutboxReuseConfirmationPreservesUnfinishedResponsibility(t *testing.T) {
	for _, scenario := range []string{"多日剩余债务", "WAL新尾在旧closed外", "查询503", "权威失效"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var armed atomic.Bool
			var unavailable atomic.Int64
			var once sync.Once
			errorsSeen := make(chan error, 1)
			var m *Manager
			var source SourceConfig
			var p *pipeline.Pipeline
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if armed.Load() && r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") {
					if scenario == "查询503" {
						unavailable.Add(1)
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					once.Do(func() {
						var err error
						switch scenario {
						case "WAL新尾在旧closed外":
							event := sixFixEvent(source, source.UTCDay, 20, 30, "确认查询中的真实未来尾部")
							err = p.WAL().Append(event)
							if err == nil {
								err = p.WAL().Commit()
							}
							if err == nil {
								err = m.persist()
							}
						case "权威失效":
							part := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
							rec := catalog.NewStableRecord(part, catalog.OwnerCold, 2, "确认中切换的真实权威")
							err = m.cat.AppendJournal(catalog.JournalEntry{Key: part, Record: rec, AuthorityCommit: true})
						}
						errorsSeen <- err
					})
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p, _ = outboxReusePendingSeed(t, client)
			key := source.LogSourceID + "/" + source.SourceGeneration
			if scenario == "多日剩余债务" {
				day, err := time.Parse("2006-01-02", source.UTCDay)
				require.NoError(t, err)
				missing := sixFixEvent(source, day.AddDate(0, 0, -2).Format("2006-01-02"), 20, 30, "从未发布的另一日责任")
				require.NoError(t, p.WAL().Append(missing))
				require.NoError(t, p.WAL().Commit())
				require.NoError(t, m.persist())
			}
			before, writes := p.Ledger().Get(p.Key()), outboxFixtureWriteCount(fixture)
			outcome := outboxRunRegularReuse(t, m, source)
			require.NoError(t, outcome.Err)
			require.True(t, outcome.Pending)
			require.True(t, needReconfirmPending(m, key))
			require.Equal(t, int64(0), m.RebuildStats().Completed)
			if scenario != "多日剩余债务" {
				partial, failed, completed := m.RebuildStats().Partial, m.RebuildStats().Failed, m.RebuildStats().Completed
				if scenario == "查询503" {
					m.rebuilds.mu.Lock()
					m.rebuilds.attempts[key] = &rebuildAttemptState{attempts: 2, totalRebuilds: 1, lastErr: errors.New("既有失败证据不得被inconclusive清掉"), nextAllowedAt: time.Now().Add(-time.Second)}
					m.rebuilds.mu.Unlock()
				}
				armed.Store(true)
				cancel, done := outboxStartRestartWorker(t, m)
				require.Eventually(t, func() bool {
					stats := m.RebuildStats()
					return stats.Partial > partial || stats.Failed > failed || stats.Completed > completed
				}, 15*time.Second, 10*time.Millisecond, "原timer必须真正执行只读确认并出真实结论，不能凭未执行断言安全")
				cancel()
				<-done
				last, exists := m.RebuildLastResult(key)
				require.True(t, exists)
				if scenario == "WAL新尾在旧closed外" {
					// 尚未有新publication attempt时旧PP可合法确认，但未来WAL不能借旧closed/ACK被回收。
					require.NoError(t, last.Err)
					require.False(t, last.Pending)
					require.True(t, last.ConfirmedOnly)
					record, exists := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
					require.True(t, exists)
					require.NotNil(t, record.PublishedProjection)
					scope, scoped := m.publishedScope(source, source.UTCDay)
					require.True(t, scoped)
					require.Equal(t, uint64(20), scope.ClosedVisibleSeq)
					require.Less(t, scope.ClosedVisibleSeq, p.Ledger().Get(p.Key()).Positions.Durable)
				} else {
					require.True(t, last.Pending)
				}
				if scenario == "查询503" {
					require.Positive(t, unavailable.Load(), "必须实际收到HTTP503，不能凭未执行宣称安全")
					if last.Err == nil {
						require.Greater(t, m.RebuildStats().Partial, partial, "既有deferred分类将临时查询失败记为inconclusive，不得冒Completed")
						require.False(t, last.ConfirmedOnly)
					} else {
						require.Contains(t, last.Err.Error(), "503", "真实错误必须保留503原因，不接受任意失败")
						require.Greater(t, m.RebuildStats().Failed, failed)
					}
					state, exists := m.rebuilds.snapshot(key)
					require.True(t, exists)
					require.Equal(t, 2, state.attempts, "只读取证503不得清既有失败预算或误消耗确定性次数")
					require.Equal(t, 1, state.totalRebuilds)
					require.NotZero(t, state.nextAllowedAt, "新Pending出口不得清原退避台账")
				} else {
					if last.Err != nil {
						require.True(t, errors.Is(last.Err, errPublicationStale) || errors.Is(last.Err, catalog.ErrInvalidState) || errors.Is(last.Err, context.DeadlineExceeded), "只能接受明确快照失效或原窗超时，不能吞任意错误: %v", last.Err)
					}
					select {
					case observed := <-errorsSeen:
						require.NoError(t, observed)
					default:
						t.Fatal("必须实际注入网络查询中的新尾或authority变化")
					}
				}
			}
			if scenario != "WAL新尾在旧closed外" {
				require.True(t, needReconfirmPending(m, key))
			}
			require.False(t, m.CutoverReadiness().LedgerReady)
			require.Equal(t, before.Positions.Delivery, p.Ledger().Get(p.Key()).Positions.Delivery)
			require.Equal(t, before.Positions.Reclaim, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.Equal(t, before.RecoveryRefs, p.Ledger().Get(p.Key()).RecoveryRefs)
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
			wantEntries := 2
			if scenario == "多日剩余债务" || scenario == "WAL新尾在旧closed外" {
				wantEntries++
			}
			require.Len(t, p.WAL().Snapshot(), wantEntries)
			require.Equal(t, writes, outboxFixtureWriteCount(fixture), "无效证据不得引发新物理写或副本")
			for _, day := range []string{source.UTCDay, time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")} {
				rows, err := m.index.ListMaterializations(context.Background(), key, day, "", 0, 20)
				require.NoError(t, err)
				require.Len(t, rows, 1, "副本台账不得因只读确认失败新增收费行")
			}
		})
	}
}

func TestDurableOutboxReuseReconfirmRespectsBackoffQueueAndCoalesce(t *testing.T) {
	for _, scenario := range []string{"nextAllowed", "queue_full", "coalesced_normal"} {
		t.Run(scenario, func(t *testing.T) {
			client, fixture := newProjectionVL(t)
			m, source, p, _ := outboxReusePendingSeed(t, client)
			key := source.LogSourceID + "/" + source.SourceGeneration
			outcome := outboxRunRegularReuse(t, m, source)
			require.NoError(t, outcome.Err)
			require.True(t, outcome.Pending)
			writes := outboxFixtureWriteCount(fixture)
			req := &rebuildRequest{key: key, source: source, days: []string{source.UTCDay}}
			for i := 0; i < 32; i++ {
				m.scheduleConfirmRequeue(req)
			}
			m.rebuilds.mu.Lock()
			firstTimer := m.rebuilds.confirmTimers[key]
			timers := len(m.rebuilds.confirmTimers)
			if scenario == "nextAllowed" {
				m.rebuilds.attempts[key] = &rebuildAttemptState{nextAllowedAt: time.Now().Add(confirmRequeueBackoff + 500*time.Millisecond)}
			}
			if scenario == "queue_full" {
				for i := 0; i < rebuildTaskQueueCap; i++ {
					busyKey := fmt.Sprintf("容量占位%d", i)
					m.rebuilds.pending[busyKey] = &rebuildRequest{key: busyKey, source: source, days: []string{source.UTCDay}}
				}
			}
			m.rebuilds.mu.Unlock()
			require.NotNil(t, firstTimer)
			require.Equal(t, 1, timers, "同一源重复续做始终只保留一个timer")
			if scenario == "coalesced_normal" {
				ok, reason := m.scheduleRebuildAsync(source, []string{source.UTCDay}, "timer到达前已有普通请求")
				require.True(t, ok, reason)
			}
			if scenario == "nextAllowed" {
				require.Eventually(t, func() bool { return m.RebuildStats().BackoffSkipped > 0 }, 10*time.Second, 10*time.Millisecond)
			} else {
				require.Eventually(t, func() bool {
					m.rebuilds.mu.Lock()
					defer m.rebuilds.mu.Unlock()
					current := m.rebuilds.confirmTimers[key]
					if scenario == "queue_full" {
						return current != nil && current != firstTimer && len(m.rebuilds.pending) == rebuildTaskQueueCap && m.rebuilds.pending[key] == nil
					}
					queued := m.rebuilds.pending[key]
					return current == nil && queued != nil && !queued.confirmPending
				}, 10*time.Second, 10*time.Millisecond, "必须观察真实timer已触发而非测试手动fire")
			}
			require.True(t, needReconfirmPending(m, key))
			require.Equal(t, writes, outboxFixtureWriteCount(fixture))
			if scenario == "queue_full" {
				m.rebuilds.mu.Lock()
				m.rebuilds.pending = make(map[string]*rebuildRequest)
				m.rebuilds.order = nil
				m.rebuilds.mu.Unlock()
			}
			cancel, done := outboxStartRestartWorker(t, m)
			require.Eventually(t, func() bool { return !needReconfirmPending(m, key) }, 15*time.Second, 10*time.Millisecond, "拒收或合并不能丢掉quiet来源最终只读确认")
			cancel()
			<-done
			require.Equal(t, writes, outboxFixtureWriteCount(fixture))
			require.Len(t, p.WAL().Snapshot(), 2)
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
		})
	}
}

func TestDurableOutboxReuseReconfirmStopsAtWorkerClosedAndManagerStop(t *testing.T) {
	for _, scenario := range []string{"worker_closed", "manager_stop"} {
		t.Run(scenario, func(t *testing.T) {
			client, fixture := newProjectionVL(t)
			m, source, _, _ := outboxReusePendingSeed(t, client)
			key := source.LogSourceID + "/" + source.SourceGeneration
			require.True(t, outboxRunRegularReuse(t, m, source).Pending)
			writes := outboxFixtureWriteCount(fixture)
			req := &rebuildRequest{key: key, source: source, days: []string{source.UTCDay}}
			if scenario == "worker_closed" {
				cancel, done := outboxStartRestartWorker(t, m)
				cancel()
				<-done
				m.rebuilds.mu.Lock()
				closed := m.rebuilds.closed
				if timer := m.rebuilds.confirmTimers[key]; timer != nil {
					timer.Stop()
					delete(m.rebuilds.confirmTimers, key)
				}
				m.rebuilds.mu.Unlock()
				require.True(t, closed)
				require.False(t, m.managerStopped(), "必须覆盖worker先停而Manager尚live的窗口")
			} else {
				require.NoError(t, m.Stop())
			}
			// 模拟已经进入的timer回调与并发续排入口，不等人工睡眠也不冒充HTTP恢复成功。
			m.fireConfirmRequeue(req)
			m.scheduleConfirmRequeue(req)
			m.scheduleConfirmRequeueDelay(req, time.Millisecond)
			m.rebuilds.mu.Lock()
			timers, queued := len(m.rebuilds.confirmTimers), len(m.rebuilds.pending)
			m.rebuilds.mu.Unlock()
			require.Zero(t, timers, "关闭终态不能被误当队列满继续造timer")
			require.Zero(t, queued)
			require.Equal(t, writes, outboxFixtureWriteCount(fixture))
		})
	}
}

func TestDurableOutboxSlowCatalogJournalAllowsNext1000RealSQLiteCommit(t *testing.T) {
	fixture := &projectionVLFixture{}
	var unavailable atomic.Bool
	unavailable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	fileStore, err := catalog.NewJSONLFileStore(filepath.Join(t.TempDir(), "catalog.jsonl"))
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	store := &outboxBlockingJournalStore{next: fileStore, entered: entered, release: release}
	journal, err := catalog.NewJournalWithStore(store)
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client, func(opts *Options) { opts.Catalog = catalog.New(journal); opts.Journal = journal })
	appendOutboxLines(t, source.Path, 0, 1000)
	_, err = p.Drain()
	require.NoError(t, err)
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	unavailable.Store(false)
	store.enabled.Store(true)
	done := make(chan error, 1)
	go func() { _, deliveryErr := p.DeliverPendingContext(context.Background()); done <- deliveryErr }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("只读确认未进入真实Catalog journal落地屏障")
	}
	appendOutboxLines(t, source.Path, 1000, 1000)
	type drainResult struct {
		events []logtypes.Event
		err    error
	}
	committed := make(chan drainResult, 1)
	go func() {
		events, drainErr := p.Drain()
		committed <- drainResult{events: events, err: drainErr}
	}()
	var second []logtypes.Event
	select {
	case got := <-committed:
		require.NoError(t, got.err)
		second = got.events
	case <-time.After(4 * time.Second):
		close(release)
		<-done
		t.Fatal("Catalog journal IO挡住窗外下一批1000的实际SQL提交")
	}
	require.Len(t, second, 1000)
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	// 2026-10-29：WAL 不再落盘（source_wal 表已删），两批共 2000 条的「已耐久」凭据是
	// position.durable_pos 必须覆盖第二批末尾；队列条数由内存队列自身断言。
	var durable uint64
	require.NoError(t, reader.QueryRow("SELECT MAX(durable_pos) FROM position").Scan(&durable))
	require.GreaterOrEqual(t, durable, second[len(second)-1].Record.End, "两批共 2000 行必须都已由独立mode=ro SQLite实际COMMIT")
	require.Len(t, p.WAL().Snapshot(), 2000, "队列必须完整保留两批共 2000 条")
	select {
	case <-done:
		t.Fatal("journal屏障未解除前实际确认owner不得退出")
	default:
	}
	close(release)
	require.ErrorIs(t, <-done, ErrVerifyDeferred, "确认原500不能给本轮未来pending借ACK")
}

func TestDurableOutboxWrittenWindowIgnoresInterleavedOtherDay(t *testing.T) {
	fixture := &projectionVLFixture{}
	var unavailable atomic.Bool
	unavailable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	dayStart, _ := time.Parse("2006-01-02", source.UTCDay)
	previous := dayStart.Add(-24 * time.Hour).Format("2006-01-02")
	events := []logtypes.Event{sixFixEvent(source, previous, 0, 10, "前日A"), sixFixEvent(source, source.UTCDay, 10, 20, "穿插今日B"), sixFixEvent(source, previous, 20, 30, "前日C")}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	unavailable.Store(false)
	_, err = p.DeliverPendingContext(context.Background())
	require.ErrorIs(t, err, ErrVerifyDeferred, "另一日的凸包内条目不能被误报为原hint变化")
	ranges := ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key()))
	require.True(t, ledger.CoveredByPositionRanges(ranges, 0, 10))
	require.True(t, ledger.CoveredByPositionRanges(ranges, 20, 30))
	require.False(t, ledger.CoveredByPositionRanges(ranges, 10, 20))
	_, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 0, 30))
	fixture.mu.Lock()
	requests := len(fixture.writes)
	rows := 0
	for _, payload := range fixture.writes {
		rows += strings.Count(strings.TrimSpace(string(payload)), "\n") + 1
	}
	fixture.mu.Unlock()
	require.Equal(t, 2, requests)
	require.Equal(t, 3, rows, "只能A/C一批、B一批，不得重复制日")
}

func TestDurableOutboxWrittenWindowRecheckedAfterHTTPVariant(t *testing.T) {
	fixture := &projectionVLFixture{}
	var unavailable atomic.Bool
	unavailable.Store(true)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") {
			if unavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			once.Do(func() { close(entered); <-release })
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	event := sixFixEvent(source, source.UTCDay, 0, 10, "网络期间仅时间更正")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	unavailable.Store(false)
	done := make(chan error, 1)
	go func() { _, deliveryErr := p.DeliverPendingContext(context.Background()); done <- deliveryErr }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("只读确认未进入HTTP屏障")
	}
	variant := logtypes.BuildEvent(event.Source, event.Record, source.UTCDay+"T12:00:00.1Z", event.IngestTimeUTC, event.Level, event.Stream, event.Message)
	require.NoError(t, p.WAL().AppendReplay(variant))
	require.NoError(t, p.WAL().Commit())
	close(release)
	require.Error(t, <-done, "捕获后的同窗变体必须使旧确认失败，不能只在网络前核digest")
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
	entries := p.WAL().Snapshot()
	require.NotEmpty(t, entries, "新变体不能被旧ACK按End回收")
	for _, entry := range entries {
		require.Equal(t, variant.CanonicalHash, entry.Event.CanonicalHash)
	}
	rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if rec != nil {
		require.Nil(t, rec.PublishedProjection)
	}
}

func TestDurableOutboxNilAuthorityWrittenHintBindsRealPermit(t *testing.T) {
	fixture := &projectionVLFixture{}
	var unavailable atomic.Bool
	unavailable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	event := sixFixEvent(source, source.UTCDay, 0, 10, "nil权威的真实物理许可")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	_, exists := m.cat.Get(catKey)
	require.False(t, exists)
	require.NoError(t, m.cat.Put(catalog.NewStableRecord(catKey, catalog.OwnerHot, 1, "replacement-root")))
	unavailable.Store(false)
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err, "nil→另一个HOT1不能借多源首写宽容复用原written")
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
	require.NotEmpty(t, p.WAL().Snapshot())
	rec, _ := m.cat.Get(catKey)
	require.Nil(t, rec.PublishedProjection)
	fixture.mu.Lock()
	copies := len(fixture.writes)
	fixture.mu.Unlock()
	require.Equal(t, 1, copies)
}

func TestDurableOutboxUnresolvedWrittenBlocksOtherEntrypoints(t *testing.T) {
	for _, entrypoint := range []string{"rebuild", "replay"} {
		t.Run(entrypoint, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			dayStart, _ := time.Parse("2006-01-02", source.UTCDay)
			otherDay := dayStart.Add(24 * time.Hour).Format("2006-01-02")
			events := []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, "原written未决"), sixFixEvent(source, otherDay, 10, 20, "另日不能越过原hint")}
			require.NoError(t, p.WAL().Append(events...))
			require.NoError(t, p.WAL().Commit())
			require.NoError(t, m.persist())
			_, err = p.DeliverPendingContext(context.Background())
			require.Error(t, err)
			key := source.LogSourceID + "/" + source.SourceGeneration
			m.mu.Lock()
			hint := m.writtenAttempts[key]
			m.mu.Unlock()
			require.NotNil(t, hint)
			if entrypoint == "rebuild" {
				out := m.rebuildDays(context.Background(), &rebuildRequest{key: key, source: source, days: []string{otherDay}, reason: "普通重建不能覆盖原未决written"})
				require.True(t, out.Pending, "未解原hint的RO入口必须明确Pending，不得物化或假完成")
			} else {
				_, err = m.runReplayWindow(context.Background(), startupSource{key: key, source: source, events: events, snapshot: m.capturePublicationSnapshot(source)}, &replayWindow{generation: "other-generation", days: []string{otherDay}})
				require.Error(t, err)
			}
			fixture.mu.Lock()
			copies := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 1, copies, "所有实际物化入口必须挡住未解决hint，不只是normal入口")
			m.mu.Lock()
			current := m.writtenAttempts[key]
			m.mu.Unlock()
			require.Same(t, hint, current)
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
		})
	}
}

func TestDurableOutboxWrittenHeadConfirmationCannotACKFuture266(t *testing.T) {
	fixture := &projectionVLFixture{}
	var unavailable atomic.Bool
	unavailable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	events := make([]logtypes.Event, 766)
	for i := range events {
		events[i] = sixFixEvent(source, source.UTCDay, uint64(i*10), uint64((i+1)*10), fmt.Sprintf("written静默后缀%d", i))
	}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	unavailable.Store(false)
	_, err = p.DeliverPendingContext(context.Background())
	require.ErrorIs(t, err, ErrVerifyDeferred)
	entry := p.Ledger().Get(p.Key())
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), 0, 5000))
	require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), 5000, 7660), "原500只读确认不能给本次新后缀266借ACK")
	fixture.mu.Lock()
	afterConfirm := len(fixture.writes)
	fixture.mu.Unlock()
	require.Equal(t, 1, afterConfirm)
	_, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 0, 7660))
	require.Empty(t, p.WAL().Snapshot())
	fixture.mu.Lock()
	physical := len(fixture.writes)
	rows := 0
	for _, payload := range fixture.writes {
		rows += strings.Count(strings.TrimSpace(string(payload)), "\n") + 1
	}
	fixture.mu.Unlock()
	require.Equal(t, 2, physical, "只有500和266两份实际批次，不得第三次复制已确认头部")
	require.Equal(t, 766, rows)
}

func TestDurableOutboxWrittenHintRejectsAuthorityAndBodyChanges(t *testing.T) {
	for _, change := range []string{"authority", "body", "stop"} {
		t.Run(change, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var unavailable atomic.Bool
			unavailable.Store(true)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
			require.NoError(t, m.cat.Put(catalog.NewStableRecord(catKey, catalog.OwnerHot, 1, "original-root")))
			event := sixFixEvent(source, source.UTCDay, 0, 10, "实际written原正文")
			require.NoError(t, p.WAL().Append(event))
			require.NoError(t, p.WAL().Commit())
			_, err = p.DeliverPendingContext(context.Background())
			require.Error(t, err)
			key := source.LogSourceID + "/" + source.SourceGeneration
			m.mu.Lock()
			hint := m.writtenAttempts[key]
			m.mu.Unlock()
			require.NotNil(t, hint)
			switch change {
			case "authority":
				require.NoError(t, m.cat.Put(catalog.NewStableRecord(catKey, catalog.OwnerHot, 2, "replacement-root")))
			case "body":
				event = sixFixEvent(source, source.UTCDay, 0, 10, "同ID不同且更长的完整正文变体")
				require.NoError(t, p.WAL().Append(event))
				require.NoError(t, p.WAL().Commit())
			case "stop":
				require.NoError(t, m.Stop())
			}
			if change != "stop" {
				unavailable.Store(false)
				_, err = p.DeliverPendingContext(context.Background())
				require.Error(t, err, "不能复用旧hint对新权威/新正文发布或ACK")
			}
			m.mu.Lock()
			remaining := len(m.writtenAttempts)
			m.mu.Unlock()
			require.Zero(t, remaining, "成功以外的废弃和Stop也必须释放提示")
			fixture.mu.Lock()
			copies := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 1, copies)
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
			require.NotEmpty(t, p.WAL().Snapshot())
			rec, _ := m.cat.Get(catKey)
			require.Nil(t, rec.PublishedProjection)
		})
	}
}

func TestDurableOutboxPartialUnknownOnlyMeasuredMissingCanRebuild(t *testing.T) {
	fixture := &projectionVLFixture{}
	var rejectWrite, queryError atomic.Bool
	rejectWrite.Store(true)
	queryError.Store(true)
	var insertRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			insertRequests.Add(1)
			if rejectWrite.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && queryError.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	event := sixFixEvent(source, source.UTCDay, 0, 10, "HTTP503后的原窗口")
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	before := insertRequests.Load()
	require.Positive(t, before)
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	unknown := m.writtenAttempts[key]
	m.mu.Unlock()
	require.NotNil(t, unknown)
	require.False(t, unknown.measuredMissing, "5xx不能归为实测缺失")
	require.Equal(t, before, insertRequests.Load())
	queryError.Store(false)
	_, err = p.DeliverPendingContext(context.Background())
	require.Error(t, err)
	m.mu.Lock()
	measured := m.writtenAttempts[key]
	m.mu.Unlock()
	require.NotNil(t, measured)
	require.True(t, measured.measuredMissing)
	require.Equal(t, before, insertRequests.Load(), "有效完整查询得到missing后也只登记既有预算worker，不在投递入口裸重写")
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
	require.NotEmpty(t, p.WAL().Snapshot())
	rejectWrite.Store(false)
	out := m.rebuildDays(context.Background(), &rebuildRequest{key: key, source: source, days: []string{source.UTCDay}, reason: "真实written窗口实测缺失"})
	require.NoError(t, out.Err)
	require.False(t, out.Pending)
	_, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.Empty(t, p.WAL().Snapshot())
	m.mu.Lock()
	remaining := len(m.writtenAttempts)
	index := m.index
	m.mu.Unlock()
	require.Zero(t, remaining)
	rows, err := index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "修复必须消费既有持久预算，不能旁路")
}

// 走真实Manager直投新正文分支；pipeline钩子替身或本地去重204不能覆盖此反例。
func TestDurableOutboxDirectACKLossNeverBecomesSynthetic200(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	var reserved, released atomic.Int64
	require.NoError(t, p.WAL().SetByteReservation(func(bytes int64) (func(), error) { reserved.Add(bytes); return func() { released.Add(bytes) }, nil }))
	events := []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, "直投新正文0"), sixFixEvent(source, source.UTCDay, 10, 20, "直投新正文1"), sixFixEvent(source, source.UTCDay, 20, 30, "直投新正文2")}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 0, 29, ledger.GapReasonDeliverError, "真实请求丢ACK反例"))
	n, err := p.Ledger().ResolveGapsCoveredByRanges(p.Key(), []ledger.PositionRange{{From: 0, To: 29}}, acquire.ResolutionReReadOnResumeDelivery, ledger.GapReasonDeliverError)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	t.Setenv("JIANMANAGER_LOG_INJECT_ACK_LOSS", "1")
	m.directDeliverResolvedReReadGap(p, source)
	fixture.mu.Lock()
	physical := len(fixture.writes)
	fixture.mu.Unlock()
	require.Positive(t, physical, "必须真的发出新正文并完成verify/publish，不得用204去重早退冒充ACK注入")
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, logtypes.DeliveryUnknown, entry.DeliveryState)
	require.Empty(t, ledger.RequestDoneRangesOf(entry), "AckLost=true,nil仍然UNKNOWN，不能裸记200")
	for _, gap := range entry.Gaps {
		if gap.StartPos == 0 && gap.EndPos == 29 {
			require.True(t, acquire.IsReReadHealableResolution(gap.Resolution), "旧待重读缝不得升终局")
		}
	}
	m.repairStalledReclaim(p, source)
	_, _ = p.TryReclaim()
	require.Zero(t, p.Ledger().Get(p.Key()).Positions.Reclaim)
	require.Len(t, p.WAL().Snapshot(), 3)
	require.Positive(t, reserved.Load())
	require.Zero(t, released.Load(), "真实ACK及恢复责任未成立，WAL长期预留不得释放")
	t.Setenv("JIANMANAGER_LOG_INJECT_ACK_LOSS", "0")
	_, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	entry = p.Ledger().Get(p.Key())
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), 0, 30))
	require.Equal(t, uint64(30), entry.Positions.Reclaim)
	require.Empty(t, p.WAL().Snapshot())
	require.Equal(t, reserved.Load(), released.Load())
}

func TestDurableOutboxManagerPreexistingSiblingsCross500KeepCanonicalRules(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("written_unknown_%v", unknown), func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var unavailable atomic.Bool
			unavailable.Store(unknown)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client, func(opts *Options) {
				opts.RecoveryHold = func(SourceConfig, string) (bool, string) { return true, "仅hold责任不挡精确ACK" }
			})
			events := make([]logtypes.Event, 766)
			for i := range events {
				events[i] = sixFixEvent(source, source.UTCDay, uint64(i*10), uint64((i+1)*10), fmt.Sprintf("真实旧兄弟%d", i))
			}
			original := events[0]
			events[765] = logtypes.BuildEvent(original.Source, original.Record, source.UTCDay+"T12:00:00.1Z", original.IngestTimeUTC, original.Level, original.Stream, original.Message)
			require.NoError(t, p.WAL().Append(events...))
			require.NoError(t, p.WAL().Commit())
			require.NoError(t, m.persist())
			_, err = p.DeliverPendingContext(context.Background())
			require.ErrorIs(t, err, ErrVerifyDeferred, "预存兄弟先进入既有G/canonical裁定，不能以A同end发布假complete")
			if unknown {
				out := m.rebuildDays(context.Background(), &rebuildRequest{key: source.LogSourceID + "/" + source.SourceGeneration, source: source, days: []string{source.UTCDay}, reason: "真实canonical赢家G"})
				require.ErrorIs(t, out.Err, errProjectionVerifyFailed)
				unavailable.Store(false)
				m.scheduleRebuildConfirmPendingAsync(source, []string{source.UTCDay}, "G已物理写入，仅只读确认原成员")
			}
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
			m.schedulePendingReplay(source.LogSourceID + "/" + source.SourceGeneration)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				var workers sync.WaitGroup
				workers.Add(2)
				go func() { defer workers.Done(); m.RunRebuildWorker(ctx) }()
				go func() { defer workers.Done(); m.RunCapacityResumeLoop(ctx) }()
				workers.Wait()
			}()
			defer func() { cancel(); <-done }()
			defer func() {
				if t.Failed() {
					m.mu.Lock()
					hint := m.writtenAttempts[source.LogSourceID+"/"+source.SourceGeneration]
					saved := m.state.Sources[source.LogSourceID+"/"+source.SourceGeneration]
					m.mu.Unlock()
					m.replayMu.Lock()
					due := m.replayDue[source.LogSourceID+"/"+source.SourceGeneration]
					m.replayMu.Unlock()
					t.Logf("diag hint=%v pending=%v positions=%+v ranges=%v due=%v", hint != nil, saved.PublicationPending, p.Ledger().Get(p.Key()).Positions, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), due)
				}
			}()
			require.Eventually(t, func() bool {
				pending, pendingErr := p.HasPendingDelivery(context.Background())
				return pendingErr == nil && !pending
			}, 8*time.Second, 20*time.Millisecond, "静默766必须送入既有Manager裁定并由已有预算修复收敛，不能只用裸200证明acquire")
			cancel()
			<-done
			require.Len(t, p.WAL().Snapshot(), 766)
			require.Zero(t, p.Ledger().Get(p.Key()).Positions.Reclaim)
			reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
			require.NoError(t, err)
			defer reader.Close()
			// 2026-10-29：`source_wal` 已删；766 条「全部已耐久」的持久凭据是 position 水位，
			// 条数由上面的内存队列断言。
			var durable uint64
			require.NoError(t, reader.QueryRow("SELECT MAX(durable_pos) FROM position").Scan(&durable))
			require.GreaterOrEqual(t, durable, events[765].Record.End, "766 条必须全部已由独立mode=ro SQLite COMMIT")
			record, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			require.True(t, ok)
			require.True(t, record.PublishedProjection.CoverageComplete)
			fixture.mu.Lock()
			physicalRows, winnerRows := 0, 0
			for _, payload := range fixture.writes {
				for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
					physicalRows++
					if strings.Contains(line, original.EventID) {
						winnerRows++
						require.Contains(t, line, events[765].CanonicalHash)
						require.NotContains(t, line, original.CanonicalHash)
					}
				}
			}
			requests := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 765, physicalRows)
			require.Equal(t, 1, winnerRows)
			require.Equal(t, 2, requests, "只读恢复不额外insert；真实G只物化765个canonical赢家")
		})
	}
}

func TestDurableOutboxManagerTrueSiblingConflictStaysExplicitAndDurable(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	events := make([]logtypes.Event, 766)
	for i := range events {
		events[i] = sixFixEvent(source, source.UTCDay, uint64(i*10), uint64((i+1)*10), fmt.Sprintf("record-%d", i))
	}
	events[0] = sixFixEvent(source, source.UTCDay, 0, 10, "same-A")
	events[765] = sixFixEvent(source, source.UTCDay, 0, 10, "same-B")
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	_, err := p.DeliverPendingContext(context.Background())
	require.ErrorContains(t, err, "canonical身份冲突")
	require.NotErrorIs(t, err, acquire.ErrWALWindowChanged)
	require.NoError(t, m.persist())
	m.mu.Lock()
	conflicts := len(m.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].IdentityConflicts)
	m.mu.Unlock()
	require.Positive(t, conflicts)
	key := source.LogSourceID + "/" + source.SourceGeneration
	fixture.mu.Lock()
	writes := len(fixture.writes)
	fixture.mu.Unlock()
	require.Zero(t, writes)
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
	require.Len(t, p.WAL().Snapshot(), 766)
	record, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if record != nil {
		require.Nil(t, record.PublishedProjection)
	}
	root, journal := m.root, m.journal
	require.NoError(t, m.Stop())
	restartCatalog := catalog.New(journal)
	// 2026-10-29（WAL 改纯内存后的**必然结果**，不是放宽断言）：
	// 旧实现重启时会从 `source_wal.ack_body_digest` 重建 ACK，并在那一轮顺带做 canonical
	// 身份核对 ⇒ 真冲突会在**启动期**就以报错拒服。表删除后没有“重启恢复队列”这件事：
	// 重启只把 read/durable 退到 reclaim 重读，冲突自然要等到**真正读到并投递**时才暴露。
	// 不补启动期核对是有意为之（那等于把一次全段磁盘遍历塞回启动路径）；
	// 对齐 Filebeat：启动期不做身份核对。故断言改为「二启成功 + 首投抛出冲突」。
	restarted, err := newTestManager(t, Options{Root: root, Catalog: restartCatalog, Journal: journal, VL: client, Sources: []SourceConfig{source}, DeferredDelivery: true})
	require.NoError(t, err, "二启本身必须成功（重启不再做恢复期身份核对）")
	restartPipe, ok := restarted.pipeFor(source.LogSourceID + "/" + source.SourceGeneration)
	require.True(t, ok)
	// 重启后退到 reclaim 重读，同一段字节会被重读一次（夹具正文是合成的、不在文件里，
	// 故这里显式把同一份事件集合植入重启后的队列，等价于重读产物）；冲突就在首次投递暴露。
	restarted.mu.Lock()
	restartConflicts := len(restarted.state.Sources[key].IdentityConflicts)
	restarted.mu.Unlock()
	require.Positive(t, restartConflicts, "冲突隔离记录必须随索引一起持久化，二启不得丢失")
	require.NoError(t, restartPipe.WAL().Append(events...))
	require.NoError(t, restartPipe.WAL().Commit())
	_, err = restartPipe.DeliverPendingContext(context.Background())
	require.ErrorContains(t, err, "canonical身份冲突", "冲突必须由首投器重报，保留全部责任且不发布假complete")
	reader, readErr := sql.Open("sqlite", "file:"+restarted.indexPath()+"?mode=ro")
	require.NoError(t, readErr)
	defer reader.Close()
	// 2026-10-29：`source_wal` 已删；继续保留责任的持久凭据是 position 水位。
	var durable uint64
	require.NoError(t, reader.QueryRow("SELECT MAX(durable_pos) FROM position").Scan(&durable))
	require.GreaterOrEqual(t, durable, events[765].Record.End, "766 条已耐久水位必须留在库里，二启不得回退")
	record, _ = restartCatalog.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	if record != nil {
		require.Nil(t, record.PublishedProjection, "真冲突不得被发布成 complete")
	}
	require.NoError(t, restarted.Stop())
}

func TestDurableOutboxHeldHeadAndSilent766UseRealSQLiteAndVL(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	m.recoveryHold = func(SourceConfig, string) (bool, string) { return true, "持续hold反例" }
	events := make([]logtypes.Event, 766)
	for i := range events {
		events[i] = sixFixEvent(source, source.UTCDay, uint64(i*10), uint64((i+1)*10), fmt.Sprintf("静默%d", i))
	}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	m.schedulePendingReplay(source.LogSourceID + "/" + source.SourceGeneration)
	// 第一次确实ACK前500，hold只拦回收；生产续排仍必须送到后266，不能等待新输入。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.RunCapacityResumeLoop(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	require.Eventually(t, func() bool {
		return ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 0, 7660)
	}, 4*time.Second, 10*time.Millisecond)
	fixture.mu.Lock()
	physical := 0
	for _, payload := range fixture.writes {
		physical += len(strings.Split(strings.TrimSpace(string(payload)), "\n"))
	}
	fixture.mu.Unlock()
	require.Equal(t, 766, physical, "各正文必须实际写入且不重复")
	require.Zero(t, p.Ledger().Get(p.Key()).Positions.Reclaim)
	require.Len(t, p.WAL().Snapshot(), 766)
	require.NotEmpty(t, p.Ledger().Get(p.Key()).RecoveryRefs)
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	var durable uint64
	require.NoError(t, reader.QueryRow("SELECT MAX(durable_pos) FROM position").Scan(&durable))
	require.Equal(t, uint64(7660), durable)
}

func TestDurableOutboxSQLiteFailureCannotDispatch(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	require.NoError(t, m.index.Close())
	appendOutboxLines(t, source.Path, 0, 1)
	_, err := p.Drain()
	require.Error(t, err)
	fixture.mu.Lock()
	writes := len(fixture.writes)
	fixture.mu.Unlock()
	require.Zero(t, writes)
	require.Empty(t, p.Ledger().Get(p.Key()).RequestDoneRanges)
	require.NotEmpty(t, p.WAL().Snapshot())
	m.mu.Lock()
	m.index = nil
	m.mu.Unlock()
	m.persistPlanMu.Lock()
	m.persistPlan = nil
	m.persistPlanMu.Unlock()
	require.NoError(t, m.persist(), "失败后的真实SQLite提交必须可恢复，不能继承资格true")
}

func TestDurableOutboxIncompleteActualPlanCannotIssueReceipt(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client, func(opts *Options) { opts.IndexCommit = &stateindex.CommitBudget{CycleMaxRows: 16} })
	events := make([]logtypes.Event, 1000)
	for i := range events {
		events[i] = sixFixEvent(source, source.UTCDay, uint64(i*10), uint64((i+1)*10), fmt.Sprintf("不完整计划%d", i))
	}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	plan, err := m.planPersist(context.Background())
	require.NoError(t, err)
	incomplete, err := m.commitPersist(context.Background(), plan)
	require.NoError(t, err)
	require.True(t, incomplete)
	id, err := acquire.CommitIdentityOf(events[0])
	require.NoError(t, err)
	require.False(t, m.committedContains(source.LogSourceID+"/"+source.SourceGeneration, p, []acquire.CommitIdentity{id}))
	_, err = p.DeliverPendingContext(context.Background())
	require.ErrorIs(t, err, ErrPersistDeferred)
	fixture.mu.Lock()
	writes := len(fixture.writes)
	fixture.mu.Unlock()
	require.Zero(t, writes)
	require.Empty(t, p.Ledger().Get(p.Key()).RequestDoneRanges)
	require.NoError(t, m.persist())
}

// 2026-10-29：原 `TestDurableOutboxActualPlanSQLBodyUsesSamePrivateEncoding` 钉的是「写进
// `source_wal` 的 body 字节必须逐字等于本计划捕获的原完整编码」。表已删，同一语义改钉在
// 提交回执上：回执里的 `CommitIdentity.BodyDigest` 必须来自**本计划捕获的那份完整正文**
// （含同计划私有 Fields），不能被构建后的同 ID 新变体顶替。
func TestDurableOutboxActualPlanReceiptKeepsPlanPrivateFields(t *testing.T) {
	m, source, p := outboxTestManager(t, nil)
	key := source.LogSourceID + "/" + source.SourceGeneration
	events := []logtypes.Event{
		sixFixEvent(source, source.UTCDay, 0, 10, "同一计划原字节0"),
		sixFixEvent(source, source.UTCDay, 10, 20, "同一计划原字节1"),
	}
	for i := range events {
		events[i].Fields = map[string]string{"计划字段": fmt.Sprintf("原值%d", i), "保留": "全字段指纹"}
	}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	identities := make([]acquire.CommitIdentity, 0, len(events))
	for _, event := range events {
		identity, err := acquire.CommitIdentityOf(event)
		require.NoError(t, err)
		identities = append(identities, identity)
	}
	plan, err := m.planPersist(context.Background())
	require.NoError(t, err)
	require.NotNil(t, plan)
	// 计划未Apply前不发布任何回执。
	require.False(t, m.committedContains(key, p, identities), "计划未提交前不得发布回执")
	_, err = m.commitPersist(context.Background(), plan)
	require.NoError(t, err)
	require.True(t, m.committedContains(key, p, identities), "完整身份（含同计划私有Fields）必须全部落在回执里")
	// 同一 EventID 但字段不同的变体不得被“同计数”顶替。
	for i, event := range events {
		changed := event
		changed.Fields = map[string]string{"计划字段": fmt.Sprintf("改后%d", i), "保留": "全字段指纹"}
		changedIdentity, err := acquire.CommitIdentityOf(changed)
		require.NoError(t, err)
		require.Equal(t, event.EventID, changedIdentity.EventID)
		require.NotEqual(t, identities[i].BodyDigest, changedIdentity.BodyDigest)
		require.False(t, m.committedContains(key, p, []acquire.CommitIdentity{changedIdentity}), "回执不得用同ID不同正文顶替")
	}
}

func TestDurableOutboxBackgroundPlanReuseStillRequiresNewerJoin(t *testing.T) {
	m, source, p := outboxTestManager(t, nil)
	key := source.LogSourceID + "/" + source.SourceGeneration
	first := sixFixEvent(source, source.UTCDay, 0, 10, "旧计划正文")
	require.NoError(t, p.WAL().Append(first))
	require.NoError(t, p.WAL().Commit())
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.rebuildSourceBuildHook.Store(func() { once.Do(func() { close(blocked); <-release }) })
	done := make(chan error, 1)
	go func() { done <- m.persistBackground() }()
	<-blocked
	second := sixFixEvent(source, source.UTCDay, 10, 20, "更晚的新正文")
	second.Fields = map[string]string{"追加": "必须进入新计划"}
	require.NoError(t, p.WAL().Append(second))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 20, 20, ledger.GapReasonDeliverError, "迫使账本修订变化"))
	close(release)
	require.NoError(t, <-done)
	m.rebuildSourceBuildHook.Store((func())(nil))
	require.NoError(t, m.persistBackground())
	// 2026-10-29：`source_wal` 已删。原来用「source_wal 行数==2」证明两条正文都进了计划；
	// 现在用两条独立事实代替：position.durable_pos 必须覆盖更晚 join 的第二条（计划没跳过它），
	// 且队列里两条正文都还在（没丢正文）。
	rows, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer rows.Close()
	require.Eventually(t, func() bool {
		var durable uint64
		if err := rows.QueryRow(`SELECT durable_pos FROM position WHERE key=?`, key).Scan(&durable); err != nil {
			return false
		}
		return durable >= second.Record.End
	}, 2*time.Second, 5*time.Millisecond, "后台旧计划不能借旧coverage跳过更晚join，也不能丢新正文")
	require.Len(t, p.WAL().Snapshot(), 2, "两条正文必须都还在队列里，不能丢新正文")
}

func TestDurableOutboxPlanBodyNotBorrowedFromLaterSameIDVariant(t *testing.T) {
	m, source, p := outboxTestManager(t, nil)
	key := source.LogSourceID + "/" + source.SourceGeneration
	event := sixFixEvent(source, source.UTCDay, 0, 10, "计划私有正文")
	event.Fields = map[string]string{"计划": "捕获值"}
	require.NoError(t, p.WAL().Append(event))
	require.NoError(t, p.WAL().Commit())
	planIdentity, err := acquire.CommitIdentityOf(event)
	require.NoError(t, err)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.rebuildSourceBuildHook.Store(func() { once.Do(func() { close(blocked); <-release }) })
	done := make(chan error, 1)
	go func() {
		plan, err := m.planPersist(context.Background())
		if err != nil {
			done <- err
			return
		}
		_, err = m.commitPersist(context.Background(), plan)
		done <- err
	}()
	<-blocked
	changed := event
	changed.Fields = map[string]string{"计划": "构建后同ID新变体"}
	// 时间必须真的变化：同哈希副本按既有去重规则会被丢弃，构造不出同ID变体。
	changed.EventTimeUTC = source.UTCDay + "T12:01:00Z"
	changed.CanonicalHash = logtypes.CanonicalContentHash(changed.EventTimeUTC, changed.Level, changed.Stream, changed.Message)
	require.NotEqual(t, event.CanonicalHash, changed.CanonicalHash)
	require.NoError(t, p.WAL().AppendReplay(changed))
	require.NoError(t, p.WAL().Commit())
	close(release)
	require.NoError(t, <-done)
	m.rebuildSourceBuildHook.Store((func())(nil))
	// 2026-10-29：`source_wal.body` 已删。原断言「SQL 正文必须来自本计划捕获的原字节」
	// 已无载体；它保护的那件事改钉在回执上——**回执只能授予 WAL 里仍然存在的完整正文身份**：
	// 同 ID 旧正文被变体原位替换后，旧身份必须立即失效（否则位置区间复用 / 同 ID 异体
	// 就能借旧回执拿到外发资格）。
	variantIdentity, err := acquire.CommitIdentityOf(changed)
	require.NoError(t, err)
	require.Equal(t, planIdentity.EventID, variantIdentity.EventID)
	require.NotEqual(t, planIdentity.BodyDigest, variantIdentity.BodyDigest)
	require.True(t, m.committedContains(key, p, []acquire.CommitIdentity{variantIdentity}),
		"回执必须覆盖 WAL 里当前那份正文（变体已 durable）")
	require.False(t, m.committedContains(key, p, []acquire.CommitIdentity{planIdentity}),
		"已被变体替换的旧正文身份不得继续有效（不得借旧回执外发）")
}

// 2026-10-29：这一整块原本钉的是「WAL 行 SQL 编码」的实现细节——
//   - `outboxDigestCounts`（观测 WALCommitSnapshot 的 Marshal/SHA 次数）；
//   - `TestDurableOutboxDigestReuse*` / `...ReceiptIndex*` / `...CompletedReceiptRetainsLightIndex*`
//     （`WALCommitSnapshot` / `WALCommitIndex` 的摘要复用与轻量索引）；
//   - `TestDurableOutboxLiveIdentities*`（复用计划已算身份以减少重复编码）。
//
// 两个“SQL body 字节同源”用例没有随表消失，只把载体从 `source_wal.body` 换成提交回执里的
// `CommitIdentity.BodyDigest`（断言原封不动）：
// `TestDurableOutboxActualPlanReceiptKeepsPlanPrivateFields`（原 `...ActualPlanSQLBodyUsesSamePrivateEncoding`）
// 与 `TestDurableOutboxPlanBodyNotBorrowedFromLaterSameIDVariant`。
//
// 这些机制的唯一目的是「把 WAL 行写进 source_wal 时少编码正文」。`source_wal` 表删除后，
// 它们连同 `WALCommitSnapshot`/`WALCommitIndex` 一起下线（见 acquire/wal.go 的注释）。
//
// 保留下来的语义由别的用例覆盖（都与"写 SQL"无关）：
//   - 快照私有性 / 锁外编码：`TestDurableOutboxWALSnapshotOwnsMutableFields` +
//     acquire 包的 `TestDurableOutboxPrivateIdentityEncodingOwnsBodiesAndReleasesLock`；
//   - 提交资格只认完整身份、不认位置区间：`TestDurableOutboxActualPlanReceiptDoesNotBorrowReplacedVariant`；
//   - prune 只能与同一份旧回执做精确交集：`TestDurableOutboxPruneReceiptIndexCounts*` 已随之删除，
//     其核心断言（不得凭 live 集合新建资格）由 `TestDurableOutboxRealPersistHandoffNeverAuthorizesDelivery` 承担。

// 尾部只唤醒既有消费者：deferredDelivery 下即使后台计划被真实阻塞，尾部也立即返回。
func TestDurableOutboxPollTailDoesNotBlockOnBackgroundPlan(t *testing.T) {
	m, _, _ := outboxTestManager(t, nil) // DeferredDelivery=true：生产形态
	require.True(t, m.deferredDelivery)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.rebuildSourceBuildHook.Store(func() { once.Do(func() { close(blocked); <-release }) })
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); _ = m.persistBackground() }()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		close(release)
		<-workerDone
		t.Fatal("后台计划未进入阻塞点（夹具前提不成立）")
	}
	tailDone := make(chan struct{})
	go func() { defer close(tailDone); m.persistPollTail() }()
	select {
	case <-tailDone:
	case <-time.After(2 * time.Second):
		close(release)
		<-workerDone
		<-tailDone
		t.Fatal("后台计划阻塞时采集尾部仍被同步拦住")
	}
	close(release)
	<-workerDone
	m.rebuildSourceBuildHook.Store((func())(nil))
}

// 默认（非deferredDelivery）尾部仍是同步落库：返回时本批已真实落盘。
func TestDurableOutboxPollTailDefaultModeStaysSynchronous(t *testing.T) {
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client, func(opts *Options) { opts.DeferredDelivery = false })
	require.False(t, m.deferredDelivery)
	appendOutboxLines(t, source.Path, 0, 1)
	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 1)
	m.persistPollTail()
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	var durable uint64
	require.NoError(t, reader.QueryRow(`SELECT durable_pos FROM position WHERE key=?`, source.LogSourceID+"/"+source.SourceGeneration).Scan(&durable))
	require.GreaterOrEqual(t, durable, events[0].Record.End, "默认模式尾部返回时必须已真实落库")
}

// 严格新批前置不动：新事件的实际SQL durable必须在对外发送之前成立。
func TestDurableOutboxStrictBatchDurableBeforeDeliveryStillHeld(t *testing.T) {
	fixture := &projectionVLFixture{}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client)
	require.True(t, m.deferredDelivery)
	appendOutboxLines(t, source.Path, 0, 1)
	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 1)
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	var durable uint64
	require.NoError(t, reader.QueryRow(`SELECT durable_pos FROM position WHERE key=?`, source.LogSourceID+"/"+source.SourceGeneration).Scan(&durable))
	require.GreaterOrEqual(t, durable, events[0].Record.End, "外发前新批必须已真实COMMIT")
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), "前置：尚未登记任何投递完成区间")
}

func TestDurableOutboxPublishedTailWithoutVerifiedRunsStallsReclaim(t *testing.T) {
	client, fixture := newProjectionVL(t)
	// 只读证据路由：需要时指向不可达端点，让"投递后可见性复查"真实失败；投递/校验仍用健康客户端。
	unreachable, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	var blockVisible atomic.Bool
	m, source, p := outboxTestManager(t, client, func(opts *Options) {
		opts.MaxReplayEventsPerDrain = 500
	})
	m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) {
		if blockVisible.Load() {
			return unreachable, nil
		}
		return client, nil
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	deliver := func() bool {
		t.Helper()
		delivered, err := p.DeliverPendingContext(context.Background())
		require.NoError(t, err)
		return delivered
	}
	// 准备阶段：用既有生产 setter 放开自愈限频（默认10s），让真实 Poll+排空按有限步推进到目标水位；
	// 断言前恢复原值，生产默认/周期/预算均不变。
	m.mu.Lock()
	prevInterval := m.selfHealInterval
	m.mu.Unlock()
	m.SetSelfHealInterval(time.Nanosecond)
	defer func() { m.SetSelfHealInterval(prevInterval) }()
	// Durable 在 commit/persist 时一次性推进（与投递解耦），真正分段的凭据是 verifiedRuns 区间；
	// 故准备阶段按 verified 最高末端推进，断言阶段才看 Reclaim/RecoveryRefs。
	verifiedThrough := func() uint64 {
		var hi uint64
		for _, run := range m.verifiedRuns(key) {
			if run.To > hi {
				hi = run.To
			}
		}
		return hi
	}
	delivery := func() uint64 { return p.Ledger().Get(p.Key()).Positions.Delivery }
	advanceTo := func(target uint64) bool {
		t.Helper()
		for i := 0; i < 12; i++ {
			if delivery() >= target {
				return true
			}
			before := delivery()
			deliver()
			if delivery() <= before {
				return false
			}
		}
		return delivery() >= target
	}
	// 水位一律取自真实 Event.Record.End，不硬码行长：段界=第667行末尾，末尾=第1067行末尾。
	var cutBytes, totalBytes uint64
	appendOutboxLines(t, source.Path, 0, 167)
	warm, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, warm, 167)
	require.True(t, deliver())
	appendOutboxLines(t, source.Path, 167, 900)
	side, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, side, 900)
	cutBytes = side[500-1].Record.End   // 第667行（167+500）的真实末端
	totalBytes = side[900-1].Record.End // 第1067行的真实末端
	// 真实投递到旧段界 667 行（单次排空有上限+退避，故按既有入口循环推进，不改任何预算）。
	require.True(t, advanceTo(cutBytes), "准备阶段未能真实推进到段界")
	require.Equal(t, uint64(totalBytes), p.Ledger().Get(p.Key()).Positions.Durable, "前置：Durable在commit时已全量")
	beforeTail, _ := p.Ledger().Positions(p.Key())
	// 尾400条：真实验证+ACK+发布成功，但可见性复查失败 ⇒ 不登记区间凭据。
	blockVisible.Store(true)
	require.True(t, advanceTo(totalBytes), "准备阶段未能真实推进到末尾")
	blockVisible.Store(false)
	// 被测断言前恢复生产限频原值。
	m.SetSelfHealInterval(prevInterval)
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, uint64(totalBytes), entry.Positions.Durable)
	require.Equal(t, uint64(totalBytes), entry.Positions.Delivery, "尾部必须真实ACK")
	require.Equal(t, cutBytes, verifiedThrough(), "前置：区间凭据只到旧段界")
	require.Empty(t, entry.RecoveryRefs, "前置：没有任何恢复段")
	rows := 0
	for _, item := range p.WAL().Snapshot() {
		if item.Durable && item.Event.Record.End > entry.Positions.Reclaim {
			rows++
		}
	}
	require.Equal(t, 400, rows, "前置：400条已durable未回收责任")
	fixture.mu.Lock()
	published := len(fixture.writes)
	fixture.mu.Unlock()
	require.Positive(t, published)
	// 前置硬事实（不改 state）：当前 source/day 的 PublishedProjection 必须完整且 closed>=total，
	// 且 target/authority 与本次证明一致 —— HTTP 200 本身不构成证据。
	rec, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.True(t, ok && rec != nil, "前置：当前 PP 必须存在")
	require.NotNil(t, rec.PublishedProjection, "前置：必须已发布投影")
	require.True(t, rec.PublishedProjection.CoverageComplete, "前置：PP 必须完整")
	require.GreaterOrEqual(t, publishedEvidenceClosed(rec, source), totalBytes, "前置：PP closed 必须覆盖到末尾")
	// 独立 RO oracle：对真实尾 400 成员做一次不减 state 的 capture，确认物理全字段可见（非仅 200）。
	tailEvents := make([]logtypes.Event, 0, 400)
	for _, item := range p.WAL().Snapshot() {
		if item.Durable && item.Event.Record.Start >= cutBytes && item.Event.Record.End <= totalBytes {
			tailEvents = append(tailEvents, item.Event)
		}
	}
	require.Len(t, tailEvents, 400, "前置：真实尾批必须恰好 400 条")
	oracle := m.capturePublishedEvidence(source, tailEvents)
	require.Len(t, oracle.events, 400, "前置：尾 400 条必须物理全字段可见（独立 RO oracle）")
	// 无新输入：只走既有回收/补段面。
	_, err = p.TryReclaim()
	require.ErrorIs(t, err, ledger.ErrReclaimNoSegment)
	require.NoError(t, m.repairStalledReclaim(p, source))
	after, err := p.TryReclaim()
	require.NoError(t, err, "已ACK+已发布的尾部必须能取得当前副本证明并推进回收")
	require.Equal(t, uint64(totalBytes), after)
	require.Greater(t, after, beforeTail.Reclaim)
	require.NoError(t, m.persist())
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	var durable, reclaim uint64
	require.NoError(t, reader.QueryRow(`SELECT durable_pos,reclaim_pos FROM position WHERE key=?`, key).Scan(&durable, &reclaim))
	require.Equal(t, uint64(totalBytes), reclaim, "Stop前SQL必须已追平，不能靠关停落库")
	// 2026-10-29：`source_wal` 已删，「已回收且队列空」由上方内存队列断言与 reclaim 水位共同证明。
	require.GreaterOrEqual(t, durable, reclaim, "durable 不得落在 reclaim 后面")
}

// 真实尾批准备只关闭额外只读路由，投递、逐字段校验、ACK与发布仍走健康HTTP。
func prepareOutboxPublishedReclaimDebt(t *testing.T, m *Manager, source SourceConfig, p *pipeline.Pipeline, client *vlsup.Client, count int) []logtypes.Event {
	t.Helper()
	appendOutboxLines(t, source.Path, 0, 1)
	warm, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, warm, 1)
	_, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	unreachable, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	if count <= 500 {
		m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) { return unreachable, nil }
	} else {
		// 大窗口只测有界回补：先保留真实ACK正文，再保守撤去内存证据模拟恢复缺采；绝不补造ACK或发布事实。
		m.recoveryHold = func(SourceConfig, string) (bool, string) { return true, "准备真实ACK大窗口" }
	}
	appendOutboxLines(t, source.Path, 1, count)
	tail, err := p.Drain()
	if errors.Is(err, ErrPersistDeferred) {
		// race下允许原热提交面按原预算交办；随后主动等待原持久化消费者，不能把交办当COMMIT。
		require.NoError(t, m.persist())
		err = nil
	}
	require.NoError(t, err)
	require.Len(t, tail, count)
	for n := 0; n < 12 && p.Ledger().Get(p.Key()).Positions.Delivery < tail[count-1].Record.End; n++ {
		before := p.Ledger().Get(p.Key()).Positions.Delivery
		_, err = p.DeliverPendingContext(context.Background())
		if err != nil && !errors.Is(err, ErrVerifyDeferred) && !strings.Contains(err.Error(), "retained by hold") {
			require.NoError(t, err)
		}
		require.GreaterOrEqual(t, p.Ledger().Get(p.Key()).Positions.Delivery, before)
	}
	if count > 500 {
		m.recoveryHold = nil
		entry := p.Ledger().Get(p.Key())
		entry.RecoveryRefs = nil
		require.NoError(t, p.Ledger().Restore([]ledger.Entry{*entry}))
		m.mu.Lock()
		key := source.LogSourceID + "/" + source.SourceGeneration
		saved := m.state.Sources[key]
		saved.VerifiedRuns = []ledger.PositionRange{{From: 0, To: warm[0].Record.End}}
		m.state.Sources[key] = saved
		m.mu.Unlock()
	}
	m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) { return client, nil }
	entry := p.Ledger().Get(p.Key())
	require.Equal(t, tail[count-1].Record.End, entry.Positions.Delivery)
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), tail[0].Record.Start, tail[count-1].Record.End))
	require.False(t, ledger.CoveredByPositionRanges(m.verifiedRuns(source.LogSourceID+"/"+source.SourceGeneration), tail[0].Record.Start, tail[count-1].Record.End))
	require.Len(t, p.WAL().Snapshot(), count)
	rec, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.True(t, ok)
	require.True(t, rec.PublishedProjection.CoverageComplete)
	require.GreaterOrEqual(t, publishedEvidenceClosed(rec, source), tail[count-1].Record.End)
	return tail
}

type outboxReclaimProofTransport struct {
	next    http.RoundTripper
	id      string
	entered chan struct{}
	release <-chan struct{}
	once    *sync.Once
}

func (tr outboxReclaimProofTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := tr.next.RoundTrip(r)
	if response != nil && strings.Contains(r.URL.Query().Get("query"), projectionVerifyFields) && strings.Contains(r.URL.Query().Get("query"), tr.id) {
		tr.once.Do(func() { close(tr.entered); <-tr.release })
	}
	return response, err
}

func TestDurableOutboxPublishedReclaimBoundedContinuousPrefixes(t *testing.T) {
	for _, count := range []int{900, 1001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			client, fixture := newProjectionVL(t)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, count)
			fixture.mu.Lock()
			writes := len(fixture.writes)
			fixture.mu.Unlock()
			require.NoError(t, m.repairStalledReclaim(p, source))
			first := p.Ledger().Get(p.Key()).Positions.Reclaim
			require.Equal(t, tail[499].Record.End, first, "首轮只证明原500成员，不要求一次覆盖整个Delivery")
			for step := 0; step < 3 && p.Ledger().Get(p.Key()).Positions.Reclaim < tail[count-1].Record.End; step++ {
				m.rollingReclaimRepair(p, source)
			}
			require.Equal(t, tail[count-1].Record.End, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.Empty(t, p.WAL().Snapshot())
			require.NoError(t, m.persist())
			reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
			require.NoError(t, err)
			defer reader.Close()
			var durable, reclaim uint64
			key := source.LogSourceID + "/" + source.SourceGeneration
			require.NoError(t, reader.QueryRow(`SELECT durable_pos,reclaim_pos FROM position WHERE key=?`, key).Scan(&durable, &reclaim))
			require.Equal(t, tail[count-1].Record.End, reclaim, "独立只读SQL在Stop前已经追平")
			// 2026-10-29：`source_wal` 已删；队列空由上方 `p.WAL().Snapshot()` 断言证明。
			require.GreaterOrEqual(t, durable, reclaim, "durable 不得落在 reclaim 后面")
			fixture.mu.Lock()
			require.Equal(t, writes, len(fixture.writes), "补证不能增加VL写入或复制正文")
			fixture.mu.Unlock()
		})
	}
}

func TestDurableOutboxPublishedReclaimOriginalCutBeforeRegistration(t *testing.T) {
	for _, change := range []string{"fields", "hash", "restore", "future1000"} {
		t.Run(change, func(t *testing.T) {
			client, _ := newProjectionVL(t)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 3)
			key := source.LogSourceID + "/" + source.SourceGeneration
			before := m.verifiedRuns(key)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer onceCloseChannel(release)
			blocked, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: client.BaseURL(), HTTPClient: &http.Client{Transport: outboxReclaimProofTransport{next: http.DefaultTransport, id: tail[0].EventID, entered: entered, release: release, once: &once}}})
			require.NoError(t, err)
			m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) { return blocked, nil }
			done := make(chan error, 1)
			go func() { done <- m.repairStalledReclaim(p, source) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("未进入实际尾部RO响应交错点")
			}
			if change == "future1000" {
				appendOutboxLines(t, source.Path, 4, 1000)
				next, drainErr := p.Drain()
				if errors.Is(drainErr, ErrPersistDeferred) {
					require.NoError(t, m.persist())
					drainErr = nil
				}
				require.NoError(t, drainErr, "网络期间不得持WAL或publishMu阻止新尾真实COMMIT")
				require.Len(t, next, 1000)
				reader, openErr := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
				require.NoError(t, openErr)
				var durable uint64
				require.NoError(t, reader.QueryRow(`SELECT durable_pos FROM position WHERE key=?`, key).Scan(&durable))
				require.NoError(t, reader.Close())
				require.Equal(t, next[999].Record.End, durable)
			} else {
				variant := tail[0]
				variant.Fields = map[string]string{"交错": "原RO没有证明的正文"}
				if change == "hash" {
					variant.Message += "变体"
					variant.CanonicalHash = logtypes.CanonicalContentHash(variant.EventTimeUTC, variant.Level, variant.Stream, variant.Message)
				}
				if change == "restore" {
					// 2026-10-29：原用 WAL.Restore 整体替换正文；不落盘后等价的生产做法是
					// 「回放原位替换」（AppendReplay）——同 ID/同区间的正文被改写。
					require.NoError(t, p.WAL().AppendReplay(variant))
					require.NoError(t, p.WAL().Commit())
				} else {
					require.NoError(t, p.WAL().Append(variant))
					require.NoError(t, p.WAL().Commit())
				}
			}
			onceCloseChannel(release)
			<-done
			if change == "future1000" {
				if p.Ledger().Get(p.Key()).Positions.Reclaim != tail[2].Record.End {
					// race下原2s只读ctx可先到期：该旧证明必须完全保债，下一次仍只证明旧ACK前缀。
					require.Equal(t, before, m.verifiedRuns(key))
					require.NoError(t, m.repairStalledReclaim(p, source))
				}
				require.True(t, ledger.CoveredByPositionRanges(m.verifiedRuns(key), tail[0].Record.Start, tail[2].Record.End))
				require.Equal(t, tail[2].Record.End, p.Ledger().Get(p.Key()).Positions.Reclaim)
				require.Len(t, p.WAL().Snapshot(), 1000, "新尾只真实提交，不能借旧证明获ACK或回收")
			} else {
				require.Equal(t, before, m.verifiedRuns(key), "必须先验证原cut与完整正文再登记，不能先污染Verified再拒回收")
				require.Equal(t, tail[0].Record.Start, p.Ledger().Get(p.Key()).Positions.Reclaim)
				require.NotEmpty(t, p.WAL().Snapshot())
			}
		})
	}
}

func onceCloseChannel(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestDurableOutboxPublishedReclaimRuleChangeRejectsOldPipe(t *testing.T) {
	client, _ := newProjectionVL(t)
	m, _, _ := outboxTestManager(t, client)
	work := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0700))
	for _, name := range []string{"first.log", "second.log"} {
		require.NoError(t, os.WriteFile(filepath.Join(work, "logs", name), nil, 0600))
	}
	reg := InstanceRegistration{UUID: "c1-owner", TargetID: "inst:991", Generation: "g1", Mode: "FILE_PRIMARY", WorkDir: work, RelativePath: "logs/first.log"}
	require.NoError(t, m.RegisterInstance(reg))
	key := "inst:991/file/g1"
	m.mu.Lock()
	source, old := m.sources[key], m.pipes[key]
	m.mu.Unlock()
	require.NotNil(t, old)
	tail := prepareOutboxPublishedReclaimDebt(t, m, source, old, client, 3)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer onceCloseChannel(release)
	blocked, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: client.BaseURL(), HTTPClient: &http.Client{Transport: outboxReclaimProofTransport{next: http.DefaultTransport, id: tail[0].EventID, entered: entered, release: release, once: &once}}})
	require.NoError(t, err)
	m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) { return blocked, nil }
	done := make(chan error, 1)
	go func() { done <- m.repairStalledReclaim(old, source) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("未进入原pipe尾部真实RO响应交错点")
	}
	reg.RelativePath = "logs/second.log"
	require.NoError(t, m.RegisterInstance(reg), "全批真实ACK后正常规则升级必须能摘旧建新，不能靠持锁封死反例")
	current, ok := m.pipeFor(key)
	require.True(t, ok)
	require.NotSame(t, old, current)
	appendOutboxLines(t, filepath.Join(work, "logs", "second.log"), 10000, 1)
	_, err = current.Drain()
	require.NoError(t, err)
	require.NoError(t, m.persist(), "新pipe正常持久化后，同key状态已真实存在")
	m.mu.Lock()
	_, stateExists := m.state.Sources[key]
	m.mu.Unlock()
	require.True(t, stateExists)
	require.Empty(t, m.verifiedRuns(key))
	onceCloseChannel(release)
	<-done
	require.Empty(t, m.verifiedRuns(key), "旧pipe的网络结果不能污染同key重建的新source状态")
	require.Zero(t, current.Ledger().Get(current.Key()).Positions.Reclaim)
}

func TestDurableOutboxPublishedReclaimUsesOneProofAndAttemptDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			client, fixture := newProjectionVL(t)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 400)
			fixture.mu.Lock()
			fixture.queryWindows = nil
			fixture.mu.Unlock()
			ctx, release, err := m.acquireSourceDelivery(context.Background(), source)
			require.NoError(t, err)
			attempt := ctx.Value(verifyAttemptKey{}).(*verifyAttemptBudget)
			if expired {
				attempt.state.deadlineNanos.Store(time.Now().Add(-time.Second).UnixNano())
				attempt.state.progressed.Store(1)
			}
			defer release()
			// 同一owner仍有效，原attempt截止也必须被继承，不能换lifecycle绕过。
			_ = m.repairStalledReclaim(p, source, ctx)
			fixture.mu.Lock()
			queries := append([]queryWindowRecord(nil), fixture.queryWindows...)
			fixture.mu.Unlock()
			if expired {
				require.Empty(t, queries, "已经到期的owner绝对ctx不得被恢复面换成lifecycle继续查询")
				require.False(t, ledger.CoveredByPositionRanges(m.verifiedRuns(source.LogSourceID+"/"+source.SourceGeneration), tail[0].Record.Start, tail[399].Record.End))
				return
			}
			for _, query := range queries {
				require.LessOrEqual(t, len(query.Query), scopedProjectionQueryMaxBytes)
				if strings.Contains(query.Query, projectionVerifyFields) {
					require.NotContains(t, query.Query, "outbox-000000", "不得查询注定不够的旧已回收前缀")
				}
			}
			require.LessOrEqual(t, len(queries), 4, "同次尾部proof应贯穿登记与原闸，不能反复oracle并重开查询预算")
		})
	}
}

type outboxReclaimArchiveBarrier struct {
	next    ledger.RecoveryArchiveStore
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (store *outboxReclaimArchiveBarrier) ArchiveRecovery(ctx context.Context, records []ledger.RecoveryArchiveRecord) ([]ledger.RecoveryArchiveReceipt, error) {
	return store.next.ArchiveRecovery(ctx, records)
}

func (store *outboxReclaimArchiveBarrier) LookupRecovery(ctx context.Context, key ledger.SourceKey, segmentID string) (ledger.RecoveryArchiveRecord, ledger.RecoveryArchiveReceipt, bool, error) {
	record, receipt, exists, err := store.next.LookupRecovery(ctx, key, segmentID)
	store.once.Do(func() { close(store.entered); <-store.release })
	return record, receipt, exists, err
}

func TestDurableOutboxPublishedReclaimArchiveLookupOutsideStateLocksAndPreparedRetry(t *testing.T) {
	client, _ := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 3)
	key := source.LogSourceID + "/" + source.SourceGeneration
	before := m.verifiedRuns(key)
	var held atomic.Bool
	m.recoveryHold = func(SourceConfig, string) (bool, string) { return held.Load(), "归档身份查询后新增hold" }
	entered, release := make(chan struct{}), make(chan struct{})
	defer onceCloseChannel(release)
	p.Ledger().SetRecoveryArchiveStore(&outboxReclaimArchiveBarrier{next: m, entered: entered, release: release})
	done := make(chan error, 1)
	go func() { done <- m.repairStalledReclaim(p, source) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("没有完成实际SQLite归档lookup，可能在外锁内自等待")
	}
	stateFree := make(chan bool, 1)
	go func() {
		m.publishMu.Lock()
		m.mu.Lock()
		sameOwner := m.pipes[key] == p
		m.mu.Unlock()
		m.publishMu.Unlock()
		p.WAL().Backlog()
		stateFree <- sameOwner
	}()
	select {
	case sameOwner := <-stateFree:
		require.True(t, sameOwner, "锁外归档查询期间读取原owner仍须一致，不能只取得空临界区")
	case <-time.After(time.Second):
		onceCloseChannel(release)
		t.Fatal("归档lookup期间不得持publishMu/m.mu/WAL.mu")
	}
	held.Store(true)
	onceCloseChannel(release)
	require.NoError(t, <-done)
	require.Equal(t, before, m.verifiedRuns(key))
	segID := fmt.Sprintf("%s%d-%d", deliveryConfirmedSegPrefix, tail[0].Record.Start, tail[2].Record.End)
	ref, exists := p.RecoveryRef(segID)
	require.True(t, exists)
	require.Equal(t, logtypes.RecoveryStaged, ref.State, "锁外准备态不可直接授予回收责任")
	held.Store(false)
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	all, err := m.canonicalRecoveryEvents(key, saved)
	require.NoError(t, err)
	result, err := m.writeProjection(source, all, "projection-prepared-g2", all, nil, true)
	require.NoError(t, err, "准备态之后通过真实写入、逐字段校验和发布合法推进到新投影代次")
	require.Equal(t, http.StatusOK, result.HTTPStatus)
	m.mu.Lock()
	currentGeneration := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()
	require.Equal(t, "projection-prepared-g2", currentGeneration)
	stillPrepared, exists := p.RecoveryRef(segID)
	require.True(t, exists)
	require.Equal(t, logtypes.RecoveryStaged, stillPrepared.State)
	require.Equal(t, ref.Path, stillPrepared.Path, "已查归档的准备身份不得随新代次改写")
	require.NoError(t, m.repairStalledReclaim(p, source), "准备态必须用当前新代次真实RO重试，不能被旧Path永久卡住")
	require.Equal(t, tail[2].Record.End, p.Ledger().Get(p.Key()).Positions.Reclaim)
	require.Empty(t, p.WAL().Snapshot())
	transferred, exists := p.RecoveryRef(segID)
	require.True(t, exists)
	require.Equal(t, "projection:"+currentGeneration, transferred.ResponsibilityReceiver, "接收者来自本次新RO，不把旧准备代次假装成新证据")
}

func TestDurableOutboxPublishedReclaimHoleAndUnACKSiblingKeepSuffix(t *testing.T) {
	for _, change := range []string{"range-hole", "unacked-fields"} {
		t.Run(change, func(t *testing.T) {
			client, _ := newProjectionVL(t)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 3)
			if change == "range-hole" {
				// 只保守撤去中间的区间凭据，实际HTTP成功、正文ACK与发布副本均不伪造。
				entry := p.Ledger().Get(p.Key())
				entry.RequestDoneRanges = []ledger.PositionRange{{From: tail[0].Record.Start, To: tail[0].Record.End}, {From: tail[2].Record.Start, To: tail[2].Record.End}}
				entry.DeliveryBatches = []ledger.DeliveryBatch{{Start: tail[0].Record.Start, End: tail[0].Record.End, State: logtypes.DeliveryRequestDone}, {Start: tail[2].Record.Start, End: tail[2].Record.End, State: logtypes.DeliveryRequestDone}}
				require.NoError(t, p.Ledger().Restore([]ledger.Entry{*entry}))
			} else {
				variant := tail[1]
				variant.Fields = map[string]string{"未ACK": "同canonical不同完整正文"}
				require.NoError(t, p.WAL().Append(variant))
				require.NoError(t, p.WAL().Commit())
			}
			require.NoError(t, m.repairStalledReclaim(p, source))
			require.Equal(t, tail[0].Record.End, p.Ledger().Get(p.Key()).Positions.Reclaim, "只能前移至洞或未ACK新体之前")
			_ = m.repairStalledReclaim(p, source)
			require.Equal(t, tail[0].Record.End, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.False(t, ledger.CoveredByPositionRanges(m.verifiedRuns(source.LogSourceID+"/"+source.SourceGeneration), tail[1].Record.Start, tail[2].Record.End))
			require.NotEmpty(t, p.WAL().Snapshot())
		})
	}
}

func TestDurableOutboxPublishedReclaimRequiresCurrentBodyACK(t *testing.T) {
	for _, change := range []string{"fields", "restore"} {
		t.Run(change, func(t *testing.T) {
			client, _ := newProjectionVL(t)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 3)
			key := source.LogSourceID + "/" + source.SourceGeneration
			before := m.verifiedRuns(key)
			variant := tail[0]
			variant.Fields = map[string]string{"完整正文": "未被原ACK确认"}
			id, err := acquire.CommitIdentityOf(tail[0])
			require.NoError(t, err)
			changed, err := acquire.CommitIdentityOf(variant)
			require.NoError(t, err)
			require.Equal(t, id.EventID, changed.EventID)
			require.Equal(t, id.Record, changed.Record)
			require.Equal(t, tail[0].CanonicalHash, variant.CanonicalHash)
			require.NotEqual(t, id.BodyDigest, changed.BodyDigest)
			if change == "restore" {
				// 2026-10-29：原用 WAL.Restore 整体替换正文；不落盘后等价的生产做法是
				// 「回放原位替换」（AppendReplay）——同 ID/同区间的正文被改写。
				require.NoError(t, p.WAL().AppendReplay(variant))
				require.NoError(t, p.WAL().Commit())
			} else {
				require.NoError(t, p.WAL().Append(variant))
				require.NoError(t, p.WAL().Commit())
			}
			require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), tail[0].Record.Start, tail[2].Record.End), "旧范围ACK确实仍覆盖新体所在位置")
			_ = m.repairStalledReclaim(p, source)
			require.Equal(t, before, m.verifiedRuns(key), "同ID同区间同canonical但未ACK的新Fields体，连Verified也不能登记")
			require.Equal(t, tail[0].Record.Start, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.NotEmpty(t, p.WAL().Snapshot())
		})
	}
}

func TestDurableOutboxPublishedReclaimPhysicalQueriesShareTwelve(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 400)
	fixture.mu.Lock()
	fixture.queryWindows = nil
	fixture.mu.Unlock()
	budget := &publishedPrefixQueryBudget{remaining: defaultVerifyMaxQueriesPerChunk}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, publishedPrefixQueryKey{}, budget)
	for n := 0; n < 3; n++ {
		proof, err := m.capturePublishedPrefixEvidence(ctx, source, tail)
		require.NoError(t, err)
		require.Len(t, proof.events, 400)
	}
	_, err := m.capturePublishedPrefixEvidence(ctx, source, tail)
	require.ErrorIs(t, err, ErrVerifyDeferred, "同ctx的后续helper不能另领12次物理查询")
	require.Zero(t, budget.available())
	fixture.mu.Lock()
	queries := append([]queryWindowRecord(nil), fixture.queryWindows...)
	fixture.mu.Unlock()
	require.Len(t, queries, defaultVerifyMaxQueriesPerChunk, "统计实际HTTP请求而不是helper调用次数")
	for _, query := range queries {
		require.LessOrEqual(t, len(query.Query), scopedProjectionQueryMaxBytes)
	}
}

func TestDurableOutboxPublishedReclaimReadFailuresKeepDebt(t *testing.T) {
	for _, failure := range []string{"503", "missing", "hash"} {
		t.Run(failure, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var unavailable atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if unavailable.Load() && r.URL.Path == "/select/logsql/query" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 3)
			before := m.verifiedRuns(source.LogSourceID + "/" + source.SourceGeneration)
			if failure == "503" {
				unavailable.Store(true)
			} else {
				fixture.mu.Lock()
				for n, payload := range fixture.writes {
					var kept strings.Builder
					for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
						var row map[string]any
						require.NoError(t, json.Unmarshal([]byte(line), &row))
						if row["event_id"] == tail[0].EventID {
							if failure == "missing" {
								continue
							}
							row["canonical_content_hash"] = "实际物理坏哈希"
						}
						body, marshalErr := json.Marshal(row)
						require.NoError(t, marshalErr)
						kept.Write(body)
						kept.WriteByte('\n')
					}
					fixture.writes[n] = []byte(kept.String())
				}
				fixture.mu.Unlock()
			}
			_ = m.repairStalledReclaim(p, source)
			require.Equal(t, before, m.verifiedRuns(source.LogSourceID+"/"+source.SourceGeneration))
			require.Equal(t, tail[0].Record.Start, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.Len(t, p.WAL().Snapshot(), 3)
		})
	}
}

func TestDurableOutboxPublishedReclaimAuthorityAndHoldDuringRO(t *testing.T) {
	for _, change := range []string{"authority", "hold"} {
		t.Run(change, func(t *testing.T) {
			client, _ := newProjectionVL(t)
			m, source, p := outboxTestManager(t, client)
			tail := prepareOutboxPublishedReclaimDebt(t, m, source, p, client, 3)
			before := m.verifiedRuns(source.LogSourceID + "/" + source.SourceGeneration)
			var held atomic.Bool
			m.recoveryHold = func(SourceConfig, string) (bool, string) { return held.Load(), "真实RO期间新增hold" }
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer onceCloseChannel(release)
			blocked, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: client.BaseURL(), HTTPClient: &http.Client{Transport: outboxReclaimProofTransport{next: http.DefaultTransport, id: tail[0].EventID, entered: entered, release: release, once: &once}}})
			require.NoError(t, err)
			m.vlReadRoute = func(SourceConfig, *catalog.Record) (*vlsup.Client, error) { return blocked, nil }
			done := make(chan error, 1)
			go func() { done <- m.repairStalledReclaim(p, source) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("未进入真实只读证明响应交错点")
			}
			if change == "authority" {
				catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
				require.NoError(t, m.cat.Put(catalog.NewStableRecord(catKey, catalog.OwnerHot, 2, "replacement-root")))
			} else {
				held.Store(true)
			}
			onceCloseChannel(release)
			<-done
			require.Equal(t, before, m.verifiedRuns(source.LogSourceID+"/"+source.SourceGeneration))
			require.Equal(t, tail[0].Record.Start, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.Len(t, p.WAL().Snapshot(), 3)
		})
	}
}

type recovery026Query struct {
	query  string
	status int
	rows   []string
}

type recovery026Natural struct {
	m               *Manager
	source          SourceConfig
	p               *pipeline.Pipeline
	fixture         *projectionVLFixture
	lost            atomic.Bool
	failReads       atomic.Bool
	failNextInsert  atomic.Bool
	rejectedInserts atomic.Int64
	truncateReads   atomic.Bool
	queryPause      atomic.Value

	faultResult                     chan error
	queryMu                         sync.Mutex
	queries                         []recovery026Query
	head, unknown, missing, written []logtypes.Event
	inputStopped                    time.Time
}

func recovery026Copies(t *testing.T, fixture *projectionVLFixture) (map[string]int, map[string]string) {
	t.Helper()
	counts, generations := make(map[string]int), make(map[string]string)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, payload := range fixture.writes {
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			if line == "" {
				continue
			}
			var row struct {
				ID         string `json:"event_id"`
				Generation string `json:"projection_generation"`
			}
			require.NoError(t, json.Unmarshal([]byte(line), &row))
			counts[row.ID]++
			generations[row.ID] = row.Generation
		}
	}
	return counts, generations
}

func recovery026Drain(t *testing.T, c *recovery026Natural, start, count int) []logtypes.Event {
	t.Helper()
	appendOutboxLines(t, c.source.Path, start, count)
	c.inputStopped = time.Now()
	events, err := c.p.Drain()
	if errors.Is(err, ErrPersistDeferred) {
		require.NoError(t, c.m.persist())
		err = nil
	}
	require.NoError(t, err)
	require.Len(t, events, count)
	return events
}

func recovery026Deliver(t *testing.T, c *recovery026Natural) (bool, error) {
	t.Helper()
	ctx, release, err := c.m.acquireSourceDelivery(context.Background(), c.source)
	require.NoError(t, err)
	defer release()
	return c.p.DeliverPendingContext(ctx)
}

// 只通过真实文件、HTTP、耐久门、正常投递和原游标形成三类责任；禁止手造hint/代次/水位。
func recovery026PrepareNatural(t *testing.T, configure ...func(*Options)) *recovery026Natural {
	t.Helper()
	c := &recovery026Natural{fixture: &projectionVLFixture{}, faultResult: make(chan error, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if r.URL.Path == "/insert/jsonline" && c.failNextInsert.CompareAndSwap(true, false) {
			c.rejectedInserts.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/select/logsql/query" && strings.Contains(query, projectionVerifyFields) && c.failReads.Load() {
			c.queryMu.Lock()
			c.queries = append(c.queries, recovery026Query{query: query, status: http.StatusServiceUnavailable})
			c.queryMu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		recorded := httptest.NewRecorder()
		c.fixture.ServeHTTP(recorded, r)
		if r.URL.Path == "/select/logsql/query" && strings.Contains(query, projectionVerifyFields) {
			if c.truncateReads.Load() && recorded.Body.Len() > 0 {
				recorded.Body.Truncate(recorded.Body.Len() / 2)
			}
			if hook, _ := c.queryPause.Load().(func(string)); hook != nil {
				hook(query)
			}
		}
		if r.URL.Path == "/insert/jsonline" && c.lost.CompareAndSwap(true, false) {
			if recorded.Code != http.StatusOK {
				c.faultResult <- fmt.Errorf("真实后端未接纳: %d", recorded.Code)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				c.faultResult <- errors.New("实际HTTP不支持断响应")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			connection, _, err := hijacker.Hijack()
			if err == nil {
				err = connection.Close()
			}
			c.faultResult <- err
			return
		}
		if r.URL.Path == "/select/logsql/query" && strings.Contains(query, projectionVerifyFields) {
			observation := recovery026Query{query: query, status: recorded.Code}
			for _, line := range strings.Split(strings.TrimSpace(recorded.Body.String()), "\n") {
				var row struct {
					ID string `json:"event_id"`
				}
				if json.Unmarshal([]byte(line), &row) == nil && row.ID != "" {
					observation.rows = append(observation.rows, row.ID)
				}
			}
			c.queryMu.Lock()
			c.queries = append(c.queries, observation)
			c.queryMu.Unlock()
		}
		for name, values := range recorded.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(recorded.Code)
		_, _ = io.Copy(w, recorded.Body)
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	c.m, c.source, c.p = outboxTestManager(t, client, configure...)
	start := 0
	for _, count := range []int{166, 1, 36, 83} {
		events := recovery026Drain(t, c, start, count)
		c.head = append(c.head, events...)
		start += count
		_, err = recovery026Deliver(t, c)
		require.NoError(t, err)
	}
	require.Empty(t, c.p.WAL().Snapshot())
	c.unknown = recovery026Drain(t, c, start, 37)
	start += 37
	c.lost.Store(true)
	_, err = recovery026Deliver(t, c)
	require.Error(t, err, "服务端真实接纳后断TCP，不能收到成功ACK")
	require.NoError(t, <-c.faultResult)
	remaining := recovery026Drain(t, c, start, 744)
	c.missing, c.written = remaining[:463], remaining[463:]
	_, err = recovery026Deliver(t, c)
	require.ErrorIs(t, err, ErrVerifyDeferred, "确认旧37不能给incoming前500借ACK")
	_, err = recovery026Deliver(t, c)
	require.ErrorContains(t, err, "同日存在未发布正文", "尾281必须自然写入/验真而被earlier debt守住")
	counts, generations := recovery026Copies(t, c.fixture)
	for _, event := range c.unknown {
		require.Equal(t, 1, counts[event.EventID])
	}
	for _, event := range c.missing {
		require.Zero(t, counts[event.EventID])
	}
	for _, event := range c.written {
		require.Equal(t, 1, counts[event.EventID])
	}
	require.NotEqual(t, generations[c.unknown[0].EventID], generations[c.written[0].EventID], "新代次必须由正常路径生成，不允许测试裸注入g2")
	key := c.source.LogSourceID + "/" + c.source.SourceGeneration
	c.m.mu.Lock()
	hint := c.m.writtenAttempts[key]
	c.m.mu.Unlock()
	require.NotNil(t, hint, "真实前置必须自然产生hint；未产生就不能宣称该因果成立")
	require.Equal(t, 281, hint.count)
	require.Equal(t, c.written[0].Record.Start, hint.from)
	require.Equal(t, c.written[280].Record.End, hint.to)
	require.True(t, hint.allWritten)
	require.False(t, hint.measuredMissing)
	require.Equal(t, http.StatusOK, hint.status)
	entry := c.p.Ledger().Get(c.p.Key())
	require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.unknown[0].Record.Start, c.unknown[36].Record.End))
	require.Equal(t, c.head[len(c.head)-1].Record.End, entry.Positions.Reclaim)
	return c
}

func recovery026ConfirmEarlier(t *testing.T, c *recovery026Natural) (bool, pipeline.DeliveryResult, error) {
	t.Helper()
	window, err := c.p.WAL().SnapshotWindow(c.unknown[0].Record.Start, c.missing[462].Record.End, 500)
	require.NoError(t, err)
	require.Len(t, window, 500)
	incoming := make([]logtypes.Event, 0, len(window))
	for _, entry := range window {
		incoming = append(incoming, entry.Event)
	}
	actual, _, capture, err := c.p.WAL().CaptureDelivery(incoming)
	require.NoError(t, err)
	ctx, release, err := c.m.acquireSourceDelivery(context.Background(), c.source)
	require.NoError(t, err)
	defer release()
	return c.m.confirmWrittenAttempt(acquire.WithDeliveryCapture(ctx, capture), c.source, actual)
}

func TestDurableOutboxACKLossHeadAndVisibleTailDoNotStarveEarlierMissing(t *testing.T) {
	c := recovery026PrepareNatural(t)
	handled, result, err := recovery026ConfirmEarlier(t, c)
	t.Logf("incoming500=[%d,%d) actualHint281=[%d,%d) handled=%v status=%d err=%v", c.unknown[0].Record.Start, c.missing[462].Record.End, c.written[0].Record.Start, c.written[280].Record.End, handled, result.HTTPStatus, err)
	counts, _ := recovery026Copies(t, c.fixture)
	for _, event := range c.written {
		require.Equal(t, 1, counts[event.EventID], "已写281不得重复物化")
	}
	entry := c.p.Ledger().Get(c.p.Key())
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.unknown[0].Record.Start, c.unknown[36].Record.End), "已有当前PP的旧37必须经新的严格幂等证明先取得自身ACK责任，不能被不相交的尾281 hint永久遮住")
	require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.missing[0].Record.Start, c.missing[462].Record.End), "新证明不能给尚未真实写入的463借ACK")
}

func TestDurableOutboxVisibleWrittenTailRepairsEarlierMissingWithoutCopy(t *testing.T) {
	c := recovery026PrepareNatural(t)
	_, _, _ = recovery026ConfirmEarlier(t, c)
	c.m.rebuilds.mu.Lock()
	req := c.m.rebuilds.pickLocked()
	c.m.rebuilds.mu.Unlock()
	require.NotNil(t, req, "只能消费真实失败自然登记的既有请求，不手造repair状态")
	c.m.runRebuildRequest(c.m.lifecycleContext(), req)
	counts, _ := recovery026Copies(t, c.fixture)
	for _, event := range c.written {
		require.Equal(t, 1, counts[event.EventID], "已写尾281必须零新增copy")
	}
	for _, event := range c.missing {
		require.Equal(t, 1, counts[event.EventID], "原budget worker必须真正修复缺463，不能无限RO尾281")
	}
	rows, err := c.m.index.ListMaterializations(context.Background(), c.source.LogSourceID+"/"+c.source.SourceGeneration, c.source.UTCDay, canonicalWindowKey(c.missing), 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "前置修复必须进入原窗口副本/小时字节计价，不能以Tail绕过G预算")
	require.Positive(t, rows[0].ReservedBytes)
}

func TestDurableOutboxACKLossVisibleTailRecoversWithoutNewInput(t *testing.T) {
	c := recovery026PrepareNatural(t)
	deadline := c.inputStopped.Add(120 * time.Second)
	before, err := os.ReadFile(c.source.Path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(c.m.lifecycleContext())
	capacityDone, rebuildDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(capacityDone); c.m.RunCapacityResumeLoop(ctx) }()
	go func() { defer close(rebuildDone); c.m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-capacityDone; <-rebuildDone }()
	key := c.source.LogSourceID + "/" + c.source.SourceGeneration
	through := c.written[len(c.written)-1].Record.End
	remaining := time.Until(deadline)
	require.Positive(t, remaining, "不能把准备耗时从原120s中抹掉")
	require.Eventually(t, func() bool { return outboxReclaimSQLCaughtUp(t, c.m, key, through) }, remaining, 20*time.Millisecond, "无新输入必须由原worker/timer真实写缺463并在Stop前SQL追平")
	after, err := os.ReadFile(c.source.Path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	counts, _ := recovery026Copies(t, c.fixture)
	for _, group := range [][]logtypes.Event{c.head, c.unknown, c.missing, c.written} {
		for _, event := range group {
			require.Equal(t, 1, counts[event.EventID], "正常幂等证明与修复只补真正缺失的463，不得复制37或281")
		}
	}
	entry := c.p.Ledger().Get(c.p.Key())
	require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), 0, through))
	require.Equal(t, through, entry.Positions.Reclaim)
	require.Empty(t, c.p.WAL().Snapshot())
}

func recovery026RunQueued(t *testing.T, c *recovery026Natural) {
	t.Helper()
	c.m.rebuilds.mu.Lock()
	req := c.m.rebuilds.pickLocked()
	c.m.rebuilds.mu.Unlock()
	require.NotNil(t, req, "必须消费自然登记的原rebuild请求")
	c.m.runRebuildRequest(c.m.lifecycleContext(), req)
}

func recovery026ConfirmCurrent(t *testing.T, c *recovery026Natural) error {
	t.Helper()
	ctx, release, err := c.m.acquireSourceDelivery(context.Background(), c.source)
	require.NoError(t, err)
	defer release()
	_, _, err = c.m.confirmWrittenAttempt(ctx, c.source, nil)
	return err
}

func TestDurableOutboxEarlierRepairUnknownNeedsMeasuredMissing(t *testing.T) {
	c := recovery026PrepareNatural(t)
	_, _, _ = recovery026ConfirmEarlier(t, c)
	c.failNextInsert.Store(true)
	recovery026RunQueued(t, c)
	require.Equal(t, int64(1), c.rejectedInserts.Load(), "必须真实收到前置insert HTTP503")
	key := c.source.LogSourceID + "/" + c.source.SourceGeneration
	c.m.mu.Lock()
	root := c.m.writtenAttempts[key]
	child := root.earlier
	c.m.mu.Unlock()
	require.NotNil(t, child)
	require.Equal(t, 463, child.count)
	require.False(t, child.allWritten)
	require.False(t, child.measuredMissing)
	c.failReads.Store(true)
	require.Error(t, recovery026ConfirmCurrent(t, c))
	c.m.mu.Lock()
	currentRoot := c.m.writtenAttempts[key]
	stillUnknown := root.earlier != nil && !root.earlier.measuredMissing
	c.m.mu.Unlock()
	require.Same(t, root, currentRoot)
	require.True(t, stillUnknown, "真实只读503仍是未知，不得写missing旗标")
	c.failReads.Store(false)
	require.ErrorIs(t, recovery026ConfirmCurrent(t, c), ErrVerifyDeferred)
	c.m.mu.Lock()
	measured := root.earlier.measuredMissing
	c.m.mu.Unlock()
	require.True(t, measured, "完整有效空响应才是前置窗口实测missing，且只交既有worker")
	counts, _ := recovery026Copies(t, c.fixture)
	for _, event := range c.missing {
		require.Zero(t, counts[event.EventID])
	}
	for _, event := range c.written {
		require.Equal(t, 1, counts[event.EventID])
	}
	ctx, cancel := context.WithCancel(c.m.lifecycleContext())
	first, second := make(chan struct{}), make(chan struct{})
	go func() { defer close(first); c.m.RunCapacityResumeLoop(ctx) }()
	go func() { defer close(second); c.m.RunRebuildWorker(ctx) }()
	defer func() { cancel(); <-first; <-second }()
	remaining := time.Until(c.inputStopped.Add(120 * time.Second))
	require.Positive(t, remaining)
	require.Eventually(t, func() bool { return outboxReclaimSQLCaughtUp(t, c.m, key, c.written[280].Record.End) }, remaining, 20*time.Millisecond, "child measuredMissing必须有原timer/worker消费者，不能只挂一个永不消费的flag")
	counts, _ = recovery026Copies(t, c.fixture)
	for _, group := range [][]logtypes.Event{c.unknown, c.missing, c.written} {
		for _, event := range group {
			require.Equal(t, 1, counts[event.EventID])
		}
	}
}

func TestDurableOutboxEarlierRepairUnknownAndHashKeepDebt(t *testing.T) {
	for _, kind := range []string{"503", "truncated", "hash"} {
		t.Run(kind, func(t *testing.T) {
			c := recovery026PrepareNatural(t)
			switch kind {
			case "503":
				c.failReads.Store(true)
			case "truncated":
				c.truncateReads.Store(true)
			default:
				c.fixture.mu.Lock()
				for n, payload := range c.fixture.writes {
					var updated strings.Builder
					for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
						var row map[string]any
						require.NoError(t, json.Unmarshal([]byte(line), &row))
						if row["event_id"] == c.written[0].EventID {
							row["canonical_content_hash"] = "真实物理坏哈希"
						}
						body, err := json.Marshal(row)
						require.NoError(t, err)
						updated.Write(body)
						updated.WriteByte('\n')
					}
					c.fixture.writes[n] = []byte(updated.String())
				}
				c.fixture.mu.Unlock()
			}
			_ = recovery026ConfirmCurrent(t, c)
			recovery026RunQueued(t, c)
			counts, _ := recovery026Copies(t, c.fixture)
			for _, event := range c.missing {
				require.Zero(t, counts[event.EventID], "未知/坏字段不能授权前置物化")
			}
			for _, event := range c.written {
				require.Equal(t, 1, counts[event.EventID])
			}
			entry := c.p.Ledger().Get(c.p.Key())
			require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.missing[0].Record.Start, c.missing[462].Record.End))
		})
	}
}

func TestDurableOutboxEarlierRepairNetworkInterleavingsKeepOriginalCut(t *testing.T) {
	for _, kind := range []string{"fields", "restore", "authority"} {
		t.Run(kind, func(t *testing.T) {
			c := recovery026PrepareNatural(t)
			_, _, _ = recovery026ConfirmEarlier(t, c)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer onceCloseChannel(release)
			c.queryPause.Store(func(query string) {
				if strings.Contains(query, c.missing[0].EventID) {
					once.Do(func() { close(entered); <-release })
				}
			})
			c.m.rebuilds.mu.Lock()
			req := c.m.rebuilds.pickLocked()
			c.m.rebuilds.mu.Unlock()
			require.NotNil(t, req)
			done := make(chan struct{})
			go func() { defer close(done); c.m.runRebuildRequest(c.m.lifecycleContext(), req) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("未进入真实earlier缺口RO交错点")
			}
			switch kind {
			case "fields":
				variant := c.missing[0]
				variant.Fields = map[string]string{"交错": "原cut之后的完整正文"}
				require.NoError(t, c.p.WAL().Append(variant))
				require.NoError(t, c.p.WAL().Commit())
			case "restore":
				// 2026-10-29：原为整体替换队列；不落盘后改为「回放原位替换」同一条目的正文。
				reseat := c.p.WAL().Snapshot()[0]
				reseat.Event.Fields = map[string]string{"交错": "原cut之后的完整正文"}
				require.NoError(t, c.p.WAL().AppendReplay(reseat.Event))
				require.NoError(t, c.p.WAL().Commit())
			case "authority":
				part := catalog.PartitionKey{StorageNamespace: c.source.StorageNamespace, UTCDay: c.source.UTCDay}
				record := catalog.NewStableRecord(part, catalog.OwnerCold, 2, "真实网络期间新权威")
				require.NoError(t, c.m.cat.AppendJournal(catalog.JournalEntry{Key: part, Record: record, AuthorityCommit: true}))
			}
			onceCloseChannel(release)
			<-done
			counts, _ := recovery026Copies(t, c.fixture)
			for _, event := range c.missing {
				require.Zero(t, counts[event.EventID])
			}
			for _, event := range c.written {
				require.Equal(t, 1, counts[event.EventID])
			}
			entry := c.p.Ledger().Get(c.p.Key())
			require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.missing[0].Record.Start, c.missing[462].Record.End))
		})
	}
}

func TestDurableOutboxEarlierRepairRechecksParentAfterPersistBeforePermit(t *testing.T) {
	c := recovery026PrepareNatural(t)
	_, _, _ = recovery026ConfirmEarlier(t, c)
	key := c.source.LogSourceID + "/" + c.source.SourceGeneration
	c.m.mu.Lock()
	parent := c.m.writtenAttempts[key]
	c.m.mu.Unlock()
	require.NotNil(t, parent)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer onceCloseChannel(release)
	// 复用真实persist门的既有观测口，只暂停执行，不伪造RO/ACK/代次/水位。
	c.m.persistGateEnsure()
	c.m.persistGate.mu.Lock()
	c.m.persistGateStage = func(_ context.Context, event string, _ int64, _ bool) {
		if event != "acquire" {
			return
		}
		c.m.mu.Lock()
		generation := c.m.state.Sources[key].ProjectionGeneration
		c.m.mu.Unlock()
		if generation == nextProjectionGeneration(parent.generation) {
			once.Do(func() { close(entered); <-release })
		}
	}
	c.m.persistGate.mu.Unlock()
	c.m.rebuilds.mu.Lock()
	req := c.m.rebuilds.pickLocked()
	c.m.rebuilds.mu.Unlock()
	require.NotNil(t, req)
	done := make(chan struct{})
	go func() { defer close(done); c.m.runRebuildRequest(c.m.lifecycleContext(), req) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("未到达7338后真实代次持久门，不能冒称覆盖许可接缝")
	}
	part := catalog.PartitionKey{StorageNamespace: c.source.StorageNamespace, UTCDay: c.source.UTCDay}
	record := catalog.NewStableRecord(part, catalog.OwnerHot, 2, "同路由的新真实权威，不能借旧RO授新许可")
	require.NoError(t, c.m.cat.AppendJournal(catalog.JournalEntry{Key: part, Record: record, AuthorityCommit: true}))
	onceCloseChannel(release)
	<-done
	counts, _ := recovery026Copies(t, c.fixture)
	for _, event := range c.missing {
		require.Zero(t, counts[event.EventID], "旧RO之后持久门期间权威变化，不能先写新物理副本再由发布CAS拒绝")
	}
	for _, event := range c.written {
		require.Equal(t, 1, counts[event.EventID])
	}
	c.m.mu.Lock()
	root := c.m.writtenAttempts[key]
	c.m.mu.Unlock()
	require.Same(t, parent, root)
	require.Nil(t, root.earlier, "新许可不能将旧父hint重基到新权威")
	entry := c.p.Ledger().Get(c.p.Key())
	require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.missing[0].Record.Start, c.missing[462].Record.End))
}

func TestDurableOutboxEarlierRepairUsesOriginalGBudgets(t *testing.T) {
	for _, kind := range []string{"hourly_bytes", "duration"} {
		t.Run(kind, func(t *testing.T) {
			c := recovery026PrepareNatural(t)
			_, _, _ = recovery026ConfirmEarlier(t, c)
			if kind == "hourly_bytes" {
				c.m.materializationLimits.HourlyBytes = 1
			} else {
				c.m.replayBudget.MaxDuration = time.Nanosecond
			}
			recovery026RunQueued(t, c)
			counts, _ := recovery026Copies(t, c.fixture)
			for _, event := range c.missing {
				require.Zero(t, counts[event.EventID], "必须实际被原G预算拒绝而非Tail旁路")
			}
			for _, event := range c.written {
				require.Equal(t, 1, counts[event.EventID])
			}
		})
	}
}

// 只读阶段见证：调用者持publishMu且原窗租约已成立时，原执行体在此栈内即确实等发布锁。
func outboxEarlierProofWaitingForCAS() bool {
	buffer := make([]byte, 1<<20)
	for _, stack := range strings.Split(string(buffer[:runtime.Stack(buffer, true)]), "\n\n") {
		if strings.Contains(stack, "(*Manager).confirmPublishedEarlier(") && strings.Contains(stack, "(*Manager).applyPublishedEvidence(") {
			return true
		}
	}
	return false
}

func TestDurableOutboxEarlierProofDeadlineAfterROBeforeACK(t *testing.T) {
	for _, phase := range []string{"final_cas", "entry_expired"} {
		t.Run(phase, func(t *testing.T) {
			c := recovery026PrepareNatural(t)
			ctx, releaseOwner, err := c.m.acquireSourceDelivery(context.Background(), c.source)
			require.NoError(t, err)
			defer releaseOwner()
			attempt := ctx.Value(verifyAttemptKey{}).(*verifyAttemptBudget)
			before := c.p.Ledger().Get(c.p.Key())
			c.queryMu.Lock()
			queriesBefore := len(c.queries)
			c.queryMu.Unlock()
			if phase == "entry_expired" {
				// 入口已截止是独立状态故障注入；不消费once、不重置进度或查询额度。
				attempt.state.deadlineNanos.Store(time.Now().UnixNano() - 1)
				_, _, err = c.m.confirmWrittenAttempt(ctx, c.source, nil)
				c.queryMu.Lock()
				queriesAfter := len(c.queries)
				c.queryMu.Unlock()
				require.ErrorIs(t, err, ErrVerifyDeferred)
				require.NoError(t, ctx.Err(), "延期必须来自原attempt截止而非另造取消ctx")
				require.Equal(t, queriesBefore, queriesAfter, "入口已截止不得发起RO，不能借第一块保底绕过")
				entry := c.p.Ledger().Get(c.p.Key())
				require.Equal(t, ledger.RequestDoneRangesOf(before), ledger.RequestDoneRangesOf(entry))
				require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.unknown[0].Record.Start, c.unknown[36].Record.End))
				return
			}
			// 准备沿生产自然初始化的原owner/ctx/budget，不人工seed覆盖准备阶段的未来500ms。
			entered, releaseResponse := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer onceCloseChannel(releaseResponse)
			c.queryPause.Store(func(query string) {
				if strings.Contains(query, c.unknown[0].EventID) {
					once.Do(func() { close(entered); <-releaseResponse })
				}
			})
			done := make(chan error, 1)
			go func() { _, _, e := c.m.confirmWrittenAttempt(ctx, c.source, nil); done <- e }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("真实旧37RO之前执行体已返回，未覆盖最终CAS：%v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("未进入真实旧37幂等RO")
			}
			c.m.publishMu.Lock()
			onceCloseChannel(releaseResponse)
			leased, waiting := false, false
			var stageErr error
			finished := false
			stageDeadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(stageDeadline) {
				// 先只读见证原执行体已持窗等锁，再探测一次；不得让测试预留与真Acquire竞争。
				if outboxEarlierProofWaitingForCAS() {
					waiting = true
					reservation, e := c.p.WAL().ReserveAppend(c.unknown)
					leased = errors.Is(e, acquire.ErrWALWindowBusy)
					if reservation != nil {
						reservation.Release()
					}
					break
				}
				select {
				case stageErr = <-done:
					finished = true
				default:
				}
				if finished {
					break
				}
				runtime.Gosched()
			}
			original := attempt.state.deadlineNanos.Load()
			outerErr := ctx.Err()
			var injected int64
			if leased && waiting && original > 0 && outerErr == nil {
				// 只缩短同一budget的截止状态；不声称整500ms流逝，也不改变once/progressed/12额度。
				injected = min(original, time.Now().UnixNano()-1)
				attempt.state.deadlineNanos.Store(injected)
			}
			c.m.publishMu.Unlock()
			// 所有断言在解锁后；阶段未达仍真失败，不能把安全提前截止算最终CAS通过。
			require.False(t, finished, "原窗/最终CAS之前已返回：%v", stageErr)
			require.True(t, leased, "完整RO尚未取得原WAL窗租约")
			require.True(t, waiting, "原执行体尚未实际等publishMu")
			require.Positive(t, original, "生产尚未自然初始化原attempt截止")
			require.NoError(t, outerErr)
			require.Positive(t, injected)
			require.LessOrEqual(t, injected, original)
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("解除发布锁后原执行体未按截止状态返回")
			}
			entry := c.p.Ledger().Get(c.p.Key())
			acked := ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), c.unknown[0].Record.Start, c.unknown[36].Record.End)
			t.Logf("最终CAS截止状态故障注入：original=%d injected=%d 原ctx有效=%v 原窗=%v 等发布锁=%v ACK37=%v err=%v", original, injected, ctx.Err() == nil, leased, waiting, acked, err)
			require.ErrorIs(t, err, ErrVerifyDeferred)
			require.False(t, acked, "真实RO+原窗+发布锁等待后截止，仍不得给旧37新增ACK")
			require.Equal(t, ledger.RequestDoneRangesOf(before), ledger.RequestDoneRangesOf(entry))
		})
	}
}

// 只读见证原release执行体已过WAL租约并等发布锁；复用测试局部缓冲，不抢写预留作轮询。
func outboxReleaseWaitingForCAS(buffer []byte) bool {
	for _, stack := range strings.Split(string(buffer[:runtime.Stack(buffer, true)]), "\n\n") {
		if strings.Contains(stack, "(*Manager).releaseRecovery(") && strings.Contains(stack, "(*Manager).applyPublishedEvidence(") {
			return true
		}
	}
	return false
}

func TestDurableOutboxReleaseDeadlineAfterROBeforeCAS(t *testing.T) {
	for _, kind := range []string{"deadline", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			var held atomic.Bool
			c := recovery026PrepareNatural(t, func(opts *Options) {
				opts.RecoveryHold = func(SourceConfig, string) (bool, string) {
					return held.Load(), "真实ACK暂持责任以核对release CAS"
				}
			})
			_, _, _ = recovery026ConfirmEarlier(t, c)
			held.Store(true)
			recovery026RunQueued(t, c)
			from, to := c.missing[0].Record.Start, c.missing[462].Record.End
			before := c.p.Ledger().Get(c.p.Key())
			require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(before), from, to))
			require.Equal(t, from, before.Positions.Reclaim)
			_, existed := c.p.RecoveryCovering(from, to)
			require.False(t, existed, "真实hold期间不应提前存在待转移ref")
			held.Store(false)
			outer, cancel := context.WithCancel(context.Background())
			defer cancel()
			owned, releaseOwner, err := c.m.acquireSourceDelivery(outer, c.source)
			require.NoError(t, err)
			defer releaseOwner()
			owned = c.m.writtenQueryContext(owned, true)
			// 新owner先沿原生产RO真实建立自然预算并计入共享12次额度，绝不人工seed未来截止。
			proof, err := c.m.capturePublishedPrefixEvidence(owned, c.source, c.missing)
			require.NoError(t, err)
			require.Len(t, proof.events, len(c.missing))
			attempt := owned.Value(verifyAttemptKey{}).(*verifyAttemptBudget)
			require.Positive(t, attempt.state.deadlineNanos.Load())
			entered, releaseResponse := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer onceCloseChannel(releaseResponse)
			c.queryPause.Store(func(query string) {
				if strings.Contains(query, c.missing[0].EventID) {
					once.Do(func() { close(entered); <-releaseResponse })
				}
			})
			done := make(chan error, 1)
			go func() { done <- c.m.releaseRecovery(c.source, c.missing, owned) }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("真实已ACK463的release RO之前已返回，未覆盖CAS：%v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("未进入真实已ACK463的release RO")
			}
			c.m.publishMu.Lock()
			onceCloseChannel(releaseResponse)
			leased, waiting := false, false
			var stageErr error
			finished := false
			stageDeadline := time.Now().Add(3 * time.Second)
			buffer := make([]byte, 1<<20)
			for time.Now().Before(stageDeadline) {
				// 先见原执行体已持窗等锁，再唯一一次验证Busy，不能让测试预留干扰Acquire。
				if outboxReleaseWaitingForCAS(buffer) {
					waiting = true
					reservation, e := c.p.WAL().ReserveAppend(c.missing)
					leased = errors.Is(e, acquire.ErrWALWindowBusy)
					if reservation != nil {
						reservation.Release()
					}
					break
				}
				select {
				case stageErr = <-done:
					finished = true
				default:
				}
				if finished {
					break
				}
				runtime.Gosched()
			}
			original := attempt.state.deadlineNanos.Load()
			outerErr := owned.Err()
			var injected int64
			if leased && waiting && original > 0 && outerErr == nil {
				// 这是阶段后的截止/取消状态故障注入，不冒称整500ms耗时或业务预算放宽。
				if kind == "deadline" {
					injected = min(original, time.Now().UnixNano()-1)
					attempt.state.deadlineNanos.Store(injected)
				} else {
					cancel()
				}
			}
			c.m.publishMu.Unlock()
			// 所有断言在解锁后；入口提前返回或阶段未达仍真失败，不能替代本次CAS证明。
			require.False(t, finished, "原窗/release CAS之前已返回：%v", stageErr)
			require.True(t, waiting, "原release执行体未实际等publishMu")
			require.True(t, leased, "完整真实RO尚未取得原窗租约，不能宣称覆盖RO后等待CAS")
			require.Positive(t, original)
			require.NoError(t, outerErr)
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("解除发布锁后原release未按截止/取消状态返回")
			}
			entry := c.p.Ledger().Get(c.p.Key())
			_, exists := c.p.RecoveryCovering(from, to)
			t.Logf("release CAS状态故障注入：kind=%s original=%d injected=%d 原ctx注入前有效=%v 原窗=%v 等发布锁=%v from=%d reclaim=%d 新ref=%v err=%v", kind, original, injected, outerErr == nil, leased, waiting, from, entry.Positions.Reclaim, exists, err)
			if entry.Positions.Reclaim != from || exists {
				audits, receipts, auditErr := c.m.ListRecoveryArchive(context.Background(), c.p.Key(), "", 16)
				t.Logf("错误转移后的真实引用及持久审计：活动=%+v 审计=%+v 回执=%+v 读取错误=%v", entry.RecoveryRefs, audits, receipts, auditErr)
			}
			require.Equal(t, from, entry.Positions.Reclaim, "原ctx截止/取消后不能新转移责任")
			require.False(t, exists, "不能先新建ref并Xfer/Released再由尾persist报超时")
			require.Equal(t, before.Positions, entry.Positions)
			require.Equal(t, ledger.RequestDoneRangesOf(before), ledger.RequestDoneRangesOf(entry))
			require.NoError(t, outboxRestartRecoveryRefsError(c.m.indexPath(), before, entry), "不得新增或转移任何活动ref，只允许持久审计证成的旧CLEANED归档")
			if kind == "deadline" {
				require.Positive(t, injected)
				require.LessOrEqual(t, injected, original)
				require.NoError(t, owned.Err())
				require.ErrorIs(t, err, ErrVerifyDeferred)
			} else {
				require.ErrorIs(t, owned.Err(), context.Canceled)
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestDurableOutboxReclaimHookLegacyAndContextPriority(t *testing.T) {
	for _, kind := range []string{"legacy", "context", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.log")
			require.NoError(t, os.WriteFile(path, nil, 0600))
			var legacy, contextual atomic.Int64
			type marker struct{}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), marker{}, "原ctx"))
			defer cancel()
			var p *pipeline.Pipeline
			opts := pipeline.Options{Mode: pipeline.ModeFilePrimary, Path: path, LogSourceID: "ctx-reclaim", SourceGeneration: "g1", ContextDelivery: func(context.Context, []logtypes.Event, bool) (pipeline.DeliveryResult, error) {
				if kind == "canceled" {
					cancel()
				}
				return pipeline.DeliveryResult{HTTPStatus: 200}, nil
			}, ReclaimProof: func([]logtypes.Event) error { legacy.Add(1); return nil }}
			if kind != "legacy" {
				opts.ContextReclaimProof = func(observed context.Context, _ []logtypes.Event) error {
					contextual.Add(1)
					require.Equal(t, "原ctx", observed.Value(marker{}))
					require.True(t, p.OwnsDeliveryGuard(observed))
					return nil
				}
			}
			var err error
			p, err = pipeline.New(opts)
			require.NoError(t, err)
			event := sixFixEvent(SourceConfig{LogSourceID: "ctx-reclaim", SourceGeneration: "g1"}, runtimeTestUTCDay(), 0, 10, "hook合同")
			require.NoError(t, p.WAL().Append(event))
			require.NoError(t, p.WAL().Commit())
			_, err = p.DeliverPendingContext(ctx)
			switch kind {
			case "legacy":
				require.NoError(t, err)
				require.Equal(t, int64(1), legacy.Load())
				require.Zero(t, contextual.Load())
			case "context":
				require.NoError(t, err)
				require.Zero(t, legacy.Load())
				require.Equal(t, int64(1), contextual.Load())
			default:
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, legacy.Load())
				require.Zero(t, contextual.Load())
			}
		})
	}
}

func TestDurableOutboxReleaseKeepsOwnerQueryBudgetAndCancellation(t *testing.T) {
	c := recovery026PrepareNatural(t)
	_, _, _ = recovery026ConfirmEarlier(t, c)
	recovery026RunQueued(t, c)
	owned, release, err := c.m.acquireSourceDelivery(context.Background(), c.source)
	require.NoError(t, err)
	defer release()
	ctx := c.m.writtenQueryContext(owned, true)
	for n := 0; n < 12; n++ {
		proof, e := c.m.capturePublishedPrefixEvidence(ctx, c.source, c.unknown)
		require.NoError(t, e)
		require.Len(t, proof.events, 37)
	}
	c.queryMu.Lock()
	before := len(c.queries)
	c.queryMu.Unlock()
	_ = c.m.releaseRecovery(c.source, c.missing, owned)
	c.queryMu.Lock()
	after := len(c.queries)
	c.queryMu.Unlock()
	require.Equal(t, before, after, "原外层ctx未带局部value时也必须消费同owner已有12计数，不能换lifecycle新开RO")
	for _, expired := range []bool{false, true} {
		var stopped context.Context
		var cancel context.CancelFunc
		if expired {
			stopped, cancel = context.WithDeadline(owned, time.Now().Add(-time.Second))
		} else {
			stopped, cancel = context.WithCancel(owned)
			cancel()
		}
		require.Error(t, c.m.releaseRecovery(c.source, c.missing, stopped))
		cancel()
	}
	c.queryMu.Lock()
	require.Equal(t, before, len(c.queries))
	c.queryMu.Unlock()
}

func TestDurableOutboxRealPersistHandoffNeverAuthorizesDelivery(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var once sync.Once
	m.persistStepHold = func() { once.Do(func() { close(entered); <-release }) }
	require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "真实提交屏障"))
	require.NoError(t, p.Ledger().ResumeAcquire(p.Key()))
	first := make(chan error, 1)
	go func() { first <- m.persist() }()
	<-entered
	appendOutboxLines(t, source.Path, 0, 1)
	_, err := p.Drain()
	require.ErrorIs(t, err, ErrPersistDeferred)
	fixture.mu.Lock()
	writes := len(fixture.writes)
	fixture.mu.Unlock()
	require.Zero(t, writes)
	require.Empty(t, p.Ledger().Get(p.Key()).RequestDoneRanges)
	m.mu.Lock()
	receipt := m.committedReceipts[source.LogSourceID+"/"+source.SourceGeneration]
	m.mu.Unlock()
	require.Nil(t, receipt, "真实handoff等待期间不得发布任何提交回执")
	close(release)
	require.NoError(t, <-first)
	require.NoError(t, m.persist())
	_, err = p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
}

func TestDurableOutboxDirtySecondThirdBootRecoverWithoutInput(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	events := []logtypes.Event{sixFixEvent(source, source.UTCDay, 0, 10, "遗留0"), sixFixEvent(source, source.UTCDay, 10, 20, "遗留1"), sixFixEvent(source, source.UTCDay, 20, 30, "遗留2")}
	require.NoError(t, p.WAL().Append(events...))
	require.NoError(t, p.WAL().Commit())
	require.NoError(t, m.persist())
	id, err := acquire.CommitIdentityOf(events[0])
	require.NoError(t, err)
	root, journal := m.root, m.journal
	require.NoError(t, m.Stop())
	oldPipe := p
	for boot := 2; boot <= 3; boot++ {
		restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}, DeferredDelivery: true, VerificationTimeout: 2 * time.Second})
		require.NoError(t, err)
		current, ok := restarted.pipeFor(source.LogSourceID + "/" + source.SourceGeneration)
		require.True(t, ok)
		require.NotSame(t, oldPipe, current)
		require.False(t, restarted.committedContains(source.LogSourceID+"/"+source.SourceGeneration, oldPipe, []acquire.CommitIdentity{id}), "旧进程pipe回执不能跨启动继承true")
		restarted.drainScheduledReplay()
		entry := current.Ledger().Get(current.Key())
		require.Equal(t, uint64(30), entry.Positions.Durable)
		require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(entry), 0, 30))
		require.Equal(t, uint64(30), entry.Positions.Reclaim)
		require.Empty(t, current.WAL().Snapshot())
		record, ok := restarted.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
		require.True(t, ok)
		require.True(t, record.PublishedProjection.CoverageComplete)
		info, err := os.Stat(source.Path)
		require.NoError(t, err)
		require.Zero(t, info.Size(), "二三启期间没有新文件输入")
		reader, err := sql.Open("sqlite", "file:"+restarted.indexPath()+"?mode=ro")
		require.NoError(t, err)
		var durable uint64
		require.NoError(t, reader.QueryRow("SELECT MAX(durable_pos) FROM position").Scan(&durable))
		require.NoError(t, reader.Close())
		require.Equal(t, uint64(30), durable)
		t.Logf("boot=%d durable=%d delivery=%d reclaim=%d retained_wal=%d", boot, entry.Positions.Durable, entry.Positions.Delivery, entry.Positions.Reclaim, len(current.WAL().Snapshot()))
		require.NoError(t, restarted.Stop())
		oldPipe = current
	}
	fixture.mu.Lock()
	physical := 0
	for _, payload := range fixture.writes {
		physical += len(strings.Split(strings.TrimSpace(string(payload)), "\n"))
	}
	fixture.mu.Unlock()
	require.Equal(t, 3, physical, "二三启必须复用真实已发布面，不重复物化遗留正文")
}

func TestDurableOutboxWorkerCompositionExplicitlyEnablesNewMode(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "apps", "worker", "main.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	calls, enabled := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := fun.X.(*ast.Ident)
		if !ok || pkg.Name != "ingest" || fun.Sel.Name != "New" {
			return true
		}
		calls++
		for _, arg := range call.Args {
			literal, ok := arg.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, entry := range literal.Elts {
				field, ok := entry.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				name, ok := field.Key.(*ast.Ident)
				if !ok || name.Name != "DeferredDelivery" {
					continue
				}
				value, ok := field.Value.(*ast.Ident)
				if ok && value.Name == "true" {
					enabled++
				}
			}
		}
		return true
	})
	require.Equal(t, 1, calls)
	require.Equal(t, calls, enabled, "真实Worker组合根必须显式选新模式，不能只库测试生效")
}

func TestDurableOutboxCandidateConflictDoesNotBanNamespaceOrHealthyDay(t *testing.T) {
	for _, sameSource := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_source_%v", sameSource), func(t *testing.T) {
			client, fixture := newProjectionVL(t)
			var healthy SourceConfig
			m, source, p := outboxTestManager(t, client, func(opts *Options) {
				opts.RecoveryHold = func(SourceConfig, string) (bool, string) { return true, "保留责任便于核对精确ACK" }
				healthy = opts.Sources[0]
				if !sameSource {
					healthy.LogSourceID = "healthy-peer"
					healthy.Path = filepath.Join(opts.Root, "peer.log")
					require.NoError(t, os.WriteFile(healthy.Path, nil, 0600))
					opts.Sources = append(opts.Sources, healthy)
				}
			})
			oldDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
			a := sixFixEvent(source, oldDay, 0, 10, "same-A")
			b := sixFixEvent(source, oldDay, 0, 10, "same-B")
			require.NoError(t, p.WAL().Append(a, b))
			require.NoError(t, p.WAL().Commit())
			_, err := p.DeliverPendingContext(context.Background())
			require.ErrorContains(t, err, "canonical身份冲突")
			require.NoError(t, m.persist())
			fixture.mu.Lock()
			require.Empty(t, fixture.writes, "真冲突源不得物化")
			fixture.mu.Unlock()
			hp, ok := m.pipeFor(healthy.LogSourceID + "/" + healthy.SourceGeneration)
			require.True(t, ok)
			start := uint64(0)
			if sameSource {
				start = 10
			}
			tail := sixFixEvent(healthy, healthy.UTCDay, start, start+10, "healthy-tail")
			require.NoError(t, hp.WAL().Append(tail))
			require.NoError(t, hp.WAL().Commit())
			_, _, capture, err := hp.WAL().CaptureDelivery([]logtypes.Event{tail})
			require.NoError(t, err)
			ctx := acquire.WithDeliveryCapture(context.Background(), capture)
			result, written, err := m.writeProjectionPlan(healthy, []logtypes.Event{tail}, "healthy-generation", projectionWritePlan{Write: []logtypes.Event{tail}, Days: []string{healthy.UTCDay}, Tail: &deliveryTailPlan{Events: []logtypes.Event{tail}}, Snapshot: m.capturePublicationSnapshot(healthy, ctx)})
			require.NoError(t, err, "历史诊断不是实际候选许可，不能连坐同namespace或同源其他日健康尾")
			require.Equal(t, []logtypes.Event{tail}, written)
			require.NoError(t, hp.RecordDeliveryResult(ctx, written, result))
			require.Equal(t, []ledger.PositionRange{{From: start, To: start + 10}}, ledger.RequestDoneRangesOf(hp.Ledger().Get(hp.Key())))
			m.mu.Lock()
			require.NotEmpty(t, m.state.Sources[source.LogSourceID+"/"+source.SourceGeneration].IdentityConflicts, "正常投递不得清诊断解锁")
			m.mu.Unlock()
			for _, entry := range p.WAL().Snapshot() {
				if entry.Event.EventID == a.EventID {
					require.Contains(t, []string{a.CanonicalHash, b.CanonicalHash}, entry.Event.CanonicalHash)
				}
			}
			require.Zero(t, p.Ledger().Get(p.Key()).Positions.Reclaim)
			require.NoError(t, m.persist())
			reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
			require.NoError(t, err)
			defer reader.Close()
			var payload string
			require.NoError(t, reader.QueryRow("SELECT payload FROM source_aux WHERE key = ?", source.LogSourceID+"/"+source.SourceGeneration).Scan(&payload))
			require.Contains(t, payload, "identity_conflicts")
			require.Contains(t, payload, a.CanonicalHash)
			require.Contains(t, payload, b.CanonicalHash)
		})
	}
}

func TestDurableOutboxGIdentityCheckKeepsOriginalCutAcrossQuery(t *testing.T) {
	for _, timeOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("time_only_%v", timeOnly), func(t *testing.T) {
			fixture := &projectionVLFixture{}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "capture-race-G") {
					once.Do(func() { close(entered); <-release })
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			a := sixFixEvent(source, source.UTCDay, 0, 10, "same-A")
			b := sixFixEvent(source, source.UTCDay, 0, 10, "same-B")
			if timeOnly {
				b = logtypes.BuildEvent(a.Source, a.Record, source.UTCDay+"T12:00:00.1Z", a.IngestTimeUTC, a.Level, a.Stream, a.Message)
			}
			require.NoError(t, p.WAL().Append(a))
			require.NoError(t, p.WAL().Commit())
			result := make(chan error, 1)
			go func() {
				_, _, err := m.writeProjectionPlan(source, []logtypes.Event{a}, "capture-race-G", projectionWritePlan{Write: []logtypes.Event{a}, Replace: true, Days: []string{source.UTCDay}})
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("真实G没有到达身份检查后的去重查询屏障")
			}
			require.NoError(t, p.WAL().Append(b))
			require.NoError(t, p.WAL().Commit())
			close(release)
			require.Error(t, <-result, "G不能在查询IO后重造cut，把新变体B洗成可忽略旧兄弟")
			record, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			if record != nil {
				require.Nil(t, record.PublishedProjection, "旧A不得单凭同record/end将新B封闭为完整投影")
			}
			require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())))
			require.Len(t, p.WAL().Snapshot(), 2)
		})
	}
}

func TestDurableOutboxActualCandidateMustMatchTrueCanonicalWinner(t *testing.T) {
	client, _ := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	a := sixFixEvent(source, source.UTCDay, 0, 10, "same-physical-record")
	b := logtypes.BuildEvent(a.Source, a.Record, source.UTCDay+"T12:00:00.1Z", a.IngestTimeUTC, a.Level, a.Stream, a.Message)
	require.NoError(t, p.WAL().Append(a))
	require.NoError(t, p.WAL().Commit())
	actual := []logtypes.Event{a}
	// 模拟真实G先构建actual=A，合法时间更正B随后进入真源，但早于实际身份检查。
	require.NoError(t, p.WAL().Append(b))
	require.NoError(t, p.WAL().Commit())
	_, _, err := m.writeProjectionPlan(source, actual, "stale-candidate-G", projectionWritePlan{Write: actual, Replace: true, Days: []string{source.UTCDay}})
	require.Error(t, err, "末尾重放候选A不能覆盖真源裁定赢家B")
	_, written, err := m.writeProjectionPlan(source, []logtypes.Event{b}, "winner-G", projectionWritePlan{Write: []logtypes.Event{b}, Replace: true, Days: []string{source.UTCDay}})
	require.NoError(t, err, "拒绝陈旧候选后仍须允许既有规则裁定的合法赢家G，不永久封源")
	require.Equal(t, []logtypes.Event{b}, written)
	require.Empty(t, ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), "合法发布也不把任意兄弟强制双ACK")
}

func TestDurableOutboxHTTPFixtureRetainsExactPayloadAndMutationSemantics(t *testing.T) {
	source := SourceConfig{LogSourceID: "cache-a", SourceGeneration: "g1", UTCDay: runtimeTestUTCDay()}
	a := sixFixEvent(source, source.UTCDay, 0, 10, "body-A")
	b := sixFixEvent(source, source.UTCDay, 0, 10, "body-B")
	first, err := projectionPayload([]logtypes.Event{a}, "projection-1")
	require.NoError(t, err)
	second, err := projectionPayload([]logtypes.Event{b}, "projection-1")
	require.NoError(t, err)
	require.Len(t, second, len(first), "必须覆盖同长度原位变更，不能仅以长度或slice指针当缓存身份")
	fixture := &projectionVLFixture{}
	insert := httptest.NewRecorder()
	fixture.ServeHTTP(insert, httptest.NewRequest(http.MethodPost, "/insert/jsonline", strings.NewReader(string(first))))
	require.Equal(t, http.StatusOK, insert.Code)
	selector := `projection_generation:="projection-1" AND log_source_id:="cache-a"`
	read := func() string {
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/select/logsql/query?query="+url.QueryEscape(selector), nil))
		require.Equal(t, http.StatusOK, response.Code)
		return response.Body.String()
	}
	require.Equal(t, string(first), read())
	fixture.mu.Lock()
	copy(fixture.writes[0], second)
	fixture.mu.Unlock()
	require.Equal(t, string(second), read(), "所有原始NDJSON字段和值必须逐字节保留，暖缓存不能掩盖同长度正文变体")
	fixture.mu.Lock()
	fixture.writes = append(fixture.writes, first)
	fixture.mu.Unlock()
	require.Equal(t, string(second)+string(first), read(), "真实重复行不能被fixture缓存隐式去重")
	fixture.mu.Lock()
	fixture.writes[0], fixture.writes[1] = fixture.writes[1], fixture.writes[0]
	fixture.mu.Unlock()
	require.Equal(t, string(first)+string(second), read(), "同数量payload重排必须按实际字节失效并保持行顺序")
	fixture.mu.Lock()
	fixture.writes[0], fixture.writes[1] = fixture.writes[1], fixture.writes[0]
	fixture.writes = fixture.writes[:1]
	fixture.mu.Unlock()
	require.Equal(t, string(second), read())
	fixture.mu.Lock()
	fixture.writes = nil
	fixture.mu.Unlock()
	require.Empty(t, read(), "删除或替换测试真源后不得保留陈旧响应")
}

func TestDurableOutboxHTTPFixtureHonorsAllSixRequestedFields(t *testing.T) {
	source := SourceConfig{LogSourceID: "fields-source", SourceGeneration: "g1", UTCDay: runtimeTestUTCDay()}
	event := sixFixEvent(source, source.UTCDay, 0, 10, "完整六字段正文")
	payload, err := projectionPayload([]logtypes.Event{event}, "projection-1")
	require.NoError(t, err)
	fixture := &projectionVLFixture{}
	fixture.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/insert/jsonline", strings.NewReader(string(payload))))
	selector := `projection_generation:="projection-1" AND log_source_id:="fields-source"` + projectionVerifyFields
	response := httptest.NewRecorder()
	fixture.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/select/logsql/query?query="+url.QueryEscape(selector), nil))
	want := fmt.Sprintf(`{"_time":%q,"_msg":%q,"event_id":%q,"level":%q,"stream":%q,"canonical_content_hash":%q}`, event.EventTimeUTC, event.Message, event.EventID, event.Level, event.Stream, event.CanonicalHash)
	require.JSONEq(t, want, response.Body.String(), "fixture必须忠实执行实际fields子句，完整六字段不减，不能额外回插入metadata来污染原预算")
}

func TestDurableOutboxHTTPFixtureSixFieldsPreserveRawEvidence(t *testing.T) {
	when := runtimeTestUTCDay() + "T12:00:00Z"
	base := fmt.Sprintf(`{"projection_generation":"projection-1","log_source_id":"fields-source","source_generation":"g1","event_id":"evidence-id","_time":%q,"_msg":"raw","level":"INFO","stream":"stdout","canonical_content_hash":"hash","extra":true}`, when)
	originals := map[string]string{"event_id": `"evidence-id"`, "_time": fmt.Sprintf("%q", when), "_msg": `"raw"`, "level": `"INFO"`, "stream": `"stdout"`, "canonical_content_hash": `"hash"`}
	cases := []struct {
		name, field, raw string
		missing          bool
	}{
		{"message_number", "_msg", `17`, false},
		{"message_object", "_msg", `{"bad":true}`, false},
		{"message_null", "_msg", `null`, false},
		{"message_missing", "_msg", "", true},
		{"message_empty", "_msg", `""`, false},
		{"message_utf8_escapes", "_msg", `"中文\u4e2d\u6587\/\"x\"<>&"`, false},
		{"level_wrong_type", "level", `["INFO"]`, false},
		{"level_null", "level", `null`, false},
		{"stream_wrong_type", "stream", `true`, false},
		{"stream_missing", "stream", "", true},
		{"hash_wrong_type", "canonical_content_hash", `{"bad":1}`, false},
		{"hash_null", "canonical_content_hash", `null`, false},
		{"time_null_original_filter_allows", "_time", `null`, false},
		{"time_missing_original_filter_allows", "_time", "", true},
		{"id_null_original_filter_allows", "event_id", `null`, false},
		{"id_missing_original_filter_allows", "event_id", "", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			old := fmt.Sprintf("%q:%s", test.field, originals[test.field])
			line := strings.Replace(base, old, fmt.Sprintf("%q:%s", test.field, test.raw), 1)
			if test.missing {
				line = strings.Replace(base, old+",", "", 1)
			}
			fixture := &projectionVLFixture{}
			fixture.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/insert/jsonline", strings.NewReader(line+"\n")))
			selector := `projection_generation:="projection-1" AND log_source_id:="fields-source"` + projectionVerifyFields
			response := httptest.NewRecorder()
			fixture.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/select/logsql/query?query="+url.QueryEscape(selector), nil))
			lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
			require.Len(t, lines, 1)
			require.NotEmpty(t, lines[0], "坏证据字段不能因fixture新增typed string解码而整行静默消失")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &fields))
			value, present := fields[test.field]
			require.Equal(t, !test.missing, present, "缺失/null/空字符串不能互相规范化")
			if !test.missing {
				require.Equal(t, test.raw, string(value), "原值类型、UTF8和转义必须原样投影，不洗白坏副本")
			}
			require.NotContains(t, fields, "extra")
		})
	}
}

func TestDurableOutboxHTTPFixtureMetadataFiltersAndDuplicateOrderStayExact(t *testing.T) {
	day := runtimeTestUTCDay()
	line := func(generation, source, id, when string) string {
		return fmt.Sprintf(`{"projection_generation":%q,"log_source_id":%q,"event_id":%q,"_time":%q,"_msg":"raw","level":"INFO","stream":"stdout","canonical_content_hash":"hash"}`, generation, source, id, when)
	}
	first := line("projection-1", "fields-source", "first", day+"T12:00:00Z")
	unknown := line("projection-1", "fields-source", "unknown", day+"T12:00:01Z")
	badTime := line("projection-1", "fields-source", "invalid-time", "not-a-time")
	wrongIDType := strings.Replace(first, `"event_id":"first"`, `"event_id":17`, 1)
	payload := strings.Join([]string{first, unknown, first, line("other", "fields-source", "wrong-generation", day+"T12:00:00Z"), line("projection-1", "other-source", "wrong-source", day+"T12:00:00Z"), line("projection-1", "fields-source", "outside-time", day+"T13:00:00Z"), wrongIDType, badTime}, "\n") + "\n"
	fixture := &projectionVLFixture{}
	fixture.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/insert/jsonline", strings.NewReader(payload)))
	read := func(ids string, limit int) []string {
		selector := `projection_generation:="projection-1" AND log_source_id:="fields-source"`
		if ids != "" {
			selector += ` AND event_id:in(` + ids + `)`
		}
		selector += projectionVerifyFields
		params := url.Values{"query": {selector}, "start": {day + "T12:00:00Z"}, "end": {day + "T12:00:01Z"}, "limit": {fmt.Sprint(limit)}}
		response := httptest.NewRecorder()
		fixture.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/select/logsql/query?"+params.Encode(), nil))
		var result []string
		for _, raw := range strings.Split(strings.TrimSpace(response.Body.String()), "\n") {
			if raw == "" {
				continue
			}
			var row struct {
				ID string `json:"event_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &row))
			result = append(result, row.ID)
		}
		return result
	}
	require.Equal(t, []string{"first", "unknown", "first", "invalid-time"}, read("", 0), "未知行和重复行不能被缓存去重；原四字段错误类型仍按原行为跳过，无法解析的时间仍按原行为返回")
	require.Equal(t, []string{"first", "unknown"}, read("", 2))
	require.Equal(t, []string{"first", "first"}, read(`"first"`, 0))
}

func TestDurableOutboxActualCandidateIdentityCheckHasLinearAllocation(t *testing.T) {
	measure := func(count int) uint64 {
		m, source, p := outboxTestManager(t, nil)
		events := make([]logtypes.Event, count)
		for i := range events {
			events[i] = sixFixEvent(source, source.UTCDay, uint64(i*10), uint64((i+1)*10), fmt.Sprintf("linear-%d", i))
		}
		require.NoError(t, p.WAL().Append(events...))
		require.NoError(t, p.WAL().Commit())
		_, _, capture, err := p.WAL().CaptureDelivery(events)
		require.NoError(t, err)
		ctx := acquire.WithDeliveryCapture(context.Background(), capture)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err = m.verifyActualProjectionIdentities(ctx, source, events, true)
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		return after.TotalAlloc - before.TotalAlloc
	}
	small, large := measure(256), measure(512)
	t.Logf("候选翻倍的实际分配：small=%d large=%d ratio=%.3f", small, large, float64(large)/float64(small))
	require.Less(t, float64(large), float64(small)*3, "逐条复制全部seen会退化为二次复杂度；必须只将同ID兄弟交给原裁定")
}

func TestDurableOutboxMixedHistoricalActualSetRequiresLiveSQLReceipt(t *testing.T) {
	client, fixture := newProjectionVL(t)
	m, source, p := outboxTestManager(t, client)
	x := sixFixEvent(source, source.UTCDay, 0, 10, "published-history-X")
	require.NoError(t, p.WAL().Append(x))
	require.NoError(t, p.WAL().Commit())
	_, err := p.DeliverPendingContext(context.Background())
	require.NoError(t, err)
	require.Empty(t, p.WAL().Snapshot(), "X必须真实发布并回收，不能伪造历史缺席")
	require.NoError(t, m.persist())
	stored, err := m.events.Count(source.LogSourceID + "/" + source.SourceGeneration)
	require.NoError(t, err)
	require.Equal(t, 1, stored)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.persistStepHold = func() { once.Do(func() { close(entered); <-release }) }
	holder := make(chan error, 1)
	go func() { holder <- m.persist() }()
	<-entered
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-holder
	}()
	actual := []logtypes.Event{x}
	for i := 0; i < 1000; i++ {
		actual = append(actual, sixFixEvent(source, source.UTCDay, uint64((i+1)*10), uint64((i+2)*10), fmt.Sprintf("live-Y-%d", i)))
	}
	require.NoError(t, p.WAL().Append(actual[1:]...))
	require.NoError(t, p.WAL().Commit())
	fixture.mu.Lock()
	before := len(fixture.writes)
	fixture.mu.Unlock()
	_, _, writeErr := m.writeProjectionPlan(source, actual, "mixed-G", projectionWritePlan{Write: actual, Replace: true, Days: []string{source.UTCDay}})
	reader, err := sql.Open("sqlite", "file:"+m.indexPath()+"?mode=ro")
	require.NoError(t, err)
	defer reader.Close()
	// 2026-10-29：`source_wal` 已删。原来用「表里 0 行」证明 Y 尚未 COMMIT；现在直接读
	// position.durable_pos：它必须仍停在历史 X 的末端（Y 的字节一条都没进库）。
	var durable uint64
	key := source.LogSourceID + "/" + source.SourceGeneration
	require.NoError(t, reader.QueryRow(`SELECT durable_pos FROM position WHERE key=?`, key).Scan(&durable))
	require.Equal(t, x.Record.End, durable, "真实门仍被旧计划占用，独立ro观察证明Y尚未COMMIT")
	fixture.mu.Lock()
	after := len(fixture.writes)
	fixture.mu.Unlock()
	require.Equal(t, before, after, "actual含历史X时也不能跳过活WAL Y严格SQL完成回执后外发")
	require.ErrorIs(t, writeErr, ErrPersistDeferred)
}

func TestDurableOutboxMixedHistoricalWrittenUnknownReusesFullActualSet(t *testing.T) {
	for _, failure := range []string{"503", "invisible"} {
		t.Run(failure, func(t *testing.T) {
			fixture := &projectionVLFixture{}
			var unavailable atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select/logsql/query" && strings.Contains(r.URL.Query().Get("query"), "canonical_content_hash") && unavailable.Load() {
					if failure == "503" {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					return
				}
				fixture.ServeHTTP(w, r)
			}))
			defer server.Close()
			client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
			require.NoError(t, err)
			m, source, p := outboxTestManager(t, client)
			x := sixFixEvent(source, source.UTCDay, 0, 10, "immutable-history-X")
			y := sixFixEvent(source, source.UTCDay, 10, 20, "live-Y")
			require.NoError(t, p.WAL().Append(x))
			require.NoError(t, p.WAL().Commit())
			_, err = p.DeliverPendingContext(context.Background())
			require.NoError(t, err)
			require.Empty(t, p.WAL().Snapshot())
			require.NoError(t, p.WAL().Append(y))
			require.NoError(t, p.WAL().Commit())
			unavailable.Store(true)
			actual := []logtypes.Event{x, y}
			_, _, err = m.writeProjectionPlan(source, actual, "mixed-original-G", projectionWritePlan{Write: actual, Replace: true, Days: []string{source.UTCDay}})
			require.ErrorIs(t, err, errProjectionVerifyFailed)
			key := source.LogSourceID + "/" + source.SourceGeneration
			m.mu.Lock()
			hint := m.writtenAttempts[key]
			m.mu.Unlock()
			require.NotNil(t, hint, "混合actual仍须保留完整原窗只读提示")
			require.Equal(t, 2, hint.count)
			require.Equal(t, "mixed-original-G", hint.generation)
			fixture.mu.Lock()
			firstWrites := len(fixture.writes)
			fixture.mu.Unlock()
			require.Equal(t, 2, firstWrites, "X最初投递一次，混合G只物化一次")
			tail := sixFixEvent(source, source.UTCDay, 20, 30, "postcapture-tail")
			require.NoError(t, p.WAL().Append(tail))
			require.NoError(t, p.WAL().Commit())
			for i := 0; i < 2; i++ {
				handled, _, confirmErr := m.confirmWrittenAttempt(context.Background(), source, []logtypes.Event{y})
				require.True(t, handled)
				require.ErrorIs(t, confirmErr, errWrittenReadOnlyVerify)
			}
			unavailable.Store(false)
			handled, result, err := m.confirmWrittenAttempt(context.Background(), source, []logtypes.Event{y})
			require.True(t, handled)
			require.NoError(t, err)
			ctx := acquire.WithDeliveryCapture(context.Background(), hint.capture)
			require.NoError(t, p.RecordDeliveryResult(ctx, []logtypes.Event{y}, result))
			require.True(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 10, 20))
			require.False(t, ledger.CoveredByPositionRanges(ledger.RequestDoneRangesOf(p.Ledger().Get(p.Key())), 20, 30), "原cut不能借新尾冒原成员ACK")
			record, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			require.True(t, ok)
			require.True(t, record.PublishedProjection.CoverageComplete)
			fixture.mu.Lock()
			require.Len(t, fixture.writes, firstWrites, "unknown和暂不可见只读重抓X+Y全字段，不得再insert或换代")
			fixture.mu.Unlock()
		})
	}
}

func TestDurableOutboxArchiveBusyDoesNotConsumeBurstThreshold(t *testing.T) {
	client, _ := newProjectionVL(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fileStore, err := catalog.NewJSONLFileStore(filepath.Join(t.TempDir(), "sampling-catalog.jsonl"))
	require.NoError(t, err)
	store := &outboxBlockingJournalStore{next: fileStore, entered: entered, release: release}
	journal, err := catalog.NewJournalWithStore(store)
	require.NoError(t, err)
	m, source, p := outboxTestManager(t, client, func(opts *Options) {
		opts.Catalog, opts.Journal = catalog.New(journal), journal
		opts.Sampling = sampling.Policy{Enabled: true, Burst: sampling.Burst{Window: time.Hour, Threshold: 3}}
	})
	line := "[12:00:01] [Server thread/INFO]: same-burst-message\n"
	require.NoError(t, os.WriteFile(source.Path, []byte(line+line), 0600))
	_, err = p.Drain()
	require.NoError(t, err)
	primed := p.WAL().Snapshot()
	require.Len(t, primed, 2, "先消费阈值N-1次")
	path := filepath.Join(filepath.Dir(source.Path), "latest.log.1")
	require.NoError(t, os.WriteFile(path, []byte(line), 0600))
	hash, size, err := acquire.HashFile(path)
	require.NoError(t, err)
	objectID := acquire.ArchiveObjectID(path, size, hash)
	require.NoError(t, p.Ledger().RegisterSegment(p.Key(), ledger.Segment{Path: path, Kind: ledger.SegmentRotatedText, PhysicalMappingKnown: true, ArchiveObjectID: objectID, BaseDay: source.UTCDay}))
	p.SetArchiveOwnerRegistry(func(string, string, string) (string, bool) { return source.SourceGeneration, true })
	store.enabled.Store(true)
	delivery := make(chan error, 1)
	go func() { _, err := p.DeliverPendingContext(context.Background()); delivery <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("真实发布journal未进入屏障")
	}
	_, err = p.ImportArchive(path)
	require.ErrorIs(t, err, acquire.ErrWALWindowBusy)
	close(release)
	require.NoError(t, <-delivery)
	require.True(t, m.SyncArchiveScan(25*time.Millisecond))
	defer m.StopArchiveScan()
	require.Eventually(t, func() bool {
		for _, segment := range p.Ledger().Get(p.Key()).Segments {
			if segment.Path == path {
				return segment.Imported
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "必须真实scanner无新输入重试")
	m.StopArchiveScan()
	entries := p.WAL().Snapshot()
	require.Len(t, entries, 1)
	require.Equal(t, primed[0].Event.Message, entries[0].Event.Message, "WAL忙拒绝不得消耗第N次额度并把重试变成第N+1次汇总")
	require.Equal(t, logtypes.RecordRange{Start: 0, End: uint64(len(line))}, entries[0].Event.Record)
	require.Empty(t, entries[0].Event.Fields["sampling_rule"])
}
