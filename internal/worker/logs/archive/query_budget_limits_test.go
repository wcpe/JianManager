package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type archiveReusablePublisher struct {
	publisherFunc
	reusable bool
}

func (p *archiveReusablePublisher) CanReuseRehydrate(context.Context, PartitionKey, string) bool {
	return p.reusable
}

func TestArchiveBudgetCompletedReusePreservesSummaryWithoutRetainedBodies(t *testing.T) {
	backend, provider, _, req, ids, payload := newArchiveBudgetBackend(t, strings.Repeat("retained body must disappear", 1024))
	tracker := &archiveBudgetTracker{}
	callbacks := tracker.callbacks()
	reserve := callbacks.ReserveStaging
	callbacks.ReserveStaging = func(ctx context.Context, n int64) (func(), error) {
		release, err := reserve(ctx, n)
		if err != nil {
			return nil, err
		}
		return func() {
			backend.Tasks.mu.Lock()
			for _, entry := range backend.Tasks.byTask {
				entry.mu.Lock()
				require.Zero(t, len(entry.events), "释放预留前必须先摘除生产任务正文")
				if entry.job.Result != nil {
					require.Zero(t, len(entry.job.Result.Events))
					require.Zero(t, len(entry.job.Result.OriginalTimes))
				}
				entry.mu.Unlock()
			}
			backend.Tasks.mu.Unlock()
			release()
		}, nil
	}
	backend.SetResourceBudget(callbacks)
	publishes := 0
	publisher := &archiveReusablePublisher{reusable: true, publisherFunc: func(context.Context, PartitionKey, []ArchivedEvent, string) error { publishes++; return nil }}
	backend.SetPublisher(publisher)
	first := backend.Rehydrate(context.Background(), req, ids)
	require.Nil(t, first.Err)
	job, err := backend.Tasks.Get(first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, job.Result)
	require.Zero(t, len(job.Result.Events))
	require.Zero(t, len(job.Result.OriginalTimes))
	summary, ok := backend.PublishedRehydrateSummary(first.TaskID)
	require.True(t, ok)
	require.Equal(t, uint64(1), summary.EventCount)
	require.True(t, summary.Verified)
	require.True(t, summary.Published)
	require.Equal(t, "2026-09-22T12:00:00Z", summary.TimeRange.FromUTC)
	require.Equal(t, "2026-09-22T12:00:00.000000001Z", summary.TimeRange.ToUTC)
	beforeAdmissions, beforeReserves, _, _, _ := tracker.snapshot()
	tracker.admitErr = errors.New("new work denied after successful recovery")
	second := backend.Rehydrate(context.Background(), req, ids)
	require.Nil(t, second.Err)
	require.Equal(t, first.TaskID, second.TaskID)
	require.Equal(t, string(TaskSucceeded), second.State)
	afterAdmissions, afterReserves, _, active, _ := tracker.snapshot()
	require.Equal(t, beforeAdmissions, afterAdmissions)
	require.Equal(t, beforeReserves, afterReserves)
	require.Zero(t, active)
	opens, gets, _, n := provider.counts()
	require.Equal(t, 1, opens)
	require.Zero(t, gets)
	require.Equal(t, int64(len(payload)), n)
	require.Equal(t, 1, publishes)
	otherScope := req
	otherScope.PermissionScope = "different-authorized-scope"
	third := backend.Rehydrate(context.Background(), otherScope, ids)
	require.NotNil(t, third.Err, "不同权限scope不能绕过重新准入")
	publisher.reusable = false
	fourth := backend.Rehydrate(context.Background(), req, ids)
	require.NotNil(t, fourth.Err, "当前发布凭据失效时不得复用历史成功")
	t.Logf("actual_archive_bytes=%d retained_events=%d summary_events=%d successful_reuse_reads=0", n, len(job.Result.Events), summary.EventCount)
}

