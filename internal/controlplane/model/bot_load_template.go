package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BotLoadTemplate 是 FR-370 命令压测可复用模板（个人所有权，不绑定目标实例）。
type BotLoadTemplate struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	UUID            string         `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	CreatedBy       uint           `gorm:"not null;index;uniqueIndex:uniq_bot_load_tpl_active_name,priority:1" json:"createdBy"`
	// ActiveNameKey 为 trim 后名称 UTF-8 的 SHA-256 hex；软删时置 null 以允许复用名称。
	ActiveNameKey   *string        `gorm:"type:char(64);uniqueIndex:uniq_bot_load_tpl_active_name,priority:2" json:"-"`
	Name            string         `gorm:"type:varchar(128);not null" json:"name"`
	Description     string         `gorm:"type:text;not null" json:"description"`
	CommandSchedule string         `gorm:"type:longtext;not null" json:"commandSchedule"`
	LoadProfile     string         `gorm:"type:longtext;not null" json:"loadProfile"`
	Thresholds      string         `gorm:"type:longtext;not null" json:"thresholds"`
	Tags            string         `gorm:"type:longtext;not null" json:"tags"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`

	Creator User `gorm:"foreignKey:CreatedBy;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (t *BotLoadTemplate) BeforeCreate(tx *gorm.DB) error {
	if t.UUID == "" {
		t.UUID = uuid.New().String()
	}
	return nil
}

// TableName 固定表名。
func (BotLoadTemplate) TableName() string { return "bot_load_templates" }
