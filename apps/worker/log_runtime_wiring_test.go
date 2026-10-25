package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type wiringFakeProcess struct{ exited atomic.Bool }

func (p *wiringFakeProcess) PID() int                 { return 990001 }
func (p *wiringFakeProcess) Exited() bool             { return p.exited.Load() }
func (p *wiringFakeProcess) Stop(time.Duration) error { p.exited.Store(true); return nil }

type wiringFakeFactory struct {
	mu       sync.Mutex
	failNext error
}

func (f *wiringFakeFactory) Start(context.Context, string, []string) (vlsup.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	return &wiringFakeProcess{}, nil
}

// wiringTransport 不创建套接字，证明同一组固定地址在状态变化后仍能真正执行读写调用。
type wiringTransport struct {
	mu      sync.Mutex
	rows    map[string][]byte
	health  bool
	queries int
	inserts int
}

func (transport *wiringTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	body, code := "", http.StatusOK
	switch request.URL.Path {
	case "/health":
		if !transport.health {
			code = http.StatusServiceUnavailable
		}
	case "/insert/jsonline":
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		transport.rows[request.URL.Host] = append(transport.rows[request.URL.Host], payload...)
		transport.inserts++
	case "/select/logsql/query":
		body = string(transport.rows[request.URL.Host])
		transport.queries++
	default:
		return nil, fmt.Errorf("测试不允许的请求 %s", request.URL.Path)
	}
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func newWiringSupervisor(t *testing.T, root string, factory *wiringFakeFactory, transport *wiringTransport, password string) *vlsup.Supervisor {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "victorialogs-fixture")
	content := []byte("isolated-supervisor-wiring-fixture")
	require.NoError(t, os.WriteFile(binary, content, 0700))
	digest := sha256.Sum256(content)
	supervisor, err := vlsup.New(vlsup.Options{
		BinaryPath: binary, AssetSHA256: hex.EncodeToString(digest[:]), DataRoot: root,
		AuthUsername: "wiring-user", AuthPassword: password, Factory: factory,
		HealthClient: func(baseURL, user, pass string) (*vlsup.Client, error) {
			return vlsup.NewClient(vlsup.ClientOptions{BaseURL: baseURL, Username: user, Password: pass,
				HTTPClient: &http.Client{Transport: transport}})
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { supervisor.StopAll(context.Background()) })
	return supervisor
}

func wiringRange(owner catalog.Owner) query.AuthoritativeRange {
	return query.AuthoritativeRange{
		Key: catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}, Owner: owner,
		Projection: &catalog.PublishedProjection{
			CoverageComplete: true, ProjectionGeneration: "p1", QueryGeneration: 1, QueryLocationDirID: "20260922",
			CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "src", SourceGeneration: "g1"}},
			SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "src", SourceGeneration: "g1"},
				ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 2}},
		},
	}
}

const wiringRow = `{"_time":"2026-09-22T01:02:03Z","_msg":"retained","event_id":"event-1","log_source_id":"src","source_generation":"g1","projection_generation":"p1","record_start":"1","record_end":"2","level":"INFO","stream":"stdout"}` + "\n"

// TestWiringFixedClientsRecoverFromColdDisabledAndFailure 覆盖初始关闭、失败后恢复及同端口二三启。
func TestWiringFixedClientsRecoverFromColdDisabledAndFailure(t *testing.T) {
	transport := &wiringTransport{rows: map[string][]byte{}, health: true}
	factory := &wiringFakeFactory{}
	supervisor := newWiringSupervisor(t, t.TempDir(), factory, transport, "constructed-password")
	clients, err := newManagedLogClients(supervisor)
	require.NoError(t, err)
	require.NotNil(t, clients.Hot)
	require.NotNil(t, clients.Cold)
	require.NotNil(t, clients.Rehydrate)
	coldPointer := clients.Cold
	coldURL := coldPointer.BaseURL()
	rng := wiringRange(catalog.OwnerCold)
	queryArgs := query.RangeQuery{ClosedVisibleSeq: 2, Budget: query.Budget{Limit: 10, MaxBytes: 1 << 20}}
	_, err = clients.Ranges.Search(context.Background(), rng, queryArgs)
	require.Error(t, err)
	require.Zero(t, transport.queries)
	factory.failNext = errors.New("constructed startup failure")
	require.Error(t, supervisor.Start(context.Background(), vlsup.NamespaceCold))
	_, err = clients.Ranges.Search(context.Background(), rng, queryArgs)
	require.Error(t, err)
	for round := 1; round <= 3; round++ {
		require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceCold))
		require.Same(t, coldPointer, clients.Cold)
		require.Equal(t, coldURL, clients.Cold.BaseURL())
		if round == 1 {
			_, err = clients.Cold.InsertJSONLines(context.Background(), []byte(wiringRow))
			require.NoError(t, err)
		}
		response, readErr := clients.Ranges.Search(context.Background(), rng, queryArgs)
		require.NoError(t, readErr)
		require.Len(t, response.Items, 1, "每轮都必须真实取回旧行，不只判断health")
		require.Equal(t, "event-1", response.Items[0].EventID)
		require.NoError(t, supervisor.Stop(context.Background(), vlsup.NamespaceCold))
		_, readErr = clients.Ranges.Search(context.Background(), rng, queryArgs)
		require.Error(t, readErr)
	}
	require.Equal(t, 1, transport.inserts)
	require.Equal(t, 3, transport.queries)
}

