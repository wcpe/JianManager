package service

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/blobstore"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// fakeReconcileStore 内存假 BlobStore：对账测试专用——ListPage 按前缀过滤 + 字典序 +
// 末键游标分页（与 s3 语义对齐），Delete 记录删除键，可注入阻塞/失败。
type fakeReconcileStore struct {
	mu         sync.Mutex
	objects    map[string]blobstore.ObjectInfo
	deleted    []string
	listCalls  int
	failDelete bool
	// blockList 非 nil 时 ListPage 阻塞至通道关闭（在途去重并发测试用）。
	blockList chan struct{}
}

func newFakeReconcileStore() *fakeReconcileStore {
	return &fakeReconcileStore{objects: map[string]blobstore.ObjectInfo{}}
}

func (f *fakeReconcileStore) put(key string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = blobstore.ObjectInfo{Key: key, Size: size, ModTime: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeReconcileStore) Kind() string { return blobstore.KindS3 }

func (f *fakeReconcileStore) PutFile(_ context.Context, key, _ string, size int64) error {
	f.put(key, size)
	return nil
}

func (f *fakeReconcileStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, blobstore.ErrBlobNotFound
}

func (f *fakeReconcileStore) Stat(_ context.Context, key string) (*blobstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.objects[key]
	if !ok {
		return nil, blobstore.ErrBlobNotFound
	}
	return &info, nil
}

func (f *fakeReconcileStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete {
		return fmt.Errorf("模拟删除失败")
	}
	f.deleted = append(f.deleted, key)
	delete(f.objects, key)
	return nil
}

func (f *fakeReconcileStore) List(ctx context.Context, prefix string, limit int) ([]blobstore.ObjectInfo, error) {
	out, _, err := f.ListPage(ctx, prefix, limit, "")
	return out, err
}

func (f *fakeReconcileStore) ListPage(_ context.Context, prefix string, limit int, token string) ([]blobstore.ObjectInfo, string, error) {
	if f.blockList != nil {
		<-f.blockList
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if limit <= 0 {
		limit = 1000
	}
	keys := make([]string, 0, len(f.objects))
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) && k > token {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	truncated := len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	out := make([]blobstore.ObjectInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, f.objects[k])
	}
	next := ""
	if truncated {
		next = keys[len(keys)-1]
	}
	return out, next, nil
}

func (f *fakeReconcileStore) Presign(string, time.Duration) (string, error) {
	return "", blobstore.ErrPresignUnsupported
}

// reconcileHarness 对账服务测试基座：内存库 + 渠道服务 + 假 store 注入。
type reconcileHarness struct {
	db       *gorm.DB
	channels *ArtifactStorageChannelService
	svc      *ArtifactReconcileService
	store    *fakeReconcileStore
	ch       *model.ArtifactStorageChannel
}

func newReconcileHarness(t *testing.T) *reconcileHarness {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Asset{}, &model.ArtifactStorageChannel{},
		&model.ArtifactReconcileRun{}, &model.ArtifactReconcileDiff{}, &model.ArtifactReconcileSetting{},
	))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)

	channels := NewArtifactStorageChannelService(db, root)
	enc, eerr := newKeyEncryptor(DevKeyEncSecretBase64)
	require.NoError(t, eerr)
	channels.SetKeyEncryptor(enc)
	require.NoError(t, channels.EnsureBuiltin())
	ch, cerr := channels.Create(SaveArtifactStorageParams{
		Name: "rustfs", Type: "s3", Endpoint: "rustfs.lan:9000", Bucket: "jm", Prefix: "jm",
		AccessKey: "ak", SecretKey: "sk",
	})
	require.NoError(t, cerr)

	store := newFakeReconcileStore()
	svc := NewArtifactReconcileService(db, channels)
	svc.storeFor = func(*model.ArtifactStorageChannel) (blobstore.Store, error) { return store, nil }
	return &reconcileHarness{db: db, channels: channels, svc: svc, store: store, ch: ch}
}

// seedS3Asset 在渠道上登记一条 s3 client-file 资产，返回其 CAS 键。
func (h *reconcileHarness) seedS3Asset(t *testing.T, shaSeed string, state model.AssetStorageState) *model.Asset {
	t.Helper()
	sha := strings.Repeat(shaSeed[:1], 64)
	rel := "var/artifacts/client-file/" + sha[:2] + "/" + sha + ".zip"
	a := &model.Asset{
		Type: model.AssetTypeClientFile, Name: "seed-" + shaSeed, Filename: shaSeed + ".zip",
		SHA256: sha, Size: 64, StorageState: state,
		StorageBackend: model.AssetBackendS3, StorageChannelID: h.ch.ID, RelPath: rel,
	}
	require.NoError(t, h.db.Create(a).Error)
	return a
}

