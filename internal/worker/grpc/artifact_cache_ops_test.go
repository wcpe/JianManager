package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// seedCache 给 server 装一个缓存并塞入一项，返回其 sha256。
func seedCache(t *testing.T, s *Server, content string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "blob.bin")
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))
	sum := sha256.Sum256([]byte(content))
	hexSum := hex.EncodeToString(sum[:])
	require.NoError(t, s.cache.Put(hexSum, src, artifactcache.Meta{Name: "paper", Type: "core", Version: "1.20", Size: int64(len(content))}))
	return hexSum
}

func newCacheServer(t *testing.T) *Server {
	t.Helper()
	tmp := t.TempDir()
	s := NewServer(process.NewManager(tmp), "node-c", nil, nil, nil)
	s.SetArtifactCache(artifactcache.New(filepath.Join(tmp, "cache-root")))
	return s
}

func TestListArtifactCache(t *testing.T) {
	s := newCacheServer(t)
	sum := seedCache(t, s, "abc-core-jar")

	resp, err := s.ListArtifactCache(context.Background(), &workerpb.ListArtifactCacheRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, sum, resp.Items[0].Sha256)
	assert.Equal(t, "paper", resp.Items[0].Name)
	assert.Equal(t, int64(len("abc-core-jar")), resp.TotalBytes)
	assert.Equal(t, int64(0), resp.CapBytes)
}

func TestListArtifactCache_DisabledReturnsEmpty(t *testing.T) {
	s := NewServer(process.NewManager(t.TempDir()), "node-d", nil, nil, nil) // 无缓存
	resp, err := s.ListArtifactCache(context.Background(), &workerpb.ListArtifactCacheRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Items)
	assert.Equal(t, int64(0), resp.TotalBytes)
}

func TestEvictArtifactCache(t *testing.T) {
	s := newCacheServer(t)
	sum := seedCache(t, s, "evict-core")

	resp, err := s.EvictArtifactCache(context.Background(), &workerpb.EvictArtifactCacheRequest{Sha256: sum})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)

	list, _ := s.ListArtifactCache(context.Background(), &workerpb.ListArtifactCacheRequest{})
	assert.Empty(t, list.Items)
}

func TestClearArtifactCache(t *testing.T) {
	s := newCacheServer(t)
	seedCache(t, s, "a-core")
	seedCache(t, s, "b-core")

	resp, err := s.ClearArtifactCache(context.Background(), &workerpb.ClearArtifactCacheRequest{})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, int32(2), resp.Removed)
}

func TestSetArtifactCacheCap_TriggersLRU(t *testing.T) {
	s := newCacheServer(t)
	// 两项各 6 字节，总 12。
	seedCache(t, s, "aaaaaa")
	seedCache(t, s, "bbbbbb")

	resp, err := s.SetArtifactCacheCap(context.Background(), &workerpb.SetArtifactCacheCapRequest{CapBytes: 6})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.Equal(t, int64(6), resp.CapBytes)
	assert.LessOrEqual(t, resp.TotalBytes, int64(6), "设上限后应 LRU 淘汰回落")
}

func TestBrowseDir(t *testing.T) {
	s := NewServer(process.NewManager(t.TempDir()), "node-b", nil, nil, nil)
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "sub1"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "sub2"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "afile.txt"), []byte("x"), 0o644))

	resp, err := s.BrowseDir(context.Background(), &workerpb.BrowseDirRequest{Path: base})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	names := map[string]bool{}
	for _, d := range resp.Dirs {
		names[d.Name] = true
	}
	assert.True(t, names["sub1"])
	assert.True(t, names["sub2"])
	assert.False(t, names["afile.txt"], "只列目录、不列文件")
}

func TestBrowseDir_RejectsRelative(t *testing.T) {
	s := NewServer(process.NewManager(t.TempDir()), "node-b2", nil, nil, nil)
	resp, err := s.BrowseDir(context.Background(), &workerpb.BrowseDirRequest{Path: "relative/path"})
	require.NoError(t, err)
	assert.False(t, resp.Success)
}

func TestBrowseDir_EmptyPathReturnsRoots(t *testing.T) {
	s := NewServer(process.NewManager(t.TempDir()), "node-b3", nil, nil, nil)
	resp, err := s.BrowseDir(context.Background(), &workerpb.BrowseDirRequest{Path: ""})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	assert.NotEmpty(t, resp.Dirs, "空路径应返回可选起点（盘符/根）")
}
