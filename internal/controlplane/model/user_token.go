package model

import (
	"time"
)

// UserToken 用户级长期令牌。
//
// 与 AgentToken 严格分离：AgentToken（jmat_）是机器凭证，由管理员签发供 MCP/CI 使用；
// UserToken（jmut_）是人类凭证，由用户自助登录获取、可在 Web 上自助吊销。
// 两者共用存储模式（只存 hash、带 scope）但分表、分 handler、分中间件，
// 以保证审计主体可区分。
type UserToken struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// UserID 所属用户；令牌的一切权限不超过该用户当前权限。
	UserID uint `gorm:"not null;index" json:"userId"`
	// Name 用户可读名称，如「我的笔记本」。
	Name string `gorm:"type:varchar(128);not null" json:"name"`
	// DeviceLabel 设备标识，由 CLI 上报（hostname + os），便于用户识别是哪台设备。
	DeviceLabel string `gorm:"type:varchar(128)" json:"deviceLabel"`

	// TokenHash 明文的 SHA-256 十六进制小写。库内不存明文。
	TokenHash string `gorm:"column:token_hash;type:char(64);uniqueIndex;not null" json:"-"`
	// TokenPrefix 明文前缀（如 jmut_ab12），仅供列表识别，不可用于鉴权。
	TokenPrefix string `gorm:"column:token_prefix;type:varchar(16);not null" json:"tokenPrefix"`

	// ScopedNodeIDs 可访问节点 ID 列表（JSON 数组）。空表示不额外收窄（受用户权限约束）。
	ScopedNodeIDs string `gorm:"column:scoped_node_ids;type:text" json:"scopedNodeIds"`
	// ScopedInstanceIDs 可访问实例 ID 列表（JSON 数组）。空表示不额外收窄。
	ScopedInstanceIDs string `gorm:"column:scoped_instance_ids;type:text" json:"scopedInstanceIds"`
	// Capabilities 能力列表（JSON 数组），复用 agent 的 capability 常量。
	// 有效能力 = 本列表 ∩ 用户当前权限（见 service.UserTokenService.Resolve）。
	Capabilities string `gorm:"column:capabilities;type:text" json:"capabilities,omitempty"`

	// AuthVersion 签发时该用户的 AuthVersion 快照。
	// 用户改密码/禁用时 AuthVersion 递增，令本令牌立即失效。
	AuthVersion uint `gorm:"column:auth_version;not null;default:0" json:"-"`

	// ExpiresAt 过期时间；到期即校验失败。
	ExpiresAt time.Time `gorm:"not null;index" json:"expiresAt"`
	// Revoked 吊销标记；true 即立即失效。
	Revoked bool `gorm:"not null;default:false;index" json:"revoked"`
	// LastUsedAt 最近一次成功鉴权时间。
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// TableName 显式指定表名，避免与 AgentToken 混淆。
func (UserToken) TableName() string { return "user_tokens" }

// DeviceCode 设备授权流程的一次性凭证（RFC 8628）。
//
// 三态：pending（等待用户确认）→ approved（已确认，等待 CLI 取走）→ consumed（已取走）。
// 另有 denied / expired 两个终态。
type DeviceCode struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// DeviceCode 高熵字符串，仅 CLI 持有，用于轮询。
	DeviceCode string `gorm:"column:device_code;type:varchar(128);uniqueIndex;not null" json:"-"`
	// UserCode 8 位易读码，用户在浏览器输入。
	// 去掉易混字符（0/O/1/I/l），格式 ABCD-1234。
	UserCode string `gorm:"column:user_code;type:varchar(16);uniqueIndex;not null" json:"userCode"`
	// DeviceLabel CLI 上报的设备标识，展示在授权页让用户确认是自己发起的。
	DeviceLabel string `gorm:"type:varchar(128)" json:"deviceLabel"`

	// Status pending | approved | denied | consumed | expired
	Status string `gorm:"type:varchar(16);not null;default:'pending';index" json:"status"`

	// UserID 授权通过后填入；pending 时为空。
	UserID *uint `gorm:"index" json:"userId,omitempty"`
	// TokenID 授权通过后签发的 user_token ID；CLI 取走后清空明文只留引用。
	TokenID *uint `gorm:"index" json:"tokenId,omitempty"`
	// Plaintext 仅在 approved 后、被 CLI 取走前存在，存一次后立即清空。
	// 这是唯一允许短暂保留明文的字段，consumed 时必须置空。
	Plaintext string `gorm:"type:varchar(128)" json:"-"`

	// ExpiresAt 设备码过期时间（默认 600s）。
	ExpiresAt time.Time `gorm:"not null;index" json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
	// ConsumedAt 被 CLI 取走的时间；非空即不可再次取用。
	ConsumedAt *time.Time `json:"consumedAt,omitempty"`
}

// TableName 显式指定表名。
func (DeviceCode) TableName() string { return "device_codes" }

// 设备码状态常量。
const (
	DeviceCodePending  = "pending"
	DeviceCodeApproved = "approved"
	DeviceCodeDenied   = "denied"
	DeviceCodeConsumed = "consumed"
	DeviceCodeExpired  = "expired"
)
