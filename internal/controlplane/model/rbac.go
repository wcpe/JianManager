package model

import "time"

// RoleTemplate 权限角色模板（FR-432）。系统 key 不可删，节点集合可改。
type RoleTemplate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Key         string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"key"`
	Name        string    `gorm:"type:varchar(64);not null" json:"name"`
	Description string    `gorm:"type:varchar(255)" json:"description"`
	IsSystem    bool      `gorm:"not null;default:false" json:"isSystem"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// RolePermission 角色模板授予的权限节点。
type RolePermission struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	RoleID uint   `gorm:"index:idx_role_perm,unique;not null" json:"roleId"`
	Node   string `gorm:"type:varchar(96);index:idx_role_perm,unique;not null" json:"node"`
}

// UserRoleBinding 用户绑定主角色模板（一用户一行）。
type UserRoleBinding struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex;not null" json:"userId"`
	RoleID    uint      `gorm:"not null" json:"roleId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UserPermissionOverride 用户级权限覆盖。deny 永胜。
type UserPermissionOverride struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index:idx_user_ov,unique;not null" json:"userId"`
	Node      string    `gorm:"type:varchar(96);index:idx_user_ov,unique;not null" json:"node"`
	Effect    string    `gorm:"type:varchar(8);not null" json:"effect"` // allow | deny
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Override effects.
const (
	OverrideAllow = "allow"
	OverrideDeny  = "deny"
)

// System role template keys（种子）。
const (
	RoleKeyPlatformAdmin = "platform_admin"
	RoleKeyGroupAdmin    = "group_admin"
	RoleKeyGroupOperator = "group_operator"
	RoleKeyGroupViewer   = "group_viewer"
	RoleKeyMember        = "member"
)

// RoleKeyForLegacyUserRole 将 users.role 兼容位映射到系统模板 key。
func RoleKeyForLegacyUserRole(role UserRole) string {
	switch role {
	case RolePlatformAdmin:
		return RoleKeyPlatformAdmin
	case RoleGroupAdmin:
		return RoleKeyGroupAdmin
	case RoleGroupOperator:
		return RoleKeyGroupOperator
	case RoleGroupViewer:
		return RoleKeyGroupViewer
	default:
		return RoleKeyMember
	}
}
