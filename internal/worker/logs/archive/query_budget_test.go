package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type archiveBudgetProvider struct {
	Provider
	mu                  sync.Mutex
	payload             []byte
	manifestPayload     []byte
	manifestOpens       int
	manifestReadBytes   int64
	enabled             bool
	opens, gets, closes int
	readBytes           int64
	beforeOpen          func()
	readError           error
}

func (p *archiveBudgetProvider) Get(ctx context.Context, key PartitionKey, path string) ([]byte, error) {
	data, err := p.Provider.Get(ctx, key, path)
	p.mu.Lock()
	if p.enabled && path != ManifestRelPath {
		p.gets++
		p.readBytes += int64(len(data))
	}
	p.mu.Unlock()
	return data, err
}

func (p *archiveBudgetProvider) OpenArchiveObject(ctx context.Context, key PartitionKey, path string) (io.ReadCloser, error) {
	if path == ManifestRelPath {
		p.mu.Lock()
		p.manifestOpens++
		data := p.manifestPayload
		p.mu.Unlock()
		if data == nil {
			var err error
			data, err = p.Provider.Get(ctx, key, path)
			if err != nil {
				return nil, err
			}
		}
		return &archiveBudgetReader{Reader: bytes.NewReader(data), owner: p, metadata: true}, nil
	}
	p.mu.Lock()
	p.opens++
	before := p.beforeOpen
	data := p.payload
	p.mu.Unlock()
	if before != nil {
		before()
	}
	return &archiveBudgetReader{Reader: bytes.NewReader(data), owner: p}, nil
}

type archiveBudgetReader struct {
	*bytes.Reader
	owner    *archiveBudgetProvider
	metadata bool
}

func (r *archiveBudgetReader) Read(dst []byte) (int, error) {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.readError != nil {
		return 0, r.owner.readError
	}
	n, err := r.Reader.Read(dst)
	if r.metadata {
		r.owner.manifestReadBytes += int64(n)
	} else {
		r.owner.readBytes += int64(n)
	}
	return n, err
}

func (r *archiveBudgetReader) Close() error {
	r.owner.mu.Lock()
	if !r.metadata {
		r.owner.closes++
	}
	r.owner.mu.Unlock()
	return nil
}

func (p *archiveBudgetProvider) counts() (int, int, int, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens, p.gets, p.closes, p.readBytes
}

type archiveBudgetTracker struct {
	mu                             sync.Mutex
	admissions, reserves, releases int
	active, peak, total            int64
	admitErr, reserveErr           error
}

func (b *archiveBudgetTracker) callbacks() RehydrateResourceBudget {
	return RehydrateResourceBudget{
		Admit: func(ctx context.Context) error {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.admissions++
			if err := ctx.Err(); err != nil {
				return err
			}
			return b.admitErr
		},
		ReserveStaging: func(ctx context.Context, n int64) (func(), error) {
			b.mu.Lock()
			b.reserves++
			if b.reserveErr != nil {
				err := b.reserveErr
				b.mu.Unlock()
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				b.mu.Unlock()
				return nil, err
			}
			b.active += n
			b.total += n
			if b.active > b.peak {
				b.peak = b.active
			}
			b.mu.Unlock()
			return func() { b.mu.Lock(); b.active -= n; b.releases++; b.mu.Unlock() }, nil
		},
	}
}

func (b *archiveBudgetTracker) snapshot() (int, int, int, int64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.admissions, b.reserves, b.releases, b.active, b.peak
}

func newArchiveBudgetBackend(t *testing.T, message string) (*QueryBackend, *archiveBudgetProvider, PartitionKey, query.QueryRequest, []string, []byte) {
	t.Helper()
	key := PartitionKey{StorageNamespace: "node:budget", UTCDay: "2026-09-22", Generation: 1}
	payload, err := json.Marshal(archivedTestEvent(key.UTCDay, message))
	require.NoError(t, err)
	payload = append(payload, '\n')
	provider := &archiveBudgetProvider{Provider: NewMemoryProvider(), payload: payload}
	registry := NewRegistry(provider)
	registered, err := registry.RegisterRaw(context.Background(), key, RawSource{Data: payload, EventCount: 1})
	require.NoError(t, err)
	_, err = registry.Seal(context.Background(), key)
	require.NoError(t, err)
	provider.enabled = true
	backend := NewQueryBackend(registry, NewRehydrateManager(RehydrateOptions{}))
	backend.SetPublisher(publisherFunc(func(context.Context, PartitionKey, []ArchivedEvent, string) error { return nil }))
	return backend, provider, key, query.QueryRequest{AuthorizedTargets: []string{key.StorageNamespace + "/" + key.UTCDay}, PermissionScope: "budget-scope"}, []string{registered.Object.ObjectID}, payload
}

