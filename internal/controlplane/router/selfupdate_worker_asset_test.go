package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func sha256RouterString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func setupWorkerAssetRouter(t *testing.T, svc *service.SelfUpdateService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSelfUpdateHandler(svc, nil)
	h.RegisterDownloadRoutes(r)
	admin := r.Group("/api/v1")
	h.RegisterRoutes(admin)
	return r
}

func newRouterTestRoot(t *testing.T) *dataroot.Root {
	t.Helper()
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	return root
}

func TestSelfUpdate_WorkerAssetDownload_RequiresScopedToken(t *testing.T) {
	body := "worker-binary"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"` + version.Version + `","artifacts":[{"component":"worker","os":"linux","arch":"amd64","url":"` + origin.URL + `","sha256":"` + sha256RouterString(body) + `"}]}`))
	}))
	defer feed.Close()

	svc := service.NewSelfUpdateService(setupTestDB(t), cpgrpc.NewClientPool(), service.SelfUpdateConfig{FeedURL: feed.URL, AllowInsecure: true}, newRouterTestRoot(t))
	_, err := svc.EnsureWorkerAsset(context.Background(), "linux", "amd64")
	require.NoError(t, err)

	r := setupWorkerAssetRouter(t, svc)
	noToken := httptest.NewRecorder()
	r.ServeHTTP(noToken, httptest.NewRequest(http.MethodGet, "/worker-assets/"+version.Version+"/linux/amd64/worker", nil))
	require.Equal(t, http.StatusForbidden, noToken.Code)

	token, err := svc.IssueWorkerAssetToken(service.WorkerAssetTokenScope{
		Version: version.Version,
		OS:      "linux",
		Arch:    "amd64",
		Purpose: service.WorkerAssetPurposeInstall,
	})
	require.NoError(t, err)
	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/worker-assets/"+version.Version+"/linux/amd64/worker?token="+token, nil))
	require.Equal(t, http.StatusOK, ok.Code)
	require.Equal(t, body, ok.Body.String())

	wrong := httptest.NewRecorder()
	r.ServeHTTP(wrong, httptest.NewRequest(http.MethodGet, "/worker-assets/"+version.Version+"/windows/amd64/worker?token="+token, nil))
	require.Equal(t, http.StatusForbidden, wrong.Code)
}

func TestSelfUpdate_WorkerAssetDownload_InstallTokenCachesOnDemand(t *testing.T) {
	body := "worker-binary"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"` + version.Version + `","artifacts":[{"component":"worker","os":"linux","arch":"amd64","url":"` + origin.URL + `","sha256":"` + sha256RouterString(body) + `"}]}`))
	}))
	defer feed.Close()

	svc := service.NewSelfUpdateService(setupTestDB(t), cpgrpc.NewClientPool(), service.SelfUpdateConfig{FeedURL: feed.URL, AllowInsecure: true}, newRouterTestRoot(t))
	token, err := svc.IssueWorkerAssetToken(service.WorkerAssetTokenScope{
		Version: version.Version,
		OS:      "linux",
		Arch:    "amd64",
		Purpose: service.WorkerAssetPurposeInstall,
	})
	require.NoError(t, err)

	r := setupWorkerAssetRouter(t, svc)
	ok := httptest.NewRecorder()
	r.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/worker-assets/"+version.Version+"/linux/amd64/worker?token="+token, nil))
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	require.Equal(t, body, ok.Body.String())

	entry, err := svc.WorkerAssetStatus("linux", "amd64")
	require.NoError(t, err)
	require.True(t, entry.Cached)
}

func TestSelfUpdate_WorkerAssetAdminCache(t *testing.T) {
	body := "worker-binary"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"` + version.Version + `","artifacts":[{"component":"worker","os":"linux","arch":"amd64","url":"` + origin.URL + `","sha256":"` + sha256RouterString(body) + `"}]}`))
	}))
	defer feed.Close()

	svc := service.NewSelfUpdateService(setupTestDB(t), cpgrpc.NewClientPool(), service.SelfUpdateConfig{FeedURL: feed.URL, AllowInsecure: true}, newRouterTestRoot(t))

	r := setupWorkerAssetRouter(t, svc)
	w := makeRequest(r, http.MethodPost, "/api/v1/self-update/worker-assets/cache", map[string]any{"os": "linux", "arch": "amd64"}, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, true, resp["cached"])
	require.Equal(t, sha256RouterString(body), resp["sha256"])

	list := makeRequest(r, http.MethodGet, "/api/v1/self-update/worker-assets", nil, "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	items := parseJSONArray(t, list)
	require.Len(t, items, 1)
}

type downloadingUpgradeWorker struct {
	workerpb.WorkerServiceClient
	got      *workerpb.UpgradeWorkerRequest
	gotBody  string
	gotError error
}

func (f *downloadingUpgradeWorker) UpgradeWorker(_ context.Context, in *workerpb.UpgradeWorkerRequest, _ ...grpc.CallOption) (*workerpb.UpgradeWorkerResponse, error) {
	f.got = in
	resp, err := http.Get(in.DownloadUrl)
	if err != nil {
		f.gotError = err
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.gotError = err
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: err.Error()}, nil
	}
	f.gotBody = string(body)
	return &workerpb.UpgradeWorkerResponse{Success: resp.StatusCode == http.StatusOK, FromVersion: "0.12.0", Error: resp.Status}, nil
}

func TestSelfUpdate_WorkerAssetUpgradeURLDownloadsFromCP(t *testing.T) {
	body := "worker-binary"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"` + version.Version + `","artifacts":[{"component":"worker","os":"linux","arch":"amd64","url":"` + origin.URL + `","sha256":"` + sha256RouterString(body) + `"}]}`))
	}))
	defer feed.Close()

	db := setupTestDB(t)
	node := model.Node{UUID: "node-upgrade", Name: "alpha", Status: model.NodeStatusOnline, OS: "linux", Arch: "amd64"}
	require.NoError(t, db.Create(&node).Error)
	pool := cpgrpc.NewClientPool()
	fake := &downloadingUpgradeWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)
	svc := service.NewSelfUpdateService(db, pool, service.SelfUpdateConfig{FeedURL: feed.URL, AllowInsecure: true}, newRouterTestRoot(t))
	r := setupWorkerAssetRouter(t, svc)
	cp := httptest.NewServer(r)
	defer cp.Close()

	from, to, err := svc.UpgradeNodeWithBaseURL(context.Background(), node.ID, "", cp.URL)
	require.NoError(t, err)
	require.Equal(t, "0.12.0", from)
	require.Equal(t, version.Version, to)
	require.NoError(t, fake.gotError)
	require.NotNil(t, fake.got)
	require.Equal(t, sha256RouterString(body), fake.got.Sha256)
	require.Equal(t, body, fake.gotBody)
}
