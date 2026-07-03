package model

import "time"

// ClientRuntimeState 客户端运行态最新快照（FR-265）。
// 按 channel_id + machine_id upsert，仅保存最新启动心跳；不作为可信身份，仅用于观测统计。
type ClientRuntimeState struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	ChannelID        string     `gorm:"column:channel_id;type:varchar(64);not null;uniqueIndex:idx_client_runtime_identity;index" json:"channelId"`
	MachineID        string     `gorm:"column:machine_id;type:varchar(128);not null;uniqueIndex:idx_client_runtime_identity;index" json:"machineId"`
	PlayerName       string     `gorm:"column:player_name;type:varchar(32);index" json:"playerName"`
	IP               string     `gorm:"type:varchar(64);index" json:"ip"`
	Platform         string     `gorm:"type:varchar(32);index" json:"platform"`
	JavaVersion      string     `gorm:"column:java_version;type:varchar(32)" json:"javaVersion"`
	Launcher         string     `gorm:"type:varchar(32);index" json:"launcher"`
	CoreVersion      string     `gorm:"column:core_version;type:varchar(64);index" json:"coreVersion"`
	LocalVersion     int        `gorm:"column:local_version;default:0;not null;index" json:"localVersion"`
	FirstSeenAt      time.Time  `gorm:"column:first_seen_at;index" json:"firstSeenAt"`
	LastHeartbeatAt  time.Time  `gorm:"column:last_heartbeat_at;index" json:"lastHeartbeatAt"`
	LastUpdateResult string     `gorm:"column:last_update_result;type:varchar(16);index" json:"lastUpdateResult"`
	LastUpdateAt     *time.Time `gorm:"column:last_update_at;index" json:"lastUpdateAt"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}
