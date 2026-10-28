package model

import (
	"time"

	"gorm.io/gorm"
)

// ConfigFileFormat 配置文件的格式枚举。创建后不可改（解析与合并规则随格式而定）。
type ConfigFileFormat string

const (
	// ConfigFormatYAML YAML（深合并，null 删键）。
	ConfigFormatYAML ConfigFileFormat = "yaml"
	// ConfigFormatJSON JSON（深合并，null 删键）。
	ConfigFormatJSON ConfigFileFormat = "json"
	// ConfigFormatProperties Java properties（键级覆盖，不支持删键）。
	ConfigFormatProperties ConfigFileFormat = "properties"
)

// ValidConfigFileFormat 校验格式枚举是否合法。
func ValidConfigFileFormat(f ConfigFileFormat) bool {
	switch f {
	case ConfigFormatYAML, ConfigFormatJSON, ConfigFormatProperties:
		return true
	}
	return false
}

// ConfigScopeLevel 层作用域级别（FR-529 §4.3）。
//
// 只有两级：group（分组树任意节点）与 instance（实例）。
// Beacon 的 namespace/bc_cluster/region/zone/server 五层在 JM 里由分组树的自引用邻接表
// 深度自然表达——顶级分组相当于集群、下一层相当于大区……代码不硬编码这些业务概念，
// 用户可自由组织层级深度。
type ConfigScopeLevel string

const (
	// ConfigScopeGroup 分组级：scope_ref_id 指向 InstanceGroupNode.ID。
	ConfigScopeGroup ConfigScopeLevel = "group"
	// ConfigScopeInstance 实例级：scope_ref_id 指向 Instance.ID（优先级最高）。
	ConfigScopeInstance ConfigScopeLevel = "instance"
)

// ValidConfigScopeLevel 校验作用域级别枚举是否合法。
func ValidConfigScopeLevel(l ConfigScopeLevel) bool {
	switch l {
	case ConfigScopeGroup, ConfigScopeInstance:
		return true
	}
	return false
}

// ConfigFile 配置逻辑文件（FR-529 §4.1）。name 全局唯一（软删后仍占用，避免与历史版本混淆）。
//
// 与 InstanceConfigVersion 的关系：后者是「某实例某文件的实际落盘快照」，承载回滚链与
// 漂移比对；本表的层版本链承载「配置的定义」。二者并存——见 config_layer.go 的说明。
type ConfigFile struct {
	ID          uint             `gorm:"primaryKey" json:"id"`
	Name        string           `gorm:"type:varchar(255);not null;uniqueIndex" json:"name"`
	Format      ConfigFileFormat `gorm:"type:varchar(16);not null" json:"format"`
	Description string           `gorm:"type:varchar(512)" json:"description"`
	// SchemaJSON JSON Schema（Draft 2020-12 子集）文本，可空。
	// 保存某层时校验该层原文，预览时校验合并结果（§6.1）。
	SchemaJSON string `gorm:"type:text" json:"schemaJson,omitempty"`
	// SensitivePaths 敏感键路径的 JSON 数组文本，用于预览脱敏（§8）。
	SensitivePaths string    `gorm:"type:text" json:"sensitivePaths,omitempty"`
	CreatedBy      uint      `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	// DeletedAt 软删（回收站）。软删后列表不可见，层版本保留以便恢复。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// ConfigLayerVersion 层版本（FR-529 §4.2）——每个 (文件 × 作用域) 一条独立的不可变追加链。
//
// 回退 = 追加新版本（based_on_version_id 指向被复制的源版本），历史行永不修改，
// 因此任意时刻都能重放出「某层当时长什么样」。
type ConfigLayerVersion struct {
	ID           uint             `gorm:"primaryKey" json:"id"`
	ConfigFileID uint             `gorm:"not null;index;uniqueIndex:idx_cfg_layer_scope_ver" json:"configFileId"`
	ScopeLevel   ConfigScopeLevel `gorm:"type:varchar(16);not null;uniqueIndex:idx_cfg_layer_scope_ver" json:"scopeLevel"`
	ScopeRefID   uint             `gorm:"not null;uniqueIndex:idx_cfg_layer_scope_ver" json:"scopeRefId"`
	VersionNo    int              `gorm:"not null;uniqueIndex:idx_cfg_layer_scope_ver" json:"versionNo"`
	// Content 该层原文（不做合并）。
	Content string `gorm:"type:longtext;not null" json:"content"`
	// ContentHash sha256(content) 小写 hex。
	ContentHash string `gorm:"type:char(64);not null;index" json:"contentHash"`
	// BasedOnVersionID 回退来源版本；普通新建为空。
	BasedOnVersionID *uint     `gorm:"index" json:"basedOnVersionId,omitempty"`
	Remark           string    `gorm:"type:varchar(255)" json:"remark"`
	CreatedBy        uint      `gorm:"index" json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
}
