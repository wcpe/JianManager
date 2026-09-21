package model

import "time"

// ConfigBaseline 配置基线（模板）（FR-458）。
// 键为 (ScopeKey, FilePath)：ScopeKey 限定「哪些实例应共享该基线」（如 group:1 / network:2 / tag:prod / all）。
// ContentHash 为 sha256(content)，与 InstanceConfigVersion.ContentHash 同口径，供漂移检测比对。
type ConfigBaseline struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ScopeKey    string    `gorm:"type:varchar(128);not null;uniqueIndex:idx_config_baseline_scope_file" json:"scopeKey"`
	FilePath    string    `gorm:"type:varchar(512);not null;uniqueIndex:idx_config_baseline_scope_file" json:"filePath"`
	Content     string    `gorm:"type:longtext;not null" json:"content"`
	ContentHash string    `gorm:"type:char(64);not null;index" json:"contentHash"`
	Message     string    `gorm:"type:varchar(255)" json:"message"`
	AuthorID    uint      `gorm:"index" json:"authorId"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