func TestArchiveBudgetBodyReservationDeniedBeforeRead(t *testing.T) {
	backend, provider, _, req, ids, _ := newArchiveBudgetBackend(t, "second reservation denied")
	tracker := &archiveBudgetTracker{}
	callbacks := tracker.callbacks()
	reserve := callbacks.ReserveStaging
	calls := 0
	callbacks.ReserveStaging = func(ctx context.Context, n int64) (func(), error) {
		calls++
		if calls == 2 {
			return nil, errors.New("body staging denied")
		}
		return reserve(ctx, n)
	}
	backend.SetResourceBudget(callbacks)
	res := backend.Rehydrate(context.Background(), req, ids)
	require.NotNil(t, res.Err)
	require.Equal(t, 2, calls)
	opens, gets, _, n := provider.counts()
	require.Zero(t, opens)
	require.Zero(t, gets)
	require.Zero(t, n)
	_, reserves, releases, active, _ := tracker.snapshot()
	require.Equal(t, reserves, releases)
	require.Zero(t, active)
}

func TestArchiveBudgetMetadataColdLoadAndOversizeAreBounded(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid_restart", true: "oversize_manifest"}[oversize], func(t *testing.T) {
			backend, provider, _, req, ids, payload := newArchiveBudgetBackend(t, "bounded metadata reload")
			backend.Registry = NewRegistry(provider)
			if oversize {
				provider.manifestPayload = bytes.Repeat([]byte(" "), int(archiveManifestBytes)+8192)
			}
			tracker := &archiveBudgetTracker{}
			backend.SetResourceBudget(tracker.callbacks())
			res := backend.Rehydrate(context.Background(), req, ids)
			if oversize {
				require.NotNil(t, res.Err)
			} else {
				require.Nil(t, res.Err)
			}
			provider.mu.Lock()
			metadataBytes, metadataOpens := provider.manifestReadBytes, provider.manifestOpens
			provider.mu.Unlock()
			require.Equal(t, 1, metadataOpens)
			require.LessOrEqual(t, metadataBytes, archiveManifestBytes+1)
			opens, gets, _, n := provider.counts()
			require.Zero(t, gets)
			if oversize {
				require.Zero(t, opens)
				require.Zero(t, n)
				require.Equal(t, archiveManifestBytes+1, metadataBytes)
			} else {
				require.Equal(t, int64(len(payload)), n)
			}
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
			t.Logf("actual_manifest_bytes=%d actual_archive_bytes=%d", metadataBytes, n)
		})
	}
}

func TestArchiveBudgetRejectsOversizeLineAndRecordAmplification(t *testing.T) {
	for _, name := range []string{"oversize_line", "record_amplification"} {
		t.Run(name, func(t *testing.T) {
			message := "record count bound"
			if name == "oversize_line" {
				message = strings.Repeat("x", archiveMaxLineBytes)
			}
			backend, provider, key, req, ids, payload := newArchiveBudgetBackend(t, message)
			if name == "record_amplification" {
				payload = append(append([]byte(nil), payload...), payload...)
				provider.payload = payload
				sum := ContentHash(payload)
				backend.Registry.mu.Lock()
				manifest := backend.Registry.manifest[key]
				manifest.RawFiles[0].ObjectID = sum
				manifest.RawFiles[0].ContentSHA256 = sum
				manifest.RawFiles[0].SizeBytes = int64(len(payload))
				manifest.Checksums.Objects[manifest.RawFiles[0].RelPath] = sum
				manifest.Coverage.ByteCount = uint64(len(payload))
				backend.Registry.mu.Unlock()
				ids[0] = sum
			}
			tracker := &archiveBudgetTracker{}
			backend.SetResourceBudget(tracker.callbacks())
			res := backend.Rehydrate(context.Background(), req, ids)
			require.NotNil(t, res.Err)
			require.NotEqual(t, string(TaskSucceeded), res.State)
			_, gets, closes, n := provider.counts()
			require.Zero(t, gets)
			require.Equal(t, 1, closes)
			if name == "oversize_line" {
				require.LessOrEqual(t, n, int64(archiveMaxLineBytes))
			} else {
				require.ErrorContains(t, res.Err, "record count exceeds")
			}
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
			t.Logf("actual_archive_bytes=%d failure=%s", n, res.Err.Message)
		})
	}
}