// TestReconcile_ThreeStates 对账三态：一致仅计数、缺失/孤儿各产明细；
// 已 lost 资产不重复报缺失但计入索引侧扫描数。
func TestReconcile_ThreeStates(t *testing.T) {
	h := newReconcileHarness(t)
	matched := h.seedS3Asset(t, "a", model.AssetStorageExternal)
	missing := h.seedS3Asset(t, "b", model.AssetStorageExternal)
	h.seedS3Asset(t, "c", model.AssetStorageLost) // 已失效：不重复报缺失
	h.store.put(matched.RelPath, matched.Size)
	h.store.put("var/artifacts/client-file/zz/"+strings.Repeat("f", 64)+".bin", 128) // 孤儿

	run, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)
	require.Equal(t, model.ArtifactReconcileSucceeded, run.Status)
	require.NotNil(t, run.FinishedAt)
	require.Equal(t, 3, run.IndexCount, "索引侧含 lost 资产")
	require.Equal(t, 2, run.ObjectCount)
	require.Equal(t, 1, run.MatchedCount)
	require.Equal(t, 1, run.MissingCount)
	require.Equal(t, 1, run.OrphanCount)

	diffs, total, derr := h.svc.ListDiffs(run.ID, "", 1, 50)
	require.NoError(t, derr)
	require.EqualValues(t, 2, total)
	byKind := map[string]model.ArtifactReconcileDiff{}
	for _, d := range diffs {
		byKind[d.Kind] = d
	}
	require.Equal(t, missing.ID, byKind[model.ArtifactDiffMissing].AssetID)
	require.Equal(t, missing.RelPath, byKind[model.ArtifactDiffMissing].ObjectKey)
	require.Equal(t, missing.SHA256, byKind[model.ArtifactDiffMissing].SHA256)
	require.Zero(t, byKind[model.ArtifactDiffOrphan].AssetID)
	require.Contains(t, byKind[model.ArtifactDiffOrphan].ObjectKey, "var/artifacts/client-file/zz/")
	require.EqualValues(t, 128, byKind[model.ArtifactDiffOrphan].Size)
	require.NotNil(t, byKind[model.ArtifactDiffOrphan].LastModified, "孤儿带对象 Last-Modified")
}

// TestReconcile_PagedTraversal 分页遍历：页大小小于对象数时跨页全量（无漏报孤儿）。
func TestReconcile_PagedTraversal(t *testing.T) {
	h := newReconcileHarness(t)
	h.svc.pageSize = 2
	for i := 0; i < 5; i++ {
		h.store.put(fmt.Sprintf("var/artifacts/client-file/%02d/orphan-%d.bin", i, i), int64(i+1))
	}

	run, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 5, run.ObjectCount)
	require.Equal(t, 5, run.OrphanCount, "跨页遍历不漏对象")
	require.GreaterOrEqual(t, h.store.listCalls, 3, "5 个对象按页 2 至少 3 次列举")
}

// TestReconcile_PrefixIsolation 前缀隔离：CAS client-file 命名空间外对象
// （probe/ 探测残留、他类型目录、桶根杂物）不参与比对、不算孤儿。
func TestReconcile_PrefixIsolation(t *testing.T) {
	h := newReconcileHarness(t)
	h.store.put("probe/jm-probe-123", 8)
	h.store.put("var/artifacts/plugin/aa/foreign.jar", 10)
	h.store.put("stray-root-object.bin", 3)
	h.store.put("var/artifacts/client-file/aa/real-orphan.bin", 7)

	run, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 1, run.ObjectCount, "命名空间外对象不计入扫描")
	require.Equal(t, 1, run.OrphanCount)
	diffs, _, _ := h.svc.ListDiffs(run.ID, model.ArtifactDiffOrphan, 1, 50)
	require.Len(t, diffs, 1)
	require.Equal(t, "var/artifacts/client-file/aa/real-orphan.bin", diffs[0].ObjectKey)
}

