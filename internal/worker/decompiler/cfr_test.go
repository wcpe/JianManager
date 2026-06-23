package decompiler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// hexSHA256 返回 data 的 SHA-256 十六进制串（测试辅助）。
func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("hello cfr")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	require.NoError(t, verifySHA256(data, good))
	require.Error(t, verifySHA256(data, "deadbeef"))
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "cfr.jar")
	require.NoError(t, writeFileAtomic(dest, []byte("jarbytes")))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, "jarbytes", string(got))
	// 目录里不应残留临时文件。
	entries, err := os.ReadDir(filepath.Dir(dest))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "cfr.jar", entries[0].Name())
}

func TestProvider_ResolveConfigPath(t *testing.T) {
	dir := t.TempDir()
	jar := filepath.Join(dir, "my-cfr.jar")
	require.NoError(t, os.WriteFile(jar, []byte("x"), 0o644))

	p := NewProvider(Config{ConfigPath: jar})
	got, err := p.Resolve()
	require.NoError(t, err)
	require.Equal(t, jar, got)

	// 配置路径不存在 → 报错。
	p2 := NewProvider(Config{ConfigPath: filepath.Join(dir, "missing.jar")})
	_, err = p2.Resolve()
	require.Error(t, err)
}

func TestProvider_ResolveEmbedded(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	p := NewProvider(Config{
		CacheDir: cache,
		Embedded: func() []byte { return []byte("embedded-cfr-bytes") },
	})
	got, err := p.Resolve()
	require.NoError(t, err)
	// 内嵌应落到缓存目录的 cfr-<ver>.jar。
	require.Equal(t, filepath.Join(cache, "cfr-"+CFRVersion+".jar"), got)
	b, err := os.ReadFile(got)
	require.NoError(t, err)
	require.Equal(t, "embedded-cfr-bytes", string(b))
}

func TestProvider_ResolveCacheHit(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	require.NoError(t, os.MkdirAll(cache, 0o755))
	cached := filepath.Join(cache, "cfr-"+CFRVersion+".jar")
	require.NoError(t, os.WriteFile(cached, []byte("cached"), 0o644))

	// 即便提供了内嵌，缓存命中应优先（不重复写）。
	embeddedCalled := false
	p := NewProvider(Config{
		CacheDir: cache,
		Embedded: func() []byte { embeddedCalled = true; return []byte("other") },
	})
	got, err := p.Resolve()
	require.NoError(t, err)
	require.Equal(t, cached, got)
	require.False(t, embeddedCalled, "缓存命中时不应触碰内嵌")
}

func TestProvider_DownloadRejectsBadPin(t *testing.T) {
	// 桩服务器返回「内容指纹不匹配」字节：下载应被 sha256 pin 拒绝，且不落地。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not the real cfr jar"))
	}))
	defer srv.Close()

	cache := filepath.Join(t.TempDir(), "cache")
	p := NewProvider(Config{CacheDir: cache, AllowDownload: true})
	p.httpClient = srv.Client()
	p.downloadURL = srv.URL

	_, err := p.Resolve()
	require.Error(t, err)
	require.Contains(t, err.Error(), "校验失败")
	// 校验失败不应落地半截 jar。
	require.NoFileExists(t, p.cachedJarPath())
}

func TestProvider_DownloadAcceptsMatchingPin(t *testing.T) {
	// 桩服务器返回内容与其真实 sha256 匹配的字节：把 Provider 的 pin 改成该内容的指纹，
	// 验证「下载→校验通过→落缓存」整链。
	payload := []byte("a faux cfr jar payload for pin test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	cache := filepath.Join(t.TempDir(), "cache")
	p := NewProvider(Config{CacheDir: cache, AllowDownload: true})
	p.httpClient = srv.Client()
	p.downloadURL = srv.URL

	got, err := p.downloadWithPin(p.cachedJarPath(), hexSHA256(payload))
	require.NoError(t, err)
	require.True(t, got)
	b, err := os.ReadFile(p.cachedJarPath())
	require.NoError(t, err)
	require.Equal(t, payload, b)
}

func TestProvider_NoSourceAvailable(t *testing.T) {
	// 无配置、无内嵌、无缓存、禁下载 → 报错。
	p := NewProvider(Config{AllowDownload: false})
	_, err := p.Resolve()
	require.Error(t, err)
}
