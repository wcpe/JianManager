package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Template 服务端模板。
type Template struct {
	ID             uint           `gorm:"primaryKey" json:"id"`
	UUID           string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name           string         `gorm:"type:varchar(128);not null" json:"name"`
	Type           string         `gorm:"type:varchar(64);not null" json:"type"` // minecraft_java, etc.
	Description    string         `gorm:"type:varchar(512)" json:"description"`
	StartCommand   string         `gorm:"type:varchar(1024)" json:"startCommand"`
	DefaultWorkDir string         `gorm:"type:varchar(512)" json:"defaultWorkDir"`
	DownloadURL    string         `gorm:"type:varchar(512)" json:"downloadUrl"`
	ConfigFiles    string         `gorm:"type:text" json:"configFiles"` // JSON
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (t *Template) BeforeCreate(tx *gorm.DB) error {
	if t.UUID == "" {
		t.UUID = uuid.New().String()
	}
	return nil
}
