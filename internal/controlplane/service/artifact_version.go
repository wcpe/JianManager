package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrArtifactPackageNotFound 表示制品包不存在。
	ErrArtifactPackageNotFound = errors.New("制品包不存在")
	// ErrArtifactSourceNotFound 表示制品来源不存在。
	ErrArtifactSourceNotFound = errors.New("制品来源不存在")
	// ErrArtifactVersionNotFound 表示制品版本不存在。
	ErrArtifactVersionNotFound = errors.New("制品版本不存在")
	// ErrArtifactVersionNotCached 表示版本尚未由 CP 校验并缓存。
	ErrArtifactVersionNotCached = errors.New("制品版本尚未缓存")
	// ErrArtifactVersionInUse 表示版本仍被默认项或运行对象引用。
	ErrArtifactVersionInUse = errors.New("制品版本仍被引用")
	// ErrArtifactProviderUnsupported 表示来源类型尚未实现。
	ErrArtifactProviderUnsupported = errors.New("制品来源类型不受支持")
	// ErrArtifactSourceNotSyncable 表示该来源不支持手动同步。
	ErrArtifactSourceNotSyncable = errors.New("制品来源不支持同步")
	// ErrArtifactReleaseInvalid 表示来源返回的版本元数据不满足部署要求。
	ErrArtifactReleaseInvalid = errors.New("制品发布元数据无效")
	// ErrArtifactLocalUploadInvalid 表示本地上传参数不满足 ServerProbe 制品要求。
	ErrArtifactLocalUploadInvalid = errors.New("本地上传制品参数无效")
	// ErrArtifactLocalUploadTooLarge 表示本地上传文件超过大小限制。
	ErrArtifactLocalUploadTooLarge = errors.New("本地上传制品超过大小限制")
	// ErrArtifactVersionAlreadyExists 表示同一来源中已存在同名版本。
	ErrArtifactVersionAlreadyExists = errors.New("制品版本已存在")
)

// ServerProbeUploadMaxSize 限制单个本地上传的 ServerProbe jar 为 64 MiB。
const ServerProbeUploadMaxSize int64 = 64 << 20

// ArtifactRelease 是 provider 归一后的一个可下载发布资产。
type ArtifactRelease struct {
	Version    string
	ReleaseRef string
	AssetName  string
	URL        string
	SHA256     string
}

// ArtifactVersionProvider 将外部来源归一为可入库的发布列表。
type ArtifactVersionProvider interface {
	ListVersions(ctx context.Context, source model.ArtifactSource) ([]ArtifactRelease, error)
}

// ProbeVersionOrigin 标识实例版本的解析层级，供 API 和 UI 解释继承状态。
type ProbeVersionOrigin string

const (
	ProbeVersionOriginGlobal   ProbeVersionOrigin = "global"
	ProbeVersionOriginNode     ProbeVersionOrigin = "node"
	ProbeVersionOriginInstance ProbeVersionOrigin = "instance"
)

// ArtifactVersionService 管理逻辑制品包、来源和版本，并复用 AssetService 保存校验后的字节。
type ArtifactVersionService struct {
	db            *gorm.DB
	assets        *AssetService
	mu            sync.RWMutex
	providers     map[string]ArtifactVersionProvider
	cacheMu       sync.Mutex
	probeTokenMu  sync.Mutex
	probeTokenKey []byte
}

// NewArtifactVersionService 创建制品版本库服务。
func NewArtifactVersionService(db *gorm.DB, assets *AssetService) *ArtifactVersionService {
	return &ArtifactVersionService{
		db:        db,
		assets:    assets,
		providers: map[string]ArtifactVersionProvider{},
	}
}

// SetProvider 注册或替换一个来源 provider；主要供主程序装配与测试使用。
func (s *ArtifactVersionService) SetProvider(name string, provider ArtifactVersionProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if provider == nil {
		delete(s.providers, name)
		return
	}
	s.providers[name] = provider
}

