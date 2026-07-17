package blobstore

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// localStore 本地数据根 CAS 适配器：封装既有 var/artifacts 落盘行为（零变化）。
// 键即数据根相对路径，经 dataroot.Root.Abs 解析（自动转本地分隔符）。
type localStore struct {
	root *dataroot.Root
}

// NewLocal 创建本地数据根适配器。
func NewLocal(root *dataroot.Root) (Store, error) {
	if root == nil {
		return nil, fmt.Errorf("本地存储缺少数据根")
	}
	return &localStore{root: root}, nil
}

func (l *localStore) Kind() string { return KindLocal }

// PutFile 原子落位：与既有 AssetService.Ingest 的 MkdirAll + os.Rename 逐字节等价。
// 若并发已落位则覆盖为同一内容，无害（内容寻址）。
func (l *localStore) PutFile(_ context.Context, key, srcPath string, _ int64) error {
	absPath := l.root.Abs(key)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("创建制品目录失败: %w", err)
	}
	if err := os.Rename(srcPath, absPath); err != nil {
		return fmt.Errorf("移动制品到 CAS 失败: %w", err)
	}
	return nil
}

func (l *localStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(l.root.Abs(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	return f, nil
}

func (l *localStore) Stat(_ context.Context, key string) (*ObjectInfo, error) {
	info, err := os.Stat(l.root.Abs(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	return &ObjectInfo{Key: key, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Delete 幂等删除：缺失不报错（与既有 Delete 的尽力删除语义一致）。
func (l *localStore) Delete(_ context.Context, key string) error {
	if err := os.Remove(l.root.Abs(key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List 枚举 prefix（数据根相对目录/键前缀）下的文件，登记键统一「/」分隔（与 CAS 键规范一致）。
func (l *localStore) List(_ context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	if limit <= 0 {
		limit = 1000
	}
	base := l.root.Abs(prefix)
	rootAbs := l.root.Abs("")
	var out []ObjectInfo
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if len(out) >= limit {
			return fs.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		rel, rerr := filepath.Rel(rootAbs, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, ObjectInfo{
			Key:     strings.ReplaceAll(rel, string(filepath.Separator), "/"),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

// Presign 本地后端由 CP ServeContent 直出，不支持预签名。
func (l *localStore) Presign(string, time.Duration) (string, error) {
	return "", ErrPresignUnsupported
}
