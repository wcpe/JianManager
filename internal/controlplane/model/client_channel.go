package model

import (
	"time"

	"gorm.io/gorm"
)

// ClientChannel 客户端分发频道：每服/每整合包一个，作为 manifest/制品端点的对外标识与路由键。
// ChannelID 为运营者指定的 slug（URL 段），CurrentVersion 为 latest 版本指针占位（FR-088 编排）。
// 参见 ADR-022、FR-086。
type ClientChannel struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ChannelID 频道 slug：对外标识与端点路径段，全局唯一。约束 `^[a-z0-9][a-z0-9-]{1,63}$`。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);uniqueIndex;not null" json:"channelId"`
	// Name 展示名。
	Name string `gorm:"type:varchar(128);not null" json:"name"`
	// Description 描述，可空。
	Description string `gorm:"type:varchar(512)" json:"description"`
	// CurrentVersion 当前 latest 版本指针占位；0=未发布。本 FR 仅建字段，编排见 FR-088。
	CurrentVersion int            `gorm:"default:0;not null" json:"currentVersion"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

// ClientPullKey 频道拉取密钥：玩家侧 updater 拉 manifest/制品时经请求头携带。
// 半公开凭据（随整包分发会泄露）——仅作鉴权路由 + 吊销，不作内容可信依据（内容可信靠 manifest 签名）。
// 落库只存 SHA-256 哈希，明文仅创建/轮换时一次性返回、不可二次读取（同构 JM 既有运行时密钥惯例）。
// 参见 ADR-022 §1。
type ClientPullKey struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ChannelID 所属频道 slug（随频道删除级联清理）。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);index;not null" json:"channelId"`
	// Name 密钥名（识别用途，如「正式包」「灰度」）。
	Name string `gorm:"type:varchar(128);not null" json:"name"`
	// KeyHash 拉取密钥明文的 SHA-256 十六进制小写。库内不存明文。
	KeyHash string `gorm:"column:key_hash;type:char(64);uniqueIndex;not null" json:"-"`
	// KeyPrefix 明文前缀（如 jmck_ab12），仅供列表识别，不足以重建密钥。
	KeyPrefix string `gorm:"column:key_prefix;type:varchar(16);not null" json:"keyPrefix"`
	// Revoked 吊销标记；true 即鉴权失败。
	Revoked bool `gorm:"default:false;index;not null" json:"revoked"`
	// ExpiresAt 可选过期时间；到期即鉴权失败。
	ExpiresAt *time.Time `json:"expiresAt"`
	// LastUsedAt 最近一次鉴权命中时间（统计用，弱一致）。
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	// RevokedAt 吊销时间。
	RevokedAt *time.Time `json:"revokedAt"`
}

// ValidChannelID 校验频道 slug 合法性：仅小写字母/数字/连字符，首字符为字母或数字，长度 2-64。
func ValidChannelID(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