// EnsureDefaultServerProbe 幂等创建首个 ServerProbe 包和官方 GitHub 来源。
func (s *ArtifactVersionService) EnsureDefaultServerProbe() (*model.ArtifactPackage, *model.ArtifactSource, error) {
	if s.db == nil {
		return nil, nil, errors.New("制品版本库未配置数据库")
	}
	var pkg model.ArtifactPackage
	err := s.db.Where("key = ?", model.ServerProbePackageKey).First(&pkg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		pkg = model.ArtifactPackage{
			Key:       model.ServerProbePackageKey,
			Name:      "ServerProbe",
			AssetType: model.AssetTypeServerProbe,
		}
		if err := s.db.Create(&pkg).Error; err != nil {
			return nil, nil, fmt.Errorf("创建 ServerProbe 制品包失败: %w", err)
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("查询 ServerProbe 制品包失败: %w", err)
	}

	config, _ := json.Marshal(githubReleaseSourceConfig{Repository: "wcpe/ServerProbe"})
	var source model.ArtifactSource
	err = s.db.Where("package_id = ? AND name = ?", pkg.ID, "官方 GitHub Releases").First(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		source = model.ArtifactSource{
			PackageID: pkg.ID,
			Provider:  model.ArtifactProviderGitHubRelease,
			Name:      "官方 GitHub Releases",
			Config:    string(config),
			Enabled:   true,
		}
		if err := s.db.Create(&source).Error; err != nil {
			return nil, nil, fmt.Errorf("创建 ServerProbe 默认来源失败: %w", err)
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("查询 ServerProbe 默认来源失败: %w", err)
	}
	if _, err := s.ensureLocalServerProbeSource(pkg.ID); err != nil {
		return nil, nil, err
	}
	return &pkg, &source, nil
}

// UploadLocalServerProbe 将平台管理员上传的 jar 直接写入 CAS，并登记为可立即选择的本地版本。
func (s *ArtifactVersionService) UploadLocalServerProbe(version, filename string, reader io.Reader) (*model.ArtifactVersion, error) {
	version = strings.TrimSpace(version)
	filename = localUploadFilename(filename)
	if version == "" || len(version) > 128 || filename == "" || !strings.EqualFold(filepath.Ext(filename), ".jar") {
		return nil, ErrArtifactLocalUploadInvalid
	}
	if reader == nil {
		return nil, ErrArtifactLocalUploadInvalid
	}
	if s.assets == nil {
		return nil, errors.New("制品版本库未配置 CAS 服务")
	}

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	pkg, _, err := s.EnsureDefaultServerProbe()
	if err != nil {
		return nil, err
	}
	source, err := s.ensureLocalServerProbeSource(pkg.ID)
	if err != nil {
		return nil, err
	}
	var existing model.ArtifactVersion
	err = s.db.Where("source_id = ? AND version = ?", source.ID, version).First(&existing).Error
	if err == nil {
		return nil, ErrArtifactVersionAlreadyExists
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询本地上传版本失败: %w", err)
	}

	asset, err := s.assets.Ingest(&serverProbeUploadReader{reader: reader, remaining: ServerProbeUploadMaxSize}, IngestParams{
		Type:        pkg.AssetType,
		Name:        pkg.Name,
		Version:     version,
		Filename:    filename,
		ContentType: "application/java-archive",
		SourceURL:   "local://upload",
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	entry := &model.ArtifactVersion{
		PackageID:      pkg.ID,
		SourceID:       source.ID,
		Version:        version,
		ReleaseRef:     "local-upload",
		AssetName:      filename,
		ExpectedSHA256: asset.SHA256,
		SourceURL:      fmt.Sprintf("local://upload/%d", asset.ID),
		AssetID:        asset.ID,
		CachedAt:       &now,
	}
	if err := s.db.Create(entry).Error; err != nil {
		return nil, fmt.Errorf("登记本地上传版本失败: %w", err)
	}
	return s.versionByID(entry.ID)
}

func (s *ArtifactVersionService) ensureLocalServerProbeSource(packageID uint) (*model.ArtifactSource, error) {
	var source model.ArtifactSource
	err := s.db.Where("package_id = ? AND name = ?", packageID, "本地上传").First(&source).Error
	if err == nil {
		if source.Provider != model.ArtifactProviderLocalUpload {
			return nil, fmt.Errorf("本地上传来源名称冲突")
		}
		return &source, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询 ServerProbe 本地上传来源失败: %w", err)
	}
	source = model.ArtifactSource{
		PackageID: packageID,
		Provider:  model.ArtifactProviderLocalUpload,
		Name:      "本地上传",
		Config:    "{}",
		Enabled:   true,
	}
	if err := s.db.Create(&source).Error; err != nil {
		return nil, fmt.Errorf("创建 ServerProbe 本地上传来源失败: %w", err)
	}
	return &source, nil
}

func localUploadFilename(filename string) string {
	return filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
}

type serverProbeUploadReader struct {
	reader    io.Reader
	remaining int64
}

func (r *serverProbeUploadReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			return 0, ErrArtifactLocalUploadTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// SyncSource 同步来源元数据；同步不下载 jar，也不自动改变默认版本。
func (s *ArtifactVersionService) SyncSource(ctx context.Context, sourceID uint) (int, error) {
	source, err := s.sourceByID(sourceID)
	if err != nil {
		return 0, err
	}
	if !source.Enabled {
		return 0, errors.New("制品来源已禁用")
	}
	if source.Provider != model.ArtifactProviderGitHubRelease {
		return 0, ErrArtifactSourceNotSyncable
	}
	provider := s.provider(source.Provider)
	if provider == nil {
		return 0, fmt.Errorf("%w: %s", ErrArtifactProviderUnsupported, source.Provider)
	}
	releases, err := provider.ListVersions(ctx, *source)
	if err != nil {
		s.recordSourceError(source.ID, err)
		return 0, err
	}
	created := 0
	for _, release := range releases {
		if err := validateArtifactRelease(release); err != nil {
			s.recordSourceError(source.ID, err)
			return 0, err
		}
		var existing model.ArtifactVersion
		err := s.db.Where("source_id = ? AND version = ?", source.ID, release.Version).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			entry := model.ArtifactVersion{
				PackageID:      source.PackageID,
				SourceID:       source.ID,
				Version:        release.Version,
				ReleaseRef:     release.ReleaseRef,
				AssetName:      release.AssetName,
				ExpectedSHA256: normalizeSHA256(release.SHA256),
				SourceURL:      release.URL,
			}
			if err := s.db.Create(&entry).Error; err != nil {
				return created, fmt.Errorf("登记制品版本失败: %w", err)
			}
			created++
			continue
		}
		if err != nil {
			return created, fmt.Errorf("查询制品版本失败: %w", err)
		}
		if existing.AssetID != 0 && !strings.EqualFold(existing.ExpectedSHA256, release.SHA256) {
			return created, fmt.Errorf("%w: 已缓存版本 %s 的来源摘要发生变化", ErrArtifactReleaseInvalid, existing.Version)
		}
		if existing.AssetID == 0 {
			if err := s.db.Model(&existing).Updates(map[string]any{
				"release_ref":     release.ReleaseRef,
				"asset_name":      release.AssetName,
				"expected_sha256": normalizeSHA256(release.SHA256),
				"source_url":      release.URL,
				"last_error":      "",
			}).Error; err != nil {
				return created, fmt.Errorf("更新制品版本元数据失败: %w", err)
			}
		}
	}
	now := time.Now()
	if err := s.db.Model(&model.ArtifactSource{}).Where("id = ?", source.ID).Updates(map[string]any{"last_synced_at": now, "last_error": ""}).Error; err != nil {
		return created, fmt.Errorf("更新来源同步状态失败: %w", err)
	}
	return created, nil
}

// ListVersions 按版本入库时间倒序返回某包的版本，并携带已缓存的 CAS asset。
func (s *ArtifactVersionService) ListVersions(packageID uint) ([]model.ArtifactVersion, error) {
	var versions []model.ArtifactVersion
	if err := s.db.Preload("Asset").Where("package_id = ?", packageID).Order("id DESC").Find(&versions).Error; err != nil {
		return nil, fmt.Errorf("查询制品版本失败: %w", err)
	}
	return versions, nil
}

// ServerProbeCatalog 返回首个接入制品包及其来源和版本列表。
func (s *ArtifactVersionService) ServerProbeCatalog() (*model.ArtifactPackage, []model.ArtifactSource, []model.ArtifactVersion, error) {
	pkg, _, err := s.EnsureDefaultServerProbe()
	if err != nil {
		return nil, nil, nil, err
	}
	var sources []model.ArtifactSource
	if err := s.db.Where("package_id = ?", pkg.ID).Order("id ASC").Find(&sources).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("查询制品来源失败: %w", err)
	}
	versions, err := s.ListVersions(pkg.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return pkg, sources, versions, nil
}

// NodeProbeVersion 返回 Worker 为后续新实例指定的版本 ID；0 表示继承全局默认。
func (s *ArtifactVersionService) NodeProbeVersion(nodeID uint) (uint, error) {
	var node model.Node
	if err := s.db.Select("probe_version_id").First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrNodeNotFound
		}
		return 0, fmt.Errorf("查询 Worker 探针版本失败: %w", err)
	}
	return node.ProbeVersionID, nil
}

// InstanceProbeVersion 返回实例显式覆盖的版本 ID；0 表示继承 Worker 或全局默认。
func (s *ArtifactVersionService) InstanceProbeVersion(instanceID uint) (uint, error) {
	var instance model.Instance
	if err := s.db.Select("probe_version_id").First(&instance, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrInstanceNotFound
		}
		return 0, fmt.Errorf("查询实例探针版本失败: %w", err)
	}
	return instance.ProbeVersionID, nil
}

// CacheVersion 下载、校验并把指定版本写入 CAS；已缓存版本直接返回。
func (s *ArtifactVersionService) CacheVersion(ctx context.Context, versionID uint) (*model.ArtifactVersion, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	version, err := s.versionByID(versionID)
	if err != nil {
		return nil, err
	}
	if version.AssetID != 0 {
		return s.versionByID(versionID)
	}
	if s.assets == nil {
		return nil, errors.New("制品版本库未配置 CAS 服务")
	}
	pkg, err := s.packageByID(version.PackageID)
	if err != nil {
		return nil, err
	}
	asset, err := s.assets.IngestFromURL(ctx, version.SourceURL, IngestParams{
		Type:           pkg.AssetType,
		Name:           pkg.Name,
		Version:        version.Version,
		Filename:       version.AssetName,
		ContentType:    "application/java-archive",
		SourceURL:      version.SourceURL,
		ExpectedSHA256: version.ExpectedSHA256,
	})
	if err != nil {
		s.recordVersionError(version.ID, err)
		return nil, err
	}
	now := time.Now()
	if err := s.db.Model(&model.ArtifactVersion{}).Where("id = ?", version.ID).Updates(map[string]any{
		"asset_id":   asset.ID,
		"cached_at":  now,
		"last_error": "",
	}).Error; err != nil {
		return nil, fmt.Errorf("登记制品缓存失败: %w", err)
	}
	return s.versionByID(version.ID)
}

// SetPackageDefaultVersion 设置包的全局默认版本；版本必须已缓存且属于该包。
func (s *ArtifactVersionService) SetPackageDefaultVersion(packageID, versionID uint) error {
	if _, err := s.cachedVersionInPackage(packageID, versionID); err != nil {
		return err
	}
	if err := s.db.Model(&model.ArtifactPackage{}).Where("id = ?", packageID).Update("default_version_id", versionID).Error; err != nil {
		return fmt.Errorf("设置制品包默认版本失败: %w", err)
	}
	return nil
}

// SetNodeProbeVersion 设置 Worker 后续新实例的默认版本；0 表示继承全局。
func (s *ArtifactVersionService) SetNodeProbeVersion(nodeID, versionID uint) error {
	if versionID != 0 {
		pkg, _, err := s.EnsureDefaultServerProbe()
		if err != nil {
			return err
		}
		if _, err := s.cachedVersionInPackage(pkg.ID, versionID); err != nil {
			return err
		}
	}
	result := s.db.Model(&model.Node{}).Where("id = ?", nodeID).Update("probe_version_id", versionID)
	if result.Error != nil {
		return fmt.Errorf("设置 Worker 探针版本失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// SetInstanceProbeVersion 设置实例显式版本；0 表示切回继承。
// 部署编排由调用方在保存成功后按 ResolveInstanceProbeVersion 的结果发起。
func (s *ArtifactVersionService) SetInstanceProbeVersion(instanceID, versionID uint) error {
	if versionID != 0 {
		pkg, _, err := s.EnsureDefaultServerProbe()
		if err != nil {
			return err
		}
		if _, err := s.cachedVersionInPackage(pkg.ID, versionID); err != nil {
			return err
		}
	}
	result := s.db.Model(&model.Instance{}).Where("id = ?", instanceID).Update("probe_version_id", versionID)
	if result.Error != nil {
		return fmt.Errorf("设置实例探针版本失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrInstanceNotFound
	}
	return nil
}

// ResolveInstanceProbeVersion 按实例 → Worker → 全局顺序解析一个已缓存的有效版本。
func (s *ArtifactVersionService) ResolveInstanceProbeVersion(instanceID uint) (*model.ArtifactVersion, ProbeVersionOrigin, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrInstanceNotFound
		}
		return nil, "", fmt.Errorf("查询实例失败: %w", err)
	}
	if inst.ProbeVersionID != 0 {
		version, err := s.cachedVersion(inst.ProbeVersionID)
		return version, ProbeVersionOriginInstance, err
	}
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		return nil, "", fmt.Errorf("查询实例所属 Worker 失败: %w", err)
	}
	if node.ProbeVersionID != 0 {
		version, err := s.cachedVersion(node.ProbeVersionID)
		return version, ProbeVersionOriginNode, err
	}
	pkg, _, err := s.EnsureDefaultServerProbe()
	if err != nil {
		return nil, "", err
	}
	if pkg.DefaultVersionID == 0 {
		return nil, "", ErrArtifactVersionNotCached
	}
	version, err := s.cachedVersion(pkg.DefaultVersionID)
	return version, ProbeVersionOriginGlobal, err
}

// DeleteVersion 删除未被默认项、Worker 或实例引用的版本记录；CAS 文件由既有制品生命周期管理。
func (s *ArtifactVersionService) DeleteVersion(versionID uint) error {
	version, err := s.versionByID(versionID)
	if err != nil {
		return err
	}
	var count int64
	if err := s.db.Model(&model.ArtifactPackage{}).Where("default_version_id = ?", version.ID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrArtifactVersionInUse
	}
	if err := s.db.Model(&model.Node{}).Where("probe_version_id = ?", version.ID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrArtifactVersionInUse
	}
	if err := s.db.Model(&model.Instance{}).Where("probe_version_id = ?", version.ID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrArtifactVersionInUse
	}
	if err := s.db.Delete(&model.ArtifactVersion{}, version.ID).Error; err != nil {
		return fmt.Errorf("删除制品版本失败: %w", err)
	}
	return nil
}

func (s *ArtifactVersionService) sourceByID(id uint) (*model.ArtifactSource, error) {
	var source model.ArtifactSource
	if err := s.db.First(&source, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrArtifactSourceNotFound
		}
		return nil, fmt.Errorf("查询制品来源失败: %w", err)
	}
	return &source, nil
}

func (s *ArtifactVersionService) packageByID(id uint) (*model.ArtifactPackage, error) {
	var pkg model.ArtifactPackage
	if err := s.db.First(&pkg, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrArtifactPackageNotFound
		}
		return nil, fmt.Errorf("查询制品包失败: %w", err)
	}
	return &pkg, nil
}

