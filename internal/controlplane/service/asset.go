package service

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

var (
	// ErrAssetNotFound 资产不存在。
	ErrAssetNotFound = errors.New("资产不存在")
	// ErrAssetInUse 资产被引用，禁止删除。
	ErrAssetInUse = errors.New("资产被引用，无法删除")
	// ErrInvalidAssetType 非法的资产类型。
	ErrInvalidAssetType = errors.New("非法的资产类型")
	// ErrChecksumMismatch 校验和不符，拒绝入库。
	ErrChecksumMismatch = errors.New("校验和不符")
)

// AssetService 制品库服务：类型分区的内容寻址存储（CAS）+ DB 索引。
// 参见 ADR-011: 资产存 var/artifacts/<type>/<sha256[:2]>/<sha256><ext>，
// 类型内按 sha256 去重，入库即算 sha256+md5 并可比对来源校验和。
type AssetService struct {
	db   *gorm.DB
	root *dataroot.Root
	// httpClient IngestFromURL 下载远端资源所用出站 client（经进程级代理，FR-174/ADR-037）。
	// 为 nil 时回退 http.DefaultClient（向后兼容）。
	httpClient *http.Client
	// httpProvider 运行时出站 client 持有者（FR-185/ADR-043）：非 nil 时每次取当前 client，
	// 使设置面板改全局代理后立即生效（无需重启），优先于 httpClient。
	httpProvider func() *http.Client
}

// NewAssetService 创建制品库服务。root 提供 var/artifacts 物理根。
func NewAssetService(db *gorm.DB, root *dataroot.Root) *AssetService {
	return &AssetService{db: db, root: root}
}

// SetHTTPClient 注入出站 client（经进程级代理，FR-174/ADR-037）：IngestFromURL 下载经此 client。
// 由 main 装配；不调用则回退 http.DefaultClient（向后兼容，测试不受影响）。
func (s *AssetService) SetHTTPClient(c *http.Client) {
	s.httpClient = c
}

// SetHTTPClientProvider 注入运行时出站持有者（FR-185/ADR-043）：每次下载取当前 client，
// 使全局代理改动即时生效。优先于 SetHTTPClient 注入的固定 client。
func (s *AssetService) SetHTTPClientProvider(p func() *http.Client) {
	s.httpProvider = p
}

// outboundClient 返回出站 client：优先运行时持有者（取当前），其次固定注入，再回退 DefaultClient。
func (s *AssetService) outboundClient() *http.Client {
	if s.httpProvider != nil {
		if c := s.httpProvider(); c != nil {
			return c
		}
	}
	if s.httpClient != nil {
		return s.httpClient
	}
	return http.DefaultClient
}

// IngestParams 入库参数。
type IngestParams struct {
	Type AssetType
	// Name 人类可读名称，可空。
	Name string
	// Version 版本标记，可空。
	Version string
	// Filename 原始文件名（决定扩展名/下载名），可空。
	Filename string
	// ContentType MIME，可空。
	ContentType string
	// SourceURL 来源地址（下载入库时），可空。
	SourceURL string
	// Metadata 类型相关扩展元数据（JSON 字符串），可空。
	Metadata string
	// ExpectedSHA256 调用方提供的期望 sha256（十六进制），非空时比对，不符拒收。
	ExpectedSHA256 string
	// ExpectedMD5 调用方提供的期望 md5（十六进制），非空时比对，不符拒收。
	ExpectedMD5 string
}

// AssetType 是 model.AssetType 的服务层别名，便于 router/调用方引用。
type AssetType = model.AssetType

// Ingest 把一个数据流写入制品库：算 sha256+md5 → 去重 →（缺失则）落 CAS。
// 流程：
//  1. 先落临时文件并边写边算 sha256/md5/size；
//  2. 若提供期望校验和则比对，不符删临时文件并拒收；
//  3. 若 (type, sha256) 已存在 → 复用记录，bump last_used_at，删临时文件；
//  4. 否则原子移动到 CAS 目标路径并建 DB 记录。
func (s *AssetService) Ingest(r io.Reader, p IngestParams) (*model.Asset, error) {
	if !model.ValidAssetType(p.Type) {
		return nil, ErrInvalidAssetType
	}
	if s.root == nil {
		return nil, fmt.Errorf("制品库未配置数据根")
	}

	cacheDir := s.root.CacheDir()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建缓存目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(cacheDir, "ingest-*.part")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	// 失败路径统一清理临时文件；成功路径已 Rename，Remove 命中不存在被忽略。
	defer func() { _ = os.Remove(tmpPath) }()

	sha := sha256.New()
	md := md5.New()
	size, err := io.Copy(io.MultiWriter(tmp, sha, md), r)
	if cerr := tmp.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("写入临时文件失败: %w", err)
	}

	sum256 := hex.EncodeToString(sha.Sum(nil))
	sumMD5 := hex.EncodeToString(md.Sum(nil))

	if err := verifyExpected(p.ExpectedSHA256, sum256); err != nil {
		return nil, err
	}
	if err := verifyExpected(p.ExpectedMD5, sumMD5); err != nil {
		return nil, err
	}

	// 去重：同 (type, sha256) 直接复用。
	var existing model.Asset
	err = s.db.Where("type = ? AND sha256 = ?", p.Type, sum256).First(&existing).Error
	if err == nil {
		now := time.Now()
		s.db.Model(&existing).Update("last_used_at", &now)
		existing.LastUsedAt = &now
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询资产失败: %w", err)
	}

	relPath := casRelPath(p.Type, sum256, p.Filename)
	absPath := s.root.Abs(relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建制品目录失败: %w", err)
	}
	// 原子落位：把临时文件移动到 CAS 路径。若并发已落位则覆盖为同一内容，无害。
	if err := os.Rename(tmpPath, absPath); err != nil {
		return nil, fmt.Errorf("移动制品到 CAS 失败: %w", err)
	}

	now := time.Now()
	asset := &model.Asset{
		Type:           p.Type,
		Name:           p.Name,
		Version:        p.Version,
		Filename:       p.Filename,
		SHA256:         sum256,
		MD5:            sumMD5,
		Size:           size,
		ContentType:    p.ContentType,
		SourceURL:      p.SourceURL,
		Metadata:       p.Metadata,
		StorageState:   model.AssetStorageHot,
		StorageBackend: model.AssetBackendLocal,
		RefCount:       0,
		RelPath:        relPath,
		LastUsedAt:     &now,
	}
	if err := s.db.Create(asset).Error; err != nil {
		// DB 失败时回滚物理文件，避免孤儿 blob。
		_ = os.Remove(absPath)
		return nil, fmt.Errorf("登记资产失败: %w", err)
	}
	return asset, nil
}

