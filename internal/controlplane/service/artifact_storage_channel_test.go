package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/blobstore"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

func newArtifactStorageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// Task 表随 FR-348 渠道删除守卫（迁移在途禁删）纳入：Delete 会查非终态迁移任务。
	require.NoError(t, db.AutoMigrate(&model.ArtifactStorageChannel{}, &model.Asset{}, &model.Task{}))
	return db
}

func newArtifactStorageService(t *testing.T) *ArtifactStorageChannelService {
	t.Helper()
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	svc := NewArtifactStorageChannelService(newArtifactStorageTestDB(t), root)
	enc, eerr := newKeyEncryptor(DevKeyEncSecretBase64)
	require.NoError(t, eerr)
	svc.SetKeyEncryptor(enc)
	require.NoError(t, svc.EnsureBuiltin())
	return svc
}

// fakeArtifactS3 渠道服务测试用最小 S3 假后端（path-style /bucket/key）：PUT/HEAD/DELETE。
type fakeArtifactS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeArtifactS3(t *testing.T) (*fakeArtifactS3, *httptest.Server) {
	t.Helper()
	f := &fakeArtifactS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/"))
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.objects[key] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			b, ok := f.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			b, ok := f.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
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

func s3ChannelParams(endpoint string) SaveArtifactStorageParams {
	useSSL := false
	return SaveArtifactStorageParams{
		Name:      "rustfs-主渠道",
		Type:      "s3",
		Endpoint:  endpoint,
		Bucket:    "jm-artifacts",
		Prefix:    "jm",
		AccessKey: "test-ak",
		SecretKey: "test-sk",
		UseSSL:    &useSSL,
	}
}

// TestArtifactStorage_EnsureBuiltin 幂等 seed 内置本机存储：只建一条、无活跃时置活跃。
func TestArtifactStorage_EnsureBuiltin(t *testing.T) {
	svc := newArtifactStorageService(t)
	// 再跑一次应幂等。
	require.NoError(t, svc.EnsureBuiltin())

	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.True(t, list[0].Builtin)
	require.True(t, list[0].Active, "无活跃行时内置渠道兜底活跃")
	require.Equal(t, model.ArtifactStorageLocal, list[0].Type)
}

// TestArtifactStorage_Create_OnlyS3 面板仅可创建 s3 渠道（local 由内置行独占）。
func TestArtifactStorage_Create_OnlyS3(t *testing.T) {
	svc := newArtifactStorageService(t)
	_, err := svc.Create(SaveArtifactStorageParams{Name: "x", Type: "local"})
	require.ErrorIs(t, err, ErrArtifactStorageInvalidType)
	_, err = svc.Create(SaveArtifactStorageParams{Name: "x", Type: "ftp"})
	require.ErrorIs(t, err, ErrArtifactStorageInvalidType)
}

// TestArtifactStorage_Create_Validation endpoint/bucket 必填、TTL 越界拒绝、未填取默认 600。
func TestArtifactStorage_Create_Validation(t *testing.T) {
	svc := newArtifactStorageService(t)

	p := s3ChannelParams("rustfs.lan:9000")
	p.Endpoint = ""
	_, err := svc.Create(p)
	require.ErrorIs(t, err, ErrArtifactStorageInvalidConfig)

	p = s3ChannelParams("rustfs.lan:9000")
	p.Bucket = ""
	_, err = svc.Create(p)
	require.ErrorIs(t, err, ErrArtifactStorageInvalidConfig)

	p = s3ChannelParams("rustfs.lan:9000")
	bad := 10
	p.PresignTTLSeconds = &bad
	_, err = svc.Create(p)
	require.ErrorIs(t, err, ErrArtifactStorageInvalidConfig, "TTL 越界 [60,3600] 拒绝")

	p = s3ChannelParams("rustfs.lan:9000")
	created, err := svc.Create(p)
	require.NoError(t, err)
	require.Equal(t, 600, created.PresignTTLSeconds, "TTL 未填取默认 600")
	require.Equal(t, "us-east-1", created.Region, "region 缺省 us-east-1")
	require.False(t, created.Active, "新渠道默认不活跃")
}

// TestArtifactStorage_Create_NameConflict 重名回业务错误（含与内置行撞名）。
func TestArtifactStorage_Create_NameConflict(t *testing.T) {
	svc := newArtifactStorageService(t)
	_, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)
	_, err = svc.Create(s3ChannelParams("rustfs.lan:9001"))
	require.ErrorIs(t, err, ErrArtifactStorageNameConflict)

	p := s3ChannelParams("rustfs.lan:9000")
	p.Name = ArtifactStorageBuiltinName
	_, err = svc.Create(p)
	require.ErrorIs(t, err, ErrArtifactStorageNameConflict)
}

