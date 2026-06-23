package model

import (
	"time"
)

// NodeEnrollToken 节点 enrollment token：新增 Worker Node 时的一次性、限时准入凭据（FR-080，见 ADR-020）。
//
// 信任模型同构 FR-086 拉取密钥与 JM 既有运行时密钥惯例：落库只存明文的 SHA-256 哈希，
// 明文仅签发时一次性返回、不可二次读取。Worker 注册时经 gRPC metadata 携带明文，
// CP 在 Register 内校验（存在 + 未过期 + 未消费 + 未吊销）并原子标记 Used（一次性）。
// enrollment token 只对「新节点首次落库」设门槛，已存在节点重注册不强制 token（ADR-020 §1）。
type NodeEnrollToken struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TokenHash enrollment token 明文的 SHA-256 十六进制小写。库内不存明文。
	TokenHash string `gorm:"column:token_hash;type:char(64);uniqueIndex;not null" json:"-"`
	// TokenPrefix 明文前缀（如 jmet_ab12），仅供列表识别，不足以重建 token。
	TokenPrefix string `gorm:"column:token_prefix;type:varchar(16);not null" json:"tokenPrefix"`
	// NodeName 预设节点名；留空则注册时以 Worker 上报名生效。
	NodeName string `gorm:"type:varchar(128)" json:"nodeName"`
	// ExpiresAt 过期时间；到期即校验失败。
	ExpiresAt time.Time `gorm:"not null" json:"expiresAt"`
	// Used 是否已消费（一次性）；true 即不可再用于注册。
	Used bool `gorm:"default:false;index;not null" json:"used"`
	// UsedAt 消费时间。
	UsedAt *time.Time `json:"usedAt"`
	// UsedByNode 消费该 token 注册成功的节点 UUID（审计/追溯用）。
	UsedByNode string `gorm:"type:char(36)" json:"usedByNode"`
	// Revoked 吊销标记（运营主动作废未消费 token）；true 即校验失败。
	Revoked   bool      `gorm:"default:false;not null" json:"revoked"`
	CreatedAt time.Time `json:"createdAt"`
	// CreatedBy 签发该 token 的平台管理员用户 ID（审计用）。
	CreatedBy uint `json:"createdBy"`
}
