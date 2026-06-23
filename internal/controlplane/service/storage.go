package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

var (
	// ErrStorageNoRoot 数据根未配置。
	ErrStorageNoRoot = errors.New("平台存储未配置数据根")
	// ErrStoragePathEscape 请求路径越出数据根。
	ErrStoragePathEscape = errors.New("路径越出数据根")
	// ErrStorageNotDir 目标不是目录。
	ErrStorageNotDir = errors.New("目标不是目录")
)

// StorageService 是「平台存储资源管理器」（FR-083）的只读浏览 + 受控清理服务。
//
// 它对 CP 侧数据根（ADR-010 的 FHS 布局）提供：
//   - 各 FHS 子目录的占用统计（大小 / 文件数）+ 用途标注；
//   - 数据根内任意目录的只读列举（路径越界守卫，绝不逃出根）；
//   - 制品库归档可见：跨 assets 表（FR-045 storage_state）给出冷热分布；
//   - cache/ 受控清理（仅限缓存目录，二次确认由前端 DangerConfirm 强制，FR-059）。
//
// 与既有边界一致：CP 只读写数据库与本机数据根文件，不经此操作游戏服进程或 Worker 侧
// 数据根（var/servers、opt/jdks 落在各 Worker 本机，按节点经既有 Worker ListFiles gRPC 浏览）。
type StorageService struct {
	db   *gorm.DB
	root *dataroot.Root
}

// NewStorageService 创建平台存储服务。root 提供 CP 侧数据根物理位置。
func NewStorageService(db *gorm.DB, root *dataroot.Root) *StorageService {
	return &StorageService{db: db, root: root}
}

// DirUsage 一个 FHS 子目录的占用统计与用途。
type DirUsage struct {
	// Path 相对数据根、以「/」分隔的路径（如 "var/artifacts"）。
	Path string `json:"path"`
	// Label 用途标注键（前端 i18n 解析，如 "artifacts"）。
	Label string `json:"label"`
	// Size 递归字节占用。
	Size int64 `json:"size"`
	// FileCount 递归文件数（不含目录本身）。
	FileCount int `json:"fileCount"`
	// Exists 目录是否实际存在（缺失子目录也列出以示布局完整）。
	Exists bool `json:"exists"`
	// Clearable 是否允许受控清理（仅 cache/）。
	Clearable bool `json:"clearable"`
}

// ArchiveSummary 制品库归档可见（FR-045 storage_state 冷热分布）。
type ArchiveSummary struct {
	HotCount      int   `json:"hotCount"`
	ArchivedCount int   `json:"archivedCount"`
	ExternalCount int   `json:"externalCount"`
	HotSize       int64 `json:"hotSize"`
	ArchivedSize  int64 `json:"archivedSize"`
	ExternalSize  int64 `json:"externalSize"`
}

// StorageOverview 平台存储概览载荷。
type StorageOverview struct {
	// Base 数据根绝对路径（运维排查用，只读展示）。
	Base string `json:"base"`
	// Dirs FHS 子目录占用统计（固定布局顺序）。
	Dirs []DirUsage `json:"dirs"`
	// TotalSize 数据根总占用。
	TotalSize int64 `json:"totalSize"`
	// TotalFiles 数据根总文件数。
	TotalFiles int `json:"totalFiles"`
	// Archive 制品库冷热分布（归档可见）。
	Archive ArchiveSummary `json:"archive"`
}

// FileEntry 数据根内一个文件/目录项（与前端 explorer FileInfo 同形）。
type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

// layoutEntry 描述一个待统计的 FHS 子目录（相对根 + 用途标签）。
type layoutEntry struct {
	rel   string
	label string
}

// fhsLayout 是概览展示的 FHS 子目录（与 ADR-010 / dataroot.layoutDirs 对齐）。
// 顺序固定，便于 UI 与测试确定；cache 标注为可清理。
var fhsLayout = []layoutEntry{
	{"bin", "bin"},
	{"etc", "etc"},
	{"opt/jdks", "jdks"},
	{"var/servers", "servers"},
	{"var/log", "log"},
	{"var/artifacts", "artifacts"},
	{"cache", "cache"},
}

// cacheRel 是唯一允许受控清理的目录（相对根）。
const cacheRel = "cache"

// Overview 统计各 FHS 子目录占用 + 制品库归档分布。
func (s *StorageService) Overview() (*StorageOverview, error) {
	if s.root == nil {
		return nil, ErrStorageNoRoot
	}
	ov := &StorageOverview{Base: s.root.Base(), Dirs: make([]DirUsage, 0, len(fhsLayout))}
	for _, e := range fhsLayout {
		abs := s.root.Abs(e.rel)
		size, count, exists, err := dirUsage(abs)
		if err != nil {
			return nil, fmt.Errorf("统计目录 %s 失败: %w", e.rel, err)
		}
		ov.Dirs = append(ov.Dirs, DirUsage{
			Path:      e.rel,
			Label:     e.label,
			Size:      size,
			FileCount: count,
			Exists:    exists,
			Clearable: e.rel == cacheRel,
		})
		ov.TotalSize += size
		ov.TotalFiles += count
	}

	archive, err := s.archiveSummary()
	if err != nil {
		return nil, err
	}
	ov.Archive = archive
	return ov, nil
}