type archiveLegacyOnlyProvider struct{ Provider }

func TestArchiveBudgetUnknownProviderNeverFallsBackToWholeGet(t *testing.T) {
	backend, provider, _, req, ids, _ := newArchiveBudgetBackend(t, "legacy provider lacks bounded IO")
	backend.Registry.provider = archiveLegacyOnlyProvider{Provider: provider}
	backend.SetResourceBudget((&archiveBudgetTracker{}).callbacks())
	res := backend.Rehydrate(context.Background(), req, ids)
	require.NotNil(t, res.Err)
	require.Contains(t, res.Err.Message, "no bounded object reader")
	opens, gets, _, n := provider.counts()
	require.Zero(t, opens)
	require.Zero(t, gets)
	require.Zero(t, n)
}

func TestArchiveBudgetArchiveStatusUsesSamePreReadGateAndPartialCoverage(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "denied"}[deny], func(t *testing.T) {
			backend, provider, _, req, ids, payload := newArchiveBudgetBackend(t, "archive status")
			tracker := &archiveBudgetTracker{}
			if deny {
				tracker.admitErr = errors.New("background status scan denied")
			}
			backend.SetResourceBudget(tracker.callbacks())
			res := backend.ArchiveStatus(context.Background(), req, ids)
			opens, gets, closes, n := provider.counts()
			require.Zero(t, gets)
			if deny {
				require.NotNil(t, res.Err)
				require.False(t, res.Coverage.Complete)
				require.Contains(t, res.Coverage.PartialReasons, query.ReasonArchiveNotRestored)
				require.Empty(t, res.AvailableIDs)
				require.Zero(t, opens)
				require.Zero(t, n)
			} else {
				require.Nil(t, res.Err)
				require.True(t, res.Coverage.Complete)
				require.Equal(t, ids, res.AvailableIDs)
				require.Equal(t, int64(len(payload)), n)
				require.Equal(t, opens, closes)
			}
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
		})
	}
}

func TestArchiveBudgetTTLExpirationCannotReleaseBlockedConsumer(t *testing.T) {
	backend, _, key, req, ids, _ := newArchiveBudgetBackend(t, "expired task with real consumer")
	base := time.Now()
	var advanced atomic.Int64
	backend.Tasks = NewRehydrateManager(RehydrateOptions{DefaultTimeout: time.Minute, Now: func() time.Time { return base.Add(time.Duration(advanced.Load())) }})
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	entered, finish := make(chan struct{}), make(chan struct{})
	backend.SetPublisher(publisherFunc(func(context.Context, PartitionKey, []ArchivedEvent, string) error {
		close(entered)
		<-finish
		return nil
	}))
	result := make(chan query.ArchiveTaskResponse, 1)
	go func() { result <- backend.Rehydrate(context.Background(), req, ids) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("消费者未进入发布")
	}
	advanced.Store(int64(2 * time.Minute))
	other := key
	other.Generation++
	_, err := backend.Tasks.Start(context.Background(), RehydrateRequest{PartitionKey: other, ArchiveObjectIDs: []string{"other-object"}})
	require.NoError(t, err)
	require.False(t, backend.Tasks.CanCleanup(key), "TTL扫描不能拆除仍在执行的消费者pin")
	_, _, releases, active, _ := tracker.snapshot()
	require.Zero(t, releases)
	require.Positive(t, active)
	close(finish)
	res := <-result
	require.NotNil(t, res.Err)
	require.Equal(t, string(TaskStale), res.State)
	require.True(t, backend.Tasks.CanCleanup(key))
	_, reserves, releases, active, _ := tracker.snapshot()
	require.Equal(t, reserves, releases)
	require.Zero(t, active)
}

