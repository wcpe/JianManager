package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuditLog 审计日志。
type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UUID       string    `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	UserID     uint      `gorm:"not null;index" json:"userId"`
	Action     string    `gorm:"type:varchar(64);not null" json:"action"`     // instance.start, user.create, etc.
	TargetType string    `gorm:"type:varchar(32)" json:"targetType"`          // instance, user, group
	TargetID   string    `gorm:"type:varchar(64)" json:"targetId"`
	Detail     string    `gorm:"type:text" json:"detail"` // JSON
	IP         string    `gorm:"type:varchar(64)" json:"ip"`
	CreatedAt  time.Time `json:"createdAt"`
	User       User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.UUID == "" {
		a.UUID = uuid.New().String()
	}
	return nil
}
