package model

import (
	"time"
)

// AgentToken Agent 专用令牌（FR-384 / FR-395，见 ADR-076/080）。
// 落库只存 SHA-256 哈希；明文仅签发时一次性返回，不可二次读取。
// 与人类 JWT 分离：scope（实例/节点白名单）+ V1 write_allowlist / V2 capabilities 约束 agent 能力面。
type AgentToken struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Name 运营可见名称。
	Name string `gorm:"type:varchar(128);not null" json:"name"`
	// TokenHash 明文的 SHA-256 十六进制小写。库内不存明文。
	TokenHash string `gorm:"column:token_hash;type:char(64);uniqueIndex;not null" json:"-"`
	// TokenPrefix 明文前缀（如 jmat_ab12），仅供列表识别。
	TokenPrefix string `gorm:"column:token_prefix;type:varchar(16);not null" json:"tokenPrefix"`
	// ScopedInstanceIDs 可访问实例 ID 列表（JSON 数组）。空表示「未授权任何实例读/写」——默认从严。
	ScopedInstanceIDs string `gorm:"column:scoped_instance_ids;type:text" json:"scopedInstanceIds"`
	// ScopedNodeIDs 可访问节点 ID 列表（JSON 数组）。空表示未授权任何节点。
	ScopedNodeIDs string `gorm:"column:scoped_node_ids;type:text" json:"scopedNodeIds"`
	// WriteAllowlist 写操作白名单（JSON 字符串数组），如 ["instance.life","node.maintenance"]。
	// 仅 V1 策略使用；V2 保持空并改用 Capabilities。
	WriteAllowlist string `gorm:"column:write_allowlist;type:text" json:"writeAllowlist"`
	// PolicyVersion 策略版本：1=V1 写白名单；2=V2 capability。既有记录默认 1。
	PolicyVersion int `gorm:"column:policy_version;not null;default:1" json:"policyVersion"`
	// Capabilities V2 能力分组（JSON 字符串数组）。仅 policy_version=2 使用。
	Capabilities string `gorm:"column:capabilities;type:text" json:"capabilities,omitempty"`
	// ExpiresAt 过期时间；到期即校验失败。
	ExpiresAt time.Time `gorm:"not null;index" json:"expiresAt"`
	// Revoked 吊销标记；true 即立即失效。
	Revoked bool `gorm:"default:false;not null;index" json:"revoked"`
	// LastUsedAt 最近一次成功鉴权时间（可选更新）。
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	// CreatedBy 签发该 token 的平台管理员用户 ID。
	CreatedBy uint `json:"createdBy"`
}
