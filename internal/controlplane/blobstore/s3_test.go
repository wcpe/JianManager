package blobstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// awsDocTime AWS SigV4 官方示例统一时间：2013-05-24T00:00:00Z。
var awsDocTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

const (
	awsDocAccessKey = "AKIAIOSFODNN7EXAMPLE"
	awsDocSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

// TestPresignGetURL_AWSDocVector 对拍 AWS SigV4 官方文档「预签名 URL」示例：
// GET https://examplebucket.s3.amazonaws.com/test.txt，86400s 有效期，
// 期望签名 aeeed9bb…（逐字节一致才算实现正确）。
func TestPresignGetURL_AWSDocVector(t *testing.T) {
	u := presignGetURL("https", "examplebucket.s3.amazonaws.com", "/test.txt",
		awsDocAccessKey, awsDocSecretKey, "us-east-1", awsDocTime, 86400*time.Second)

	require.Equal(t,
		"https://examplebucket.s3.amazonaws.com/test.txt"+
			"?X-Amz-Algorithm=AWS4-HMAC-SHA256"+
			"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request"+
			"&X-Amz-Date=20130524T000000Z"+
			"&X-Amz-Expires=86400"+
			"&X-Amz-SignedHeaders=host"+
			"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404",
		u)
}

// TestSignV4_AWSDocVector_ListObjects 对拍 AWS SigV4 官方文档「GET Bucket (List Objects)」
// 示例（max-keys=2&prefix=J）：签名头恰为 host;x-amz-content-sha256;x-amz-date，
// 与本实现的固定签名头集合一致，期望签名 34b48302…。
func TestSignV4_AWSDocVector_ListObjects(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	require.NoError(t, err)

	signV4(req, emptyPayloadHash, awsDocAccessKey, awsDocSecretKey, "us-east-1", awsDocTime)

	auth := req.Header.Get("Authorization")
	require.Contains(t, auth, "Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request")
	require.Contains(t, auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date")
	require.Contains(t, auth, "Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7")
}

// fakeS3 内存假 S3 后端：path-style 语义（/bucket/key），存 PUT 字节、答 GET/HEAD/DELETE，
// 并留存最近请求头供断言（Authorization/UNSIGNED-PAYLOAD）。
type fakeS3 struct {
	t       *testing.T
	bucket  string
	objects map[string][]byte // key（不含 bucket 段）→ 内容
	lastPut http.Header
}

func newFakeS3(t *testing.T, bucket string) (*fakeS3, *httptest.Server) {
	f := &fakeS3{t: t, bucket: bucket, objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if !strings.HasPrefix(path, f.bucket) {
		http.Error(w, "wrong bucket", http.StatusNotFound)
		return
	}
	key, _ := url.PathUnescape(strings.TrimPrefix(strings.TrimPrefix(path, f.bucket), "/"))
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		f.lastPut = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			f.answerList(w, r)
			return
		}
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
		w.Header().Set("Content-Length", intToStr(len(b)))
		w.Header().Set("Last-Modified", awsDocTime.Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) answerList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	for k, v := range f.objects {
		if strings.HasPrefix(k, prefix) {
			b.WriteString("<Contents><Key>" + k + "</Key><Size>" + intToStr(len(v)) + "</Size>" +
				"<LastModified>2013-05-24T00:00:00.000Z</LastModified></Contents>")
		}
	}
	b.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(b.String()))
}

func intToStr(n int) string { return strconv.Itoa(n) }

// newTestS3Store 指向假 S3 的适配器（固定时钟便于断言）。
func newTestS3Store(t *testing.T, srv *httptest.Server, prefix string) *s3Store {
	st, err := NewS3(S3Config{
		Endpoint:  srv.URL, // 带 http:// scheme，构造器应自动剥离
		Bucket:    "jm-artifacts",
		Region:    "us-east-1",
		Prefix:    prefix,
		AccessKey: "test-ak",
		SecretKey: "test-sk",
		UseSSL:    false,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	})
	require.NoError(t, err)
	s := st.(*s3Store)
	s.now = func() time.Time { return awsDocTime }
	return s
}

// TestS3Store_RoundTrip_FakeServer 假 S3 全链路：PutFile（path-style + UNSIGNED-PAYLOAD +
// Authorization 结构）→ Open 内容一致 → Stat 尺寸 → Delete 幂等 → 缺失 ErrBlobNotFound。
func TestS3Store_RoundTrip_FakeServer(t *testing.T) {
	fake, srv := newFakeS3(t, "jm-artifacts")
	store := newTestS3Store(t, srv, "jm-prefix")
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "blob.bin")
	content := []byte("hello artifact external storage")
	require.NoError(t, os.WriteFile(src, content, 0o644))

	key := "var/artifacts/client-file/ab/abcdef123456.zip"
	require.NoError(t, store.PutFile(ctx, key, src, int64(len(content))))

	// 对象键 = prefix + CAS 相对路径（path-style，bucket 在路径段）。
	require.Equal(t, content, fake.objects["jm-prefix/"+key])
	// SigV4 请求形态：流式上传用 UNSIGNED-PAYLOAD，Authorization 为 SigV4 结构。
	require.Equal(t, "UNSIGNED-PAYLOAD", fake.lastPut.Get("X-Amz-Content-Sha256"))
	require.Contains(t, fake.lastPut.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test-ak/20130524/us-east-1/s3/aws4_request")
	require.Contains(t, fake.lastPut.Get("Authorization"), "SignedHeaders=host;x-amz-content-sha256;x-amz-date")

	rc, err := store.Open(ctx, key)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, content, got)

	info, err := store.Stat(ctx, key)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), info.Size)

	require.NoError(t, store.Delete(ctx, key))
	require.NoError(t, store.Delete(ctx, key), "重复删除应幂等")

	_, err = store.Open(ctx, key)
	require.ErrorIs(t, err, ErrBlobNotFound)
	_, err = store.Stat(ctx, key)
	require.ErrorIs(t, err, ErrBlobNotFound)
}