// TestWiringHealthFailureDoesNotReplaceClientsAndIsNotPublication 健康恢复不伪造Catalog发布。
func TestWiringHealthFailureDoesNotReplaceClientsAndIsNotPublication(t *testing.T) {
	transport := &wiringTransport{rows: map[string][]byte{}, health: false}
	supervisor := newWiringSupervisor(t, t.TempDir(), &wiringFakeFactory{}, transport, "constructed-password")
	clients, err := newManagedLogClients(supervisor)
	require.NoError(t, err)
	require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceHot))
	require.Error(t, clients.ready(context.Background(), catalog.OwnerHot))
	transport.health = true
	require.NoError(t, clients.ready(context.Background(), catalog.OwnerHot))
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20260922")))
	cat.StartingRecover()
	restored, exists := cat.Get(key)
	require.True(t, exists)
	require.Nil(t, restored.PublishedProjection, "传输健康不能补出不存在的PublishedProjection")
	viewID, err := cat.NewQueryViewID(time.Now())
	require.NoError(t, err)
	snapshots, err := cat.SnapshotAndLease(catalog.QuerySnapshotRequest{ViewID: viewID, Keys: []catalog.PartitionKey{key}, TTL: time.Minute})
	require.NoError(t, err)
	require.Empty(t, snapshots, "没有可发布投影时不能因HTTP健康登记可读快照")
}

// TestWiringAdmissionNeverFallsBackHotDuringFreeze 新namespace冻结时零外发，已持许可仍可完成。
func TestWiringAdmissionNeverFallsBackHotDuringFreeze(t *testing.T) {
	transport := &wiringTransport{rows: map[string][]byte{}, health: true}
	supervisor := newWiringSupervisor(t, t.TempDir(), &wiringFakeFactory{}, transport, "constructed-password")
	clients, err := newManagedLogClients(supervisor)
	require.NoError(t, err)
	require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceHot))
	require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceCold))
	cat := catalog.New(nil)
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20260922")
	record.PublishedProjection = wiringRange(catalog.OwnerHot).Projection
	record.PublishedProjection.ManifestVersion = "manifest"
	require.NoError(t, cat.Put(record))
	permit, err := cat.AcquireWritePermit(key)
	require.NoError(t, err)
	defer cat.ReleaseWritePermit(permit)
	_, err = cat.BeginDayMigration(key.UTCDay, catalog.OwnerCold, "20260922")
	require.ErrorIs(t, err, catalog.ErrWritePermitsActive)
	client, frozen, err := managedLogWriteRoute(cat, clients, clients.Hot)(ingest.SourceConfig{StorageNamespace: "new", UTCDay: key.UTCDay})
	require.ErrorIs(t, err, catalog.ErrPhysicalDayFrozen)
	require.Nil(t, client)
	require.True(t, frozen)
	client, err = managedLogPermitRoute(clients, clients.Hot)(ingest.SourceConfig{}, permit)
	require.NoError(t, err)
	require.Same(t, clients.Hot, client)
	coldKey := catalog.PartitionKey{StorageNamespace: "old", UTCDay: "2026-09-21"}
	require.NoError(t, cat.Put(catalog.NewStableRecord(coldKey, catalog.OwnerCold, 2, "20260921")))
	client, frozen, err = managedLogWriteRoute(cat, clients, clients.Hot)(ingest.SourceConfig{StorageNamespace: "new", UTCDay: coldKey.UTCDay})
	require.NoError(t, err)
	require.False(t, frozen)
	require.Same(t, clients.Cold, client, "缺key时必须遵守同物理日COLD归属而非默认HOT")
	readRoute := managedLogReadRoute(clients, clients.Hot)
	coldRecord := catalog.NewStableRecord(coldKey, catalog.OwnerCold, 2, "20260921")
	coldRecord.PublishedProjection = wiringRange(catalog.OwnerCold).Projection
	client, err = readRoute(ingest.SourceConfig{}, coldRecord)
	require.NoError(t, err)
	require.Same(t, clients.Cold, client, "只读取证不能误走冻结写路由或默认HOT")
	_, err = readRoute(ingest.SourceConfig{}, nil)
	require.Error(t, err)
	_, err = managedLogPermitRoute(clients, clients.Hot)(ingest.SourceConfig{}, nil)
	require.Error(t, err)
}

type wiringPublisher struct{ calls int }

func (p *wiringPublisher) Publish(context.Context, archive.PartitionKey, []archive.ArchivedEvent, string) error {
	p.calls++
	return nil
}

