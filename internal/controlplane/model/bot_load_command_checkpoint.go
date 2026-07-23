package model

import (
	"time"

	"gorm.io/gorm"
)

// BotLoadCommandCheckpointStatus 是命令编排 checkpoint 状态机。
type BotLoadCommandCheckpointStatus string

const (
	BotLoadCommandCheckpointPrepared BotLoadCommandCheckpointStatus = "prepared"
	BotLoadCommandCheckpointScheduled BotLoadCommandCheckpointStatus = "scheduled"
	BotLoadCommandCheckpointSent BotLoadCommandCheckpointStatus = "sent"
	BotLoadCommandCheckpointFailed BotLoadCommandCheckpointStatus = "failed"
	BotLoadCommandCheckpointTimedOut BotLoadCommandCheckpointStatus = "timed_out"
	BotLoadCommandCheckpointCancelled BotLoadCommandCheckpointStatus = "cancelled"
)

// BotLoadCommandCheckpoint 是 FR-369 命令编排 occurrence 账本。
type BotLoadCommandCheckpoint struct {
	ID               uint                            `gorm:"primaryKey" json:"id"`
	StressSessionID  uint                            `gorm:"not null;index" json:"stressSessionId"`
	BotID            *uint                           `gorm:"index" json:"botId,omitempty"`
	RunUUID          string                          `gorm:"type:char(36);not null;uniqueIndex:uniq_bot_load_cmd_ckpt_key" json:"runUuid"`
	BotUUID          string                          `gorm:"type:char(36);not null;uniqueIndex:uniq_bot_load_cmd_ckpt_key" json:"botUuid"`
	StepID           string                          `gorm:"type:varchar(64);not null;uniqueIndex:uniq_bot_load_cmd_ckpt_key" json:"stepId"`
	CommandID        string                          `gorm:"type:varchar(64);not null;uniqueIndex:uniq_bot_load_cmd_ckpt_key" json:"commandId"`
	Occurrence       int                             `gorm:"not null;uniqueIndex:uniq_bot_load_cmd_ckpt_key" json:"occurrence"`
	Generation       int64                           `gorm:"not null" json:"generation"`
	ScheduleRunID    string                          `gorm:"type:char(36);not null;index" json:"scheduleRunId"`
	ActionRunID      string                          `gorm:"type:char(36);not null;index" json:"actionRunId"`
	PlannedAtUnixMs  *int64                          `json:"plannedAtUnixMs,omitempty"`
	SentAtUnixMs     *int64                          `json:"sentAtUnixMs,omitempty"`
	Attempt          int                             `gorm:"not null;default:0" json:"attempt"`
	Status           BotLoadCommandCheckpointStatus  `gorm:"type:varchar(32);not null;index" json:"status"`
	ErrorCode        string                          `gorm:"type:varchar(64)" json:"errorCode,omitempty"`
	EndedAt          *time.Time                      `json:"endedAt,omitempty"`
	CreatedAt        time.Time                       `json:"createdAt"`
	UpdatedAt        time.Time                       `json:"updatedAt"`

	StressSession BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
}

// BeforeCreate 自动填充生成时间；ID 由数据库生成。
func (c *BotLoadCommandCheckpoint) BeforeCreate(tx *gorm.DB) error {
	if c.Status == "" {
		c.Status = BotLoadCommandCheckpointPrepared
	}
	return nil
}