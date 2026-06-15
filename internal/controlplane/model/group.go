package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Group 用户组。
type Group struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	UUID        string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name        string         `gorm:"type:varchar(128);not null" json:"name"`
	Description string         `gorm:"type:varchar(512)" json:"description"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`

	Members  []GroupMember  `gorm:"foreignKey:GroupID" json:"members,omitempty"`
	Quota    *GroupQuota    `gorm:"foreignKey:GroupID" json:"quota,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (g *Group) BeforeCreate(tx *gorm.DB) error {
	if g.UUID == "" {
		g.UUID = uuid.New().String()
	}
	return nil
}

// GroupMemberRole 组内角色。
type GroupMemberRole int

const (
	GroupMemberRoleMember GroupMemberRole = 0 // 普通成员
	GroupMemberRoleAdmin  GroupMemberRole = 1 // 组管理员
)

// GroupMember 组成员关联。
type GroupMember struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	GroupID   uint            `gorm:"not null;uniqueIndex:idx_group_user" json:"groupId"`
	UserID    uint            `gorm:"not null;uniqueIndex:idx_group_user" json:"userId"`
	Role      GroupMemberRole `gorm:"default:0" json:"role"`
	CreatedAt time.Time       `json:"createdAt"`

	Group Group `gorm:"foreignKey:GroupID" json:"-"`
	User  User  `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// GroupQuota 组配额。
type GroupQuota struct {
	ID            uint `gorm:"primaryKey" json:"id"`
	GroupID       uint `gorm:"uniqueIndex;not null" json:"groupId"`
	MaxInstances  int  `gorm:"default:10" json:"maxInstances"`
	MaxBots       int  `gorm:"default:50" json:"maxBots"`
	MaxStorageMB  int  `gorm:"default:10240" json:"maxStorageMb"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
