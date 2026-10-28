package model

import "time"

// 助手工具调用的确认票据与执行回执（FR-516 §6）。
//
// 两张表分开而不是合成一张：票据表达「用户是否批准了一次写入」，回执表达
// 「这次调用实际发生了什么」。只读调用**不出票**（也就没有票据可关联），
// 但它同样需要回执来串联审计——所以回执的 confirmation_id 必须可空。

// 票据来源枚举（FR-516 §6.1：受限枚举，不是自由文本）。
const (
	AssistantConfirmSourceChat = "chat"
	// AssistantConfirmSourceJmat 预留给 FR-396 统一骨架，本 FR 不写入。
	AssistantConfirmSourceJmat = "jmat"
)

// 票据状态（FR-516 §6.1）。
const (
	AssistantConfirmStatusPending     = "pending"
	AssistantConfirmStatusApproved    = "approved"
	AssistantConfirmStatusRejected    = "rejected"
	AssistantConfirmStatusExpired     = "expired"
	AssistantConfirmStatusConsumed    = "consumed"
	AssistantConfirmStatusInvalidated = "invalidated"
)

// 执行策略枚举（FR-516 §6.1）。
const (
	// AssistantPolicySelfConfirm 本 FR 唯一写入值：用户在聊天里自行确认。
	AssistantPolicySelfConfirm = "self_confirm"
	// AssistantPolicyPlatformGate 预留（FR-490～493 平台审批中心）。
	AssistantPolicyPlatformGate = "platform_gate"
)

// 回执结果枚举（FR-516 §6.2）。
const (
	AssistantReceiptInFlight = "in_flight"
	AssistantReceiptOK       = "ok"
	AssistantReceiptFailed   = "failed"
	AssistantReceiptRejected = "rejected"
	AssistantReceiptExpired  = "expired"
	// AssistantReceiptUnverified 已发出写调用但未取得确定结果。
	// **禁止**当失败重试，也禁止当成功（§6.2）。
	AssistantReceiptUnverified = "unverified"
)

// AssistantConfirmation 一次写入调用的确认票据（FR-516 §6.1）。
//
// 绑定字段（任一变化即失效，见 §6.3）：owner + conversation + run + call +
// schema 指纹 + 策略版本 + 参数哈希 + 目标指纹 + 有效期。
type AssistantConfirmation struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// Source 票据来源（chat / 预留 jmat）。
	Source string `gorm:"size:16;not null;default:'chat'" json:"source"`

	OwnerUserID    uint   `gorm:"index;not null" json:"ownerUserId"`
	ConversationID uint   `gorm:"index;not null" json:"conversationId"`
	RunID          uint   `gorm:"index;not null" json:"runId"`
	CallID         string `gorm:"size:64;not null" json:"callId"`

	// ToolName 命名空间名（本 FR 为 system.<action>）。
	ToolName string `gorm:"size:128;not null" json:"toolName"`
	// SchemaFingerprint 工具 schema 指纹。内置工具用版本号，外部 MCP 用 schema_hash；
	// 统一按「字符串不等即失效」处理，不假设存在单调递增的版本号。
	SchemaFingerprint string `gorm:"size:64;not null" json:"schemaFingerprint"`
	// PolicyVersion 执行策略版本变化使旧确认失效。
	PolicyVersion int `gorm:"not null;default:1" json:"policyVersion"`
	// ArgsHash 规范化参数的 SHA-256（键排序后序列化），只用于比对。
	ArgsHash string `gorm:"size:64;not null" json:"argsHash"`
	// ArgsFrozen 冻结参数正文（票据创建时原样快照）。
	// 核对 UI 与执行都以它为权威来源；不含敏感明文。
	ArgsFrozen string `gorm:"type:text;not null" json:"argsFrozen"`
	// AuthVersion 出票时的用户 AuthVersion 快照。
	// **必要不充分**：角色模板改权/绑定变更/组成员移除都不会递增它，
	// 因此执行前必须重跑完整有效权限判定（§5.1），本字段只挡明显失效。
	AuthVersion uint `gorm:"not null;default:0" json:"authVersion"`
	// Policy 执行策略（本 FR 只写 self_confirm）。
	Policy string `gorm:"size:24;not null;default:'self_confirm'" json:"policy"`
	// TargetFingerprint 真实目标指纹（resourceType + ID）。
	TargetFingerprint string `gorm:"size:128;not null" json:"targetFingerprint"`

	Status     string     `gorm:"size:16;not null;default:'pending';index" json:"status"`
	DecidedAt  *time.Time `json:"decidedAt"`
	ExpiresAt  time.Time  `gorm:"index;not null" json:"expiresAt"`
	ConsumedAt *time.Time `json:"consumedAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 表名。
func (AssistantConfirmation) TableName() string { return "assistant_confirmations" }

// AssistantReceipt 一次工具调用的执行回执（FR-516 §6.2）。
//
// 只读调用也写回执（confirmation_id 为 null）——否则审计里串不起只读调用。
type AssistantReceipt struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// ConfirmationID 写路径必填、只读为 null（只读不出票）。
	ConfirmationID *uint  `gorm:"index" json:"confirmationId"`
	RunID          uint   `gorm:"index;not null" json:"runId"`
	CallID         string `gorm:"size:64;not null" json:"callId"`

	ToolName string `gorm:"size:128;not null" json:"toolName"`

	// Outcome in_flight / ok / failed / rejected / expired / unverified。
	Outcome      string `gorm:"size:16;not null" json:"outcome"`
	ErrorCode    string `gorm:"size:64" json:"errorCode"`
	ErrorMessage string `gorm:"size:512" json:"errorMessage"`
	// ResultSummary 有界摘要（上限见 §8）。
	ResultSummary string `gorm:"type:text" json:"resultSummary"`

	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TableName 表名。
func (AssistantReceipt) TableName() string { return "assistant_receipts" }
