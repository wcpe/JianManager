package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type manifestReadErrorProvider struct{ Provider }

func (p manifestReadErrorProvider) Exists(context.Context, PartitionKey, string) (bool, error) {
	return true, nil
}

func (p manifestReadErrorProvider) Get(context.Context, PartitionKey, string) ([]byte, error) {
	return nil, errors.New("object storage unavailable")
}

func TestRegistryManifestReadErrorIsNotTreatedAsMissing(t *testing.T) {
	reg := NewRegistry(manifestReadErrorProvider{NewMemoryProvider()})
	_, err := reg.GetManifest(testKey(1))
	require.ErrorContains(t, err, "object storage unavailable")
}

func testKey(gen uint64) PartitionKey {
	return PartitionKey{
		StorageNamespace: "ns/game-1",
		UTCDay:           "2026-09-20",
		Generation:       gen,
	}
}

func sampleManifest(key PartitionKey) *Manifest {
	m := NewManifest(key, "worker-log-parser/v2", "victorialogs/v1.52.0")
	m.TimeRange = TimeRange{
		FromUTC: "2026-09-20T00:00:00Z",
		ToUTC:   "2026-09-21T00:00:00Z",
	}
	m.Coverage = Coverage{
		Complete:          true,
		EventCount:        42,
		ByteCount:         1024,
		SourceGenerations: []SourceRef{{LogSourceID: "src-1", SourceGeneration: "g1"}},
		EnumerationState:  EnumerationExhausted,
	}
	payload := []byte("raw-payload")
	sum := ContentHash(payload)
	rf := RawFile{
		ObjectID:         ObjectIDFor(sum, int64(len(payload))),
		RelPath:          "raw/objects/" + sum,
		SizeBytes:        int64(len(payload)),
		ContentSHA256:    sum,
		Origin:           OriginWorkerStdio,
		State:            ObjectStateManaged,
		LogSourceID:      "src-1",
		SourceGeneration: "g1",
		ParserVersion:    "worker-log-parser/v2",
	}
	m.RawFiles = []RawFile{rf}
	m.Checksums.Objects = map[string]string{rf.RelPath: sum}
	return m
}

// TestManifestRoundtrip 表驱动：manifest 序列化往返必须保留全部契约字段。
func TestManifestRoundtrip(t *testing.T) {
	cases := []struct {
		name string
		key  PartitionKey
	}{
		{name: "generation-1", key: testKey(1)},
		{name: "generation-2", key: testKey(2)},
		{name: "nested-namespace", key: PartitionKey{StorageNamespace: "ns/a/b", UTCDay: "2026-01-01", Generation: 9}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := sampleManifest(tc.key)
			data, err := MarshalManifest(orig)
			require.NoError(t, err)

			got, err := UnmarshalManifest(data)
			require.NoError(t, err)
			require.Equal(t, orig.SchemaVersion, got.SchemaVersion)
			require.Equal(t, orig.ParserVersion, got.ParserVersion)
			require.Equal(t, orig.EngineVersion, got.EngineVersion)
			require.Equal(t, orig.Generation, got.Generation)
			require.Equal(t, orig.StorageNamespace, got.StorageNamespace)
			require.Equal(t, orig.UTCDay, got.UTCDay)
			require.Equal(t, orig.TimeRange, got.TimeRange)
			require.Equal(t, orig.Coverage, got.Coverage)
			require.Equal(t, orig.RawFiles, got.RawFiles)
			require.Equal(t, orig.Checksums.Objects, got.Checksums.Objects)

			// 二次 roundtrip 仍稳定。
			data2, err := MarshalManifest(got)
			require.NoError(t, err)
			require.JSONEq(t, string(data), string(data2))
		})
	}
}

