package model

import "time"

// AgentTransferTicket 保存传输票据的摘要与消费状态，确保重启及多进程部署下仍保持一次性语义。
type AgentTransferTicket struct {
	Digest     string     `gorm:"primaryKey;type:char(64)"`
	ExpiresAt  time.Time  `gorm:"not null;index"`
	ConsumedAt *time.Time `gorm:"index"`
	CreatedAt  time.Time
}
