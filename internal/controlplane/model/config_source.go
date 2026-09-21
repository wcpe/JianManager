package model

import "time"

// ConfigSourceKind 受管配置项来源（FR-451 / ADR-092）。
// 二态互斥、显式：一项同一时刻只能是内联值或文件引用，杜绝「平台改了却被文件覆盖」的静默冲突。
type ConfigSourceKind string

const (
	// ConfigSourceInline 平台持有该值，可写、可版本化、可跨实例下发。
	ConfigSourceInline ConfigSourceKind = "inline"
	// ConfigSourceFile 该项由外部文件提供，平台不覆写，只读展示生效值预览。
	ConfigSourceFile ConfigSourceKind = "file"
)

// ValidConfigSourceKind 校验来源枚举是否合法。
func ValidConfigSourceKind(k ConfigSourceKind) bool {
	switch k {
	case ConfigSourceInline, ConfigSourceFile:
		return true
	}
	return false
}

// InstanceConfigSource 受管配置项登记表（FR-451）。
// ItemKey 是稳定标识，分三类前缀：startup.（启动项）、props.（server.properties 项）、flags.（预留）。
type InstanceConfigSource struct {
	ID         uint             `gorm:"primaryKey" json:"id"`
	InstanceID uint             `gorm:"not null;uniqueIndex:idx_config_source_inst_item" json:"instanceId"`
	ItemKey    string           `gorm:"type:varchar(128);not null;uniqueIndex:idx_config_source_inst_item" json:"itemKey"`
	Source     ConfigSourceKind `gorm:"type:varchar(16);not null" json:"source"`
	// InlineValue 是 Source=inline 时的值（启动项为命令串，properties 项为字符串值）。
	InlineValue string `gorm:"type:text" json:"inlineValue,omitempty"`
	// FilePath 是 Source=file 时引用的文件路径（相对工作目录，如 server.properties）。
	FilePath string `gorm:"type:varchar(512)" json:"filePath,omitempty"`
	// FileKey 是 Source=file 时文件内对应的键（properties 平铺键），供读取生效值预览。
	FileKey   string    `gorm:"type:varchar(128)" json:"fileKey,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