// TestReconcile_InflightDedup 在途去重：同渠道对账进行中再次触发 → ErrReconcileInProgress；
// 完成后可再次触发。
func TestReconcile_InflightDedup(t *testing.T) {
	h := newReconcileHarness(t)
	gate := make(chan struct{})
	h.store.blockList = gate

	first, err := h.svc.Trigger(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)
	require.Equal(t, model.ArtifactReconcileRunning, first.Status)

	_, err = h.svc.Trigger(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.ErrorIs(t, err, ErrReconcileInProgress)

	close(gate)
	require.Eventually(t, func() bool {
		run, gerr := h.svc.GetRun(first.ID)
		return gerr == nil && run.Status == model.ArtifactReconcileSucceeded
	}, 5*time.Second, 10*time.Millisecond, "解除阻塞后对账应完成")

	h.store.blockList = nil
	_, err = h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err, "在途释放后可再次触发")
}

// TestReconcile_TriggerAll 全局触发：s3 渠道逐个起 run、在途渠道跳过回报；local 不参与。
func TestReconcile_TriggerAll(t *testing.T) {
	h := newReconcileHarness(t)
	ch2, err := h.channels.Create(SaveArtifactStorageParams{
		Name: "rustfs-2", Type: "s3", Endpoint: "rustfs2.lan:9000", Bucket: "jm2",
		AccessKey: "ak", SecretKey: "sk",
	})
	require.NoError(t, err)

	// 渠道 1 在途：全局触发应跳过之并回报。
	h.svc.mu.Lock()
	h.svc.inflight[h.ch.ID] = true
	h.svc.mu.Unlock()

	started, skipped, terr := h.svc.TriggerAll(model.ArtifactReconcileTriggerManual)
	require.NoError(t, terr)
	require.Len(t, started, 1)
	require.Equal(t, ch2.ID, started[0].ChannelID)
	require.Len(t, skipped, 1)
	require.Equal(t, h.ch.ID, skipped[0].ChannelID)

	require.Eventually(t, func() bool {
		run, gerr := h.svc.GetRun(started[0].ID)
		return gerr == nil && run.Status != model.ArtifactReconcileRunning
	}, 5*time.Second, 10*time.Millisecond)
}

// TestReconcile_LocalAndNoChannel local 渠道拒绝对账；无 s3 渠道时全局触发报无渠道。
func TestReconcile_LocalAndNoChannel(t *testing.T) {
	h := newReconcileHarness(t)
	var builtin model.ArtifactStorageChannel
	require.NoError(t, h.db.Where("builtin = ?", true).First(&builtin).Error)
	_, err := h.svc.Trigger(builtin.ID, model.ArtifactReconcileTriggerManual)
	require.ErrorIs(t, err, ErrReconcileChannelUnsupported)

	require.NoError(t, h.db.Delete(&model.ArtifactStorageChannel{}, h.ch.ID).Error)
	_, _, err = h.svc.TriggerAll(model.ArtifactReconcileTriggerManual)
	require.ErrorIs(t, err, ErrReconcileNoChannel)
}

// TestReconcile_ScheduledCheck 定期调度：首查置 NextRunAt（不立即跑）；到点触发 scheduled run
// 并推进 NextRunAt；禁用不触发。
func TestReconcile_ScheduledCheck(t *testing.T) {
	h := newReconcileHarness(t)
	t0 := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	h.svc.now = func() time.Time { return t0 }

	// 首查：只置 NextRunAt=now+24h，不起 run。
	h.svc.checkScheduled(t0)
	setting, err := h.svc.Settings()
	require.NoError(t, err)
	require.True(t, setting.Enabled, "默认启用")
	require.Equal(t, 24, setting.IntervalHours, "默认每日")
	require.NotNil(t, setting.NextRunAt)
	require.Equal(t, t0.Add(24*time.Hour), setting.NextRunAt.UTC())
	var count int64
	require.NoError(t, h.db.Model(&model.ArtifactReconcileRun{}).Count(&count).Error)
	require.Zero(t, count, "首查不在启动瞬间扫存储")

	// 未到点：不触发。
	h.svc.checkScheduled(t0.Add(23 * time.Hour))
	require.NoError(t, h.db.Model(&model.ArtifactReconcileRun{}).Count(&count).Error)
	require.Zero(t, count)

	// 到点：触发 scheduled run 并推进 NextRunAt。
	due := t0.Add(24 * time.Hour)
	h.svc.now = func() time.Time { return due }
	h.svc.checkScheduled(due)
	var runs []model.ArtifactReconcileRun
	require.NoError(t, h.db.Find(&runs).Error)
	require.Len(t, runs, 1)
	require.Equal(t, model.ArtifactReconcileTriggerScheduled, runs[0].TriggeredBy)
	setting, _ = h.svc.Settings()
	require.Equal(t, due.Add(24*time.Hour), setting.NextRunAt.UTC(), "NextRunAt 推进一个周期")
	require.Eventually(t, func() bool {
		run, gerr := h.svc.GetRun(runs[0].ID)
		return gerr == nil && run.Status != model.ArtifactReconcileRunning
	}, 5*time.Second, 10*time.Millisecond)

	// 禁用：到点也不触发。
	_, err = h.svc.UpdateSettings(false, 24)
	require.NoError(t, err)
	setting, _ = h.svc.Settings()
	require.Nil(t, setting.NextRunAt, "禁用清空 NextRunAt")
	h.svc.checkScheduled(due.Add(48 * time.Hour))
	require.NoError(t, h.db.Model(&model.ArtifactReconcileRun{}).Count(&count).Error)
	require.EqualValues(t, 1, count, "禁用后不再触发")
}