type archiveLocalReadProbe struct {
	*LocalArchive
	mu    sync.Mutex
	bytes int64
}
type archiveProbeReader struct {
	io.ReadCloser
	owner *archiveLocalReadProbe
}

func (p *archiveLocalReadProbe) OpenArchiveObject(ctx context.Context, key PartitionKey, path string) (io.ReadCloser, error) {
	r, err := p.LocalArchive.OpenArchiveObject(ctx, key, path)
	if err != nil {
		return nil, err
	}
	return &archiveProbeReader{ReadCloser: r, owner: p}, nil
}
func (r *archiveProbeReader) Read(dst []byte) (int, error) {
	n, err := r.ReadCloser.Read(dst)
	r.owner.mu.Lock()
	r.owner.bytes += int64(n)
	r.owner.mu.Unlock()
	return n, err
}

func TestArchiveBudgetRealLocalFileUsesTypedControllerAndPreservesWALDrain(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:local-budget", UTCDay: "2026-09-22", Generation: 1}
	payload, err := json.Marshal(archivedTestEvent(key.UTCDay, "real local file"))
	require.NoError(t, err)
	payload = append(payload, '\n')
	provider := &archiveLocalReadProbe{LocalArchive: NewLocalArchive(t.TempDir())}
	reg := NewRegistry(provider)
	registered, err := reg.RegisterRaw(ctx, key, RawSource{Data: payload, EventCount: 1})
	require.NoError(t, err)
	_, err = reg.Seal(ctx, key)
	require.NoError(t, err)
	controller, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{DiskBytes: 256 << 20, RSSBytes: 128 << 20, Reservations: map[vlsup.ReservationKind]int64{vlsup.ReservationStaging: 64 << 20}})
	require.NoError(t, err)
	sample := vlsup.ResourceSample{Namespaces: map[vlsup.Namespace]vlsup.NamespaceSample{vlsup.NamespaceHot: {RSSKnown: true}}, WorkerRSSKnown: true, WorkerRSSBytes: 16 << 20, DiskKnown: true, DiskUsagePercent: 1, DiskBytesKnown: true, DiskUsedBytes: 1 << 20, SampledAt: time.Now()}
	controller.Update(sample)
	backend := NewQueryBackend(reg, NewRehydrateManager(RehydrateOptions{}))
	backend.SetPublisher(publisherFunc(func(context.Context, PartitionKey, []ArchivedEvent, string) error { return nil }))
	backend.SetResourceBudget(RehydrateResourceBudget{Admit: func(context.Context) error { return controller.Admit(vlsup.WorkBackground) }, ReserveStaging: func(_ context.Context, n int64) (func(), error) {
		return controller.Reserve(vlsup.ReservationStaging, n)
	}})
	req := query.QueryRequest{AuthorizedTargets: []string{key.StorageNamespace + "/" + key.UTCDay}}
	res := backend.Rehydrate(ctx, req, []string{registered.Object.ObjectID})
	require.Nil(t, res.Err)
	provider.mu.Lock()
	actual := provider.bytes
	provider.mu.Unlock()
	require.Equal(t, int64(len(payload)), actual)
	release, err := controller.Reserve(vlsup.ReservationStaging, 64<<20)
	require.NoError(t, err, "恢复结束后完整staging额度可重新获得")
	release()
	sample.DiskUsagePercent = 90
	controller.Update(sample)
	rejected := backend.Rehydrate(ctx, req, []string{registered.Object.ObjectID})
	require.NotNil(t, rejected.Err)
	provider.mu.Lock()
	after := provider.bytes
	provider.mu.Unlock()
	require.Equal(t, actual, after)
	require.NoError(t, controller.Admit(vlsup.WorkWALDrain), "归档预算不足不能阻塞有界WAL排空")
	data, err := provider.Get(ctx, key, registered.Object.RelPath)
	require.NoError(t, err)
	require.Equal(t, payload, data, "预算拒绝不能删改归档原物")
	t.Logf("actual_archive_file_bytes=%d typed_controller=true source_preserved=true", actual)
}

