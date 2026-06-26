package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// InstanceGroupNode 是「实例组织分组树」的一个节点（ADR-XXXX 实例组织分组树）。
// 多级嵌套用自引用 ParentID 邻接表表达（NULL=根）；与用户组（ADR-004 RBAC/配额）、
// 网络群组（ADR-007 proxy↔backend 部署）三者正交——仅供人为组织归类、折叠、批量运维，
// 不承载权限/配额，也不表达运行时拓扑。删非空节点默认拒删（service 校验），不级联删实例。
type InstanceGroupNode struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name string `gorm:"type:varchar(128);not null" json:"name"`
	// ParentID 父节点 ID，NULL 表示根分组；自引用邻接表表达树。建索引便于按父查子。
	ParentID *uint `gorm:"index" json:"parentId,omitempty"`
	// Sort 同级排序权重（升序），由 service 维护；前端可拖拽改序。
	Sort      int            `gorm:"default:0" json:"sort"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (n *InstanceGroupNode) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = uuid.New().String()
	}
	return nil
}

// InstanceGroupMember 是分组节点与实例的 M:N 关联（一个实例可属多个组织分组）。
// UNIQUE(group_id, instance_id) 保证同组内不重复；删组只硬删成员关系、不触及实例。
type InstanceGroupMember struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	GroupID    uint      `gorm:"not null;index;uniqueIndex:idx_igmember_group_inst" json:"groupId"`
	InstanceID uint      `gorm:"not null;index;uniqueIndex:idx_igmember_group_inst" json:"instanceId"`
	CreatedAt  time.Time `json:"createdAt"`
}
