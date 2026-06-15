package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BotStatus Bot 状态。
type BotStatus string

const (
	BotStatusPending   BotStatus = "pending"
	BotStatusConnecting BotStatus = "connecting"
	BotStatusConnected  BotStatus = "connected"
	BotStatusError      BotStatus = "error"
	BotStatusStopped    BotStatus = "stopped"
)

// Bot Mineflayer Bot。
type Bot struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	UUID       string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID uint           `gorm:"not null;index" json:"instanceId"`
	Name       string         `gorm:"type:varchar(128);not null" json:"name"`
	Status     BotStatus      `gorm:"type:varchar(32);default:pending" json:"status"`
	Config     string         `gorm:"type:text" json:"config"` // JSON: server, port, auth
	Behavior   string         `gorm:"type:varchar(64)" json:"behavior"`
	WorkerID   string         `gorm:"type:varchar(128)" json:"workerId"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *Bot) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}
