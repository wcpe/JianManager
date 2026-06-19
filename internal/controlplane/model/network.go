package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Network 是 MC 群组服的非独占软标签（ADR-007）：仅供 UI 分组/筛选/批量运维，
// 一个实例可属于多个 network；真实路由只由 server_registrations 驱动，network 不做归属容器。
// 删除 network 不影响成员实例与其 server_registrations。
type Network struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	// Name 群组名，唯一性由 service 在未软删记录间校验（不加 DB 唯一索引，便于删除后重用同名）。
	Name        string         `gorm:"type:varchar(128);not null;index" json:"name"`
	Description string         `gorm:"type:varchar(512)" json:"description"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (n *Network) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = uuid.New().String()
	}
	return nil
}

// NetworkMember 群组成员关联（Network M:N Instance）。
// 删除 network 时一并硬删除其成员关系，但不触及实例与 server_registrations。
type NetworkMember struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	NetworkID  uint      `gorm:"not null;index;uniqueIndex:idx_netmember_net_inst" json:"networkId"`
	InstanceID uint      `gorm:"not null;index;uniqueIndex:idx_netmember_net_inst" json:"instanceId"`
	CreatedAt  time.Time `json:"createdAt"`
}
