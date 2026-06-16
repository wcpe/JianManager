package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// NodeStatus 节点状态。
type NodeStatus int

const (
	NodeStatusOffline  NodeStatus = 0 // 离线
	NodeStatusOnline   NodeStatus = 1 // 在线
	NodeStatusStarting NodeStatus = 2 // 启动中
)

// Node Worker Node 节点。
type Node struct {
	ID            uint           `gorm:"primaryKey" json:"id"`
	UUID          string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name          string         `gorm:"type:varchar(128);not null" json:"name"`
	Host          string         `gorm:"type:varchar(256);not null" json:"host"`
	GRPCPort      int            `gorm:"not null" json:"grpcPort"`
	WSPort        int            `gorm:"not null" json:"wsPort"`
	Secret        string         `gorm:"type:varchar(128);not null" json:"-"`
	Status        NodeStatus     `gorm:"default:0" json:"status"`
	OS            string         `gorm:"type:varchar(64)" json:"os"`
	Arch          string         `gorm:"type:varchar(32)" json:"arch"`
	CPUCores      int            `json:"cpuCores"`
	MemoryMB      int64          `json:"memoryMb"`
	DiskTotalMB   int64          `json:"diskTotalMb"`
	CPUUsage      float32        `gorm:"default:0" json:"cpuUsage"`
	MemoryUsage   float32        `gorm:"default:0" json:"memoryUsage"`
	DiskUsage     float32        `gorm:"default:0" json:"diskUsage"`
	MemoryUsedMB  int64          `gorm:"default:0" json:"memoryUsedMb"`
	DiskUsedMB    int64          `gorm:"default:0" json:"diskUsedMb"`
	LastHeartbeat *time.Time     `json:"lastHeartbeat"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (n *Node) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = uuid.New().String()
	}
	return nil
}