// TestArtifactStorage_Create_EncryptorMissing 加密器未配置时创建 s3 渠道 422 快失败（不落明文）。
func TestArtifactStorage_Create_EncryptorMissing(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	svc := NewArtifactStorageChannelService(newArtifactStorageTestDB(t), root)
	require.NoError(t, svc.EnsureBuiltin())

	_, err = svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.ErrorIs(t, err, ErrArtifactStorageEncryptorMissing)
}

// TestArtifactStorage_ResponseNeverContainsCredentials 响应 JSON 永不含凭证明文或密文，
// 以 hasAccessKey/hasSecretKey 布尔标示。
func TestArtifactStorage_ResponseNeverContainsCredentials(t *testing.T) {
	svc := newArtifactStorageService(t)
	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)
	require.NotEmpty(t, created.AccessKeyEnc, "凭证已加密落库")
	require.NotEqual(t, "test-ak", created.AccessKeyEnc, "落库为密文非明文")

	raw, merr := json.Marshal(created)
	require.NoError(t, merr)
	require.NotContains(t, string(raw), "test-ak")
	require.NotContains(t, string(raw), "test-sk")
	require.NotContains(t, string(raw), created.AccessKeyEnc, "密文也不出响应")
	require.Contains(t, string(raw), `"hasAccessKey":true`)
	require.Contains(t, string(raw), `"hasSecretKey":true`)
}

// TestArtifactStorage_Update_KeepCredentialsWhenBlank 编辑凭证留空=保留原密文（脱敏编辑），
// 传非空=重加密覆盖；成功后清 LastTest*。
func TestArtifactStorage_Update_KeepCredentialsWhenBlank(t *testing.T) {
	svc := newArtifactStorageService(t)
	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)

	// 先写入一条测试结论，验证编辑后被清。
	_, err = svc.TestSaved(created.ID)
	require.NoError(t, err)

	p := s3ChannelParams("rustfs.lan:9100")
	p.AccessKey = ""
	p.SecretKey = ""
	updated, err := svc.Update(created.ID, p)
	require.NoError(t, err)
	require.Equal(t, "rustfs.lan:9100", updated.Endpoint)
	require.Nil(t, updated.LastTestAt, "配置已变，旧测试结论清空")

	ak, sk, derr := svc.decryptCredentials(updated)
	require.NoError(t, derr)
	require.Equal(t, "test-ak", ak, "留空保留原凭证")
	require.Equal(t, "test-sk", sk)

	p.SecretKey = "rotated-sk"
	updated, err = svc.Update(created.ID, p)
	require.NoError(t, err)
	_, sk, derr = svc.decryptCredentials(updated)
	require.NoError(t, derr)
	require.Equal(t, "rotated-sk", sk, "传非空重加密覆盖")
}

// TestArtifactStorage_Update_Guards 内置不可编辑；type 不可改。
func TestArtifactStorage_Update_Guards(t *testing.T) {
	svc := newArtifactStorageService(t)
	list, err := svc.List()
	require.NoError(t, err)
	builtinID := list[0].ID

	_, err = svc.Update(builtinID, s3ChannelParams("rustfs.lan:9000"))
	require.ErrorIs(t, err, ErrArtifactStorageBuiltinImmutable)

	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)
	p := s3ChannelParams("rustfs.lan:9000")
	p.Type = "local"
	_, err = svc.Update(created.ID, p)
	require.ErrorIs(t, err, ErrArtifactStorageTypeImmutable)
}

