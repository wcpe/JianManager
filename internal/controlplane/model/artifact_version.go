package model

import "time"

const (
	// ServerProbePackageKey 是 ServerProbe 制品包的稳定机器键（FR-409）。
	ServerProbePackageKey = "serverprobe"
	// ArtifactProviderGitHubRelease 是 GitHub Releases 来源 provider。
	ArtifactProviderGitHubRelease = "github-release"
	// ArtifactProviderLocalUpload 是平台管理员上传到 CP 制品库的来源 provider。
	ArtifactProviderLocalUpload = "local-upload"
)

// ArtifactPackage 是可版本化的逻辑制品，不保存二进制字节。
// 实际文件仍由 Asset 的 CAS 负责，旧制品消费者无需迁移。
type ArtifactPackage struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	Key              string    `gorm:"type:varchar(128);not null;uniqueIndex" json:"key"`
	Name             string    `gorm:"type:varchar(255);not null" json:"name"`
	AssetType        AssetType `gorm:"type:varchar(32);not null" json:"assetType"`
	DefaultVersionID uint      `gorm:"default:0;index" json:"defaultVersionId"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ArtifactSource 是某制品包的版本来源；Config 只保存 provider 的非敏感配置 JSON。
type ArtifactSource struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	PackageID    uint       `gorm:"not null;index;uniqueIndex:idx_artifact_sources_package_name" json:"packageId"`
	Provider     string     `gorm:"type:varchar(64);not null;index" json:"provider"`
	Name         string     `gorm:"type:varchar(255);not null;uniqueIndex:idx_artifact_sources_package_name" json:"name"`
	Config       string     `gorm:"type:text;not null" json:"config"`
	Enabled      bool       `gorm:"default:true;not null" json:"enabled"`
	LastSyncedAt *time.Time `json:"lastSyncedAt"`
	LastError    string     `gorm:"type:varchar(512)" json:"lastError"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// ArtifactVersion 记录来源的一条语义版本；AssetID 非零才表示字节已通过校验并入 CAS。
type ArtifactVersion struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	PackageID      uint       `gorm:"not null;index" json:"packageId"`
	SourceID       uint       `gorm:"not null;index;uniqueIndex:idx_artifact_versions_source_version" json:"sourceId"`
	Version        string     `gorm:"type:varchar(128);not null;uniqueIndex:idx_artifact_versions_source_version" json:"version"`
	ReleaseRef     string     `gorm:"type:varchar(255);not null" json:"releaseRef"`
	AssetName      string     `gorm:"type:varchar(255);not null" json:"assetName"`
	ExpectedSHA256 string     `gorm:"type:char(64);not null" json:"expectedSha256"`
	SourceURL      string     `gorm:"type:varchar(1024);not null" json:"sourceUrl"`
	AssetID        uint       `gorm:"default:0;index" json:"assetId"`
	CachedAt       *time.Time `json:"cachedAt"`
	LastError      string     `gorm:"type:varchar(512)" json:"lastError"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`

	Asset *Asset `gorm:"foreignKey:AssetID" json:"asset,omitempty"`
}
