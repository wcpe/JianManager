package model

import "time"

// ClientDistEvent 客户端分发拉取/下载明细事件（FR-093，见 ADR-023）。
// **短保留 + 滚动清理**（数据量治理）；供按 IP/机器码/频道/版本/时间检索与近窗去重分布。
// 机器码/IP 客户端可伪造、不可信，仅追踪统计。
type ClientDistEvent struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ChannelID 频道 slug。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);not null;index:idx_cde_channel_time" json:"channelId"`
	// MachineID 客户端机器码（不可信）。
	MachineID string `gorm:"column:machine_id;type:varchar(128);index" json:"machineId"`
	// IP 来源 IP（限流/分布主维度）。
	IP string `gorm:"type:varchar(64);index" json:"ip"`
	// Kind 事件类型：manifest | artifact。
	Kind string `gorm:"type:varchar(16);not null" json:"kind"`
	// Version manifest 拉取的版本号（artifact 事件为 0，制品跨版本共享）。
	Version int `gorm:"default:0;not null" json:"version"`
	// ArtifactSHA 制品 sha256（仅 artifact 事件）。
	ArtifactSHA string `gorm:"column:artifact_sha;type:char(64)" json:"artifactSha"`
	// Bytes 响应字节数。
	Bytes int64 `gorm:"default:0;not null" json:"bytes"`
	// Status HTTP 状态码（200/206/304/404…）。
	Status int `gorm:"default:0;not null" json:"status"`
	// DurationMs 处理耗时（毫秒）。
	DurationMs int64 `gorm:"column:duration_ms;default:0;not null" json:"durationMs"`
	// CreatedAt 事件时间（清理基准）。
	CreatedAt time.Time `gorm:"index:idx_cde_channel_time" json:"createdAt"`
}

// ClientDistDaily 客户端分发按日聚合（FR-093）。**长保留**，写时增量 upsert；供下载量趋势 + 版本分布（FR-095）。
type ClientDistDaily struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Day UTC 日期 YYYY-MM-DD。与 channel/version/kind 组成唯一键。
	Day string `gorm:"type:char(10);not null;uniqueIndex:idx_cdd_day_chan_ver_kind" json:"day"`
	// ChannelID 频道 slug。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);not null;uniqueIndex:idx_cdd_day_chan_ver_kind" json:"channelId"`
	// Version manifest 版本（artifact 聚合为 0）。
	Version int `gorm:"not null;uniqueIndex:idx_cdd_day_chan_ver_kind" json:"version"`
	// Kind manifest | artifact。
	Kind string `gorm:"type:varchar(16);not null;uniqueIndex:idx_cdd_day_chan_ver_kind" json:"kind"`
	// Requests 当日该维度请求数。
	Requests int64 `gorm:"default:0;not null" json:"requests"`
	// Bytes 当日该维度总字节数。
	Bytes int64 `gorm:"default:0;not null" json:"bytes"`
}