func TestArchiveBudgetManifestDecodeAmplificationCannotBypassPreflight(t *testing.T) {
	for _, field := range []string{"raw_files", "RAW_FILES", "raw_fileſ", "source_generations", "objects", "depth"} {
		t.Run(field, func(t *testing.T) {
			backend, provider, _, req, ids, _ := newArchiveBudgetBackend(t, "metadata allocation bound")
			backend.Registry = NewRegistry(provider)
			var content string
			switch field {
			case "source_generations":
				content = `{"coverage":{"source_generations":[` + strings.Repeat(`{},`, int(archiveMaxEvents)) + `{}]}}`
			case "objects":
				content = `{"checksums":{"objects":{` + strings.Repeat(`"x":"",`, archiveMaxObjects) + `"last":""}}}`
			case "depth":
				content = strings.Repeat(`[`, 66) + `0` + strings.Repeat(`]`, 66)
			default:
				content = `{"` + field + `":[` + strings.Repeat(`{},`, archiveMaxObjects) + `{}]}`
			}
			provider.manifestPayload = []byte(content)
			tracker := &archiveBudgetTracker{}
			backend.SetResourceBudget(tracker.callbacks())
			res := backend.Rehydrate(context.Background(), req, ids)
			require.NotNil(t, res.Err)
			require.Contains(t, res.Err.Message, "bound")
			opens, gets, _, n := provider.counts()
			require.Zero(t, opens)
			require.Zero(t, gets)
			require.Zero(t, n)
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
		})
	}
}

func TestArchiveBudgetDuplicateObjectIDsDoNotDuplicateReadOrReservation(t *testing.T) {
	backend, provider, _, req, ids, payload := newArchiveBudgetBackend(t, "duplicate object id")
	tracker := &archiveBudgetTracker{}
	backend.SetResourceBudget(tracker.callbacks())
	res := backend.Rehydrate(context.Background(), req, []string{ids[0], ids[0]})
	require.Nil(t, res.Err)
	opens, gets, _, n := provider.counts()
	require.Equal(t, 1, opens)
	require.Zero(t, gets)
	require.Equal(t, int64(len(payload)), n)
	_, reserves, releases, active, _ := tracker.snapshot()
	require.Equal(t, 2, reserves)
	require.Equal(t, reserves, releases)
	require.Zero(t, active)
}

func TestArchiveBudgetCaseSensitiveObjectPathsRemainValid(t *testing.T) {
	ctx := context.Background()
	key := PartitionKey{StorageNamespace: "node:case-budget", UTCDay: "2026-09-22", Generation: 1}
	provider := NewMemoryProvider()
	registry := NewRegistry(provider)
	var ids []string
	for i, path := range []string{"raw/A.log", "raw/a.log"} {
		event := archivedTestEvent(key.UTCDay, path)
		if i == 1 {
			event.RecordStart, event.RecordEnd = 8, 16
			event.EventID = logtypes.EventID(logtypes.SourceIdentity{LogSourceID: event.LogSourceID, SourceGeneration: event.SourceGeneration, ParserVersion: event.ParserVersion}, logtypes.RecordRange{Start: event.RecordStart, End: event.RecordEnd})
		}
		payload, err := json.Marshal(event)
		require.NoError(t, err)
		registered, err := registry.RegisterRaw(ctx, key, RawSource{Data: append(payload, '\n'), RelPath: path, EventCount: 1})
		require.NoError(t, err)
		ids = append(ids, registered.Object.ObjectID)
	}
	manifest, err := registry.Seal(ctx, key)
	require.NoError(t, err)
	require.Len(t, manifest.Checksums.Objects, 2)
	require.NotEqual(t, manifest.Checksums.Objects["raw/A.log"], manifest.Checksums.Objects["raw/a.log"])
	data, err := MarshalManifest(manifest)
	require.NoError(t, err)
	require.NoError(t, preflightManifestJSON(json.NewDecoder(bytes.NewReader(data)), "root", 0))
	backend := NewQueryBackend(NewRegistry(provider), NewRehydrateManager(RehydrateOptions{}))
	backend.SetResourceBudget((&archiveBudgetTracker{}).callbacks())
	backend.SetPublisher(publisherFunc(func(_ context.Context, _ PartitionKey, events []ArchivedEvent, _ string) error {
		require.Len(t, events, 2)
		require.Equal(t, "raw/A.log", events[0].Message)
		require.Equal(t, "raw/a.log", events[1].Message)
		return nil
	}))
	res := backend.Rehydrate(ctx, query.QueryRequest{AuthorizedTargets: []string{key.StorageNamespace + "/" + key.UTCDay}}, ids)
	require.Nil(t, res.Err)
	require.Equal(t, string(TaskSucceeded), res.State)
}

