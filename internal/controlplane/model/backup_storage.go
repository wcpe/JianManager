package model

import (
	"time"

	"gorm.io/gorm"
)

// BackupStorageType 备份远程存储后端类型（FR-057）。
// 与制品库 Asset.StorageBackend 的语义对齐：local 表示节点本地数据根，其余为外置后端。
type BackupStorageType string

const (
	// BackupStorageLocal 本地：备份仅留在节点数据根 var/backups（默认）。
	BackupStorageLocal BackupStorageType = "local"
	// BackupStorageS3 S3 兼容对象存储（AWS S3 / MinIO / 阿里 OSS 等）。
	BackupStorageS3 BackupStorageType = "s3"
	// BackupStorageSFTP SFTP/SSH 远程主机。
	BackupStorageSFTP BackupStorageType = "sftp"
	// BackupStorageWebDAV WebDAV 服务。
	BackupStorageWebDAV BackupStorageType = "webdav"
)

// BackupStorage 备份远程存储后端配置（FR-057）。
//
// 凭证字段（AccessKey/SecretKey）按 .claude/rules/config-files.md 规范以 ${ENV_VAR} 形式存储，
// 不落明文；下发 Worker 前由 service 从环境变量解析为明文（CP 拥有配置）。参见 ADR-011 对齐。
type BackupStorage struct {
	ID   uint              `gorm:"primaryKey" json:"id"`
	Name string            `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Type BackupStorageType `gorm:"type:varchar(32);not null" json:"type"`
	// Endpoint S3 endpoint / WebDAV 基地址 / SFTP host[:port]。
	Endpoint string `gorm:"type:varchar(512)" json:"endpoint"`
	// Bucket S3 bucket（其余类型忽略）。
	Bucket string `gorm:"type:varchar(255)" json:"bucket"`
	// Region S3 region（SigV4 用，缺省 us-east-1）。
	Region string `gorm:"type:varchar(64)" json:"region"`
	// Prefix 对象键/远程目录前缀，便于多实例/多平台共用一个后端。
	Prefix string `gorm:"type:varchar(255)" json:"prefix"`
	// AccessKeyEnv 访问密钥的环境变量引用（如 ${JIANMANAGER_BACKUP_S3_AK}）。
	AccessKeyEnv string `gorm:"type:varchar(255)" json:"accessKeyEnv"`
	// SecretKeyEnv 密钥的环境变量引用（如 ${JIANMANAGER_BACKUP_S3_SK}）。
	SecretKeyEnv string `gorm:"type:varchar(255)" json:"secretKeyEnv"`
	// UseSSL S3 是否启用 TLS。默认由应用层（router/service）设为 true；
	// 不用 gorm default:true，否则 GORM 会把显式 false（零值）覆盖回 true。
	UseSSL    bool           `json:"useSsl"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// ValidBackupStorageType 校验后端类型是否在允许枚举内（local 仅用于占位，不作为远程后端创建）。
func ValidBackupStorageType(t BackupStorageType) bool {
	switch t {
	case BackupStorageS3, BackupStorageSFTP, BackupStorageWebDAV:
		return true
	}
	return false
}
