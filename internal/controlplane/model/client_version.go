package model

import "time"

// ClientVersion 客户端分发版本快照（FR-087，见 ADR-022、contract §2）。
// 一条记录 = 频道某次发布的完整文件清单 + 同步策略 + 自更新段。Version 单调递增、与 ClientChannel.CurrentVersion
// 配合定位 latest。manifest 由该快照即时组装 + Ed25519 签名（不缓存签名，密钥轮换/防降级见 ADR-022）。
//
// 本 FR（latest-only）只需「能发布一版并以 latest 返回」；完整版本历史/运营回滚/管理台见 FR-088。
type ClientVersion struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ChannelID 所属频道 slug（随频道删除级联清理）。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);index:idx_client_versions_channel_version,unique;not null" json:"channelId"`
	// Version 单调递增整数（同频道内唯一）；防降级基准（contract §3）。
	Version int `gorm:"index:idx_client_versions_channel_version,unique;not null" json:"version"`
	// FilesJSON 文件清单快照（[]ManifestFile 的 JSON）：path/sha256/md5/size/sync/platform/artifact。
	FilesJSON string `gorm:"column:files_json;type:text;not null" json:"-"`
	// ManagedDirsJSON 托管目录快照（[]string 的 JSON）：仅这些目录可增删（减量）。
	ManagedDirsJSON string `gorm:"column:managed_dirs_json;type:text;not null" json:"-"`
	// AgentJSON 楔子 + updater-core 自更新段快照（ManifestAgent 的 JSON），可空（未声明自更新段）。
	AgentJSON string `gorm:"column:agent_json;type:text" json:"-"`
	// Note 发布备注（运营可读，信息性）。
	Note string `gorm:"type:varchar(512)" json:"note"`
	// CreatedBy 发布者用户 ID（审计辅助，0=未知）。
	CreatedBy uint      `gorm:"default:0;not null" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}
