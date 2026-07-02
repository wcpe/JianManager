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
	// ErrCode 语义错误码（FR-249）。成功事件为空；失败事件填与响应 error 字段一致的码
	// （INVALID_CLIENT_KEY/NO_LATEST_VERSION/ARTIFACT_NOT_FOUND/SIGN_KEY_NOT_CONFIGURED/CHANNEL_NOT_FOUND/INTERNAL_ERROR）。
	ErrCode string `gorm:"column:err_code;type:varchar(48);index" json:"errCode"`
	// ErrReason 可读错误原因（FR-265）。
	ErrReason string `gorm:"column:err_reason;type:varchar(255)" json:"errReason"`
	// Method HTTP 方法（FR-265 日志详情）。
	Method string `gorm:"type:varchar(8)" json:"method"`
	// Path 请求路径（不含 query，FR-265 日志详情）。
	Path string `gorm:"type:varchar(512)" json:"path"`
	// RequestHeadersJSON 脱敏后的请求头白名单 JSON（FR-265）。
	RequestHeadersJSON string `gorm:"column:request_headers_json;type:text" json:"-"`
	// ResponseHeadersJSON 响应头白名单 JSON（FR-265）。
	ResponseHeadersJSON string `gorm:"column:response_headers_json;type:text" json:"-"`
	// ETag 响应 ETag 快捷列（FR-265）。
	ETag string `gorm:"column:etag;type:varchar(128)" json:"etag"`
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