// TestArtifactStorage_Delete_Guards 内置不可删；活跃不可删；被制品引用不可删（附引用数）。
func TestArtifactStorage_Delete_Guards(t *testing.T) {
	svc := newArtifactStorageService(t)
	list, err := svc.List()
	require.NoError(t, err)
	builtinID := list[0].ID

	require.ErrorIs(t, svc.Delete(builtinID), ErrArtifactStorageBuiltinImmutable)

	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)

	_, err = svc.SetActive(created.ID)
	require.NoError(t, err)
	require.ErrorIs(t, svc.Delete(created.ID), ErrArtifactStorageActiveDelete)

	// 切回内置后：被制品引用仍拒删。
	_, err = svc.SetActive(builtinID)
	require.NoError(t, err)
	require.NoError(t, svc.db.Create(&model.Asset{
		Type: model.AssetTypeClientFile, SHA256: strings.Repeat("a", 64), Size: 1,
		StorageBackend: model.AssetBackendS3, StorageChannelID: created.ID,
	}).Error)
	err = svc.Delete(created.ID)
	require.ErrorIs(t, err, ErrArtifactStorageInUse)
	require.Contains(t, err.Error(), "1 个制品")

	// 清引用后可删。
	require.NoError(t, svc.db.Where("storage_channel_id = ?", created.ID).Delete(&model.Asset{}).Error)
	require.NoError(t, svc.Delete(created.ID))
}

// TestArtifactStorage_SetActive_ExactlyOne 设活跃事务先清后设，全表恰一条活跃；
// 切活跃不影响存量记录自述。
func TestArtifactStorage_SetActive_ExactlyOne(t *testing.T) {
	svc := newArtifactStorageService(t)
	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)

	activated, err := svc.SetActive(created.ID)
	require.NoError(t, err)
	require.True(t, activated.Active)

	var activeCount int64
	require.NoError(t, svc.db.Model(&model.ArtifactStorageChannel{}).Where("active = ?", true).Count(&activeCount).Error)
	require.EqualValues(t, 1, activeCount, "全表恰一条活跃")

	active, err := svc.Active()
	require.NoError(t, err)
	require.Equal(t, created.ID, active.ID)

	_, err = svc.SetActive(9999)
	require.ErrorIs(t, err, ErrArtifactStorageNotFound)
}

// TestArtifactStorage_StoreFor 渠道→BlobStore 解析：内置→local；s3 渠道→s3（凭证解密注入）。
func TestArtifactStorage_StoreFor(t *testing.T) {
	svc := newArtifactStorageService(t)
	list, err := svc.List()
	require.NoError(t, err)

	localStore, err := svc.StoreFor(&list[0])
	require.NoError(t, err)
	require.Equal(t, blobstore.KindLocal, localStore.Kind())

	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)
	s3Store, err := svc.StoreFor(created)
	require.NoError(t, err)
	require.Equal(t, blobstore.KindS3, s3Store.Kind())
}

// TestArtifactStorage_StoreForAsset 读路径按记录自述路由：local 记录（含渠道 ID=0 历史行）→
// local；s3 记录→渠道 s3；s3 记录缺渠道引用→报错。
func TestArtifactStorage_StoreForAsset(t *testing.T) {
	svc := newArtifactStorageService(t)
	created, err := svc.Create(s3ChannelParams("rustfs.lan:9000"))
	require.NoError(t, err)

	local, err := svc.StoreForAsset(&model.Asset{StorageBackend: model.AssetBackendLocal})
	require.NoError(t, err)
	require.Equal(t, blobstore.KindLocal, local.Kind())

	s3, err := svc.StoreForAsset(&model.Asset{StorageBackend: model.AssetBackendS3, StorageChannelID: created.ID})
	require.NoError(t, err)
	require.Equal(t, blobstore.KindS3, s3.Kind())

	_, err = svc.StoreForAsset(&model.Asset{StorageBackend: model.AssetBackendS3})
	require.Error(t, err, "s3 记录缺渠道引用应报错")
}