// TestReconcile_SettingsValidation 周期钳制 [1,720]；更新即重算 NextRunAt。
func TestReconcile_SettingsValidation(t *testing.T) {
	h := newReconcileHarness(t)
	_, err := h.svc.UpdateSettings(true, 0)
	require.ErrorIs(t, err, ErrReconcileInvalidInterval)
	_, err = h.svc.UpdateSettings(true, 721)
	require.ErrorIs(t, err, ErrReconcileInvalidInterval)

	t0 := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	h.svc.now = func() time.Time { return t0 }
	setting, err := h.svc.UpdateSettings(true, 12)
	require.NoError(t, err)
	require.Equal(t, 12, setting.IntervalHours)
	require.Equal(t, t0.Add(12*time.Hour), setting.NextRunAt.UTC())
}

// TestReconcile_MarkInterruptedRuns 启动清障：遗留 running 运行行置 failed。
func TestReconcile_MarkInterruptedRuns(t *testing.T) {
	h := newReconcileHarness(t)
	stale := &model.ArtifactReconcileRun{
		ChannelID: h.ch.ID, ChannelName: h.ch.Name,
		Status: model.ArtifactReconcileRunning, TriggeredBy: model.ArtifactReconcileTriggerManual,
		StartedAt: time.Now().Add(-time.Hour),
	}
	require.NoError(t, h.db.Create(stale).Error)

	h.svc.markInterruptedRuns()
	run, err := h.svc.GetRun(stale.ID)
	require.NoError(t, err)
	require.Equal(t, model.ArtifactReconcileFailed, run.Status)
	require.Equal(t, "CP 重启中断", run.ErrorMessage)
	require.NotNil(t, run.FinishedAt)
}

// TestResolveMissing 标记失效：缺失明细的资产 StorageState 置 lost、明细 resolved(marked_lost)；
// run 后资产已删 → 守卫翻 stale 不动资产；重复处置无 open 明细为空操作。
func TestResolveMissing(t *testing.T) {
	h := newReconcileHarness(t)
	gone := h.seedS3Asset(t, "b", model.AssetStorageExternal)
	deleted := h.seedS3Asset(t, "d", model.AssetStorageExternal)

	run, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 2, run.MissingCount)

	// run 之后其中一条资产被删除：处置守卫应翻 stale。
	require.NoError(t, h.db.Delete(&model.Asset{}, deleted.ID).Error)

	res, err := h.svc.ResolveMissing(run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Marked)
	require.Equal(t, 1, res.Stale)

	var asset model.Asset
	require.NoError(t, h.db.First(&asset, gone.ID).Error)
	require.Equal(t, model.AssetStorageLost, asset.StorageState, "缺失资产标记失效")

	diffs, _, _ := h.svc.ListDiffs(run.ID, model.ArtifactDiffMissing, 1, 50)
	for _, d := range diffs {
		require.Equal(t, model.ArtifactDiffResolved, d.Status)
	}

	again, err := h.svc.ResolveMissing(run.ID)
	require.NoError(t, err)
	require.Zero(t, again.Marked+again.Stale, "无 open 明细时为空操作")
}

