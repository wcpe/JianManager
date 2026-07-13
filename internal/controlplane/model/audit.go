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
	// Success 操作是否成功（FR-321：失败操作也留痕并带错误内容，回答「这个操作为什么报错」）。
	Success bool `gorm:"default:true" json:"success"`
	// Error 失败时的错误内容（响应 error body 截断，FR-321）。
	Error     string    `gorm:"type:varchar(512)" json:"error"`
	CreatedAt time.Time `json:"createdAt"`
	User      User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.UUID == "" {
		a.UUID = uuid.New().String()
	}
	return nil
}