// TestArtifactStorage_PresignForAsset 预签名 URL 含渠道 TTL（X-Amz-Expires）与签名参数。
func TestArtifactStorage_PresignForAsset(t *testing.T) {
	svc := newArtifactStorageService(t)
	p := s3ChannelParams("rustfs.lan:9000")
	ttl := 300
	p.PresignTTLSeconds = &ttl
	created, err := svc.Create(p)
	require.NoError(t, err)

	u, err := svc.PresignForAsset(&model.Asset{
		StorageBackend: model.AssetBackendS3, StorageChannelID: created.ID,
		RelPath: "var/artifacts/client-file/ab/abc123.zip",
	})
	require.NoError(t, err)
	parsed, perr := url.Parse(u)
	require.NoError(t, perr)
	require.Equal(t, "http", parsed.Scheme, "UseSSL=false 走 http")
	require.Equal(t, "/jm-artifacts/jm/var/artifacts/client-file/ab/abc123.zip", parsed.Path)
	require.Equal(t, "300", parsed.Query().Get("X-Amz-Expires"), "TTL 取渠道配置")
	require.NotEmpty(t, parsed.Query().Get("X-Amz-Signature"))

	_, err = svc.PresignForAsset(&model.Asset{StorageBackend: model.AssetBackendLocal})
	require.Error(t, err, "local 制品无预签名语义")
}

// TestArtifactStorage_TestCandidate_LocalAndS3 真连探测：local=数据根可写；
// s3=写探测对象（PUT→HEAD→DELETE 挂 prefix 下 probe/ 键）。
func TestArtifactStorage_TestCandidate_LocalAndS3(t *testing.T) {
	svc := newArtifactStorageService(t)

	local := svc.TestCandidate(SaveArtifactStorageParams{Type: "local"}, 0)
	require.True(t, local.OK, local.Message)

	fake, srv := newFakeArtifactS3(t)
	p := s3ChannelParams(srv.URL)
	result := svc.TestCandidate(p, 0)
	require.True(t, result.OK, result.Message)
	require.Empty(t, fake.objects, "探测对象用后即删不留垃圾")

	// 探测失败：服务器已关。
	srv.Close()
	bad := svc.TestCandidate(p, 0)
	require.False(t, bad.OK)
	require.Equal(t, "PROBE_FAILED", bad.ErrorCode)
}

// TestArtifactStorage_TestCandidate_ReuseSavedCredentials 编辑态凭证留空 + 带 id：
// 用存库密文解密后探测（不要求重填 SK）。
func TestArtifactStorage_TestCandidate_ReuseSavedCredentials(t *testing.T) {
	svc := newArtifactStorageService(t)
	_, srv := newFakeArtifactS3(t)
	created, err := svc.Create(s3ChannelParams(srv.URL))
	require.NoError(t, err)

	p := s3ChannelParams(srv.URL)
	p.AccessKey = ""
	p.SecretKey = ""
	result := svc.TestCandidate(p, created.ID)
	require.True(t, result.OK, result.Message)
}

// TestArtifactStorage_TestSaved_PersistsLastTest 已存渠道测试持久化 LastTest*。
func TestArtifactStorage_TestSaved_PersistsLastTest(t *testing.T) {
	svc := newArtifactStorageService(t)
	_, srv := newFakeArtifactS3(t)
	created, err := svc.Create(s3ChannelParams(srv.URL))
	require.NoError(t, err)

	result, err := svc.TestSaved(created.ID)
	require.NoError(t, err)
	require.True(t, result.OK)

	fresh, err := svc.GetByID(created.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.LastTestAt)
	require.True(t, fresh.LastTestOk)
	require.Equal(t, "连接正常", fresh.LastTestMessage)
}
