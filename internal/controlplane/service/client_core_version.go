package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrCoreArtifactNotFound 登记 core 版本时引用的制品不在 client-core 库（须先上传）。
	ErrCoreArtifactNotFound = errors.New("updater-core 制品不存在")
	// ErrCoreVersionNotFound 指定的 core 版本号未登记。
	ErrCoreVersionNotFound = errors.New("updater-core 版本不存在")
)

// coreManifestPlatforms manifest agent.core.platforms 须填的平台键集合（contract §2、ADR-045 决策 5）。
// ADR-021「一份 jar 三平台通用」——上传一份 core jar，fan-out 填这三键（同制品）。
// 客户端 Platform.tag() 取 windows/macos/linux 之一；other 平台无键、不自更新（沿用 FR-091）。
var coreManifestPlatforms = []string{"windows", "macos", "linux"}

// ClientCoreVersionService updater-core 集中版本管理（FR-193，见 ADR-045）。
//
// 职责：
//   - UploadCore：把 updater-core jar 入 FR-045 制品库（type=client-core，内容寻址 + 去重）；
//   - RegisterVersion：把已上传制品登记为带版本号的 core 版本（version 全局单调递增）；
//   - ListVersions / LatestVersion：列出 / 取最新 core 版本；
//   - ResolveForChannel：据频道 pin（0=最新）解析当前应分发的 core 版本（无注册返回 nil）；
//   - SetChannelPin：设 / 更新频道 pin（校验目标版本存在）；
//   - RollbackChannelCore：回退坏 core——以更高版本号重发旧 core 字节为新版并 pin（ADR-045 决策 4）。
//
// 两条版本轴（ADR-045 决策 3）：core 自身版本（本服务管理、对客户端单调只升不降）与 manifest 内容
// 版本（ClientVersionService，防降级）正交。回退不降版，靠「重发更高版本」同 FR-088 内容回滚法。
type ClientCoreVersionService struct {
	db     *gorm.DB
	assets *AssetService
}

// NewClientCoreVersionService 创建 core 版本管理服务。
func NewClientCoreVersionService(db *gorm.DB, assets *AssetService) *ClientCoreVersionService {
	return &ClientCoreVersionService{db: db, assets: assets}
}

// UploadCoreParams 上传 core jar 制品参数。
type UploadCoreParams struct {
	// Filename 原始文件名（决定 CAS 扩展名/下载名），可空。
	Filename string
	// Codec 制品压缩算法（"zstd" | "none"），信息性元数据；落库 Metadata + 回填 ClientFileResult。
	Codec string
	// ExpectedSHA256 期望的制品自身 sha256；非空则比对，不符拒收。
	ExpectedSHA256 string
}

// UploadCore 把 updater-core jar 制品入制品库（type=client-core，按制品自身 sha256 内容寻址去重）。
// 返回的 SHA256 即 manifest agent.core.platforms[os].artifact.sha256；用于随后 RegisterVersion。
func (s *ClientCoreVersionService) UploadCore(r io.Reader, p UploadCoreParams) (*ClientFileResult, error) {
	codec := p.Codec
	if codec == "" {
		codec = "none"
	}
	meta, _ := json.Marshal(map[string]string{"codec": codec})
	asset, err := s.assets.Ingest(r, IngestParams{
		Type:           model.AssetTypeClientCore,
		Filename:       p.Filename,
		Metadata:       string(meta),
		ExpectedSHA256: p.ExpectedSHA256,
	})
	if err != nil {
		return nil, err
	}
	return &ClientFileResult{SHA256: asset.SHA256, MD5: asset.MD5, Size: asset.Size, Codec: codec}, nil
}

// RegisterCoreVersionParams 登记 core 版本参数（制品须已存在于 client-core 库）。
type RegisterCoreVersionParams struct {
	// ArtifactSHA256 core jar 制品自身 sha256（须由 UploadCore 返回、已在 client-core 库）。
	ArtifactSHA256 string
	// ArtifactSize 制品字节数（信息性，落库 + manifest 回填）。
	ArtifactSize int64
	// Codec 制品压缩算法。
	Codec string
	// Note 登记备注。
	Note string
	// SourceVersion 回退来源版本号（>0=重发，0=直接上传登记）；仅信息性/审计。
	SourceVersion int
	// CreatedBy 登记者用户 ID（审计辅助）。
	CreatedBy uint
}