func TestPartitionKeyValidate(t *testing.T) {
	cases := []struct {
		name    string
		key     PartitionKey
		wantErr bool
	}{
		{name: "ok", key: testKey(1), wantErr: false},
		{name: "empty-ns", key: PartitionKey{UTCDay: "2026-09-20", Generation: 1}, wantErr: true},
		{name: "empty-day", key: PartitionKey{StorageNamespace: "ns", Generation: 1}, wantErr: true},
		{name: "bad-day", key: PartitionKey{StorageNamespace: "ns", UTCDay: "20-09-2026", Generation: 1}, wantErr: true},
		{name: "zero-generation", key: PartitionKey{StorageNamespace: "ns", UTCDay: "2026-09-20"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.key.Validate()
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrInvalidKey)
			} else {
				require.NoError(t, err)
				require.Equal(t, "ns/game-1/2026-09-20/g1", testKey(1).String())
			}
		})
	}
}

// corruptOnPutProvider 在 Put 时写入被污染的字节，模拟上传中断/对象损坏。
type corruptOnPutProvider struct {
	Provider
	corrupt bool
}

func (c *corruptOnPutProvider) Put(ctx context.Context, key PartitionKey, relPath string, data []byte) error {
	if c.corrupt {
		poisoned := append([]byte(nil), data...)
		poisoned = append(poisoned, 'X')
		return c.Provider.Put(ctx, key, relPath, poisoned)
	}
	return c.Provider.Put(ctx, key, relPath, data)
}

// TestRegistryRegisterIdempotent 表驱动：幂等登记、路径冲突、受管状态。
func TestRegistryRegisterIdempotent(t *testing.T) {	ctx := context.Background()
	payload := []byte("stdio-primary-segment-payload")
	sum := ContentHash(payload)
	oid := ObjectIDFor(sum, int64(len(payload)))

	providers := map[string]func(t *testing.T) Provider{
		"local": func(t *testing.T) Provider { return NewLocalArchive(t.TempDir()) },
		"s3":    func(t *testing.T) Provider { return NewS3Provider("logs", "deep") },
		"minio": func(t *testing.T) Provider { return NewMinioProvider("logs", "deep") },
	}

	for pname, pfactory := range providers {
		t.Run(pname, func(t *testing.T) {
			reg := NewRegistry(pfactory(t))
			key := testKey(1)
			src := RawSource{
				Data:             payload,
				Origin:           OriginWorkerStdio,
				LogSourceID:      "src-stdio",
				SourceGeneration: "g1",
				ParserVersion:    DefaultParserVersion,
				EventCount:       3,
				EventTimeFromUTC: "2026-09-20T01:00:00Z",
				EventTimeToUTC:   "2026-09-20T02:00:00Z",
			}

			// 登记前：source 不是 archive。
			require.Equal(t, ObjectStateSourceOnly, reg.ObjectStateOf(key, oid))
			require.False(t, reg.IsManaged(key, oid))

			r1, err := reg.RegisterRaw(ctx, key, src)
			require.NoError(t, err)
			require.False(t, r1.AlreadyRegistered)
			require.Equal(t, ObjectStateManaged, r1.Object.State)
			require.Equal(t, oid, r1.Object.ObjectID)
			require.Equal(t, sum, r1.Object.ContentSHA256)
			require.True(t, reg.IsManaged(key, oid))

			// 幂等重登记：同内容不复制、不重复 raw_files。
			r2, err := reg.RegisterRaw(ctx, key, src)
			require.NoError(t, err)
			require.True(t, r2.AlreadyRegistered)
			require.Equal(t, oid, r2.Object.ObjectID)
			require.Len(t, r2.Manifest.RawFiles, 1)
			require.EqualValues(t, 3, r2.Manifest.Coverage.EventCount)
			require.Equal(t, "2026-09-20T01:00:00Z", r2.Manifest.TimeRange.FromUTC)

			// 同 rel_path 不同内容：禁止盲覆盖。
			_, err = reg.RegisterRaw(ctx, key, RawSource{
				Data:    []byte("different-payload"),
				Origin:  OriginWorkerStdio,
				RelPath: r1.Object.RelPath,
			})
			require.Error(t, err)
			var aerr *Error
			require.ErrorAs(t, err, &aerr)
			require.Equal(t, ReasonPathConflict, aerr.Reason)
			require.False(t, aerr.Retryable)

			// manifest 仍只有一条受管对象。
			m, err := reg.GetManifest(key)
			require.NoError(t, err)
			require.Len(t, m.RawFiles, 1)
			require.NoError(t, m.Validate())
		})
	}
}

