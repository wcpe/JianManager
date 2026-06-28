package model

import "time"

// ClientDistSnapshot 客户端分发观测时序快照：每「频道 × 小时桶」一行（FR-217，见 ADR-049）。
//
// 由后台任务把 client_dist_event（FR-093）+ client_telemetry（FR-094）离线卷积而来，
// 与玩家热路径的写时聚合（client_dist_daily/client_telemetry_daily）解耦——观测富维度交给离线卷积，
// 热路径零新增负担。ChannelID 可为空串（制品端点跨频道共享、明细 channel 可空 → 归入空频道桶；
// 查询「总」时含之）。分布维度以 JSON map 存 TEXT 列，免随版本/平台/滞后基数增长改表结构。
type ClientDistSnapshot struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ChannelID 频道 slug；与 BucketTS 组成唯一键。空串=跨频道/制品共享桶。
	ChannelID string `gorm:"column:channel_id;type:varchar(64);not null;default:'';uniqueIndex:idx_cds_channel_bucket,priority:1" json:"channelId"`
	// BucketTS 小时桶起点（UTC，整小时对齐）。与 ChannelID 唯一；单列索引供区间扫描/TTL 清理。
	BucketTS time.Time `gorm:"not null;uniqueIndex:idx_cds_channel_bucket,priority:2;index:idx_cds_bucket" json:"bucketTs"`

	// 拉取侧（源 client_dist_event）。
	ManifestPulls int64 `gorm:"default:0;not null" json:"manifestPulls"`           // 桶内 manifest 拉取次数
	ArtifactPulls int64 `gorm:"default:0;not null" json:"artifactPulls"`           // 桶内制品拉取次数
	DownloadBytes int64 `gorm:"default:0;not null" json:"downloadBytes"`           // 桶内总响应字节
	CASHit        int64 `gorm:"column:cas_hit;default:0;not null" json:"casHit"`   // 制品 CAS 命中（artifact 且 status=304）
	CASMiss       int64 `gorm:"column:cas_miss;default:0;not null" json:"casMiss"` // 制品 CAS 未命中（artifact 且 status∈{200,206}）
	// ActiveMachines 桶内 machineId 精确去重计数（排空串）。machineId 不可信（ADR-023），仅统计近似；
	// 跨桶不可简单求和（同客户端跨小时会重复），跨区间去重口径见 ADR-049 §4。
	ActiveMachines int64 `gorm:"default:0;not null" json:"activeMachines"`
	// VersionDist 版本分布 JSON map[version]count（manifest 拉取按 version 计数）。
	VersionDist string `gorm:"column:version_dist;type:text" json:"-"`
	// PlatformDist 平台分布 JSON map[os]count（来源遥测 os 字段）。
	PlatformDist string `gorm:"column:platform_dist;type:text" json:"-"`

	// 更新侧（源 client_telemetry，按 result 分桶计数）。
	UpdateTotal      int64 `gorm:"default:0;not null" json:"updateTotal"`
	UpdateSuccess    int64 `gorm:"default:0;not null" json:"updateSuccess"`
	UpdateFailStatic int64 `gorm:"column:update_fail_static;default:0;not null" json:"updateFailStatic"` // 断网兜底启动
	UpdateRolledBack int64 `gorm:"column:update_rolled_back;default:0;not null" json:"updateRolledBack"`
	UpdateError      int64 `gorm:"default:0;not null" json:"updateError"`
	// LagDist 版本滞后分布 JSON map[lag]count（current_version - toVersion，下界 0；遥测 toVersion>0 时计）。
	LagDist string `gorm:"column:lag_dist;type:text" json:"-"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
