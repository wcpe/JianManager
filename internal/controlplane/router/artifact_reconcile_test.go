package router

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/blobstore"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// reconcileFakeStore 路由级对账测试假 BlobStore（内存对象 + 前缀过滤 + 末键游标分页）。
type reconcileFakeStore struct {
	mu      sync.Mutex
	objects map[string]int64
	deleted []string
}

func (f *reconcileFakeStore) Kind() string { return blobstore.KindS3 }
func (f *reconcileFakeStore) PutFile(_ context.Context, key, _ string, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = size
	return nil
}
func (f *reconcileFakeStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, blobstore.ErrBlobNotFound
}
func (f *reconcileFakeStore) Stat(_ context.Context, key string) (*blobstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	size, ok := f.objects[key]
	if !ok {
		return nil, blobstore.ErrBlobNotFound
	}
	return &blobstore.ObjectInfo{Key: key, Size: size}, nil
}
func (f *reconcileFakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, key)
	delete(f.objects, key)
	return nil
}
func (f *reconcileFakeStore) List(ctx context.Context, prefix string, limit int) ([]blobstore.ObjectInfo, error) {
	out, _, err := f.ListPage(ctx, prefix, limit, "")
	return out, err
}
func (f *reconcileFakeStore) ListPage(_ context.Context, prefix string, limit int, token string) ([]blobstore.ObjectInfo, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	next := ""
	if len(keys) > limit {
		keys = keys[:limit]
		next = keys[len(keys)-1]
	}
	out := make([]blobstore.ObjectInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, blobstore.ObjectInfo{Key: k, Size: f.objects[k]})
	}
	return out, next, nil
}
func (f *reconcileFakeStore) Presign(string, time.Duration) (string, error) {
	return "", blobstore.ErrPresignUnsupported
}

// setupReconcileRouter 路由 + 假 store 注入 + 一条 s3 渠道，返回 (engine, token, channelID, store)。
func setupReconcileRouter(t *testing.T, db *gorm.DB) (*gin.Engine, string, uint, *reconcileFakeStore) {
	t.Helper()
	r := setupTestRouter(db)
	store := &reconcileFakeStore{objects: map[string]int64{}}
	require.NotNil(t, testArtifactReconcile, "testhelper 应装配对账服务")
	testArtifactReconcile.SetStoreFactory(func(*model.ArtifactStorageChannel) (blobstore.Store, error) {
		return store, nil
	})
	token := getAdminToken(t, r)
	w := makeRequest(r, "POST", "/api/v1/artifact-storages", artifactStorageBody("rustfs-rec", "rustfs.lan:9000"), token)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	chID := uint(parseJSON(t, w)["id"].(float64))
	return r, token, chID, store
}

// seedReconcileAsset 直插一条该渠道的 s3 client-file 资产。
func seedReconcileAsset(t *testing.T, db *gorm.DB, chID uint, shaSeed string, state model.AssetStorageState) *model.Asset {
	t.Helper()
	sha := strings.Repeat(shaSeed[:1], 64)
	a := &model.Asset{
		Type: model.AssetTypeClientFile, SHA256: sha, Size: 8,
		StorageBackend: model.AssetBackendS3, StorageChannelID: chID, StorageState: state,
		RelPath: "var/artifacts/client-file/" + sha[:2] + "/" + sha + ".bin",
	}
	require.NoError(t, db.Create(a).Error)
	return a
}

// waitRunDone 轮询 run 至非 running 终态。
func waitRunDone(t *testing.T, r *gin.Engine, token string, runID float64) map[string]any {
	t.Helper()
	var run map[string]any
	require.Eventually(t, func() bool {
		w := makeRequest(r, "GET", "/api/v1/artifact-reconcile/runs/"+itoa(uint(runID)), nil, token)
		if w.Code != http.StatusOK {
			return false
		}
		run = parseJSON(t, w)
		return run["status"] != string(model.ArtifactReconcileRunning)
	}, 5*time.Second, 10*time.Millisecond, "对账应在超时前完成")
	return run
}

