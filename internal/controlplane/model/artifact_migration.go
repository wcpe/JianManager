package model

import "time"

// ArtifactMigration 一次制品存量迁移任务的登记与实时计数（FR-348，底座见 ADR-073）。
// task_id 1:1 关联 tasks 行（kind=artifact_migrate）。计数独立成行而非塞 Task.Result/Detail：
// 失败任务无 result、Detail 被阶段文本覆写，独立行保证「总数/已迁/失败/跳过」随迁移逐条
// 持久推进，前端渠道页可精确展示。
type ArtifactMigration struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	TaskID string `gorm:"type:varchar(64);uniqueIndex;not null" json:"taskId"`
	// TargetChannelID 目标渠道（重试 = 对同目标重新发起的解析源）。
	TargetChannelID uint `gorm:"not null;index" json:"targetChannelId"`
	// Total 快照内全部 client-file 存量数。
	Total int `gorm:"not null;default:0" json:"total"`
	// Migrated 本次任务实际搬运成功数。
	Migrated int `gorm:"not null;default:0" json:"migrated"`
	// Failed 本次任务失败条数（明细见 ArtifactMigrationFailure）。
	Failed int `gorm:"not null;default:0" json:"failed"`
	// Skipped 发起时已在目标渠道数（续跑时「上轮已迁」体现于此）。
	Skipped   int       `gorm:"not null;default:0" json:"skipped"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ArtifactMigrationFailure 迁移失败明细（FR-348）：逐条落库可查（制品 sha256 + 原因），
// 按 task_id 隔离（每次任务独立一批）；重试 = 重新发起同目标迁移。
type ArtifactMigrationFailure struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	TaskID  string `gorm:"type:varchar(64);not null;index" json:"taskId"`
	AssetID uint   `json:"assetId"`
	// SHA256 制品内容寻址键（定位失败制品）。
	SHA256    string    `gorm:"type:char(64);not null" json:"sha256"`
	Filename  string    `gorm:"type:varchar(255)" json:"filename"`
	Size      int64     `json:"size"`
	Reason    string    `gorm:"type:text" json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}
