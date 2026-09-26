package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Provider 归档对象存储抽象。
//
// LocalArchive 为 Worker 本地目录实现；S3/MinIO 为同接口 stub（foundation 单测
// 使用内存后端，不要求真实对象存储）。生产接线属于后续 FR，不得在本包内发明
// 未登记的网络协议或凭证模型。
type Provider interface {
	// Kind 标识实现：local / s3 / minio。
	Kind() string
	Put(ctx context.Context, key PartitionKey, relPath string, data []byte) error
	Get(ctx context.Context, key PartitionKey, relPath string) ([]byte, error)
	Exists(ctx context.Context, key PartitionKey, relPath string) (bool, error)
	List(ctx context.Context, key PartitionKey) ([]string, error)
	Delete(ctx context.Context, key PartitionKey, relPath string) error
}

// LocalArchive 基于本地目录的 Provider。
// 物理路径：Root / storage_namespace / utc_day / g{generation} / relPath。
type LocalArchive struct {
	Root string
}

// NewLocalArchive 创建本地归档 Provider。root 为空时返回错误路径由调用方拒绝。
func NewLocalArchive(root string) *LocalArchive {
	return &LocalArchive{Root: root}
}

func (l *LocalArchive) Kind() string { return "local" }

// PartitionDir 返回分区根目录（generation 隔离）。
func (l *LocalArchive) PartitionDir(key PartitionKey) string {
	return filepath.Join(l.Root, filepath.FromSlash(key.StorageNamespace), key.UTCDay, fmt.Sprintf("g%d", key.Generation))
}

func (l *LocalArchive) objectPath(key PartitionKey, relPath string) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	clean := filepath.Clean(filepath.FromSlash(relPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: rel_path %q escapes partition", ErrInvalidKey, relPath)
	}
	return filepath.Join(l.PartitionDir(key), clean), nil
}

func (l *LocalArchive) Put(ctx context.Context, key PartitionKey, relPath string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.objectPath(key, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (l *LocalArchive) Get(ctx context.Context, key PartitionKey, relPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := l.objectPath(key, relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (l *LocalArchive) Exists(ctx context.Context, key PartitionKey, relPath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	p, err := l.objectPath(key, relPath)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !st.IsDir(), nil
}

func (l *LocalArchive) List(ctx context.Context, key PartitionKey) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := l.PartitionDir(key)
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (l *LocalArchive) Delete(ctx context.Context, key PartitionKey, relPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.objectPath(key, relPath)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// 对象存储 Provider 种类。
const (
	KindS3    = "s3"
	KindMinio = "minio"
	KindMem   = "memory"
)

// ObjectStore S3/MinIO 同接口 stub。
//
// foundation 阶段后端为进程内 map，保证单测不依赖真实 S3/MinIO；
// 生产客户端（凭证、分片上传、重试策略）在后续 FR 接线，不得在此臆造协议细节。
type ObjectStore struct {
	kind   string
	bucket string
	prefix string

	mu  sync.RWMutex
	mem map[string][]byte
}

// NewS3Provider 返回 S3 形态 Provider（内存后端 stub）。
func NewS3Provider(bucket, prefix string) *ObjectStore {
	return newObjectStore(KindS3, bucket, prefix)
}

// NewMinioProvider 返回 MinIO 形态 Provider（内存后端 stub）。
func NewMinioProvider(bucket, prefix string) *ObjectStore {
	return newObjectStore(KindMinio, bucket, prefix)
}

// NewMemoryProvider 返回通用内存 Provider（测试/本地开发）。
func NewMemoryProvider() *ObjectStore {
	return newObjectStore(KindMem, "mem", "")
}

func newObjectStore(kind, bucket, prefix string) *ObjectStore {
	return &ObjectStore{
		kind:   kind,
		bucket: bucket,
		prefix: strings.Trim(prefix, "/"),
		mem:    make(map[string][]byte),
	}
}

func (o *ObjectStore) Kind() string { return o.kind }

// Bucket 返回对象桶名（stub 可观测性字段）。
func (o *ObjectStore) Bucket() string { return o.bucket }

func (o *ObjectStore) objectKey(key PartitionKey, relPath string) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	clean := pathCleanSlash(relPath)
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: rel_path %q invalid", ErrInvalidKey, relPath)
	}
	parts := []string{}
	if o.prefix != "" {
		parts = append(parts, o.prefix)
	}
	parts = append(parts, key.StorageNamespace, key.UTCDay, fmt.Sprintf("g%d", key.Generation), clean)
	return strings.Join(parts, "/"), nil
}

func pathCleanSlash(p string) string {
	p = filepath.ToSlash(p)
	p = pathClean(p)
	return strings.TrimPrefix(p, "/")
}

// pathClean 是不依赖 OS 分隔符的 slash clean。
func pathClean(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return strings.Join(out, "/")
}

func (o *ObjectStore) Put(ctx context.Context, key PartitionKey, relPath string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ok, err := o.objectKey(key, relPath)
	if err != nil {
		return err
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mem[ok] = buf
	return nil
}

func (o *ObjectStore) Get(ctx context.Context, key PartitionKey, relPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := o.objectKey(key, relPath)
	if err != nil {
		return nil, err
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	data, exists := o.mem[ok]
	if !exists {
		return nil, fmt.Errorf("%w: object %s not found in %s bucket=%s", ErrProviderUnavailable, ok, o.kind, o.bucket)
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	return buf, nil
}

func (o *ObjectStore) Exists(ctx context.Context, key PartitionKey, relPath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ok, err := o.objectKey(key, relPath)
	if err != nil {
		return false, err
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	_, exists := o.mem[ok]
	return exists, nil
}

func (o *ObjectStore) List(ctx context.Context, key PartitionKey) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	prefix := ""
	if o.prefix != "" {
		prefix = o.prefix + "/"
	}
	prefix += key.StorageNamespace + "/" + key.UTCDay + fmt.Sprintf("/g%d/", key.Generation)
	o.mu.RLock()
	defer o.mu.RUnlock()
	var out []string
	for k := range o.mem {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (o *ObjectStore) Delete(ctx context.Context, key PartitionKey, relPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ok, err := o.objectKey(key, relPath)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.mem, ok)
	return nil
}

// Corrupt 测试辅助：故意破坏已存储对象，用于验证损坏对象可重试。
func (o *ObjectStore) Corrupt(key PartitionKey, relPath string, poisoned []byte) error {
	ok, err := o.objectKey(key, relPath)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.mem[ok]; !exists {
		return fmt.Errorf("%w: cannot corrupt missing object %s", ErrProviderUnavailable, ok)
	}
	buf := make([]byte, len(poisoned))
	copy(buf, poisoned)
	o.mem[ok] = buf
	return nil
}

// 确保三种 Provider 在编译期满足同一接口。
var (
	_ Provider = (*LocalArchive)(nil)
	_ Provider = (*ObjectStore)(nil)
)