// TestRegistryInstanceGzSourceUntilRegistered 实例 .gz 在登记前只是 source。
func TestRegistryInstanceGzSourceUntilRegistered(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "logs", "server.log.1.gz")
	require.NoError(t, os.MkdirAll(filepath.Dir(srcPath), 0o755))
	require.NoError(t, os.WriteFile(srcPath, []byte("gzip-bytes"), 0o644))

	reg := NewArchiveRegistryForTest(t)
	key := testKey(1)
	oid := ObjectIDFor(ContentHash([]byte("gzip-bytes")), int64(len("gzip-bytes")))

	// 源文件存在，但尚未登记 → 不是受管归档。
	require.True(t, SourceFilePathExists(srcPath))
	require.Equal(t, ObjectStateSourceOnly, reg.ObjectStateOf(key, oid))

	res, err := reg.RegisterRaw(ctx, key, RawSource{
		SourcePath:       srcPath,
		Origin:           OriginInstanceGz,
		LogSourceID:      "src-inst",
		SourceGeneration: "g7",
	})
	require.NoError(t, err)
	require.Equal(t, OriginInstanceGz, res.Object.Origin)
	require.Equal(t, ObjectStateManaged, res.Object.State)
	require.Equal(t, srcPath, res.Object.SourcePath)
	require.True(t, reg.IsManaged(key, oid))

	// 登记后受管副本在 provider 中，与实例源路径区分。
	require.NotEqual(t, srcPath, reg.PhysicalPath(key, res.Object.RelPath))
}

// TestRegistryGenerationIsolation 不同 generation 物理/manifest 隔离，互不覆盖。
func TestRegistryGenerationIsolation(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(NewLocalArchive(t.TempDir()))
	k1, k2 := testKey(1), testKey(2)
	payload := []byte("same-source-content")
	src := RawSource{Data: payload, Origin: OriginWorkerStdio, LogSourceID: "src", SourceGeneration: "g"}

	r1, err := reg.RegisterRaw(ctx, k1, src)
	require.NoError(t, err)
	m1Before, err := reg.GetManifest(k1)
	require.NoError(t, err)

	r2, err := reg.RegisterRaw(ctx, k2, src)
	require.NoError(t, err)
	require.False(t, r2.AlreadyRegistered)
	require.Equal(t, r1.Object.ObjectID, r2.Object.ObjectID, "same content identity across generations")

	// generation 2 登记不得改写 generation 1 manifest。
	m1After, err := reg.GetManifest(k1)
	require.NoError(t, err)
	require.Equal(t, m1Before.RawFiles, m1After.RawFiles)

	m2, err := reg.GetManifest(k2)
	require.NoError(t, err)
	require.Equal(t, uint64(2), m2.Generation)
	require.Len(t, m2.RawFiles, 1)

	// 物理路径按 generation 隔离。
	p1 := reg.PhysicalPath(k1, r1.Object.RelPath)
	p2 := reg.PhysicalPath(k2, r2.Object.RelPath)
	require.NotEqual(t, p1, p2)
	require.Contains(t, p1, string(filepath.Separator)+"g1"+string(filepath.Separator))
	require.Contains(t, p2, string(filepath.Separator)+"g2"+string(filepath.Separator))

	// 路径字符串直接体现 generation 段。
	require.Equal(t, "ns/game-1/2026-09-20/g1", k1.String())
	require.Equal(t, "ns/game-1/2026-09-20/g2", k2.String())
}

