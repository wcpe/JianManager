// Package artifactcache 提供 Worker 节点本地的内容寻址制品缓存（FR-178）。
//
// 痛点：建实例时 DownloadCore 每次都把核心 jar 重新下载到实例工作目录、不查本地有没有，
// 删实例再建 = 重下大 jar 等很久。本缓存按 sha256 持久缓存下载过的核心 jar，建实例命中
// 即从缓存秒拷到工作目录（免网络），未命中才下载并在校验后存入缓存。
//
// 布局：<root>/<sha256[:2]>/<sha256> 为 blob，<root>/<sha256[:2]>/<sha256>.meta 为
// sidecar 元数据 JSON（name/type/version/sourceUrl/size/cachedAt/lastUsedAt）。
// 这是 Worker 本地派生资产，绝不进 CP 数据库，可随时整体删除重建。
//
// 范围（首版写死）：仅服务于 DownloadCore 的服务端核心 jar，不缓存插件/其它下载路径。
// 并发安全：写用临时文件 + 原子 rename；同 sha256 并发写「最后 rename 胜」，内容一致无害。
package artifactcache

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Meta 是缓存项的 sidecar 元数据（随 blob 一同落盘，命中时 touch LastUsedAt）。
type Meta struct {
	// Name 人类可读名称（如 "paper-1.20.4"），缺省可空。
	Name string `json:"name"`
	// Type 制品类型（首版固定 "core"）。
	Type string `json:"type"`
	// Version 版本标记（如 "1.20.4-435"），可空。
	Version string `json:"version"`
	// SourceURL 首次下载来源 URL（诊断用），可空。
	SourceURL string `json:"sourceUrl"`
	// Size 字节数。
	Size int64 `json:"size"`
	// CachedAt 首次存入缓存时间。
	CachedAt time.Time `json:"cachedAt"`
	// LastUsedAt 最近命中（GetTo）或存入时间，LRU 淘汰依据。
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// Entry 是 List 返回的一条缓存项（合并 sha256 与元数据）。
// meta 缺失（仅有 blob）时 Name/Version 为空、Size 取 blob 实际大小、LastUsedAt 取 blob mtime。
type Entry struct {
	SHA256     string    `json:"sha256"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Version    string    `json:"version"`
	Size       int64     `json:"size"`
	CachedAt   time.Time `json:"cachedAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// Cache 是一个根目录下的内容寻址制品缓存。可被多个 goroutine 安全共享。
type Cache struct {
	root string
	mu   sync.Mutex // 保护 cap 与淘汰临界区（List/Put 的 LRU 决策）
	cap  int64      // 容量上限（字节）；0=不限
}

// New 创建一个以 root 为根的缓存（root 缺失会在写入时按需创建）。
func New(root string) *Cache {
	return &Cache{root: root}
}

// SetCap 设置容量上限（字节，0=不限）。下次 Put 存入后若超限按 LRU 淘汰。
func (c *Cache) SetCap(maxBytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxBytes < 0 {
		maxBytes = 0
	}
	c.cap = maxBytes
}

// Cap 返回当前容量上限（字节，0=不限）。
func (c *Cache) Cap() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cap
}

// blobPath 返回 sha256 对应 blob 的绝对路径（<root>/<sha[:2]>/<sha>）。
func (c *Cache) blobPath(sha string) string {
	return filepath.Join(c.root, sha[:2], sha)
}

// metaPath 返回 sha256 对应 sidecar meta 的绝对路径。
func (c *Cache) metaPath(sha string) string {
	return c.blobPath(sha) + ".meta"
}

// validSHA 粗校验 sha256 hex（64 位十六进制小写），防止路径穿越与脏键。
func validSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Has 报告 sha256 是否已缓存（blob 存在）。
func (c *Cache) Has(sha string) bool {
	if !validSHA(sha) {
		return false
	}
	_, err := os.Stat(c.blobPath(sha))
	return err == nil
}

// GetTo 把缓存中 sha256 的 blob 拷贝到 dest（命中返回 true 并 touch LastUsedAt）。
// 未命中返回 (false, nil) 且不创建 dest，调用方据此走下载路径。
// 拷贝用临时文件 + 原子 rename，避免半成品文件被实例读到。
func (c *Cache) GetTo(sha, dest string) (bool, error) {
	if !validSHA(sha) {
		return false, nil
	}
	blob := c.blobPath(sha)
	src, err := os.Open(blob)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("打开缓存 blob 失败: %w", err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, fmt.Errorf("创建目标目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".artifactcache-*.tmp")
	if err != nil {
		return false, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return false, fmt.Errorf("拷贝缓存内容失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return false, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return false, fmt.Errorf("落地缓存内容失败: %w", err)
	}

	c.touch(sha)
	return true, nil
}

// Put 把 srcPath 文件存入缓存（键为 sha256，调用方保证内容确为该 sha256）。
// 已存在则仅刷新 meta（不重复拷贝）。存入后若设了 cap 且超限，按 LRU（lastUsedAt 升序）淘汰。
// 写用临时文件 + 原子 rename 保证并发安全。
func (c *Cache) Put(sha, srcPath string, meta Meta) error {
	if !validSHA(sha) {
		return fmt.Errorf("非法 sha256 缓存键")
	}
	blob := c.blobPath(sha)
	dir := filepath.Dir(blob)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建缓存分片目录失败: %w", err)
	}

	now := time.Now()
	meta.CachedAt = now
	meta.LastUsedAt = now
	if meta.Size == 0 {
		if st, err := os.Stat(srcPath); err == nil {
			meta.Size = st.Size()
		}
	}

	// blob 已存在则跳过拷贝（内容寻址，相同 sha 内容必同），仅刷新 meta。
	if _, err := os.Stat(blob); err != nil {
		if err := copyAtomic(srcPath, blob, dir); err != nil {
			return err
		}
	}
	if err := c.writeMeta(sha, meta); err != nil {
		return err
	}

	return c.EnforceCap()
}

// List 返回所有缓存项（按 LastUsedAt 降序，最近用在前）。meta 缺失项回退 blob 大小/mtime。
func (c *Cache) List() ([]Entry, error) {
	entries := c.scan()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastUsedAt.After(entries[j].LastUsedAt)
	})
	return entries, nil
}

// TotalBytes 返回缓存总占用字节（所有 blob 之和，不含 meta）。
func (c *Cache) TotalBytes() (int64, error) {
	var total int64
	for _, e := range c.scan() {
		total += e.Size
	}
	return total, nil
}

// Evict 删除指定 sha256 的 blob 与 meta（幂等：不存在不报错）。
func (c *Cache) Evict(sha string) error {
	if !validSHA(sha) {
		return fmt.Errorf("非法 sha256")
	}
	if err := os.Remove(c.blobPath(sha)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除缓存 blob 失败: %w", err)
	}
	if err := os.Remove(c.metaPath(sha)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除缓存 meta 失败: %w", err)
	}
	return nil
}

// Clear 清空整个缓存，返回被删除的缓存项数量。
func (c *Cache) Clear() (int, error) {
	entries := c.scan()
	for _, e := range entries {
		if err := c.Evict(e.SHA256); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}

// touch 刷新 sha256 的 LastUsedAt 为当前时间（命中复用时调用）。meta 不存在则按 blob 现状新建。
func (c *Cache) touch(sha string) {
	m, ok := c.readMeta(sha)
	if !ok {
		// meta 丢失：用 blob 现状重建一份最小 meta。
		if st, err := os.Stat(c.blobPath(sha)); err == nil {
			m = Meta{Size: st.Size(), CachedAt: st.ModTime()}
		}
	}
	m.LastUsedAt = time.Now()
	_ = c.writeMeta(sha, m)
}

// setLastUsedForTest 仅供测试：直接改写某项 lastUsedAt，便于构造 LRU 冷热顺序。
func (c *Cache) setLastUsedForTest(sha string, t time.Time) error {
	m, ok := c.readMeta(sha)
	if !ok {
		if st, err := os.Stat(c.blobPath(sha)); err == nil {
			m = Meta{Size: st.Size(), CachedAt: st.ModTime()}
		}
	}
	m.LastUsedAt = t
	return c.writeMeta(sha, m)
}

// EnforceCap 在设了 cap 时按 LRU（lastUsedAt 升序）淘汰，直到总占用回落到 cap 以内。
// Put 内部会自动调用；外部改 cap（SetCap）后可显式调用以立即收敛。
func (c *Cache) EnforceCap() error {
	c.mu.Lock()
	cap := c.cap
	c.mu.Unlock()
	if cap <= 0 {
		return nil
	}
	entries := c.scan()
	var total int64
	for _, e := range entries {
		total += e.Size
	}
	if total <= cap {
		return nil
	}
	// 按 lastUsedAt 升序（最冷在前）淘汰。
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastUsedAt.Before(entries[j].LastUsedAt)
	})
	for _, e := range entries {
		if total <= cap {
			break
		}
		if err := c.Evict(e.SHA256); err != nil {
			return err
		}
		total -= e.Size
	}
	return nil
}