// IngestFromURL 下载远端资源并入库（download → store）。
// 供 FR-034 建服取核心时复用：优先命中去重、缺失才真正落盘。
// 若 p.SourceURL 为空则填为 rawURL；若 p.Filename 为空则取 URL 末段。
func (s *AssetService) IngestFromURL(ctx context.Context, rawURL string, p IngestParams) (*model.Asset, error) {
	if p.SourceURL == "" {
		p.SourceURL = rawURL
	}
	if p.Filename == "" {
		if u, err := url.Parse(rawURL); err == nil {
			p.Filename = filepath.Base(u.Path)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造下载请求失败: %w", err)
	}
	resp, err := s.outboundClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	if p.ContentType == "" {
		p.ContentType = resp.Header.Get("Content-Type")
	}
	return s.Ingest(resp.Body, p)
}

// IngestFromPath 从本地路径登记入库（register-from-path）。
func (s *AssetService) IngestFromPath(path string, p IngestParams) (*model.Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()
	if p.Filename == "" {
		p.Filename = filepath.Base(path)
	}
	return s.Ingest(f, p)
}

// List 按类型分页查询资产；typeFilter 为空时不过滤类型。
// 返回当前页、总数。page 从 1 起，pageSize<=0 时取默认 20。
func (s *AssetService) List(typeFilter AssetType, page, pageSize int) ([]model.Asset, int64, error) {
	if typeFilter != "" && !model.ValidAssetType(typeFilter) {
		return nil, 0, ErrInvalidAssetType
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	q := s.db.Model(&model.Asset{})
	if typeFilter != "" {
		q = q.Where("type = ?", typeFilter)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计资产失败: %w", err)
	}
	var assets []model.Asset
	if err := q.Order("id desc").Limit(pageSize).Offset((page - 1) * pageSize).Find(&assets).Error; err != nil {
		return nil, 0, fmt.Errorf("查询资产失败: %w", err)
	}
	return assets, total, nil
}

// GetByID 按 ID 获取资产。
func (s *AssetService) GetByID(id uint) (*model.Asset, error) {
	var asset model.Asset
	if err := s.db.First(&asset, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssetNotFound
		}
		return nil, fmt.Errorf("查询资产失败: %w", err)
	}
	return &asset, nil
}

// Delete 删除资产；被引用（ref_count>0）时拒绝。删除 DB 记录与物理文件。
func (s *AssetService) Delete(id uint) error {
	asset, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if asset.RefCount > 0 {
		return fmt.Errorf("%w: 当前引用数 %d", ErrAssetInUse, asset.RefCount)
	}
	if err := s.db.Delete(&model.Asset{}, id).Error; err != nil {
		return fmt.Errorf("删除资产记录失败: %w", err)
	}
	if asset.RelPath != "" && s.root != nil {
		_ = os.Remove(s.root.Abs(asset.RelPath))
	}
	return nil
}

// AbsPath 返回资产物理文件的绝对路径（供下载/读取）。
func (s *AssetService) AbsPath(a *model.Asset) string {
	if s.root == nil || a.RelPath == "" {
		return ""
	}
	return s.root.Abs(a.RelPath)
}

// casRelPath 计算资产相对数据根的 CAS 路径：
// var/artifacts/<type>/<sha256 前 2 位>/<sha256><ext>。
func casRelPath(t AssetType, sha256hex, filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	shard := sha256hex[:2]
	// 以「/」分隔保证跨平台便携登记，Abs 时再转本地分隔符。
	return "var/artifacts/" + string(t) + "/" + shard + "/" + sha256hex + ext
}

// verifyExpected 当 expected 非空时与 actual 比对（忽略大小写），不符返回 ErrChecksumMismatch。
func verifyExpected(expected, actual string) error {
	if expected == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(expected), actual) {
		return fmt.Errorf("%w: 期望 %s，实际 %s", ErrChecksumMismatch, expected, actual)
	}
	return nil
}