// TestRegistryDamagedObjectRetryable 损坏对象：结构化可重试，修复后登记成功。
func TestRegistryDamagedObjectRetryable(t *testing.T) {
	ctx := context.Background()
	base := NewLocalArchive(t.TempDir())
	flaky := &corruptOnPutProvider{Provider: base, corrupt: true}
	reg := NewRegistry(flaky)
	key := testKey(1)
	payload := []byte("will-be-corrupted-in-flight")

	_, err := reg.RegisterRaw(ctx, key, RawSource{Data: payload, Origin: OriginWorkerStdio})
	require.Error(t, err)
	var aerr *Error
	require.ErrorAs(t, err, &aerr)
	require.True(t, aerr.IsRetryable())
	require.Equal(t, ReasonChecksumMismatch, aerr.Reason)

	// 失败不得把对象登记为受管归档。
	oid := ObjectIDFor(ContentHash(payload), int64(len(payload)))
	require.False(t, reg.IsManaged(key, oid))
	if m, gerr := reg.GetManifest(key); gerr == nil {
		require.Empty(t, m.RawFiles, "damaged upload must not populate raw_files")
	}

	// 重试路径：provider 恢复后可成功。
	flaky.corrupt = false
	res, err := reg.RegisterRaw(ctx, key, RawSource{Data: payload, Origin: OriginWorkerStdio})
	require.NoError(t, err)
	require.Equal(t, ObjectStateManaged, res.Object.State)
	m, err := reg.GetManifest(key)
	require.NoError(t, err)
	require.Len(t, m.RawFiles, 1)
}

func TestMergeKeyStability(t *testing.T) {
	key := testKey(3)
	a := MergeKey(key, []string{"obj-a", "obj-b"})
	b := MergeKey(key, []string{"obj-b", "obj-a"})
	c := MergeKey(key, []string{"obj-a", "obj-b", "obj-a"})
	require.Equal(t, a, b)
	require.Equal(t, a, c)
	d := MergeKey(key, []string{"obj-a"})
	require.NotEqual(t, a, d)
	e := MergeKey(testKey(4), []string{"obj-a", "obj-b"})
	require.NotEqual(t, a, e, "generation must be part of merge key")
}

// TestRehydrateLeaseReuse 并发同合并键复用在途任务与租约。
func TestRehydrateLeaseReuse(t *testing.T) {
	mgr := NewRehydrateManager(RehydrateOptions{})
	key := testKey(1)
	req := RehydrateRequest{
		PartitionKey:     key,
		ArchiveObjectIDs: []string{"obj-b", "obj-a"},
		DiskReserveBytes: 128,
		Holder:           "worker-1",
		Events: []ArchivedEvent{
			{EventID: "e1", EventTimeUTC: "2026-09-20T10:00:00Z", Message: "hello"},
		},
	}

	origEvents := req.Events
	j1, err := mgr.Start(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, TaskRunning, j1.State)
	require.Equal(t, uint64(1), j1.Generation)

	// 顺序不同的同一 object 集合 → 同 merge key → 复用。
	req2 := req
	req2.ArchiveObjectIDs = []string{"obj-a", "obj-b"}
	j2, err := mgr.Start(context.Background(), req2)
	require.NoError(t, err)
	require.Equal(t, j1.TaskID, j2.TaskID)
	require.Equal(t, j1.MergeKey, j2.MergeKey)
	require.Equal(t, j1.Lease.LeaseID, j2.Lease.LeaseID)
	require.GreaterOrEqual(t, j2.Waiters, 2)

	// 不同 object 集合 → 新任务。
	req3 := req
	req3.ArchiveObjectIDs = []string{"obj-c"}
	j3, err := mgr.Start(context.Background(), req3)
	require.NoError(t, err)
	require.NotEqual(t, j1.TaskID, j3.TaskID)

	// 终态后同 key 可重新发起（幂等重恢复）。
	_, err = mgr.Complete(j1.TaskID, origEvents)
	require.NoError(t, err)
	j4, err := mgr.Start(context.Background(), req)
	require.NoError(t, err)
	require.NotEqual(t, j1.TaskID, j4.TaskID)
	require.Equal(t, j1.MergeKey, j4.MergeKey)
}

