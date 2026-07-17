package model

import (
	"time"
)

// ArtifactStorageChannelType 制品存储渠道类型（FR-347，见 ADR-073）。
type ArtifactStorageChannelType string

const (
	// ArtifactStorageLocal 本地数据根 CAS（内置渠道独占，恒可用）。
	ArtifactStorageLocal ArtifactStorageChannelType = "local"
	// ArtifactStorageS3 S3 兼容对象存储（AWS S3 / MinIO / rustfs 等）。
	ArtifactStorageS3 ArtifactStorageChannelType = "s3"
)

// ArtifactStorageChannel 制品存储渠道（FR-347，见 ADR-073，修订 ADR-011 存储节）。
//
// 与备份域 BackupStorage 独立（消费方不同：备份配置下发 Worker 执行，本渠道由 CP 自身
// 上传/签名消费）。凭证面板直填、AES-256-GCM 可逆加密落库（复用 FR-192 KeyEncryptor
// 基建），与备份域的 ${ENV_VAR} 引用形态分道（理由见 ADR-073 决策 4）。
//
// 活跃语义：全表恰一条 Active（service 事务保证），仅作用于**新上传**的 client-file
// 制品落点；存量 Asset 按自身 StorageBackend + StorageChannelID 自述读取。
// 硬删除（无软删列）：被 Asset 引用/内置/活跃的渠道禁删，名称删除即释放。
type ArtifactStorageChannel struct {
	ID   uint                       `gorm:"primaryKey" json:"id"`
	Name string                     `gorm:"type:varchar(128);not null;uniqueIndex" json:"name"`
	Type ArtifactStorageChannelType `gorm:"type:varchar(16);not null" json:"type"`
	// Endpoint S3 主机[:端口]，容带 scheme（适配器剥离）。
	Endpoint string `gorm:"type:varchar(512)" json:"endpoint"`
	// Bucket S3 桶名。
	Bucket string `gorm:"type:varchar(255)" json:"bucket"`
	// Region SigV4 region，缺省 us-east-1。
	Region string `gorm:"type:varchar(64)" json:"region"`
	// Prefix 对象键前缀；真实对象键 = <Prefix>/<CAS 相对路径>。
	Prefix string `gorm:"type:varchar(255)" json:"prefix"`
	// AccessKeyEnc/SecretKeyEnc AES-256-GCM 可逆加密密文（base64）。
	// json:"-"：任何 API 响应不回明文也不回密文，列表以 Has* 布尔示意。
	AccessKeyEnc string `gorm:"type:text" json:"-"`
	SecretKeyEnc string `gorm:"type:text" json:"-"`
	// UseSSL S3 走 https；false 走 http（rustfs/MinIO 内网常态）。
	// 不用 gorm default:true，否则 GORM 会把显式 false（零值）覆盖回 true（同 BackupStorage 先例）。
	UseSSL bool `json:"useSsl"`
	// PresignTTLSeconds 预签名 URL 有效期秒数，默认 600（10 分钟），service 钳制 [60, 3600]。
	PresignTTLSeconds int `gorm:"default:600;not null" json:"presignTtlSeconds"`
	// Active 活跃渠道标记：写路径唯一路由开关（全表恰一条，service 事务保证）。
	Active bool `gorm:"default:false;not null" json:"active"`
	// Builtin 内置「本机存储」渠道：不可删、不可编辑，兜底活跃。
	Builtin bool `gorm:"default:false;not null" json:"builtin"`
	// LastTest* 最近一次已存渠道连通性测试结果；表单临时测试不写入。
	LastTestAt      *time.Time `json:"lastTestAt,omitempty"`
	LastTestOk      bool       `json:"lastTestOk"`
	LastTestMessage string     `gorm:"type:varchar(255)" json:"lastTestMessage"`
	// HasAccessKey/HasSecretKey 列表标示凭证已配（不回明文/密文），service 填充。
	HasAccessKey bool      `gorm:"-" json:"hasAccessKey"`
	HasSecretKey bool      `gorm:"-" json:"hasSecretKey"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ValidArtifactStorageChannelType 校验渠道类型是否在允许枚举内。
func ValidArtifactStorageChannelType(t ArtifactStorageChannelType) bool {
	switch t {
	case ArtifactStorageLocal, ArtifactStorageS3:
		return true
	}
	return false
}
