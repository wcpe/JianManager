package artifactcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCache_GetToEvictsCorruptBlob 命中校验（FR-330）：缓存 blob 内容与 sha256 键不符
// （磁盘损坏/被外部篡改）时，GetTo 必须按未命中返回、不落地脏内容，并作废该条目，
// 让调用方回退远程下载重建缓存。
func TestCache_GetToEvictsCorruptBlob(t *testing.T) {
	root := t.TempDir()
	c := New(root)
	src, sum := writeTemp(t, []byte("good-core-jar-content"))
	require.NoError(t, c.Put(sum, src, Meta{Name: "paper-1.21", Type: "core", Size: 21}))

	// 篡改 blob：内容不再匹配 sha256 键。
	blob := filepath.Join(root, sum[:2], sum)
	require.NoError(t, os.WriteFile(blob, []byte("corrupted-bits"), 0o644))

	dst := filepath.Join(t.TempDir(), "server.jar")
	hit, err := c.GetTo(sum, dst)
	require.NoError(t, err)
	assert.False(t, hit, "损坏条目必须按未命中处理")
	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "损坏内容不得落地到目标")
	assert.False(t, c.Has(sum), "损坏条目应被作废（blob 删除）")
	_, metaExists := c.readMeta(sum)
	assert.False(t, metaExists, "损坏条目的 meta 应一并删除")
}

// TestCache_LookupCoreKey 组合键反查（FR-330）：无 sha256 的下载源按 core|version|build
// 组合键定位缓存条目；未知键/空键返回未命中。
func TestCache_LookupCoreKey(t *testing.T) {
	c := New(t.TempDir())
	src, sum := writeTemp(t, []byte("sponge-core-jar"))
	require.NoError(t, c.Put(sum, src, Meta{
		Name: "spongevanilla-1.21.1", Type: "core", CoreKey: "spongevanilla|1.21.1|2665", Size: 15,
	}))

	got, ok := c.LookupCoreKey("spongevanilla|1.21.1|2665")
	require.True(t, ok)
	assert.Equal(t, sum, got)

	_, ok = c.LookupCoreKey("spongevanilla|1.21.1|9999")
	assert.False(t, ok, "未知组合键不得命中")
	_, ok = c.LookupCoreKey("")
	assert.False(t, ok, "空键不得命中")
}

// TestCache_LookupCoreKeyPrefersNewest 同一组合键存在多条（异常态：重复缓存）时取最近使用的一条。
func TestCache_LookupCoreKeyPrefersNewest(t *testing.T) {
	c := New(t.TempDir())
	const key = "spongevanilla|1.21.1|2665"
	oldSrc, oldSum := writeTemp(t, []byte("old-build-bytes"))
	newSrc, newSum := writeTemp(t, []byte("new-build-bytes!"))
	require.NoError(t, c.Put(oldSum, oldSrc, Meta{Type: "core", CoreKey: key, Size: 15}))
	require.NoError(t, c.Put(newSum, newSrc, Meta{Type: "core", CoreKey: key, Size: 16}))
	require.NoError(t, c.setLastUsedForTest(oldSum, time.Now().Add(-time.Hour)))

	got, ok := c.LookupCoreKey(key)
	require.True(t, ok)
	assert.Equal(t, newSum, got, "多条命中应取 lastUsedAt 最新一条")
}