func TestRehydrateDiskReserve(t *testing.T) {
	var avail int64 = 50
	mgr := NewRehydrateManager(RehydrateOptions{
		DiskAvailable: func(PartitionKey) int64 { return avail },
	})
	key := testKey(1)
	req := RehydrateRequest{
		PartitionKey:     key,
		ArchiveObjectIDs: []string{"o1"},
		DiskReserveBytes: 100,
	}
	_, err := mgr.Start(context.Background(), req)
	require.Error(t, err)
	var aerr *Error
	require.ErrorAs(t, err, &aerr)
	require.Equal(t, ReasonDiskReserveUnmet, aerr.Reason)
	require.True(t, aerr.Retryable)

	avail = 1000
	j, err := mgr.Start(context.Background(), req)
	require.NoError(t, err)
	require.EqualValues(t, 100, j.DiskReserveBytes)
}

// TestRehydrateGenerationIsolation 清理 hold 按 generation 隔离；Query View 租约互斥清理。
func TestRehydrateGenerationIsolation(t *testing.T) {
	mgr := NewRehydrateManager(RehydrateOptions{})
	k1, k2 := testKey(1), testKey(2)

	j1, err := mgr.Start(context.Background(), RehydrateRequest{
		PartitionKey:     k1,
		ArchiveObjectIDs: []string{"o1"},
	})
	require.NoError(t, err)

	// gen1 任务 hold 阻止 gen1 清理，不阻止 gen2。
	require.False(t, mgr.CanCleanup(k1))
	require.True(t, mgr.CanCleanup(k2))
	holds1 := mgr.ActiveHolds(k1)
	require.Len(t, holds1, 1)
	require.Equal(t, HoldKindRehydrate, holds1[0].Kind)
	require.EqualValues(t, 1, holds1[0].Generation)

	// Query View 租约登记在 gen2：阻止 gen2，不影响「gen1 仍被 rehydrate hold」事实。
	qv, err := mgr.RegisterQueryViewLease(k2, "qv-lease-1", "view-1", "query", time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, HoldKindQueryView, qv.Kind)
	require.False(t, mgr.CanCleanup(k2))
	require.False(t, mgr.CanCleanup(k1))

	// 完成 gen1 任务 → rehydrate hold 释放；gen1 可清理。
	_, err = mgr.Complete(j1.TaskID, nil)
	require.NoError(t, err)
	require.True(t, mgr.CanCleanup(k1))
	require.False(t, mgr.CanCleanup(k2), "query view lease on gen2 still holds")

	// 释放 Query View 租约后 gen2 可清理。
	mgr.ReleaseQueryViewLease("qv-lease-1")
	require.True(t, mgr.CanCleanup(k2))
}

