package artifactcache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTemp 写一个临时源文件并返回其路径与内容 sha256（hex 小写）。
func writeTemp(t *testing.T, data []byte) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.bin")
	require.NoError(t, os.WriteFile(p, data, 0o644))
	sum := sha256.Sum256(data)
	return p, hex.EncodeToString(sum[:])
}

func TestCache_PutThenGetToHit(t *testing.T) {
	c := New(t.TempDir())
	src, sum := writeTemp(t, []byte("hello-core-jar"))

	require.NoError(t, c.Put(sum, src, Meta{Name: "paper", Type: "core", Version: "1.20", Size: 14}))

	// 命中：拷到目标且内容一致。
	dst := filepath.Join(t.TempDir(), "server.jar")
	hit, err := c.GetTo(sum, dst)
	require.NoError(t, err)
	assert.True(t, hit)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "hello-core-jar", string(got))
}

func TestCache_GetToMiss(t *testing.T) {
	c := New(t.TempDir())
	dst := filepath.Join(t.TempDir(), "server.jar")
	hit, err := c.GetTo("0000000000000000000000000000000000000000000000000000000000000000", dst)
	require.NoError(t, err)
	assert.False(t, hit)
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "未命中不应创建目标文件")
}

func TestCache_PathLayoutAndShard(t *testing.T) {
	root := t.TempDir()
	c := New(root)
	src, sum := writeTemp(t, []byte("shard-test"))
	require.NoError(t, c.Put(sum, src, Meta{Name: "x", Size: 10}))

	// 分片目录 <root>/<sha[:2]>/<sha> + sidecar <sha>.meta。
	blob := filepath.Join(root, sum[:2], sum)
	meta := blob + ".meta"
	assert.FileExists(t, blob)
	assert.FileExists(t, meta)
}

func TestCache_ListReturnsMeta(t *testing.T) {
	c := New(t.TempDir())
	src, sum := writeTemp(t, []byte("list-me"))
	require.NoError(t, c.Put(sum, src, Meta{Name: "paper", Type: "core", Version: "1.21", Size: 7}))

	items, err := c.List()
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, sum, items[0].SHA256)
	assert.Equal(t, "paper", items[0].Name)
	assert.Equal(t, "1.21", items[0].Version)
	assert.Equal(t, int64(7), items[0].Size)
	assert.False(t, items[0].LastUsedAt.IsZero())
}

func TestCache_ListMissingMetaFallsBackToSizeOnly(t *testing.T) {
	root := t.TempDir()
	c := New(root)
	// 手工放一个无 meta 的 blob（模拟外部拷入或 meta 丢失）。
	_, sum := writeTemp(t, []byte("orphan-blob"))
	dir := filepath.Join(root, sum[:2])
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, sum), []byte("orphan-blob"), 0o644))

	items, err := c.List()
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, sum, items[0].SHA256)
	assert.Equal(t, int64(11), items[0].Size)
	assert.Empty(t, items[0].Name)
}

func TestCache_TotalBytes(t *testing.T) {
	c := New(t.TempDir())
	a, sumA := writeTemp(t, []byte("aaaa")) // 4
	b, sumB := writeTemp(t, []byte("bbbbbb")) // 6
	require.NoError(t, c.Put(sumA, a, Meta{Size: 4}))
	require.NoError(t, c.Put(sumB, b, Meta{Size: 6}))
	total, err := c.TotalBytes()
	require.NoError(t, err)
	assert.Equal(t, int64(10), total)
}

func TestCache_Evict(t *testing.T) {
	c := New(t.TempDir())
	src, sum := writeTemp(t, []byte("evict-me"))
	require.NoError(t, c.Put(sum, src, Meta{Size: 8}))

	require.NoError(t, c.Evict(sum))
	items, err := c.List()
	require.NoError(t, err)
	assert.Empty(t, items)

	// 幂等：再删不报错。
	assert.NoError(t, c.Evict(sum))
}

func TestCache_Clear(t *testing.T) {
	c := New(t.TempDir())
	a, sumA := writeTemp(t, []byte("aa"))
	b, sumB := writeTemp(t, []byte("bbb"))
	require.NoError(t, c.Put(sumA, a, Meta{Size: 2}))
	require.NoError(t, c.Put(sumB, b, Meta{Size: 3}))

	n, err := c.Clear()
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	items, err := c.List()
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestCache_GetToTouchesLastUsed(t *testing.T) {
	c := New(t.TempDir())
	src, sum := writeTemp(t, []byte("touch-me"))
	require.NoError(t, c.Put(sum, src, Meta{Size: 8}))

	before, err := c.List()
	require.NoError(t, err)
	require.Len(t, before, 1)
	// 把 lastUsed 人为回拨，确认 GetTo 后变新。
	old := time.Now().Add(-time.Hour)
	require.NoError(t, c.setLastUsedForTest(sum, old))

	dst := filepath.Join(t.TempDir(), "out.jar")
	hit, err := c.GetTo(sum, dst)
	require.NoError(t, err)
	require.True(t, hit)

	after, err := c.List()
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.True(t, after[0].LastUsedAt.After(old), "GetTo 命中应 touch lastUsedAt")
}

func TestCache_LRUEvictsColdestOnCap(t *testing.T) {
	c := New(t.TempDir())
	c.SetCap(10) // 上限 10 字节

	// 放三个 4 字节项，分别设递增 lastUsed（a 最冷，c 最热）。
	a, sumA := writeTemp(t, []byte("aaaa"))
	b, sumB := writeTemp(t, []byte("bbbb"))
	d, sumD := writeTemp(t, []byte("dddd"))
	require.NoError(t, c.Put(sumA, a, Meta{Size: 4}))
	require.NoError(t, c.setLastUsedForTest(sumA, time.Now().Add(-3*time.Hour)))
	require.NoError(t, c.Put(sumB, b, Meta{Size: 4}))
	require.NoError(t, c.setLastUsedForTest(sumB, time.Now().Add(-2*time.Hour)))
	// 此时 8 字节 <= 10，未淘汰。
	mid, err := c.TotalBytes()
	require.NoError(t, err)
	assert.Equal(t, int64(8), mid)

	// 放第三个（12 > 10），应按 lastUsed 升序淘汰最冷的 a，直到回落 <=10。
	require.NoError(t, c.Put(sumD, d, Meta{Size: 4}))
	total, err := c.TotalBytes()
	require.NoError(t, err)
	assert.LessOrEqual(t, total, int64(10))

	items, err := c.List()
	require.NoError(t, err)
	have := map[string]bool{}
	for _, it := range items {
		have[it.SHA256] = true
	}
	assert.False(t, have[sumA], "最冷的 a 应被 LRU 淘汰")
	assert.True(t, have[sumD], "最新放入的 d 应保留")
}

func TestCache_SetCapZeroDisablesLRU(t *testing.T) {
	c := New(t.TempDir())
	c.SetCap(0) // 0 = 不限
	for i := 0; i < 5; i++ {
		src, sum := writeTemp(t, []byte{byte('a' + i), byte('a' + i), byte('a' + i)})
		require.NoError(t, c.Put(sum, src, Meta{Size: 3}))
	}
	items, err := c.List()
	require.NoError(t, err)
	assert.Len(t, items, 5, "cap=0 时不淘汰")
}
