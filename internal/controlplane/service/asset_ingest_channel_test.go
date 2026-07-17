package service

import (
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

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// ingestChannelHarness Ingest 渠道路由测试基座：Asset + 渠道服务共库同根、双向接线。
type ingestChannelHarness struct {
	assets   *AssetService
	channels *ArtifactStorageChannelService
	versions *ClientVersionService
	root     *dataroot.Root
	db       *gorm.DB
}

func newIngestChannelHarness(t *testing.T) *ingestChannelHarness {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}, &model.ArtifactStorageChannel{}, &model.ClientChannel{}, &model.ClientVersion{}))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)

	channels := NewArtifactStorageChannelService(db, root)
	enc, eerr := newKeyEncryptor(DevKeyEncSecretBase64)
	require.NoError(t, eerr)
	channels.SetKeyEncryptor(enc)
	require.NoError(t, channels.EnsureBuiltin())

	assets := NewAssetService(db, root)
	assets.SetStorageChannels(channels)
	channelSvc := NewClientChannelService(db)
	versions := NewClientVersionService(db, assets, channelSvc)
	versions.SetStorageChannels(channels)
	return &ingestChannelHarness{assets: assets, channels: channels, versions: versions, root: root, db: db}
}

// countingFakeS3 记录 PUT 次数的假 S3（path-style /bucket/key）。
type countingFakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newCountingFakeS3(t *testing.T) (*countingFakeS3, *httptest.Server) {
	t.Helper()
	f := &countingFakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/"))
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.objects[key] = body
			f.puts++
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

// activateS3Channel 建 s3 渠道（prefix=jm）并设活跃，返回渠道。
func (h *ingestChannelHarness) activateS3Channel(t *testing.T, endpoint string) *model.ArtifactStorageChannel {
	t.Helper()
	useSSL := false
	ch, err := h.channels.Create(SaveArtifactStorageParams{
		Name: "rustfs", Type: "s3", Endpoint: endpoint, Bucket: "jm-artifacts",
		Prefix: "jm", AccessKey: "ak", SecretKey: "sk", UseSSL: &useSSL,
	})
	require.NoError(t, err)
	_, err = h.channels.SetActive(ch.ID)
	require.NoError(t, err)
	return ch
}

// TestIngest_ChannelInjected_LocalActive_BehaviorUnchanged 渠道服务已注入但活跃=内置 local：
// Ingest 行为与主线逐字段一致（local 回归证明，spec 验收 §5）。
func TestIngest_ChannelInjected_LocalActive_BehaviorUnchanged(t *testing.T) {
	h := newIngestChannelHarness(t)
	data := []byte("client-file-on-local")

	asset, err := h.assets.Ingest(strings.NewReader(string(data)), IngestParams{
		Type: model.AssetTypeClientFile, Filename: "pack.zip",
	})
	require.NoError(t, err)

	wantSHA := sha256hex(data)
	wantRel := "var/artifacts/client-file/" + wantSHA[:2] + "/" + wantSHA + ".zip"
	require.Equal(t, model.AssetBackendLocal, asset.StorageBackend)
	require.EqualValues(t, 0, asset.StorageChannelID, "local 入库不打渠道标（历史行为原样）")
	require.Equal(t, model.AssetStorageHot, asset.StorageState)
	require.Equal(t, wantRel, asset.RelPath)
	got, rerr := os.ReadFile(h.root.Abs(wantRel))
	require.NoError(t, rerr)
	require.Equal(t, data, got, "物理文件落 CAS 且内容一致")
}

// TestIngest_S3Active_UploadsToChannel 活跃=s3：对象上到 fake S3（键=prefix+CAS 相对路径）、
// Asset 记 s3+渠道 ID+external、本地 CAS 无文件；去重命中不重传。
func TestIngest_S3Active_UploadsToChannel(t *testing.T) {
	h := newIngestChannelHarness(t)
	fake, srv := newCountingFakeS3(t)
	ch := h.activateS3Channel(t, srv.URL)

	data := []byte("client-file-on-s3")
	asset, err := h.assets.Ingest(strings.NewReader(string(data)), IngestParams{
		Type: model.AssetTypeClientFile, Filename: "pack.zip",
	})
	require.NoError(t, err)

	wantSHA := sha256hex(data)
	wantRel := "var/artifacts/client-file/" + wantSHA[:2] + "/" + wantSHA + ".zip"
	require.Equal(t, model.AssetBackendS3, asset.StorageBackend)
	require.Equal(t, ch.ID, asset.StorageChannelID)
	require.Equal(t, model.AssetStorageExternal, asset.StorageState)
	require.Equal(t, wantRel, asset.RelPath, "RelPath 仍登记 CAS 键（跨后端存储键，ADR-073）")

	require.Equal(t, data, fake.objects["jm-artifacts/jm/"+wantRel], "对象键 = bucket/prefix/CAS 相对路径")
	_, serr := os.Stat(h.root.Abs(wantRel))
	require.True(t, os.IsNotExist(serr), "本地 CAS 不落文件（CP 不存大对象）")

	// 去重命中：同内容再入库复用记录、不重传（PUT 计数不增）。
	putsBefore := fake.puts
	again, err := h.assets.Ingest(strings.NewReader(string(data)), IngestParams{
		Type: model.AssetTypeClientFile, Filename: "pack.zip",
	})
	require.NoError(t, err)
	require.Equal(t, asset.ID, again.ID)
	require.Equal(t, putsBefore, fake.puts, "去重命中不迁移不重传")
}

// TestIngest_S3Active_OtherTypesStayLocal 活跃=s3 时其余类型（core 等）恒本地。
func TestIngest_S3Active_OtherTypesStayLocal(t *testing.T) {
	h := newIngestChannelHarness(t)
	fake, srv := newCountingFakeS3(t)
	h.activateS3Channel(t, srv.URL)

	asset, err := h.assets.Ingest(strings.NewReader("core-jar-bytes"), IngestParams{
		Type: model.AssetTypeCore, Filename: "paper.jar",
	})
	require.NoError(t, err)
	require.Equal(t, model.AssetBackendLocal, asset.StorageBackend)
	require.EqualValues(t, 0, asset.StorageChannelID)
	require.FileExists(t, h.root.Abs(asset.RelPath))
	require.Zero(t, fake.puts, "非 client-file 不经渠道")
}

// TestIngest_S3UploadFailure_FailsFast S3 上传失败快失败：报错、不落 DB 记录、不静默回落本地。
func TestIngest_S3UploadFailure_FailsFast(t *testing.T) {
	h := newIngestChannelHarness(t)
	_, srv := newCountingFakeS3(t)
	h.activateS3Channel(t, srv.URL)
	srv.Close() // 渠道故障态。

	_, err := h.assets.Ingest(strings.NewReader("doomed"), IngestParams{
		Type: model.AssetTypeClientFile, Filename: "x.bin",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "上传制品到对象存储失败")

	var count int64
	require.NoError(t, h.db.Model(&model.Asset{}).Count(&count).Error)
	require.Zero(t, count, "失败不落记录")
	entries, _ := os.ReadDir(h.root.Abs("var/artifacts"))
	require.Empty(t, entries, "失败不静默回落本地")
}

// TestAssetDelete_S3RemovesObject 删除 s3 制品：DB 记录删除 + 渠道对象尽力清除。
func TestAssetDelete_S3RemovesObject(t *testing.T) {
	h := newIngestChannelHarness(t)
	fake, srv := newCountingFakeS3(t)
	h.activateS3Channel(t, srv.URL)

	asset, err := h.assets.Ingest(strings.NewReader("to-delete"), IngestParams{
		Type: model.AssetTypeClientFile, Filename: "d.bin",
	})
	require.NoError(t, err)
	require.Len(t, fake.objects, 1)

	require.NoError(t, h.assets.Delete(asset.ID))
	require.Empty(t, fake.objects, "对象随删除清理")
	_, gerr := h.assets.GetByID(asset.ID)
	require.ErrorIs(t, gerr, ErrAssetNotFound)
}

// TestReadArtifactText_S3 s3 制品管理面文本预览经 BlobStore 读取，降级口径不变。
func TestReadArtifactText_S3(t *testing.T) {
	h := newIngestChannelHarness(t)
	_, srv := newCountingFakeS3(t)
	h.activateS3Channel(t, srv.URL)

	content := "motd=Hello\nmax-players=20\n"
	asset, err := h.versions.PublishFile(strings.NewReader(content), PublishFileParams{Filename: "server.properties", Codec: "none"})
	require.NoError(t, err)

	preview, perr := h.versions.ReadArtifactText(asset.SHA256)
	require.NoError(t, perr)
	require.Equal(t, "text", preview.Kind)
	require.Equal(t, content, preview.Content)
}

// TestManifestFileContentPath_S3Materializes 发布期补丁生成的源物化：s3 制品（codec=none 快路径
// 本地无文件）经 BlobStore.Open 物化到临时文件参与 diff；local 快路径原样。
func TestManifestFileContentPath_S3Materializes(t *testing.T) {
	h := newIngestChannelHarness(t)
	_, srv := newCountingFakeS3(t)
	h.activateS3Channel(t, srv.URL)

	content := "old-version-bytes-for-patch"
	res, err := h.versions.PublishFile(strings.NewReader(content), PublishFileParams{Filename: "mod.jar", Codec: "none"})
	require.NoError(t, err)

	mf := ManifestFile{
		Path: "mods/mod.jar", SHA256: res.SHA256, Size: res.Size, Sync: "strict",
		Artifact: ManifestArtifact{SHA256: res.SHA256, Size: res.Size, Codec: "none"},
	}
	path, cleanup, ok, err := h.versions.manifestFileContentPath(mf)
	require.NoError(t, err)
	require.True(t, ok, "s3 源应可物化参与 diff")
	defer cleanup()
	got, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	require.Equal(t, content, string(got))
	require.NotEqual(t, h.root.Abs(mf.Artifact.SHA256), path, "s3 源物化为临时文件而非 CAS 路径")
}
