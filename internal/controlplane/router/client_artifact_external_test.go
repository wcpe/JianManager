package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// externalDistFakeS3 302 链路测试用假 S3：path-style 存取 + 记录预签名 GET。
type externalDistFakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newExternalDistFakeS3(t *testing.T) (*externalDistFakeS3, *httptest.Server) {
	t.Helper()
	f := &externalDistFakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/"))
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.objects[key] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			b, ok := f.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodHead:
			b, ok := f.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// setupExternalDistRouter 客户端分发路由 + 制品存储渠道全接线（FR-347）：
// 渠道服务注入 Asset 写路径与 ClientVersion 读路径。
func setupExternalDistRouter(t *testing.T, db *gorm.DB) (*gin.Engine, *service.ArtifactStorageChannelService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}

	root, err := dataroot.Init(filepath.Join(os.TempDir(), "jm-extdist-test-"+strconv.FormatInt(time.Now().UnixNano(), 10)))
	require.NoError(t, err)
	assetSvc := service.NewAssetService(db, root)
	channelSvc := service.NewClientChannelService(db)
	versionSvc := service.NewClientVersionService(db, assetSvc, channelSvc)

	artifactStorageSvc := service.NewArtifactStorageChannelService(db, root)
	enc, _, eerr := service.ResolveKeyEncryptor("", true, "")
	require.NoError(t, eerr)
	artifactStorageSvc.SetKeyEncryptor(enc)
	require.NoError(t, artifactStorageSvc.EnsureBuiltin())
	assetSvc.SetStorageChannels(artifactStorageSvc)
	versionSvc.SetStorageChannels(artifactStorageSvc)

	svcs := &Services{
		Auth:            service.NewAuthService(db, jwtCfg),
		Authz:           service.NewAuthzService(db),
		Audit:           service.NewAuditService(db),
		Asset:           assetSvc,
		ArtifactStorage: artifactStorageSvc,
		ClientChannel:   channelSvc,
		ClientVersion:   versionSvc,
		ClientMachine:   service.NewClientMachineService(db),
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret), artifactStorageSvc
}

// createAndActivateS3Channel 经 API 建 s3 渠道并设活跃，返回渠道 ID。
func createAndActivateS3Channel(t *testing.T, r *gin.Engine, token, endpoint string, ttl int) uint {
	t.Helper()
	body := map[string]any{
		"name": "rustfs", "type": "s3", "endpoint": endpoint, "bucket": "jm-artifacts",
		"prefix": "jm", "accessKey": "ak", "secretKey": "sk", "useSsl": false,
		"presignTtlSeconds": ttl,
	}
	w := makeRequest(r, "POST", "/api/v1/artifact-storages", body, token)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	id := uint(parseJSON(t, w)["id"].(float64))
	wa := makeRequest(r, "POST", "/api/v1/artifact-storages/"+itoa(id)+"/activate", nil, token)
	require.Equal(t, http.StatusOK, wa.Code)
	return id
}

// TestClientDist_GetArtifact_S3_302Presign 玩家消费端点对 s3 制品回 302 预签名短时效 URL：
// Location 含签名参数与渠道 TTL、Cache-Control: no-store；跟随 URL 可取回原字节（FR-347 验收）。
func TestClientDist_GetArtifact_S3_302Presign(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupExternalDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	fake, srv := newExternalDistFakeS3(t)
	createAndActivateS3Channel(t, r, token, srv.URL, 300)

	content := []byte("s3-artifact-bytes-via-302")
	sha := uploadClientFileNone(t, r, token, channelID, content, "data.bin")
	require.Len(t, fake.objects, 1, "制品应上到 S3")

	req := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+sha, nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusFound, w.Code, w.Body.String())
	loc := w.Header().Get("Location")
	require.NotEmpty(t, loc)
	parsed, perr := url.Parse(loc)
	require.NoError(t, perr)
	q := parsed.Query()
	assert.Equal(t, "AWS4-HMAC-SHA256", q.Get("X-Amz-Algorithm"))
	assert.Equal(t, "300", q.Get("X-Amz-Expires"), "TTL 取渠道配置")
	assert.NotEmpty(t, q.Get("X-Amz-Signature"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), "短时效 URL 禁缓存")

	// 跟随 Location 直连 fake S3 取回原字节（updater followRedirects 语义）。
	resp, gerr := http.Get(loc)
	require.NoError(t, gerr)
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, content, got)
}

// TestClientDist_GetArtifact_Local_Unchanged local 制品仍 CP ServeContent 直出（200 + 强缓存），
// 302 分流不影响历史行为。
func TestClientDist_GetArtifact_Local_Unchanged(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupExternalDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	content := []byte("local-artifact-bytes")
	sha := uploadClientFileNone(t, r, token, channelID, content, "data.bin")

	req := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+sha, nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, bytes.Equal(w.Body.Bytes(), content))
	assert.Contains(t, w.Header().Get("Cache-Control"), "immutable", "local 强缓存头不变")
}

// TestClientDist_GetArtifact_S3_ChannelBroken_503 渠道失效（被绕守卫直删）→ 503
// ARTIFACT_STORAGE_UNAVAILABLE（对 updater 可重试语义）。
func TestClientDist_GetArtifact_S3_ChannelBroken_503(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupExternalDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	_, srv := newExternalDistFakeS3(t)
	chID := createAndActivateS3Channel(t, r, token, srv.URL, 300)
	sha := uploadClientFileNone(t, r, token, channelID, []byte("orphan"), "o.bin")

	// 模拟数据异常：渠道行被直删（正常路径有删除守卫，此为防御路径验证）。
	require.NoError(t, db.Exec("DELETE FROM artifact_storage_channels WHERE id = ?", chID).Error)

	req := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+sha, nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "ARTIFACT_STORAGE_UNAVAILABLE", parseJSON(t, w)["error"])
}

// TestClientDist_AdminDownloadAndPreview_S3Proxy 管理面下载对 s3 制品 CP 代理直流
//（200 + Content-Length + attachment，字节一致，不 302）；文本预览正常。
func TestClientDist_AdminDownloadAndPreview_S3Proxy(t *testing.T) {
	db := setupTestDB(t)
	r, _ := setupExternalDistRouter(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	_ = createChannelAndKey(t, r, token, channelID)

	_, srv := newExternalDistFakeS3(t)
	createAndActivateS3Channel(t, r, token, srv.URL, 300)

	content := []byte("motd=Hello S3\n")
	sha := uploadClientFileNone(t, r, token, channelID, content, "server.properties")

	// 管理面下载：CP 代理直流。
	w := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/download?sha256="+sha, nil, token)
	require.Equal(t, http.StatusOK, w.Code, "管理面 s3 下载走 CP 代理，不 302")
	require.True(t, bytes.Equal(w.Body.Bytes(), content), "代理字节一致")
	assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
	assert.Equal(t, strconv.Itoa(len(content)), w.Header().Get("Content-Length"))

	// 管理面文本预览：BlobStore 读取，口径不变。
	wp := makeRequest(r, "GET", "/api/v1/client-channels/"+channelID+"/files/content?sha256="+sha, nil, token)
	require.Equal(t, http.StatusOK, wp.Code)
	pb := parseJSON(t, wp)
	assert.Equal(t, "text", pb["kind"])
	assert.Contains(t, pb["content"], "motd=Hello S3")
}
