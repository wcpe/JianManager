package model

import (
	"time"

	"gorm.io/gorm"
)

// ScheduleLogStatus 定时任务执行状态。
type ScheduleLogStatus string

const (
	ScheduleLogStatusSuccess ScheduleLogStatus = "success"
	ScheduleLogStatusFailed  ScheduleLogStatus = "failed"
)

// ScheduleExecutionLog 定时任务执行日志。
type ScheduleExecutionLog struct {
	ID         uint              `gorm:"primaryKey" json:"id"`
	ScheduleID uint              `gorm:"not null;index" json:"scheduleId"`
	Action     string            `gorm:"type:varchar(32);not null" json:"action"`
	Status     ScheduleLogStatus `gorm:"type:varchar(16);not null" json:"status"`
	Error      string            `gorm:"type:text" json:"error"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt time.Time         `json:"finishedAt"`
	Schedule   Schedule          `gorm:"foreignKey:ScheduleID" json:"schedule,omitempty"`
}

// BeforeCreate 预留钩子。
func (l *ScheduleExecutionLog) BeforeCreate(tx *gorm.DB) error {
	return nil
}
