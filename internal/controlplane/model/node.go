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
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	// Name 节点名（人类可读标签）。身份由 UUID 锚定（见 ADR-039），name 仅为可变标签，
	// 但活跃节点间名字唯一：唯一性由 database.AutoMigrate 建的「部分唯一索引」
	// （仅约束 deleted_at IS NULL 的活跃行）强制，软删除节点可释放其名供新节点复用。
	Name string `gorm:"type:varchar(128);not null" json:"name"`
	Host             string         `gorm:"type:varchar(256);not null" json:"host"`
	GRPCPort         int            `gorm:"column:grpc_port;not null" json:"grpcPort"`
	WSPort           int            `gorm:"not null" json:"wsPort"`
	Secret           string         `gorm:"type:varchar(128);not null" json:"-"`
	Status           NodeStatus     `gorm:"default:0" json:"status"`
	OS               string         `gorm:"type:varchar(64)" json:"os"`
	Arch             string         `gorm:"type:varchar(32)" json:"arch"`
	CPUCores         int            `json:"cpuCores"`
	MemoryMB         int64          `json:"memoryMb"`
	DiskTotalMB      int64          `json:"diskTotalMb"`
	CPUUsage         float32        `gorm:"default:0" json:"cpuUsage"`
	MemoryUsage      float32        `gorm:"default:0" json:"memoryUsage"`
	DiskUsage        float32        `gorm:"default:0" json:"diskUsage"`
	// Maintenance 维护模式（cordon）标记。为 true 时禁止新实例调度/分配到本节点，
	// 与 Status（在线/离线，由心跳驱动）正交：节点可同时「在线 + 维护中」。
	// 参见 FR-048。
	Maintenance bool `gorm:"default:false" json:"maintenance"`
	MemoryUsedMB     int64          `gorm:"default:0" json:"memoryUsedMb"`
	DiskUsedMB       int64          `gorm:"default:0" json:"diskUsedMb"`
	NetworkBytesSent int64          `gorm:"default:0" json:"networkBytesSent"`
	NetworkBytesRecv int64          `gorm:"default:0" json:"networkBytesRecv"`
	// LoadAvg1 节点 1 分钟 load average（FR-062，心跳驱动）。
	LoadAvg1         float64        `gorm:"default:0" json:"loadAvg1"`
	LastHeartbeat    *time.Time     `json:"lastHeartbeat"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (n *Node) BeforeCreate(tx *gorm.DB) error {
	if n.UUID == "" {
		n.UUID = uuid.New().String()
	}
	return nil
}
