package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObjectKey(t *testing.T) {
	require.Equal(t, "inst-1/bk-1.tar.gz", ObjectKey("", "inst-1", "bk-1"))
	require.Equal(t, "p/inst-1/bk-1.tar.gz", ObjectKey("p", "inst-1", "bk-1"))
	require.Equal(t, "a/b/inst-1/bk-1.tar.gz", ObjectKey("/a/b/", "inst-1", "bk-1"))
}

func TestNew_LocalRejected(t *testing.T) {
	_, err := New(Config{Type: TypeLocal})
	require.Error(t, err)
	_, err = New(Config{Type: ""})
	require.Error(t, err)
	_, err = New(Config{Type: "ftp"})
	require.Error(t, err)
}

func TestNew_S3RequiresBucketAndEndpoint(t *testing.T) {
	_, err := New(Config{Type: TypeS3, Endpoint: "s3.local"})
	require.Error(t, err) // 缺 bucket
	_, err = New(Config{Type: TypeS3, Bucket: "b"})
	require.Error(t, err) // 缺 endpoint
	b, err := New(Config{Type: TypeS3, Bucket: "b", Endpoint: "s3.local:9000"})
	require.NoError(t, err)
	require.NotNil(t, b)
}

// fakeDAV 是一个最小内存 WebDAV 服务器，支持 PUT/GET/DELETE/MKCOL。
type fakeDAV struct {
	mu      sync.Mutex
	objects map[string][]byte
	user    string
	pass    string
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.user != "" {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.user || p != f.pass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case "MKCOL":
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = body
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		if _, ok := f.objects[r.URL.Path]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestWebDAV_RoundTrip(t *testing.T) {
	dav := &fakeDAV{objects: map[string][]byte{}, user: "u", pass: "p"}
	srv := httptest.NewServer(dav)
	defer srv.Close()

	b, err := New(Config{Type: TypeWebDAV, Endpoint: srv.URL + "/backups", AccessKey: "u", SecretKey: "p"})
	require.NoError(t, err)

	ctx := context.Background()
	key := "inst-1/bk-1.tar.gz"
	payload := []byte("archive-bytes")

	// 上传 → 下载内容一致。
	require.NoError(t, b.Upload(ctx, key, bytes.NewReader(payload), int64(len(payload))))
	rc, err := b.Download(ctx, key)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, payload, got)

	// 删除后再下载应失败。
	require.NoError(t, b.Delete(ctx, key))
	_, err = b.Download(ctx, key)
	require.Error(t, err)

	// 删除不存在对象幂等成功。
	require.NoError(t, b.Delete(ctx, "inst-1/missing.tar.gz"))
}

func TestWebDAV_AuthFailure(t *testing.T) {
	dav := &fakeDAV{objects: map[string][]byte{}, user: "u", pass: "p"}
	srv := httptest.NewServer(dav)
	defer srv.Close()

	b, err := New(Config{Type: TypeWebDAV, Endpoint: srv.URL, AccessKey: "u", SecretKey: "wrong"})
	require.NoError(t, err)
	err = b.Upload(context.Background(), "k.tar.gz", strings.NewReader("x"), 1)
	require.Error(t, err)
}

// TestS3_SignedRoundTrip 用 httptest 模拟 S3：校验带 SigV4 Authorization 头并完成上传/下载/删除。
func TestS3_SignedRoundTrip(t *testing.T) {
	objects := map[string][]byte{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 必须带 SigV4 授权头与内容哈希头。
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Amz-Content-Sha256") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			b, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	endpoint := strings.TrimPrefix(srv.URL, "http://")
	b, err := New(Config{Type: TypeS3, Endpoint: endpoint, Bucket: "backups", Region: "us-east-1", AccessKey: "ak", SecretKey: "sk", UseSSL: false})
	require.NoError(t, err)

	ctx := context.Background()
	key := "inst-1/bk-1.tar.gz"
	payload := []byte("s3-archive")
	require.NoError(t, b.Upload(ctx, key, bytes.NewReader(payload), int64(len(payload))))

	rc, err := b.Download(ctx, key)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, payload, got)

	require.NoError(t, b.Delete(ctx, key))
	// path-style：对象落在 /backups/inst-1/bk-1.tar.gz。
	_, ok := objects["/backups/"+key]
	require.False(t, ok)
}