// TestRehydrateOriginalTimeUnchanged 恢复不得改写原始 _time。
func TestRehydrateOriginalTimeUnchanged(t *testing.T) {
	cases := []struct {
		name       string
		original   []ArchivedEvent
		restored   []ArchivedEvent
		wantErr    bool
		wantReason string
	}{
		{
			name: "times-preserved",
			original: []ArchivedEvent{
				{EventID: "e1", EventTimeUTC: "2026-09-20T00:00:01Z", Message: "a"},
				{EventID: "e2", EventTimeUTC: "2026-09-20T00:00:02Z", Message: "b"},
			},
			restored: []ArchivedEvent{
				{EventID: "e1", EventTimeUTC: "2026-09-20T00:00:01Z", Message: "a"},
				{EventID: "e2", EventTimeUTC: "2026-09-20T00:00:02Z", Message: "b"},
			},
			wantErr: false,
		},
		{
			name: "time-mutated-rejected",
			original: []ArchivedEvent{
				{EventID: "e1", EventTimeUTC: "2026-09-20T00:00:01Z"},
			},
			restored: []ArchivedEvent{
				{EventID: "e1", EventTimeUTC: "2026-09-21T00:00:01Z"},
			},
			wantErr:    true,
			wantReason: ReasonOriginalTimeMutated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewRehydrateManager(RehydrateOptions{})
			key := testKey(1)
			job, err := mgr.Start(context.Background(), RehydrateRequest{
				PartitionKey:     key,
				ArchiveObjectIDs: []string{"obj"},
				Events:           tc.original,
			})
			require.NoError(t, err)

			done, err := mgr.Complete(job.TaskID, tc.restored)
			if tc.wantErr {
				require.Error(t, err)
				var aerr *Error
				require.ErrorAs(t, err, &aerr)
				require.Equal(t, tc.wantReason, aerr.Reason)
				require.Equal(t, TaskFailed, done.State)
				require.Equal(t, tc.wantReason, done.Reason)
				require.Nil(t, done.Result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, TaskSucceeded, done.State)
			require.NotNil(t, done.Result)
			require.Equal(t, key, done.Result.PartitionKey)
			require.NoError(t, VerifyTimesUnchanged(tc.original, done.Result.Events))
			for _, orig := range tc.original {
				require.Equal(t, orig.EventTimeUTC, done.Result.OriginalTimes[orig.EventID])
			}
			// RestoreEvents 也不得改写时间。
			restored := RestoreEvents(tc.original)
			require.NoError(t, VerifyTimesUnchanged(tc.original, restored))
		})
	}
}

func TestRehydrateCancelAndTimeout(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		mgr := NewRehydrateManager(RehydrateOptions{})
		j, err := mgr.Start(context.Background(), RehydrateRequest{
			PartitionKey:     testKey(1),
			ArchiveObjectIDs: []string{"o"},
		})
		require.NoError(t, err)
		require.False(t, mgr.CanCleanup(testKey(1)))
		require.NoError(t, mgr.Cancel(j.TaskID))
		got, err := mgr.Get(j.TaskID)
		require.NoError(t, err)
		require.Equal(t, TaskCancelled, got.State)
		require.Equal(t, ReasonTaskCancelled, got.Reason)
		require.True(t, mgr.CanCleanup(testKey(1)))
	})

	t.Run("timeout", func(t *testing.T) {
		base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
		now := base
		mgr := NewRehydrateManager(RehydrateOptions{
			DefaultTimeout: 10 * time.Second,
			Now:            func() time.Time { return now },
		})
		j, err := mgr.Start(context.Background(), RehydrateRequest{
			PartitionKey:     testKey(1),
			ArchiveObjectIDs: []string{"o"},
			Timeout:          10 * time.Second,
		})
		require.NoError(t, err)
		require.False(t, mgr.CanCleanup(testKey(1)))

		now = base.Add(30 * time.Second)
		_, err = mgr.Complete(j.TaskID, nil)
		require.Error(t, err)
		var aerr *Error
		require.ErrorAs(t, err, &aerr)
		require.Equal(t, ReasonTaskTimeout, aerr.Reason)
		got, err := mgr.Get(j.TaskID)
		require.NoError(t, err)
		require.Equal(t, TaskStale, got.State)
		require.True(t, mgr.CanCleanup(testKey(1)))
	})
}