// archiveSummary 从 assets 表聚合制品库冷热分布（FR-045 storage_state）。
func (s *StorageService) archiveSummary() (ArchiveSummary, error) {
	var sum ArchiveSummary
	if s.db == nil {
		return sum, nil
	}
	var assets []model.Asset
	if err := s.db.Select("size", "storage_state").Find(&assets).Error; err != nil {
		return sum, fmt.Errorf("查询制品归档分布失败: %w", err)
	}
	for _, a := range assets {
		switch a.StorageState {
		case model.AssetStorageArchived:
			sum.ArchivedCount++
			sum.ArchivedSize += a.Size
		case model.AssetStorageExternal:
			sum.ExternalCount++
			sum.ExternalSize += a.Size
		default:
			sum.HotCount++
			sum.HotSize += a.Size
		}
	}
	return sum, nil
}

// List 列举数据根内某相对目录的直接子项（只读，路径越界守卫）。
// rel 为空表示数据根本身。返回项按「目录在前、再按名」稳定排序。
func (s *StorageService) List(rel string) ([]FileEntry, error) {
	if s.root == nil {
		return nil, ErrStorageNoRoot
	}
	abs, err := s.safeAbs(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 布局中声明但尚未创建的目录视为空，返回空列表而非报错。
			return []FileEntry{}, nil
		}
		return nil, fmt.Errorf("访问目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, ErrStorageNotDir
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}
	out := make([]FileEntry, 0, len(dirents))
	for _, d := range dirents {
		fi, ierr := d.Info()
		if ierr != nil {
			continue
		}
		out = append(out, FileEntry{
			Name:    d.Name(),
			IsDir:   d.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime().Unix(),
		})
	}
	sortEntries(out)
	return out, nil
}

// ClearCache 清空 cache/ 目录内容（受控清理）。仅删除 cache 下的直接子项，
// 保留 cache 目录本身。返回删除的条目数。绝不触及 cache 之外。
func (s *StorageService) ClearCache() (int, error) {
	if s.root == nil {
		return 0, ErrStorageNoRoot
	}
	cacheDir := s.root.Abs(cacheRel)
	dirents, err := os.ReadDir(cacheDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取缓存目录失败: %w", err)
	}
	removed := 0
	for _, d := range dirents {
		target := filepath.Join(cacheDir, d.Name())
		if err := os.RemoveAll(target); err != nil {
			return removed, fmt.Errorf("清理缓存项 %s 失败: %w", d.Name(), err)
		}
		removed++
	}
	return removed, nil
}

// safeAbs 把相对数据根的路径解析为绝对路径，并守卫其落在根内（防 ../ 逃逸）。
func (s *StorageService) safeAbs(rel string) (string, error) {
	// 规整：去掉前导分隔、统一为本地分隔、Clean 折叠 . 与 ..。
	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	if clean == "." || clean == string(filepath.Separator) {
		return s.root.Base(), nil
	}
	if filepath.IsAbs(clean) {
		return "", ErrStoragePathEscape
	}
	abs := filepath.Join(s.root.Base(), clean)
	// 二次确认：Join 后仍须在根内（折叠 .. 后可能逃逸）。
	relBack, err := filepath.Rel(s.root.Base(), abs)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", ErrStoragePathEscape
	}
	return abs, nil
}

// dirUsage 递归统计目录字节占用与文件数。目录不存在返回 (0,0,false,nil)。
func dirUsage(abs string) (size int64, fileCount int, exists bool, err error) {
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return 0, 0, false, nil
		}
		return 0, 0, false, statErr
	}
	if !info.IsDir() {
		// 非目录（异常布局）按单文件计。
		return info.Size(), 1, true, nil
	}
	walkErr := filepath.WalkDir(abs, func(_ string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		fi, ie := d.Info()
		if ie != nil {
			return nil
		}
		size += fi.Size()
		fileCount++
		return nil
	})
	if walkErr != nil {
		return 0, 0, true, walkErr
	}
	return size, fileCount, true, nil
}

// sortEntries 稳定排序：目录在前，组内按名（区分大小写，与文件系统列举一致体验）。
func sortEntries(entries []FileEntry) {
	sort.SliceStable(entries, func(a, b int) bool {
		if entries[a].IsDir != entries[b].IsDir {
			return entries[a].IsDir
		}
		return entries[a].Name < entries[b].Name
	})
}
