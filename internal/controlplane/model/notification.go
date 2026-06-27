package model

import "time"

// NotificationLevel 站内信级别。
type NotificationLevel string

const (
	// NotificationLevelInfo 一般信息。
	NotificationLevelInfo NotificationLevel = "info"
	// NotificationLevelSuccess 成功通知（如任务完成）。
	NotificationLevelSuccess NotificationLevel = "success"
	// NotificationLevelWarning 警告。
	NotificationLevelWarning NotificationLevel = "warning"
	// NotificationLevelError 失败 / 错误通知（如任务失败）。
	NotificationLevelError NotificationLevel = "error"
)

// Notification 站内信。一条投递给某用户的消息，可关联到产生它的任务（见 ADR-040）。
type Notification struct {
	ID     uint              `gorm:"primaryKey" json:"id"`
	UserID uint              `gorm:"index;not null" json:"userId"` // 收件人
	Level  NotificationLevel `gorm:"type:varchar(16);not null;default:info" json:"level"`
	Title  string            `gorm:"type:varchar(256);not null" json:"title"`
	Body   string            `gorm:"type:text" json:"body"`
	// TaskID 关联任务的业务 ID（可空，非任务类通知留空）。
	TaskID string `gorm:"type:varchar(64);index" json:"taskId,omitempty"`
	// ReadAt 已读时间，nil 表示未读。
	ReadAt    *time.Time `json:"readAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}
