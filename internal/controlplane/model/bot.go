package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BotStatus Bot 状态。
type BotStatus string

const (
	BotStatusPending      BotStatus = "pending"
	BotStatusConnecting   BotStatus = "connecting"
	BotStatusConnected    BotStatus = "connected"
	BotStatusDisconnected BotStatus = "disconnected"
	BotStatusError        BotStatus = "error"
	BotStatusStopped      BotStatus = "stopped"
)

// Bot Mineflayer Bot。
type Bot struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	UUID            string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID      uint           `gorm:"not null;index" json:"instanceId"`
	StressSessionID *uint          `gorm:"index" json:"stressSessionId,omitempty"`
	Name            string         `gorm:"type:varchar(128);not null" json:"name"`
	Status          BotStatus      `gorm:"type:varchar(32);default:pending" json:"status"`
	Config          string         `gorm:"type:text" json:"config"` // JSON: server, port, auth
	Behavior        string         `gorm:"type:varchar(64)" json:"behavior"`
	WorkerID        string         `gorm:"type:varchar(128)" json:"workerId"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`

	// Instance 所属实例，仅用于批量委托时预加载节点路由信息，不参与序列化。
	Instance Instance `gorm:"foreignKey:InstanceID" json:"-"`
	// StressSession 所属压测会话；删除会话时保留 Bot，并清空关联。
	StressSession *BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *Bot) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}

// BotStressSessionStatus 压测会话状态。
type BotStressSessionStatus string

const (
	BotStressSessionPending BotStressSessionStatus = "pending"
	BotStressSessionRunning BotStressSessionStatus = "running"
	BotStressSessionStopped BotStressSessionStatus = "stopped"
	BotStressSessionError   BotStressSessionStatus = "error"
)

// BotStressSession Bot 压测会话。
type BotStressSession struct {
	ID                   uint                   `gorm:"primaryKey" json:"id"`
	UUID                 string                 `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID           uint                   `gorm:"not null;index" json:"instanceId"`
	Name                 string                 `gorm:"type:varchar(128);not null" json:"name"`
	NamePrefix           string                 `gorm:"type:varchar(64);not null" json:"namePrefix"`
	Status               BotStressSessionStatus `gorm:"type:varchar(32);default:pending;index" json:"status"`
	BotCount             int                    `gorm:"not null" json:"botCount"`
	Behavior             string                 `gorm:"type:varchar(64)" json:"behavior"`
	Config               string                 `gorm:"type:text" json:"config"`
	OrchestrationYAML    string                 `gorm:"type:text" json:"orchestrationYaml,omitempty"`
	OrchestrationSummary string                 `gorm:"type:text" json:"orchestrationSummary,omitempty"`
	Succeeded            int                    `gorm:"default:0" json:"succeeded"`
	Failed               int                    `gorm:"default:0" json:"failed"`
	LastError            string                 `gorm:"type:text" json:"lastError,omitempty"`
	StartedAt            *time.Time             `json:"startedAt,omitempty"`
	EndedAt              *time.Time             `json:"endedAt,omitempty"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
	DeletedAt            gorm.DeletedAt         `gorm:"index" json:"-"`

	Instance Instance `gorm:"foreignKey:InstanceID" json:"instance,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (s *BotStressSession) BeforeCreate(tx *gorm.DB) error {
	if s.UUID == "" {
		s.UUID = uuid.New().String()
	}
	return nil
}