// TestArtifactReconcileAPI_TriggerAndReport 触发 → 202 异步 → 报告：缺失/孤儿明细可查、
// 计数正确、审计留痕（FR-349 主链路）。
func TestArtifactReconcileAPI_TriggerAndReport(t *testing.T) {
	db := setupTestDB(t)
	r, token, chID, store := setupReconcileRouter(t, db)
	missing := seedReconcileAsset(t, db, chID, "a", model.AssetStorageExternal)
	matched := seedReconcileAsset(t, db, chID, "b", model.AssetStorageExternal)
	store.objects[matched.RelPath] = 8
	store.objects["var/artifacts/client-file/zz/orphan.bin"] = 16
	store.objects["probe/jm-probe-1"] = 8 // 命名空间外：不算孤儿

	w := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs", map[string]any{"channelId": chID}, token)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	started := resp["started"].([]any)
	require.Len(t, started, 1)
	runID := started[0].(map[string]any)["id"].(float64)

	run := waitRunDone(t, r, token, runID)
	assert.Equal(t, string(model.ArtifactReconcileSucceeded), run["status"])
	assert.EqualValues(t, 2, run["indexCount"])
	assert.EqualValues(t, 2, run["objectCount"], "probe/ 命名空间外不计")
	assert.EqualValues(t, 1, run["missingCount"])
	assert.EqualValues(t, 1, run["orphanCount"])

	// 差异明细分页 + kind 过滤。
	wd := makeRequest(r, "GET", "/api/v1/artifact-reconcile/runs/"+itoa(uint(runID))+"/diffs?kind=missing", nil, token)
	require.Equal(t, http.StatusOK, wd.Code)
	dresp := parseJSON(t, wd)
	require.EqualValues(t, 1, dresp["total"])
	item := dresp["items"].([]any)[0].(map[string]any)
	assert.Equal(t, missing.RelPath, item["objectKey"])
	assert.EqualValues(t, missing.ID, item["assetId"])

	wbad := makeRequest(r, "GET", "/api/v1/artifact-reconcile/runs/"+itoa(uint(runID))+"/diffs?kind=weird", nil, token)
	require.Equal(t, http.StatusBadRequest, wbad.Code, "kind 非法 400")

	// 运行列表可查（最近 N 次）。
	wl := makeRequest(r, "GET", "/api/v1/artifact-reconcile/runs?channelId="+itoa(chID), nil, token)
	require.Equal(t, http.StatusOK, wl.Code)
	require.Len(t, parseJSONArray(t, wl), 1)

	// 审计留痕。
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "artifact_reconcile.trigger").Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

// TestArtifactReconcileAPI_Disposal 处置端点：标记失效（资产翻 lost）+ 清理孤儿（对象删除），
// 均写审计；重复处置为空操作。
func TestArtifactReconcileAPI_Disposal(t *testing.T) {
	db := setupTestDB(t)
	r, token, chID, store := setupReconcileRouter(t, db)
	missing := seedReconcileAsset(t, db, chID, "c", model.AssetStorageExternal)
	orphanKey := "var/artifacts/client-file/yy/orphan-2.bin"
	store.objects[orphanKey] = 32

	w := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs", map[string]any{"channelId": chID}, token)
	require.Equal(t, http.StatusAccepted, w.Code)
	runID := parseJSON(t, w)["started"].([]any)[0].(map[string]any)["id"].(float64)
	waitRunDone(t, r, token, runID)

	// 标记失效：资产 StorageState 翻 lost。
	wm := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs/"+itoa(uint(runID))+"/resolve-missing", nil, token)
	require.Equal(t, http.StatusOK, wm.Code, wm.Body.String())
	assert.EqualValues(t, 1, parseJSON(t, wm)["marked"])
	var asset model.Asset
	require.NoError(t, db.First(&asset, missing.ID).Error)
	assert.Equal(t, model.AssetStorageLost, asset.StorageState)

	// 清理孤儿：对象被删。
	wc := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs/"+itoa(uint(runID))+"/cleanup-orphans", nil, token)
	require.Equal(t, http.StatusOK, wc.Code)
	assert.EqualValues(t, 1, parseJSON(t, wc)["cleaned"])
	assert.Equal(t, []string{orphanKey}, store.deleted)

	// 重复处置：无 open 明细，空操作。
	wm2 := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs/"+itoa(uint(runID))+"/resolve-missing", nil, token)
	require.Equal(t, http.StatusOK, wm2.Code)
	assert.EqualValues(t, 0, parseJSON(t, wm2)["marked"])

	for _, action := range []string{"artifact_reconcile.mark_lost", "artifact_reconcile.cleanup_orphans"} {
		var n int64
		require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error)
		assert.GreaterOrEqual(t, n, int64(1), "处置写审计: %s", action)
	}
}

