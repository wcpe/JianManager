package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuditLog 审计日志。
type AuditLog struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UUID       string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	UserID     uint   `gorm:"not null;index" json:"userId"`
	Action     string `gorm:"type:varchar(64);not null" json:"action"` // instance.start, user.create, etc.
	TargetType string `gorm:"type:varchar(32)" json:"targetType"`      // instance, user, group
	TargetID   string `gorm:"type:varchar(64)" json:"targetId"`
	Detail     string `gorm:"type:text" json:"detail"` // JSON
	IP         string `gorm:"type:varchar(64)" json:"ip"`
	// Failed 操作是否失败（FR-321：失败操作也留痕并带错误内容）。语义取「失败」而非「成功」：
	// 失败是非零值 true，规避 GORM 零值列不进 INSERT 的坑（存 success 时 false 会被 default 吃掉），
	// 且历史行零值天然=未失败，无需迁移回填。
	Failed bool `json:"failed"`
	// Error 失败时的错误内容（响应 error body 截断，FR-321）。
	Error     string    `gorm:"type:varchar(512)" json:"error"`
	CreatedAt time.Time `json:"createdAt"`
	User      User      `gorm:"foreignKey:UserID" json:"user,omitempty"`

	// 助手调用的串联维度（FR-516 §9）。
	//
	// 为什么是独立列而不是塞进 Detail JSON：验收要求「按 conversationId/runId/callId
	// 串起一次完整调用」。放进 JSON 就只能 `LIKE '%"runId":123%'` 全表扫描，
	// 而审计表正是最容易变大的那张表——一条「能查到」的验收标准不该建立在全表扫描上。
	//
	// 三者都可空：绝大多数审计行不是助手产生的（HTTP 端点写的那些）。
	// 不设外键约束：审计行必须在被引用的会话/run 被清理后依然留存。
	ConversationID *uint  `gorm:"index" json:"conversationId,omitempty"`
	RunID          *uint  `gorm:"index" json:"runId,omitempty"`
	CallID         string `gorm:"type:varchar(64);index" json:"callId,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.UUID == "" {
		a.UUID = uuid.New().String()
	}
	return nil
}
