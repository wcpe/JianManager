package model

import "time"

// ClientIPRule 客户端分发端点 IP 防护规则（FR-096，见 ADR-023）。运行时可改、入审计。
//
// mode=deny：匹配即拒（黑名单，deny 始终优先）；mode=allow：存在任一 allow 规则即进入**白名单模式**
// （仅匹配 allow 的 IP 放行）。CIDR 支持单 IP（视作 /32、/128）或网段。
type ClientIPRule struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// CIDR IP 或 CIDR（单 IP 视作 /32、/128）。
	CIDR string `gorm:"column:cidr;type:varchar(64);not null" json:"cidr"`
	// Mode deny | allow。
	Mode string `gorm:"type:varchar(8);not null" json:"mode"`
	// Note 规则备注。
	Note string `gorm:"type:varchar(255)" json:"note"`
	// CreatedBy 创建者用户 ID（审计辅助）。
	CreatedBy uint      `gorm:"default:0;not null" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}