func TestArchiveBudgetRejectsBeforeBodyOpenAndRead(t *testing.T) {
	for _, name := range []string{"admit", "reserve", "unknown_sample"} {
		t.Run(name, func(t *testing.T) {
			backend, provider, _, req, ids, _ := newArchiveBudgetBackend(t, "budget denial")
			tracker := &archiveBudgetTracker{}
			callbacks := tracker.callbacks()
			switch name {
			case "admit":
				tracker.admitErr = errors.New("background budget denied")
			case "reserve":
				tracker.reserveErr = errors.New("staging exhausted")
			case "unknown_sample":
				controller, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{})
				require.NoError(t, err)
				callbacks.Admit = func(context.Context) error { return controller.Admit(vlsup.WorkBackground) }
			}
			backend.SetResourceBudget(callbacks)
			published := false
			backend.SetPublisher(publisherFunc(func(context.Context, PartitionKey, []ArchivedEvent, string) error { published = true; return nil }))
			res := backend.Rehydrate(context.Background(), req, ids)
			observedOpens, observedGets, _, observedBytes := provider.counts()
			t.Logf("actual_archive_bytes=%d opens=%d unbounded_gets=%d state=%s", observedBytes, observedOpens, observedGets, res.State)
			require.NotNil(t, res.Err, "拒绝后台预算时不能生成成功恢复产物")
			require.Equal(t, query.ErrCodeNotReady, res.Err.Code)
			require.True(t, res.Err.Retryable)
			opens, gets, _, n := provider.counts()
			require.Zero(t, opens, "Admit/Reserve 必须在打开归档正文之前")
			require.Zero(t, gets, "不能回退到不受限 Get")
			require.Zero(t, n)
			require.False(t, published)
		})
	}
}

func TestArchiveBudgetValidatesManifestUpperBoundsBeforeRead(t *testing.T) {
	for _, name := range []string{"zero", "negative", "overflow", "event_unknown", "event_overflow", "byte_mismatch"} {
		t.Run(name, func(t *testing.T) {
			backend, provider, key, req, ids, _ := newArchiveBudgetBackend(t, "untrusted metadata")
			backend.Registry.mu.Lock()
			manifest := backend.Registry.manifest[key]
			switch name {
			case "zero":
				manifest.RawFiles[0].SizeBytes = 0
			case "negative":
				manifest.RawFiles[0].SizeBytes = -1
			case "overflow":
				manifest.RawFiles[0].SizeBytes = math.MaxInt64
			case "event_unknown":
				manifest.Coverage.EventCount = 0
			case "event_overflow":
				manifest.Coverage.EventCount = math.MaxUint64
			case "byte_mismatch":
				manifest.Coverage.ByteCount++
			}
			backend.Registry.mu.Unlock()
			backend.SetResourceBudget((&archiveBudgetTracker{}).callbacks())
			res := backend.Rehydrate(context.Background(), req, ids)
			require.NotNil(t, res.Err)
			opens, gets, _, n := provider.counts()
			require.Zero(t, opens)
			require.Zero(t, gets)
			require.Zero(t, n)
		})
	}
}

func TestArchiveBudgetSmallPathReservesBeforeOpenAndReleasesAfterPublish(t *testing.T) {
	backend, provider, key, req, ids, payload := newArchiveBudgetBackend(t, "small valid event")
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	provider.beforeOpen = func() {
		admissions, reserves, _, active, _ := tracker.snapshot()
		require.Positive(t, admissions)
		require.Positive(t, reserves)
		require.Greater(t, active, int64(len(payload)), "不是预留一字节后全量解码")
		require.False(t, backend.Tasks.CanCleanup(key), "读取之前必须获得受管恢复 hold")
	}
	backend.SetPublisher(publisherFunc(func(_ context.Context, _ PartitionKey, events []ArchivedEvent, _ string) error {
		_, _, releases, active, _ := tracker.snapshot()
		require.Zero(t, releases, "实际发布消费者退出之前不得释放任何预留")
		require.Positive(t, active)
		require.Len(t, events, 1)
		require.Equal(t, "small valid event", events[0].Message)
		return nil
	}))
	res := backend.Rehydrate(context.Background(), req, ids)
	require.Nil(t, res.Err)
	require.Equal(t, string(TaskSucceeded), res.State)
	opens, gets, closes, n := provider.counts()
	require.Equal(t, 1, opens)
	require.Zero(t, gets)
	require.Equal(t, opens, closes)
	require.Equal(t, int64(len(payload)), n)
	_, reserves, releases, active, _ := tracker.snapshot()
	require.Equal(t, reserves, releases)
	require.Zero(t, active)
	require.True(t, backend.Tasks.CanCleanup(key))
	t.Logf("actual_archive_bytes=%d staging_reservations=%d", n, reserves)
}