func TestArchiveBudgetDuplicateManifestContainersCannotMergePastPreflight(t *testing.T) {
	left, right := make(map[string]string), make(map[string]string)
	for i := 0; i < 512; i++ {
		left["left-"+strconv.Itoa(i)] = "test-hash"
	}
	for i := 0; i < 513; i++ {
		right["right-"+strconv.Itoa(i)] = "test-hash"
	}
	leftJSON, err := json.Marshal(left)
	require.NoError(t, err)
	rightJSON, err := json.Marshal(right)
	require.NoError(t, err)
	for _, name := range []string{"objects", "ObJeCtS", "objectſ", "checksums", "CHECKSUMS", "checkſumſ"} {
		t.Run(name, func(t *testing.T) {
			backend, provider, key, req, ids, _ := newArchiveBudgetBackend(t, "duplicate manifest containers")
			manifest, err := backend.Registry.GetManifest(key)
			require.NoError(t, err)
			base, err := MarshalManifest(manifest)
			require.NoError(t, err)
			checksums, err := json.Marshal(manifest.Checksums)
			require.NoError(t, err)
			fragment := `"checksums":{"objects":` + string(leftJSON) + `,"` + name + `":` + string(rightJSON) + `}`
			if strings.EqualFold(name, "checksums") {
				fragment = `"checksums":{"objects":` + string(leftJSON) + `},"` + name + `":{"objects":` + string(rightJSON) + `}`
			}
			payload := []byte(strings.Replace(string(base), `"checksums":`+string(checksums), fragment, 1))
			// 仅在构造夹具内解码1025项，证明标准解码器确实会合并，而非只以静态推断代替红例。
			decoded, err := UnmarshalManifest(payload)
			require.NoError(t, err)
			require.Len(t, decoded.Checksums.Objects, 1025)
			err = preflightManifestJSON(json.NewDecoder(bytes.NewReader(payload)), "root", 0)
			t.Logf("typed_merged_entries=%d manifest_bytes=%d preflight_error=%v", len(decoded.Checksums.Objects), len(payload), err)
			require.Error(t, err, "重复规范容器必须在强类型解码之前拒绝")
			require.Contains(t, err.Error(), "duplicate")
			backend.Registry = NewRegistry(provider)
			provider.manifestPayload = payload
			tracker := &archiveBudgetTracker{}
			backend.SetResourceBudget(tracker.callbacks())
			res := backend.Rehydrate(context.Background(), req, ids)
			require.NotNil(t, res.Err)
			require.Contains(t, res.Err.Message, "duplicate")
			opens, gets, _, n := provider.counts()
			require.Zero(t, opens)
			require.Zero(t, gets)
			require.Zero(t, n)
			_, reserves, releases, active, _ := tracker.snapshot()
			require.Equal(t, reserves, releases)
			require.Zero(t, active)
		})
	}
}
