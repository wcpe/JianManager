package model

import "time"

// ServerRegistration 表示一个后端子服注册进一个代理（proxy↔backend 的 M:N，ADR-007）。
// 每条注册携带「代理内本地属性」：在该代理 servers{} 中的 alias、try/优先级、forced-host、restricted。
// 同一 backend 可注册进多个 proxy；同一代理内 alias 唯一。
//
// 本模型只承载关系数据；将注册「写入代理配置 + Velocity secret 下发」属 FR-035。
type ServerRegistration struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ProxyID 代理实例 ID（role=proxy）。
	ProxyID uint `gorm:"not null;index;uniqueIndex:idx_reg_proxy_alias" json:"proxyId"`
	// BackendID 后端实例 ID（role=backend）。
	BackendID uint `gorm:"not null;index" json:"backendId"`
	// Alias 在该代理 servers{} 中的本地名（[a-z0-9_-]{1,64}），同一代理内唯一。
	Alias string `gorm:"type:varchar(64);not null;uniqueIndex:idx_reg_proxy_alias" json:"alias"`
	// Priority try/优先级顺序，小值优先。
	Priority int `gorm:"default:0" json:"priority"`
	// ForcedHost 可选 forced-host 域名 → 该后端。
	ForcedHost string `gorm:"type:varchar(255)" json:"forcedHost"`
	// Restricted Velocity restricted：仅可经 forced-host/命令访问。
	Restricted bool `gorm:"default:false" json:"restricted"`
	// Enabled 是否启用（写入代理配置时跳过 disabled）。
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