// TestProviderParity 三种 Provider 同接口行为一致（S3/MinIO 为内存 stub，无需真实服务）。
func TestProviderParity(t *testing.T) {
	key := testKey(2)
	rel := "raw/objects/abc"
	data := []byte("parity-payload")

	providers := map[string]Provider{
		"local": NewLocalArchive(t.TempDir()),
		"s3":    NewS3Provider("bucket", "prefix"),
		"minio": NewMinioProvider("bucket", "prefix"),
	}

	for name, p := range providers {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			require.NotEmpty(t, p.Kind())

			exists, err := p.Exists(ctx, key, rel)
			require.NoError(t, err)
			require.False(t, exists)

			require.NoError(t, p.Put(ctx, key, rel, data))
			exists, err = p.Exists(ctx, key, rel)
			require.NoError(t, err)
			require.True(t, exists)

			got, err := p.Get(ctx, key, rel)
			require.NoError(t, err)
			require.Equal(t, data, got)

			list, err := p.List(ctx, key)
			require.NoError(t, err)
			require.Contains(t, list, rel)

			// generation 隔离：gen3 路径不应看到 gen2 对象。
			exists, err = p.Exists(ctx, testKey(3), rel)
			require.NoError(t, err)
			require.False(t, exists)

			require.NoError(t, p.Delete(ctx, key, rel))
			exists, err = p.Exists(ctx, key, rel)
			require.NoError(t, err)
			require.False(t, exists)
		})
	}
}

// TestRegistryAcrossProviders 受管登记在 Local/S3/MinIO 上语义一致。
func TestRegistryAcrossProviders(t *testing.T) {
	ctx := context.Background()
	payload := []byte("cross-provider")
	providers := map[string]Provider{
		"local": NewLocalArchive(t.TempDir()),
		"s3":    NewS3Provider("b", ""),
		"minio": NewMinioProvider("b", ""),
	}
	var ids []string
	for name, p := range providers {
		reg := NewRegistry(p)
		key := testKey(1)
		res, err := reg.RegisterRaw(ctx, key, RawSource{Data: payload, Origin: OriginWorkerStdio})
		require.NoError(t, err, name)
		require.True(t, reg.IsManaged(key, res.Object.ObjectID), name)
		ids = append(ids, res.Object.ObjectID)
	}
	require.Equal(t, ids[0], ids[1])
	require.Equal(t, ids[1], ids[2])
}

func TestManifestJSONShape(t *testing.T) {
	// 契约字段必须出现在 JSON 中（防止序列化 tag 回归）。
	m := sampleManifest(testKey(1))
	data, err := json.Marshal(m)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	for _, field := range []string{
		"schema_version", "parser_version", "engine_version", "generation",
		"checksums", "time_range", "coverage", "raw_files",
	} {
		require.Contains(t, raw, field, "manifest json must carry %s", field)
	}
}

func TestErrorsAreStructured(t *testing.T) {
	err := newError("RegisterRaw", "ns/day/g1", ReasonPathConflict, false,
		fmt.Errorf("%w: boom", ErrPathConflict))
	require.True(t, errors.Is(err, ErrPathConflict))
	require.False(t, err.IsRetryable())
	require.Contains(t, err.Error(), ReasonPathConflict)
	require.Contains(t, err.Error(), "ns/day/g1")
}

// NewArchiveRegistryForTest 便捷构造。
func NewArchiveRegistryForTest(t *testing.T) *Registry {
	t.Helper()
	return NewRegistry(NewLocalArchive(t.TempDir()))
}

// TestRegistryEngineVersionOverride manifest 记录受管 VL 真实 build_id（FR-475），而非占位。
func TestRegistryEngineVersionOverride(t *testing.T) {
	ctx := context.Background()
	reg := NewRegistry(NewLocalArchive(t.TempDir()))
	reg.SetEngineVersion("victorialogs/20260716-022147-tags-v1.52.0-0-g46a54c9")
	key := testKey(2)
	_, err := reg.RegisterRaw(ctx, key, RawSource{Data: []byte("x"), LogSourceID: "s", SourceGeneration: "g1", EventCount: 1})
	require.NoError(t, err)
	mf, err := reg.Seal(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "victorialogs/20260716-022147-tags-v1.52.0-0-g46a54c9", mf.EngineVersion)
}
