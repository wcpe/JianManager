package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AlertRule 告警规则。
type AlertRule struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	UUID         string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	Name         string         `gorm:"type:varchar(128);not null" json:"name"`
	TargetType   string         `gorm:"type:varchar(32);not null" json:"targetType"` // node, instance
	TargetID     *uint          `json:"targetId"`                                    // nil 表示全局
	Metric       string         `gorm:"type:varchar(64);not null" json:"metric"`     // cpu, memory, disk
	Operator     string         `gorm:"type:varchar(4);not null" json:"operator"`    // >, <, >=, <=
	Threshold    float64        `gorm:"not null" json:"threshold"`
	DurationSec  int            `gorm:"default:0" json:"durationSec"`
	NotifyType   string         `gorm:"type:varchar(32)" json:"notifyType"`   // webhook
	NotifyTarget string         `gorm:"type:varchar(512)" json:"notifyTarget"` // webhook URL
	Enabled      bool           `gorm:"default:true" json:"enabled"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (a *AlertRule) BeforeCreate(tx *gorm.DB) error {
	if a.UUID == "" {
		a.UUID = uuid.New().String()
	}
	return nil
}

// AlertEvent 告警事件。
type AlertEvent struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	RuleID    uint       `gorm:"not null;index" json:"ruleId"`
	TargetID  uint       `json:"targetId"`
	Value     float64    `json:"value"`
	Message   string     `gorm:"type:varchar(512)" json:"message"`
	Resolved  bool       `gorm:"default:false" json:"resolved"`
	FiredAt   time.Time  `json:"firedAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	Rule      AlertRule  `gorm:"foreignKey:RuleID" json:"rule,omitempty"`
}