// scan 遍历缓存根，返回所有缓存项（meta 缺失回退 blob 大小/mtime）。
func (c *Cache) scan() []Entry {
	shards, err := os.ReadDir(c.root)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		shardDir := filepath.Join(c.root, shard.Name())
		files, err := os.ReadDir(shardDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || strings.HasSuffix(name, ".meta") || strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".") {
				continue
			}
			if !validSHA(name) {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			e := Entry{SHA256: name, Size: info.Size(), LastUsedAt: info.ModTime(), CachedAt: info.ModTime()}
			if m, ok := c.readMeta(name); ok {
				e.Name = m.Name
				e.Type = m.Type
				e.Version = m.Version
				if m.Size > 0 {
					e.Size = m.Size
				}
				if !m.CachedAt.IsZero() {
					e.CachedAt = m.CachedAt
				}
				if !m.LastUsedAt.IsZero() {
					e.LastUsedAt = m.LastUsedAt
				}
			}
			out = append(out, e)
		}
	}
	return out
}

// readMeta 读取 sha256 的 sidecar meta。
func (c *Cache) readMeta(sha string) (Meta, bool) {
	raw, err := os.ReadFile(c.metaPath(sha))
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, false
	}
	return m, true
}

// writeMeta 原子写入 sha256 的 sidecar meta。
func (c *Cache) writeMeta(sha string, m Meta) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.metaPath(sha))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".meta-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, c.metaPath(sha)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// copyAtomic 把 src 拷到 dst（同目录临时文件 + 原子 rename），dir 为 dst 所在目录。
func copyAtomic(src, dst, dir string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开源文件失败: %w", err)
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, ".blob-*.tmp")
	if err != nil {
		return fmt.Errorf("创建缓存临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("写入缓存失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("落地缓存 blob 失败: %w", err)
	}
	return nil
}