// TestWiringMoversAndRehydrateRecoverWithoutReassembly 已装配的搬运与恢复边界能够随运行时启层恢复。
func TestWiringMoversAndRehydrateRecoverWithoutReassembly(t *testing.T) {
	transport := &wiringTransport{rows: map[string][]byte{}, health: true}
	supervisor := newWiringSupervisor(t, t.TempDir(), &wiringFakeFactory{}, transport, "constructed-password")
	clients, err := newManagedLogClients(supervisor)
	require.NoError(t, err)
	moves := 0
	mover := readyRetentionMover(retention.MoverFunc(func(context.Context, string, string) (string, error) { moves++; return "cold", nil }), clients, nil)
	_, err = mover.MoveToCold(context.Background(), "inst:1", "2026-09-22")
	require.Error(t, err)
	require.Zero(t, moves)
	require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceHot))
	require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceCold))
	_, err = mover.MoveToCold(context.Background(), "inst:1", "2026-09-22")
	require.NoError(t, err)
	require.Equal(t, 1, moves)
	budget, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{Namespaces: vlsup.DefaultNamespaceBudgets()})
	require.NoError(t, err)
	blockedMover := readyRetentionMover(retention.MoverFunc(func(context.Context, string, string) (string, error) { moves++; return "cold", nil }), clients, budget)
	_, err = blockedMover.MoveToCold(context.Background(), "inst:1", "2026-09-22")
	require.Error(t, err, "controller必须真正阻挡未知预算的昂贵搬运")
	require.Equal(t, 1, moves)
	bridge := &dayMigrationInputBridge{}
	_, err = bridge.input(nil)
	require.Error(t, err, "canonical恢复前不能伪造可搬运输入")
	cat := catalog.New(nil)
	newLogPhysicalDayRecovery(lifecycle.NewDayManager(cat, lifecycle.Ops{}, nil), cat, nil, budget).step(context.Background())
	publisher := &wiringPublisher{}
	guarded := readyRehydratePublisher{clients: clients, publisher: publisher}
	require.Error(t, guarded.Publish(context.Background(), archive.PartitionKey{}, nil, "task"))
	require.Zero(t, publisher.calls)
	require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceRehydrate))
	require.NoError(t, guarded.Publish(context.Background(), archive.PartitionKey{}, nil, "task"))
	require.Equal(t, 1, publisher.calls)
	require.False(t, guarded.CanReuseRehydrate(context.Background(), archive.PartitionKey{}, "task"), "无当前投影证明不能复用成功状态")
}

// TestWiringBudgetSamplingPreservesUnknownAndDeduplicatesRoots 验证采样不是把失败当作零。
func TestWiringBudgetSamplingPreservesUnknownAndDeduplicatesRoots(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.MkdirAll(child, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(child, "wal"), []byte("1234567"), 0600))
	bytes, known := logDiskBytes([]string{child, root, child}, 100, time.Second)
	require.True(t, known)
	require.EqualValues(t, 7, bytes)
	_, known = logDiskBytes([]string{root}, 1, time.Second)
	require.False(t, known, "有界采样未完成必须unknown")
	sample := sampleLogResources(nil, []string{root}, false)
	require.True(t, sample.WorkerRSSKnown)
	require.True(t, sample.DiskKnown)
	require.True(t, sample.DiskBytesKnown)
	for _, namespace := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold, vlsup.NamespaceRehydrate} {
		require.True(t, sample.Namespaces[namespace].RSSKnown)
		require.Zero(t, sample.Namespaces[namespace].RSSBytes)
	}
	external := sampleLogResources(nil, []string{root}, true)
	require.False(t, external.Namespaces[vlsup.NamespaceHot].RSSKnown, "外部HOT缺PID证据不能伪装成零")
	controller, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{RSSBytes: 1, Namespaces: vlsup.DefaultNamespaceBudgets()})
	require.NoError(t, err)
	controller.Update(sample)
	require.Error(t, controller.Admit(vlsup.WorkForeground))
	require.Error(t, controller.Admit(vlsup.WorkBackground))
	require.NoError(t, controller.Admit(vlsup.WorkWALDrain))
}

// TestWiringOrphanAndStartFailureLogsNeverContainCredentials 使用构造口令核对main所消费的诊断边界。
func TestWiringOrphanAndStartFailureLogsNeverContainCredentials(t *testing.T) {
	const password = "constructed-secret-should-never-be-logged"
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	transport := &wiringTransport{rows: map[string][]byte{}, health: true}
	factory := &wiringFakeFactory{failNext: fmt.Errorf("failed -httpAuth.password=%s", password)}
	supervisor := newWiringSupervisor(t, t.TempDir(), factory, transport, password)
	err := supervisor.Start(context.Background(), vlsup.NamespaceCold)
	require.Error(t, err)
	slog.Error("VictoriaLogs COLD 启动失败", "error", err)
	logReapedVLOrphan(vlsup.OrphanMatch{PID: 1234, Exe: "/isolated/victorialogs", Namespace: vlsup.NamespaceCold})
	require.NotContains(t, output.String(), password)
	require.NotContains(t, output.String(), "cmdline")
	require.Contains(t, output.String(), `"namespace":"cold"`)
}