// TestS3Store_List_FakeServer List 走 ListObjectsV2 且返回键剥掉渠道 Prefix。
func TestS3Store_List_FakeServer(t *testing.T) {
	fake, srv := newFakeS3(t, "jm-artifacts")
	store := newTestS3Store(t, srv, "pfx")
	fake.objects["pfx/var/artifacts/client-file/aa/one.zip"] = []byte("1")
	fake.objects["pfx/var/artifacts/client-file/bb/two.zip"] = []byte("22")
	fake.objects["pfx/other/notmatch.bin"] = []byte("x")

	out, err := store.List(context.Background(), "var/artifacts/client-file/", 100)
	require.NoError(t, err)
	require.Len(t, out, 2)
	keys := []string{out[0].Key, out[1].Key}
	require.Contains(t, keys, "var/artifacts/client-file/aa/one.zip")
	require.Contains(t, keys, "var/artifacts/client-file/bb/two.zip")
}

// TestS3Store_Presign_ParamsAndTTL 适配器 Presign 出 path-style URL 且含全部
// X-Amz-* 参数与指定 TTL。
func TestS3Store_Presign_ParamsAndTTL(t *testing.T) {
	_, srv := newFakeS3(t, "jm-artifacts")
	store := newTestS3Store(t, srv, "pfx")

	u, err := store.Presign("var/artifacts/client-file/ab/abc.zip", 600*time.Second)
	require.NoError(t, err)

	parsed, perr := url.Parse(u)
	require.NoError(t, perr)
	require.Equal(t, "/jm-artifacts/pfx/var/artifacts/client-file/ab/abc.zip", parsed.Path)
	q := parsed.Query()
	require.Equal(t, "AWS4-HMAC-SHA256", q.Get("X-Amz-Algorithm"))
	require.Equal(t, "600", q.Get("X-Amz-Expires"))
	require.Equal(t, "host", q.Get("X-Amz-SignedHeaders"))
	require.Equal(t, "20130524T000000Z", q.Get("X-Amz-Date"))
	require.NotEmpty(t, q.Get("X-Amz-Signature"))
	require.Contains(t, q.Get("X-Amz-Credential"), "test-ak/20130524/us-east-1/s3/aws4_request")
}

// TestNewS3_Validation bucket/endpoint 必填；endpoint 剥 scheme。
func TestNewS3_Validation(t *testing.T) {
	_, err := NewS3(S3Config{Endpoint: "minio:9000"})
	require.Error(t, err)
	_, err = NewS3(S3Config{Bucket: "b"})
	require.Error(t, err)

	st, err := NewS3(S3Config{Endpoint: "https://minio.example:9000/", Bucket: "b", UseSSL: true})
	require.NoError(t, err)
	s := st.(*s3Store)
	require.Equal(t, "minio.example:9000", s.endpoint, "endpoint 剥离 scheme 与尾斜杠")
	require.Equal(t, "https", s.scheme)
	require.Equal(t, "us-east-1", s.region, "region 缺省 us-east-1")

	plain, err := NewS3(S3Config{Endpoint: "http://rustfs.lan:9000", Bucket: "b", UseSSL: false})
	require.NoError(t, err)
	require.Equal(t, "http", plain.(*s3Store).scheme, "UseSSL=false 走 http（rustfs 内网常态）")
}

// TestS3Store_RealMinIO 真实 MinIO 端到端（JM_ACC_MINIO_ENDPOINT 门控，不影响常规 CI）：
// PutFile → Stat → Open → Presign（匿名 GET 直下）→ Delete 全链路被真实 S3 接受。
func TestS3Store_RealMinIO(t *testing.T) {
	endpoint := os.Getenv("JM_ACC_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("未设置 JM_ACC_MINIO_ENDPOINT，跳过真实 MinIO 集成测试")
	}
	bucket := os.Getenv("JM_ACC_MINIO_BUCKET")
	if bucket == "" {
		bucket = "jm-artifacts"
	}
	ak := os.Getenv("JM_ACC_MINIO_AK")
	if ak == "" {
		ak = "minioadmin"
	}
	sk := os.Getenv("JM_ACC_MINIO_SK")
	if sk == "" {
		sk = "minioadmin"
	}
	store, err := NewS3(S3Config{Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", Prefix: "fr347-test", AccessKey: ak, SecretKey: sk, UseSSL: false})
	require.NoError(t, err)
	ctx := context.Background()

	src := filepath.Join(t.TempDir(), "real.bin")
	content := []byte("fr-347 real minio round trip")
	require.NoError(t, os.WriteFile(src, content, 0o644))
	key := "var/artifacts/client-file/te/realminio-test.bin"

	require.NoError(t, store.PutFile(ctx, key, src, int64(len(content))))
	defer func() { _ = store.Delete(ctx, key) }()

	info, err := store.Stat(ctx, key)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), info.Size)

	rc, err := store.Open(ctx, key)
	require.NoError(t, err)
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	require.Equal(t, content, got)

	// 预签名 URL 匿名可下（302 分发的地基）。
	u, err := store.Presign(key, 5*time.Minute)
	require.NoError(t, err)
	resp, err := http.Get(u)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "预签名 URL 应匿名可下载: %s", string(body))
	require.Equal(t, content, body)

	require.NoError(t, store.Delete(ctx, key))
}
