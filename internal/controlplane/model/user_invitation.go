package model

import (
	"time"

	"gorm.io/gorm"
)

// UserInvitation 记录由平台管理员签发的一次性成员邀请。
// 明文令牌只在签发响应与邮件正文中短暂存在，数据库仅保存哈希。
type UserInvitation struct {
	ID            uint           `gorm:"primaryKey" json:"id"`
	Email         string         `gorm:"type:varchar(254);not null;index" json:"email"`
	TokenHash     []byte         `gorm:"type:binary(32);uniqueIndex;not null" json:"-"`
	TokenPrefix   string         `gorm:"type:varchar(16);not null" json:"-"`
	Role          UserRole       `gorm:"not null;default:0" json:"role"`
	ExpiresAt     time.Time      `gorm:"not null;index" json:"expiresAt"`
	CreatedByID   uint           `gorm:"not null;index" json:"createdBy"`
	UsedAt        *time.Time     `json:"usedAt"`
	RevokedAt     *time.Time     `json:"revokedAt"`
	EmailSentAt   *time.Time     `json:"emailSentAt"`
	EmailDelivery string         `gorm:"type:varchar(32);not null;default:not_configured" json:"emailDelivery"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"-"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}
