package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BackupStatus 备份状态。
type BackupStatus int

const (
	BackupStatusPending    BackupStatus = 0 // 待处理
	BackupStatusCompleted  BackupStatus = 1 // 完成
	BackupStatusFailed     BackupStatus = 2 // 失败
)

// BackupType 备份类型。
type BackupType int

const (
	BackupTypeManual    BackupType = 0 // 手动
	BackupTypeScheduled BackupType = 1 // 定时
)

// Backup 备份记录。
type Backup struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	UUID       string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID uint           `gorm:"not null;index" json:"instanceId"`
	Name       string         `gorm:"type:varchar(128);not null" json:"name"`
	FilePath   string         `gorm:"type:varchar(512)" json:"filePath"`
	FileSizeMB float64        `gorm:"default:0" json:"fileSizeMb"`
	Type       BackupType     `gorm:"default:0" json:"type"`
	Status     BackupStatus   `gorm:"default:0" json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *Backup) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}