func TestArchiveBudgetLyingSizeCannotReadWholeObject(t *testing.T) {
	backend, provider, key, req, ids, _ := newArchiveBudgetBackend(t, strings.Repeat("x", 8192))
	backend.Registry.mu.Lock()
	backend.Registry.manifest[key].RawFiles[0].SizeBytes = 1
	backend.Registry.manifest[key].Coverage.ByteCount = 1
	backend.Registry.mu.Unlock()
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	res := backend.Rehydrate(context.Background(), req, ids)
	require.NotNil(t, res.Err)
	opens, gets, closes, n := provider.counts()
	require.Equal(t, 1, opens)
	require.Zero(t, gets)
	require.Equal(t, 1, closes)
	require.LessOrEqual(t, n, int64(2), "清单上界之外仅允许一个越界探测字节")
	_, reserves, releases, active, _ := tracker.snapshot()
	require.Equal(t, reserves, releases)
	require.Zero(t, active)
	t.Logf("actual_archive_bytes=%d declared_archive_bytes=1", n)
}

func TestArchiveBudgetCancelledAndFailedRequestsReleaseExactlyOnce(t *testing.T) {
	for _, name := range []string{"cancelled_before", "cancelled_after_reserve", "read_failure", "publish_failure"} {
		t.Run(name, func(t *testing.T) {
			backend, provider, _, req, ids, _ := newArchiveBudgetBackend(t, "failure cleanup")
			tracker := &archiveBudgetTracker{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callbacks := tracker.callbacks()
			switch name {
			case "cancelled_before":
				cancel()
			case "cancelled_after_reserve":
				reserve := callbacks.ReserveStaging
				callbacks.ReserveStaging = func(ctx context.Context, n int64) (func(), error) {
					release, err := reserve(ctx, n)
					cancel()
					return release, err
				}
			case "read_failure":
				provider.readError = errors.New("constructed reader failure")
			case "publish_failure":
				backend.SetPublisher(publisherFunc(func(context.Context, PartitionKey, []ArchivedEvent, string) error {
					return errors.New("constructed publisher failure")
				}))
			}
			backend.SetResourceBudget(callbacks)
			res := backend.Rehydrate(ctx, req, ids)
			require.NotNil(t, res.Err)
			_, reserves, releases, active, _ := tracker.snapshot()
			if name != "cancelled_after_reserve" {
				require.Equal(t, reserves, releases)
			}
			require.Zero(t, active)
			if strings.HasPrefix(name, "cancelled_") {
				opens, gets, _, n := provider.counts()
				require.Zero(t, opens)
				require.Zero(t, gets)
				require.Zero(t, n)
			}
		})
	}
}

func TestArchiveBudgetConcurrentMergeDoesNotRereadOrReleaseOwner(t *testing.T) {
	backend, provider, key, req, ids, payload := newArchiveBudgetBackend(t, "shared work")
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	entered, finish := make(chan string, 1), make(chan struct{})
	backend.SetPublisher(publisherFunc(func(_ context.Context, _ PartitionKey, _ []ArchivedEvent, taskID string) error {
		entered <- taskID
		<-finish
		return nil
	}))
	first := make(chan query.ArchiveTaskResponse, 1)
	go func() { first <- backend.Rehydrate(context.Background(), req, ids) }()
	var taskID string
	select {
	case taskID = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("恢复没有进入发布阶段")
	}
	second := backend.Rehydrate(context.Background(), req, ids)
	require.Nil(t, second.Err)
	require.Equal(t, taskID, second.TaskID)
	opens, gets, _, n := provider.counts()
	require.Equal(t, 1, opens)
	require.Zero(t, gets)
	require.Equal(t, int64(len(payload)), n)
	_, _, releases, active, _ := tracker.snapshot()
	require.Zero(t, releases)
	require.Positive(t, active)
	require.False(t, backend.Tasks.CanCleanup(key))
	close(finish)
	res := <-first
	require.Nil(t, res.Err)
	_, reserves, released, active, _ := tracker.snapshot()
	require.Equal(t, reserves, released)
	require.Zero(t, active)
}

func TestArchiveBudgetTaskCancellationKeepsPhysicalConsumerReservation(t *testing.T) {
	backend, _, key, req, ids, _ := newArchiveBudgetBackend(t, "blocked publisher")
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	entered, finish := make(chan string, 1), make(chan struct{})
	backend.SetPublisher(publisherFunc(func(_ context.Context, _ PartitionKey, _ []ArchivedEvent, taskID string) error {
		entered <- taskID
		<-finish
		return nil
	}))
	result := make(chan query.ArchiveTaskResponse, 1)
	go func() { result <- backend.Rehydrate(context.Background(), req, ids) }()
	var taskID string
	select {
	case taskID = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("恢复没有进入发布阶段")
	}
	require.NoError(t, backend.Tasks.Cancel(taskID))
	_, _, released, active, _ := tracker.snapshot()
	require.Zero(t, released)
	require.Positive(t, active)
	require.False(t, backend.Tasks.CanCleanup(key), "任务取消不能提前摘除尚未退出消费者的 hold")
	close(finish)
	res := <-result
	require.NotNil(t, res.Err)
	require.NotEqual(t, string(TaskSucceeded), res.State)
	_, reserves, released, active, _ := tracker.snapshot()
	require.Equal(t, reserves, released)
	require.Zero(t, active)
	require.True(t, backend.Tasks.CanCleanup(key))
}