// TestArtifactReconcileAPI_TriggerGuards local 渠道 422、渠道不存在 404、无 s3 渠道全局触发 422。
func TestArtifactReconcileAPI_TriggerGuards(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	store := &reconcileFakeStore{objects: map[string]int64{}}
	testArtifactReconcile.SetStoreFactory(func(*model.ArtifactStorageChannel) (blobstore.Store, error) {
		return store, nil
	})
	token := getAdminToken(t, r)

	// 无 s3 渠道：全局触发 422。
	w := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs", nil, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)

	// local 内置渠道：422。
	list := makeRequest(r, "GET", "/api/v1/artifact-storages", nil, token)
	builtinID := uint(parseJSONArray(t, list)[0].(map[string]any)["id"].(float64))
	w = makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs", map[string]any{"channelId": builtinID}, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, parseJSON(t, w)["message"], "不参与对账")

	// 渠道不存在：404。
	w = makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs", map[string]any{"channelId": 9999}, token)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// TestArtifactReconcileAPI_Settings 设置读写：默认 enabled/24h；周期越界 422；更新重算 nextRunAt。
func TestArtifactReconcileAPI_Settings(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/artifact-reconcile/settings", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	got := parseJSON(t, w)
	assert.Equal(t, true, got["enabled"], "默认启用")
	assert.EqualValues(t, 24, got["intervalHours"], "默认每日")
	assert.NotContains(t, got, "id", "设置响应不暴露内部单行 ID")
	assert.NotContains(t, got, "updatedAt", "设置响应严格按公开契约")

	w = makeRequest(r, "PUT", "/api/v1/artifact-reconcile/settings", map[string]any{"enabled": true, "intervalHours": 0}, token)
	require.Equal(t, http.StatusBadRequest, w.Code, "intervalHours=0 binding required 拒收")
	w = makeRequest(r, "PUT", "/api/v1/artifact-reconcile/settings", map[string]any{"enabled": true, "intervalHours": 800}, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "越界 422")

	w = makeRequest(r, "PUT", "/api/v1/artifact-reconcile/settings", map[string]any{"enabled": true, "intervalHours": 12}, token)
	require.Equal(t, http.StatusOK, w.Code)
	got = parseJSON(t, w)
	assert.EqualValues(t, 12, got["intervalHours"])
	assert.NotEmpty(t, got["nextRunAt"], "启用即重算 NextRunAt")

	w = makeRequest(r, "PUT", "/api/v1/artifact-reconcile/settings", map[string]any{"enabled": false, "intervalHours": 12}, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Nil(t, parseJSON(t, w)["nextRunAt"], "禁用清空 NextRunAt")
}

// TestArtifactReconcileAPI_FailedRunCannotResolve 失败运行没有完整报告，两个处置端点均拒绝。
func TestArtifactReconcileAPI_FailedRunCannotResolve(t *testing.T) {
	db := setupTestDB(t)
	r, token, chID, _ := setupReconcileRouter(t, db)
	failed := &model.ArtifactReconcileRun{
		ChannelID: chID, ChannelName: "rustfs-rec", Status: model.ArtifactReconcileFailed,
		TriggeredBy: model.ArtifactReconcileTriggerManual, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(failed).Error)

	for _, path := range []string{"resolve-missing", "cleanup-orphans"} {
		w := makeRequest(r, "POST", "/api/v1/artifact-reconcile/runs/"+itoa(failed.ID)+"/"+path, nil, token)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
		assert.Equal(t, "BUSINESS_ERROR", parseJSON(t, w)["error"])
	}
}

// TestArtifactReconcileAPI_RequiresAdmin 未带 token 一律 401（admin 组 JWT 门）。
func TestArtifactReconcileAPI_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r)
	for _, tc := range [][2]string{
		{"GET", "/api/v1/artifact-reconcile/settings"},
		{"POST", "/api/v1/artifact-reconcile/runs"},
		{"GET", "/api/v1/artifact-reconcile/runs"},
	} {
		w := makeRequest(r, tc[0], tc[1], nil, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, "%s %s 应 401", tc[0], tc[1])
	}
}