// TestCleanupOrphans 清理孤儿：逐键 store.Delete、明细 resolved(cleaned)；
// run 后同键已被新上传合法引用 → 过时守卫翻 stale 不删；删除失败保持 open 可重试。
func TestCleanupOrphans(t *testing.T) {
	h := newReconcileHarness(t)
	cleanKey := "var/artifacts/client-file/aa/clean-me.bin"
	reusedKey := "var/artifacts/client-file/bb/reused.bin"
	h.store.put(cleanKey, 1)
	h.store.put(reusedKey, 2)

	run, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 2, run.OrphanCount)

	// run 之后同键出现新资产（同内容再上传）：该对象已被合法引用，绝不能删。
	require.NoError(t, h.db.Create(&model.Asset{
		Type: model.AssetTypeClientFile, SHA256: strings.Repeat("e", 64), Size: 2,
		StorageBackend: model.AssetBackendS3, StorageChannelID: h.ch.ID,
		StorageState: model.AssetStorageExternal, RelPath: reusedKey,
	}).Error)

	res, err := h.svc.CleanupOrphans(run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Cleaned)
	require.Equal(t, 1, res.Stale)
	require.Zero(t, res.Failed)
	require.Equal(t, []string{cleanKey}, h.store.deleted, "只删未被引用的孤儿对象")
	_, serr := h.store.Stat(context.Background(), reusedKey)
	require.NoError(t, serr, "被引用对象保留")
}

// TestCleanupOrphans_FailureKeepsOpen 删除失败：明细保持 open + ResolveError，可重试。
func TestCleanupOrphans_FailureKeepsOpen(t *testing.T) {
	h := newReconcileHarness(t)
	h.store.put("var/artifacts/client-file/cc/fail.bin", 1)
	run, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
	require.NoError(t, err)

	h.store.failDelete = true
	res, err := h.svc.CleanupOrphans(run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Failed)
	diffs, _, _ := h.svc.ListDiffs(run.ID, model.ArtifactDiffOrphan, 1, 50)
	require.Len(t, diffs, 1)
	require.Equal(t, model.ArtifactDiffOpen, diffs[0].Status, "失败保持 open 供重试")
	require.Contains(t, diffs[0].ResolveError, "模拟删除失败")

	// 重试成功后翻 resolved。
	h.store.failDelete = false
	res, err = h.svc.CleanupOrphans(run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Cleaned)
}

// TestReconcile_ResolveOnRunningRun 运行中 run 拒绝处置（差异报告未生成）。
func TestReconcile_ResolveOnRunningRun(t *testing.T) {
	h := newReconcileHarness(t)
	running := &model.ArtifactReconcileRun{
		ChannelID: h.ch.ID, ChannelName: h.ch.Name,
		Status: model.ArtifactReconcileRunning, TriggeredBy: model.ArtifactReconcileTriggerManual,
		StartedAt: time.Now(),
	}
	require.NoError(t, h.db.Create(running).Error)

	_, err := h.svc.ResolveMissing(running.ID)
	require.ErrorIs(t, err, ErrReconcileRunRunning)
	_, err = h.svc.CleanupOrphans(running.ID)
	require.ErrorIs(t, err, ErrReconcileRunRunning)

	failed := &model.ArtifactReconcileRun{
		ChannelID: h.ch.ID, ChannelName: h.ch.Name,
		Status: model.ArtifactReconcileFailed, TriggeredBy: model.ArtifactReconcileTriggerManual,
		StartedAt: time.Now(),
	}
	require.NoError(t, h.db.Create(failed).Error)
	_, err = h.svc.ResolveMissing(failed.ID)
	require.ErrorIs(t, err, ErrReconcileRunNotSucceeded)
	_, err = h.svc.CleanupOrphans(failed.ID)
	require.ErrorIs(t, err, ErrReconcileRunNotSucceeded)
}

// TestReconcile_ListRuns 运行记录按 id desc、渠道过滤与上限生效。
func TestReconcile_ListRuns(t *testing.T) {
	h := newReconcileHarness(t)
	for i := 0; i < 3; i++ {
		_, err := h.svc.ReconcileSync(h.ch.ID, model.ArtifactReconcileTriggerManual)
		require.NoError(t, err)
	}
	runs, err := h.svc.ListRuns(0, 2)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Greater(t, runs[0].ID, runs[1].ID, "最新在前")

	runs, err = h.svc.ListRuns(h.ch.ID+999, 10)
	require.NoError(t, err)
	require.Empty(t, runs)
}