// RegisterVersion 把已上传的 core 制品登记为新 core 版本（version=当前最大版本+1，全局单调递增）。
// 制品须存在于 client-core 库（否则 ErrCoreArtifactNotFound，防登记空指针版本）。
func (s *ClientCoreVersionService) RegisterVersion(p RegisterCoreVersionParams) (*model.ClientCoreVersion, error) {
	if p.ArtifactSHA256 == "" {
		return nil, fmt.Errorf("%w: 缺制品 sha256", ErrCoreArtifactNotFound)
	}
	codec := p.Codec
	if codec == "" {
		codec = "none"
	}
	var rec model.ClientCoreVersion
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 制品须已在 client-core 库（内容寻址、由 UploadCore 落库）。
		var cnt int64
		if e := tx.Model(&model.Asset{}).
			Where("type = ? AND sha256 = ?", model.AssetTypeClientCore, p.ArtifactSHA256).
			Count(&cnt).Error; e != nil {
			return fmt.Errorf("查询 core 制品失败: %w", e)
		}
		if cnt == 0 {
			return ErrCoreArtifactNotFound
		}
		// version 全局单调递增（与频道无关）：取最大 +1，并发拿同号靠唯一索引兜底。
		var maxVer struct{ Max int }
		if e := tx.Model(&model.ClientCoreVersion{}).
			Select("COALESCE(MAX(version),0) AS max").Scan(&maxVer).Error; e != nil {
			return fmt.Errorf("查询 core 版本号失败: %w", e)
		}
		rec = model.ClientCoreVersion{
			Version:        maxVer.Max + 1,
			ArtifactSHA256: p.ArtifactSHA256,
			ArtifactSize:   p.ArtifactSize,
			Codec:          codec,
			Note:           p.Note,
			SourceVersion:  p.SourceVersion,
			CreatedBy:      p.CreatedBy,
		}
		if e := tx.Create(&rec).Error; e != nil {
			return fmt.Errorf("登记 core 版本失败: %w", e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// CoreVersionSummary core 版本列表项（管理面）。
type CoreVersionSummary struct {
	// Version core 自身版本号 = manifest agent.core.version。
	Version int `json:"version"`
	// ArtifactSHA256 制品自身 sha256（= 各平台 artifact.sha256，三平台同此值）。
	ArtifactSHA256 string `json:"artifactSha256"`
	// ArtifactSize 制品字节数。
	ArtifactSize int64 `json:"artifactSize"`
	// Codec 制品压缩算法。
	Codec string `json:"codec"`
	// Note 登记/回退备注。
	Note string `json:"note"`
	// SourceVersion 回退来源版本号（>0=该版本是某历史版本的重发，0=直接上传）。
	SourceVersion int `json:"sourceVersion"`
	// CreatedBy 登记者用户 ID（0=未知）。
	CreatedBy uint `json:"createdBy"`
	// CreatedAt 登记时间。
	CreatedAt time.Time `json:"createdAt"`
}

// ListVersions 列出全部已登记 core 版本（版本号 DESC）。
func (s *ClientCoreVersionService) ListVersions() ([]CoreVersionSummary, error) {
	var versions []model.ClientCoreVersion
	if err := s.db.Order("version DESC").Find(&versions).Error; err != nil {
		return nil, fmt.Errorf("查询 core 版本失败: %w", err)
	}
	out := make([]CoreVersionSummary, 0, len(versions))
	for i := range versions {
		v := &versions[i]
		out = append(out, CoreVersionSummary{
			Version:        v.Version,
			ArtifactSHA256: v.ArtifactSHA256,
			ArtifactSize:   v.ArtifactSize,
			Codec:          v.Codec,
			Note:           v.Note,
			SourceVersion:  v.SourceVersion,
			CreatedBy:      v.CreatedBy,
			CreatedAt:      v.CreatedAt,
		})
	}
	return out, nil
}

// LatestVersion 返回当前最大已登记 core 版本号（0=尚无登记）。
func (s *ClientCoreVersionService) LatestVersion() (int, error) {
	var maxVer struct{ Max int }
	if err := s.db.Model(&model.ClientCoreVersion{}).
		Select("COALESCE(MAX(version),0) AS max").Scan(&maxVer).Error; err != nil {
		return 0, fmt.Errorf("查询最新 core 版本失败: %w", err)
	}
	return maxVer.Max, nil
}

// ResolveForChannel 据频道 pin 解析当前应分发的 core 版本：
//   - pin>0：返回该版本（不存在则 ErrCoreVersionNotFound——pin 指向被删版本属数据不一致）；
//   - pin=0：返回最新已登记 core 版本；尚无任何登记则返回 (nil, nil)，由 manifest 回退手填透传。
func (s *ClientCoreVersionService) ResolveForChannel(pinnedCoreVersion int) (*model.ClientCoreVersion, error) {
	if pinnedCoreVersion > 0 {
		return s.findVersion(pinnedCoreVersion)
	}
	var rec model.ClientCoreVersion
	err := s.db.Order("version DESC").First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil // 无任何已登记 core → 回退手填透传（兼容 FR-087/088）。
	}
	if err != nil {
		return nil, fmt.Errorf("查询最新 core 版本失败: %w", err)
	}
	return &rec, nil
}

// SetChannelPin 设 / 更新频道的 core pin（version=0 表示恢复「自动用最新」；>0 须为已登记版本）。
// 频道不存在返回 ErrChannelNotFound；目标版本不存在返回 ErrCoreVersionNotFound。
func (s *ClientCoreVersionService) SetChannelPin(channelID string, version int) error {
	if version < 0 {
		return fmt.Errorf("%w: 版本号非法", ErrCoreVersionNotFound)
	}
	var ch model.ClientChannel
	if err := s.db.Where("channel_id = ?", channelID).First(&ch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrChannelNotFound
		}
		return fmt.Errorf("查询频道失败: %w", err)
	}
	if version > 0 {
		if _, err := s.findVersion(version); err != nil {
			return err
		}
	}
	if err := s.db.Model(&model.ClientChannel{}).Where("channel_id = ?", channelID).
		Update("pinned_core_version", version).Error; err != nil {
		return fmt.Errorf("更新频道 core pin 失败: %w", err)
	}
	return nil
}

// RollbackChannelCore 回退坏 core（ADR-045 决策 4）：取历史 core 版本 sourceVersion 的字节（内容寻址制品），
// **以更高版本号重发为新 core 版本**（不降版——客户端 core 只升不降），再把频道 pin 指向新版本。
// 客户端看到更高 agent.core.version → 照常 promote「上去」→ 跑到旧 core 内容。坏 core 应急下线、不违反防降级。
// 频道不存在返回 ErrChannelNotFound；源版本不存在返回 ErrCoreVersionNotFound。
func (s *ClientCoreVersionService) RollbackChannelCore(channelID string, sourceVersion int, createdBy uint, note string) (*model.ClientCoreVersion, error) {
	// 频道须存在。
	var ch model.ClientChannel
	if err := s.db.Where("channel_id = ?", channelID).First(&ch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrChannelNotFound
		}
		return nil, fmt.Errorf("查询频道失败: %w", err)
	}
	src, err := s.findVersion(sourceVersion)
	if err != nil {
		return nil, err
	}
	if note == "" {
		note = fmt.Sprintf("回退至 core v%d", sourceVersion)
	}
	// 以更高版本号重发旧字节（同一内容寻址制品，无需重新上传）。
	rec, err := s.RegisterVersion(RegisterCoreVersionParams{
		ArtifactSHA256: src.ArtifactSHA256,
		ArtifactSize:   src.ArtifactSize,
		Codec:          src.Codec,
		Note:           note,
		SourceVersion:  sourceVersion,
		CreatedBy:      createdBy,
	})
	if err != nil {
		return nil, err
	}
	// pin 指向新版本（必然存在，刚登记）。
	if err := s.SetChannelPin(channelID, rec.Version); err != nil {
		return nil, err
	}
	return rec, nil
}

// findVersion 查指定 core 版本号；不存在返回 ErrCoreVersionNotFound。
func (s *ClientCoreVersionService) findVersion(version int) (*model.ClientCoreVersion, error) {
	var rec model.ClientCoreVersion
	err := s.db.Where("version = ?", version).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrCoreVersionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询 core 版本失败: %w", err)
	}
	return &rec, nil
}

// coreVersionToManifest 把一个 core 版本记录转为 manifest agent.core 段（fan-out 三平台同制品，ADR-045 决策 5）。
func coreVersionToManifest(rec *model.ClientCoreVersion) *ManifestCore {
	platforms := make(map[string]ManifestAgentArtifact, len(coreManifestPlatforms))
	for _, os := range coreManifestPlatforms {
		platforms[os] = ManifestAgentArtifact{
			SHA256: rec.ArtifactSHA256,
			Size:   rec.ArtifactSize,
			Codec:  rec.Codec,
		}
	}
	return &ManifestCore{Version: rec.Version, Platforms: platforms}
}
