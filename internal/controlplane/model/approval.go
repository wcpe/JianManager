package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 平台级审批票据与执行回执（FR-490 / FR-491）。
//
// 与 `assistant_confirmations`（FR-516 聊天侧确认）是**两套**，刻意不合并：
//   - 批准人语义不同：聊天确认由「会话所有者本人」确认；审批由「具备审批权限的人类」批准，
//     不要求是申请人（含双人复核语义）；
//   - 有效期不同：聊天 10 分钟，审批 24 小时（跨越班次）；
//   - 目的不同：前者防模型误操作，后者防 Agent 越权。
//
// 合并会让「谁能批」变成一处需要分支判断的地方，而权限漏洞最常出现在那里。
// 但状态机**形状**一致（都含 CAS 消费），差异只允许在语义层。

// 票据状态（FR-490 §2 冻结，实施时不得增删）。
const (
	// ApprovalStatusPending 待审批：唯一可被决策（批准/驳回/撤回）的状态。
	ApprovalStatusPending = "pending"
	// ApprovalStatusWithdrawn 申请人撤回（仅 pending 可撤回）。
	ApprovalStatusWithdrawn = "withdrawn"
	// ApprovalStatusRejected 人类驳回。
	ApprovalStatusRejected = "rejected"
	// ApprovalStatusExpired 超过 24h 未决策。
	ApprovalStatusExpired = "expired"
	// ApprovalStatusExecuting 已被批准并认领，正在执行。
	ApprovalStatusExecuting = "executing"
	// ApprovalStatusSucceeded 执行成功。
	ApprovalStatusSucceeded = "succeeded"
	// ApprovalStatusFailed 执行失败（领域失败或目标漂移），保留失败原因。
	ApprovalStatusFailed = "failed"
)

// ApprovalStatuses 全部状态（用于校验与断言，避免各处散落字面量）。
func ApprovalStatuses() []string {
	return []string{
		ApprovalStatusPending, ApprovalStatusWithdrawn, ApprovalStatusRejected,
		ApprovalStatusExpired, ApprovalStatusExecuting, ApprovalStatusSucceeded,
		ApprovalStatusFailed,
	}
}

// ApprovalTerminalStatuses 终态：不再变化，也不可再决策。
func ApprovalTerminalStatuses() []string {
	return []string{
		ApprovalStatusWithdrawn, ApprovalStatusRejected,
		ApprovalStatusExpired, ApprovalStatusSucceeded, ApprovalStatusFailed,
	}
}

// 申请人渠道（审计区分人类自助与 Agent 代办）。
const (
	ApprovalApplicantHuman = "human"
	ApprovalApplicantAgent = "agent"
)

// 执行回执结果（与 FR-516 回执同构，便于审计口径统一）。
const (
	ApprovalReceiptInFlight = "in_flight"
	ApprovalReceiptOK       = "ok"
	ApprovalReceiptFailed   = "failed"
	// ApprovalReceiptUnverified 可能已产生副作用但未确认（超时/连接中断）。
	// **不得当成功，也不得自动重试**——与 FR-516 的 unverified 同一语义。
	ApprovalReceiptUnverified = "unverified"
)

// ApprovalTicket 一张审批票据。
type ApprovalTicket struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	UUID string `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`

	// ApplicantUserID 申请人。Agent 调用时是 Token 归属的用户（不是 Token 自己）。
	ApplicantUserID uint `gorm:"not null;index" json:"applicantUserId"`
	// ApplicantKind human / agent：记录是哪个渠道提的，便于审计区分。
	ApplicantKind string `gorm:"type:varchar(16);not null" json:"applicantKind"`
	// TokenID Agent 调用时记下是哪把 Token（人类申请为空）。
	// 不设外键：Token 删除后审计仍需保留「当时用的是哪把」。
	TokenID string `gorm:"type:varchar(64);index" json:"tokenId,omitempty"`

	// Action 按 **action 粒度**登记（如 mcp.instance_stop），不按 Operation 分类：
	// instance_start 与 instance_stop 同属 write，但只有后者需审，分类无法表达该差异。
	Action string `gorm:"type:varchar(64);not null;index" json:"action"`

	// 冻结目标。TargetType/TargetID 由服务端解析后写入，客户端不得伪造归属。
	TargetType string `gorm:"type:varchar(32)" json:"targetType"`
	TargetID   string `gorm:"type:varchar(64)" json:"targetId"`

	// PayloadHash 参数规范化后的 SHA-256；PayloadFrozen 是原文快照（供批准人核对）。
	// 执行前重算并比对，防「批准后改参」。
	PayloadHash   string `gorm:"type:varchar(64);not null" json:"payloadHash"`
	PayloadFrozen string `gorm:"type:text" json:"payloadFrozen"`

	// Evidence 申请时的资源快照（JSON：状态、版本、名称等），供批准人核对与执行前漂移检测。
	Evidence string `gorm:"type:text" json:"evidence,omitempty"`

	// Reason 申请人填写的理由（**必填**）：没有理由的审批请求等于让批准人凭空判断。
	Reason string `gorm:"type:varchar(512);not null" json:"reason"`

	Status string `gorm:"type:varchar(16);not null;index" json:"status"`

	// IdempotencyKey 幂等键：同一 (申请人, action, 目标, payload) 在 pending 期间重复提交
	// 返回同一张票。实际唯一性由部分唯一索引保证（见 database 迁移），不靠应用层先查再插。
	IdempotencyKey string `gorm:"type:varchar(64)" json:"idempotencyKey,omitempty"`

	DecidedByUserID *uint      `gorm:"index" json:"decidedByUserId,omitempty"`
	DecidedAt       *time.Time `json:"decidedAt,omitempty"`
	ConsumedAt      *time.Time `json:"consumedAt,omitempty"`
	ExpiresAt       time.Time  `gorm:"not null;index" json:"expiresAt"`

	// FailureReason status=failed 时的原因（保留批准事实，只记失败原因）。
	FailureReason string `gorm:"type:varchar(512)" json:"failureReason,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BeforeCreate 创建前生成 UUID（与既有模型一致）。
func (a *ApprovalTicket) BeforeCreate(tx *gorm.DB) error {
	if a.UUID == "" {
		a.UUID = uuid.New().String()
	}
	return nil
}

// ApprovalReceipt 一张票据的执行回执（FR-491 §4.4）。
type ApprovalReceipt struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	TicketID     uint       `gorm:"not null;index" json:"ticketId"`
	Outcome      string     `gorm:"type:varchar(16);not null" json:"outcome"`
	ErrorCode    string     `gorm:"type:varchar(64)" json:"errorCode,omitempty"`
	ErrorMessage string     `gorm:"type:varchar(512)" json:"errorMessage,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}
