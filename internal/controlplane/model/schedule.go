package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Schedule 定时任务。
type Schedule struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	UUID       string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID uint           `gorm:"not null;index" json:"instanceId"`
	Name       string         `gorm:"type:varchar(128);not null" json:"name"`
	CronExpr   string         `gorm:"type:varchar(64);not null" json:"cronExpr"`
	Action     string         `gorm:"type:varchar(32);not null" json:"action"` // start, stop, restart, command, backup
	Payload    string         `gorm:"type:text" json:"payload"`                // JSON: command text, etc.
	Enabled    bool           `gorm:"default:true" json:"enabled"`
	LastRun    *time.Time     `json:"lastRun"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (s *Schedule) BeforeCreate(tx *gorm.DB) error {
	if s.UUID == "" {
		s.UUID = uuid.New().String()
	}
	return nil
}
