package model

import "time"

// NodeRuntime 节点登记/托管的非 JDK 运行时（FR-298 节点运行时库）。
// JDK 沿用 node_jdks 表不迁移（实例外键 instances.jdk_id 零变更）；本表承载
// nodejs / python（预留）等其它类型，读侧与 node_jdks 拼成统一 Runtime 视图。
type NodeRuntime struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	NodeID uint `gorm:"not null;index;uniqueIndex:uniq_node_runtime_type_path" json:"nodeId"`
	// Type 运行时类型（nodejs / python 预留；jdk 不落本表）。
	Type string `gorm:"type:varchar(16);not null;uniqueIndex:uniq_node_runtime_type_path" json:"type"`
	// Name 展示名（如 "Node.js 22"）。
	Name string `gorm:"type:varchar(64);not null" json:"name"`
	// Version 完整版本（如 "22.17.0"）。
	Version string `gorm:"type:varchar(64);not null" json:"version"`
	// Major 主版本（如 22），排序/选择用。
	Major int `gorm:"not null;index" json:"major"`
	Arch  string `gorm:"type:varchar(32)" json:"arch"`
	Path  string `gorm:"type:varchar(512);not null;uniqueIndex:uniq_node_runtime_type_path" json:"path"`
	// Managed 是否平台托管（波1 登记均为外部发现 false；FR-299 安装器落 true）。
	Managed   bool      `gorm:"default:false" json:"managed"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
