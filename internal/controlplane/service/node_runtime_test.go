package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeRuntimeWorker 伪 WorkerServiceClient：覆盖缓存/catalog/browse RPC。
type fakeRuntimeWorker struct {
	workerpb.WorkerServiceClient
	listResp  *workerpb.ListArtifactCacheResponse
	evicted   string
	cleared   bool
	capSet    int64
	catalog   *workerpb.JDKCatalogResponse
	browse    *workerpb.BrowseDirResponse
}

func (f *fakeRuntimeWorker) ListArtifactCache(_ context.Context, _ *workerpb.ListArtifactCacheRequest, _ ...grpc.CallOption) (*workerpb.ListArtifactCacheResponse, error) {
	return f.listResp, nil
}
func (f *fakeRuntimeWorker) EvictArtifactCache(_ context.Context, in *workerpb.EvictArtifactCacheRequest, _ ...grpc.CallOption) (*workerpb.EvictArtifactCacheResponse, error) {
	f.evicted = in.Sha256
	return &workerpb.EvictArtifactCacheResponse{Success: true}, nil
}
func (f *fakeRuntimeWorker) ClearArtifactCache(_ context.Context, _ *workerpb.ClearArtifactCacheRequest, _ ...grpc.CallOption) (*workerpb.ClearArtifactCacheResponse, error) {
	f.cleared = true
	return &workerpb.ClearArtifactCacheResponse{Success: true, Removed: 3}, nil
}
func (f *fakeRuntimeWorker) SetArtifactCacheCap(_ context.Context, in *workerpb.SetArtifactCacheCapRequest, _ ...grpc.CallOption) (*workerpb.SetArtifactCacheCapResponse, error) {
	f.capSet = in.CapBytes
	return &workerpb.SetArtifactCacheCapResponse{Success: true, CapBytes: in.CapBytes, TotalBytes: 5}, nil
}
func (f *fakeRuntimeWorker) JDKCatalog(_ context.Context, _ *workerpb.JDKCatalogRequest, _ ...grpc.CallOption) (*workerpb.JDKCatalogResponse, error) {
	return f.catalog, nil
}
func (f *fakeRuntimeWorker) BrowseDir(_ context.Context, _ *workerpb.BrowseDirRequest, _ ...grpc.CallOption) (*workerpb.BrowseDirResponse, error) {
	return f.browse, nil
}

func newRuntimeDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:noderuntime_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Asset{}))
	return db
}

func setupRuntime(t *testing.T, fake *fakeRuntimeWorker) (*NodeRuntimeService, *model.Node) {
	t.Helper()
	db := newRuntimeDB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-rt", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	pool.SetWorkerClientForTest(node.UUID, fake)
	return NewNodeRuntimeService(db, pool), node
}

func TestNodeRuntime_ListArtifactCache_EnrichesFromAsset(t *testing.T) {
	fake := &fakeRuntimeWorker{listResp: &workerpb.ListArtifactCacheResponse{
		Items: []*workerpb.ArtifactCacheItem{
			{Sha256: "aa" + repeat("0", 62), Name: "", Version: "", Size: 100}, // 名字缺失，待补全
		},
		TotalBytes: 100,
		CapBytes:   0,
	}}
	svc, _ := setupRuntime(t, fake)
	// 在 asset 表放一条同 sha256，供补全。
	require.NoError(t, svc.db.Create(&model.Asset{
		Type: "server_core", Name: "paper-1.20.4", Version: "1.20.4-435", SHA256: "aa" + repeat("0", 62), Size: 100,
	}).Error)

	view, err := svc.ListArtifactCache(1)
	require.NoError(t, err)
	require.Len(t, view.Items, 1)
	assert.Equal(t, "paper-1.20.4", view.Items[0].Name, "name 应由 asset 表补全")
	assert.Equal(t, "1.20.4-435", view.Items[0].Version)
	assert.Equal(t, int64(100), view.TotalBytes)
}

func TestNodeRuntime_Evict(t *testing.T) {
	fake := &fakeRuntimeWorker{}
	svc, _ := setupRuntime(t, fake)
	require.NoError(t, svc.EvictArtifactCache(1, "deadbeef"))
	assert.Equal(t, "deadbeef", fake.evicted)
}

func TestNodeRuntime_Clear(t *testing.T) {
	fake := &fakeRuntimeWorker{}
	svc, _ := setupRuntime(t, fake)
	n, err := svc.ClearArtifactCache(1)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.True(t, fake.cleared)
}

func TestNodeRuntime_SetCap(t *testing.T) {
	fake := &fakeRuntimeWorker{}
	svc, _ := setupRuntime(t, fake)
	view, err := svc.SetArtifactCacheCap(1, 2048)
	require.NoError(t, err)
	assert.EqualValues(t, 2048, fake.capSet)
	assert.EqualValues(t, 2048, view.CapBytes)
}

func TestNodeRuntime_JDKCatalog(t *testing.T) {
	fake := &fakeRuntimeWorker{catalog: &workerpb.JDKCatalogResponse{
		Packages: []*workerpb.JDKCatalogPackage{
			{Distribution: "temurin", MajorVersion: 21, JavaVersion: "21.0.4", ArchiveType: "tar.gz", Latest: true},
		},
	}}
	svc, _ := setupRuntime(t, fake)
	pkgs, err := svc.JDKCatalog(1, "Temurin", 21, "x64")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	assert.Equal(t, "21.0.4", pkgs[0].JavaVersion)
}

func TestNodeRuntime_JDKCatalog_WorkerError(t *testing.T) {
	fake := &fakeRuntimeWorker{catalog: &workerpb.JDKCatalogResponse{Error: "foojay 不可达"}}
	svc, _ := setupRuntime(t, fake)
	_, err := svc.JDKCatalog(1, "Temurin", 21, "x64")
	require.Error(t, err)
}

func TestNodeRuntime_BrowseDir(t *testing.T) {
	fake := &fakeRuntimeWorker{browse: &workerpb.BrowseDirResponse{
		Success: true, Path: "/opt", Parent: "/",
		Dirs: []*workerpb.BrowseDirEntry{{Name: "jdks", Path: "/opt/jdks"}},
	}}
	svc, _ := setupRuntime(t, fake)
	view, err := svc.BrowseDir(1, "/opt")
	require.NoError(t, err)
	assert.Equal(t, "/opt", view.Path)
	require.Len(t, view.Dirs, 1)
	assert.Equal(t, "jdks", view.Dirs[0].Name)
}

func TestNodeRuntime_NodeNotFound(t *testing.T) {
	fake := &fakeRuntimeWorker{}
	svc, _ := setupRuntime(t, fake)
	_, err := svc.ListArtifactCache(999)
	require.ErrorIs(t, err, ErrNodeNotFound)
}

func TestNodeRuntime_NodeOffline(t *testing.T) {
	db := newRuntimeDB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{UUID: "u-off-rt", Name: "n", Host: "h", GRPCPort: 1, WSPort: 2, Secret: "s"}
	require.NoError(t, db.Create(node).Error)
	svc := NewNodeRuntimeService(db, pool) // 未注入 worker client → 离线
	_, err := svc.ListArtifactCache(node.ID)
	require.ErrorIs(t, err, ErrNodeOffline)
}

// repeat 生成 n 个 s 拼接（构造 64 位 sha256 字符串占位）。
func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