func (s *ArtifactVersionService) versionByID(id uint) (*model.ArtifactVersion, error) {
	var version model.ArtifactVersion
	if err := s.db.Preload("Asset").First(&version, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrArtifactVersionNotFound
		}
		return nil, fmt.Errorf("查询制品版本失败: %w", err)
	}
	return &version, nil
}

func (s *ArtifactVersionService) cachedVersionInPackage(packageID, versionID uint) (*model.ArtifactVersion, error) {
	version, err := s.cachedVersion(versionID)
	if err != nil {
		return nil, err
	}
	if version.PackageID != packageID {
		return nil, fmt.Errorf("%w: 版本不属于目标制品包", ErrArtifactVersionNotFound)
	}
	return version, nil
}

func (s *ArtifactVersionService) cachedVersion(versionID uint) (*model.ArtifactVersion, error) {
	version, err := s.versionByID(versionID)
	if err != nil {
		return nil, err
	}
	if version.AssetID == 0 || version.Asset == nil {
		return nil, ErrArtifactVersionNotCached
	}
	return version, nil
}

func (s *ArtifactVersionService) provider(name string) ArtifactVersionProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providers[name]
}

func (s *ArtifactVersionService) recordSourceError(sourceID uint, cause error) {
	_ = s.db.Model(&model.ArtifactSource{}).Where("id = ?", sourceID).Update("last_error", truncateArtifactError(cause)).Error
}

func (s *ArtifactVersionService) recordVersionError(versionID uint, cause error) {
	_ = s.db.Model(&model.ArtifactVersion{}).Where("id = ?", versionID).Update("last_error", truncateArtifactError(cause)).Error
}

func validateArtifactRelease(release ArtifactRelease) error {
	if strings.TrimSpace(release.Version) == "" || strings.TrimSpace(release.ReleaseRef) == "" || strings.TrimSpace(release.AssetName) == "" || strings.TrimSpace(release.URL) == "" {
		return ErrArtifactReleaseInvalid
	}
	sha := normalizeSHA256(release.SHA256)
	if len(sha) != 64 {
		return ErrArtifactReleaseInvalid
	}
	for _, char := range sha {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return ErrArtifactReleaseInvalid
		}
	}
	return nil
}

func normalizeSHA256(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.TrimPrefix(value, "sha256:")
}

func truncateArtifactError(cause error) string {
	if cause == nil {
		return ""
	}
	value := cause.Error()
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
