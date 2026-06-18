package model

import (
	"time"
)

// AssetType 制品类型。类型间物理分目录、类型内按 sha256 去重。参见 ADR-011。
type AssetType string

const (
	// AssetTypeCore MC 服务器核心 jar（Paper/Spigot/Velocity 等）。
	AssetTypeCore AssetType = "core"
	// AssetTypePlugin 插件 jar。
	AssetTypePlugin AssetType = "plugin"
	// AssetTypeImage 图片。
	AssetTypeImage AssetType = "image"
	// AssetTypeVideo 视频。
	AssetTypeVideo AssetType = "video"
	// AssetTypeArchive 归档包（zip/tar 等）。
	AssetTypeArchive AssetType = "archive"
	// AssetTypeBlob 通用二进制。
	AssetTypeBlob AssetType = "blob"
)

// AssetStorageState 制品存储状态，驱动归档/外置生命周期（归档策略为后续 FR，此处先立模型）。
type AssetStorageState string

const (
	// AssetStorageHot 热数据，存于本地 var/artifacts。
	AssetStorageHot AssetStorageState = "hot"
	// AssetStorageArchived 已归档（仍由平台管理，位置变化）。
	AssetStorageArchived AssetStorageState = "archived"
	// AssetStorageExternal 外置到对象存储等后端。
	AssetStorageExternal AssetStorageState = "external"
)

// AssetBackendLocal 本地存储后端标识（默认）。
const AssetBackendLocal = "local"

// Asset 制品库资产索引（内容寻址 + 类型化元数据）。
// sha256 既是寻址键也是去重键：同 (type, sha256) 复用同一条记录与同一份物理文件。
// 参见 ADR-011: 制品库——类型分区的内容寻址与资产模型。
type Asset struct {
	ID   uint      `gorm:"primaryKey" json:"id"`
	Type AssetType `gorm:"type:varchar(32);not null;index;uniqueIndex:idx_assets_type_sha256" json:"type"`
	// Name 人类可读名称（如 "paper-1.20.4"），非寻址键。
	Name string `gorm:"type:varchar(255);index" json:"name"`
	// Version 版本标记（如 "1.20.4-435"），可空。
	Version string `gorm:"type:varchar(128)" json:"version"`
	// Filename 入库时的原始文件名，决定存储扩展名与下载名。
	Filename string `gorm:"type:varchar(255)" json:"filename"`
	// SHA256 内容寻址 + 去重键（十六进制小写）。
	SHA256 string `gorm:"type:char(64);not null;index;uniqueIndex:idx_assets_type_sha256" json:"sha256"`
	// MD5 辅助完整性校验（十六进制小写）。
	MD5 string `gorm:"type:char(32)" json:"md5"`
	// Size 字节数。
	Size int64 `gorm:"not null" json:"size"`
	// ContentType MIME 类型。
	ContentType string `gorm:"type:varchar(128)" json:"contentType"`
	// SourceURL 来源地址（下载入库时记录），可空。
	SourceURL string `gorm:"type:varchar(1024)" json:"sourceUrl"`
	// Metadata 类型相关的扩展元数据（JSON 字符串）。
	Metadata string `gorm:"type:text" json:"metadata"`
	// StorageState 冷热/外置状态，默认 hot。
	StorageState AssetStorageState `gorm:"type:varchar(32);default:hot;not null" json:"storageState"`
	// StorageBackend 存储后端标识，默认 local。
	StorageBackend string `gorm:"type:varchar(64);default:local;not null" json:"storageBackend"`
	// RefCount 引用计数，>0 时禁止删除（被模板/实例占用）。
	RefCount int `gorm:"default:0;not null" json:"refCount"`
	// RelPath 物理文件相对数据根的路径（var/artifacts/<type>/<ab>/<sha256><ext>），便携登记。
	RelPath    string     `gorm:"type:varchar(512)" json:"relPath"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// ValidAssetType 校验类型是否在允许枚举内。
func ValidAssetType(t AssetType) bool {
	switch t {
	case AssetTypeCore, AssetTypePlugin, AssetTypeImage, AssetTypeVideo, AssetTypeArchive, AssetTypeBlob:
		return true
	}
	return false
}
