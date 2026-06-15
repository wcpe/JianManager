package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserRole 用户角色。
type UserRole int

const (
	RoleMember         UserRole = 0  // 组成员
	RoleGroupAdmin     UserRole = 1  // 组管理员
	RolePlatformAdmin  UserRole = 10 // 平台管理员
)

// UserStatus 用户状态。
type UserStatus int

const (
	UserStatusActive   UserStatus = 0 // 启用
	UserStatusDisabled UserStatus = 1 // 禁用
)

// User 用户模型。
type User struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UUID      string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Username  string         `gorm:"type:varchar(64);uniqueIndex;not null" json:"username"`
	Password  string         `gorm:"type:varchar(128);not null" json:"-"`
	Role      UserRole       `gorm:"default:0" json:"role"`
	Status    UserStatus     `gorm:"default:0" json:"status"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.UUID == "" {
		u.UUID = uuid.New().String()
	}
	return nil
}
