package model

import "time"

type LogCutoverState struct {
	ID        uint      `gorm:"primaryKey"`
	Enabled   bool      `gorm:"not null;default:false"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type LogCutoverWorker struct {
	WorkerUUID          string    `gorm:"primaryKey;type:varchar(128)"`
	CapabilityConfirmed bool      `gorm:"not null;default:false"`
	LedgerReady         bool      `gorm:"not null;default:false"`
	CutoffTime          time.Time `json:"cutoffTime"`
	CutoverApplied      bool      `gorm:"not null;default:false"`
	UpdatedAt           time.Time `json:"updatedAt"`
}
