package model

import "time"

// ClientTelemetry 客户端遥测明细（FR-094，见 ADR-023、contract §4.3）。
// **短保留 + 滚动清理**（数据量治理）。仅环境粗粒度 + 不可逆机器码，不收集敏感个人数据；隐私可关（客户端 opt-out）。
type ClientTelemetry struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ChannelID   string    `gorm:"column:channel_id;type:varchar(64);index:idx_ct_channel_time" json:"channelId"`
	MachineID   string    `gorm:"column:machine_id;type:varchar(128);index" json:"machineId"`
	IP          string    `gorm:"type:varchar(64)" json:"ip"`
	Result      string    `gorm:"type:varchar(16);not null" json:"result"` // success|fail-static|rolled-back|error
	FromVersion int       `gorm:"default:0;not null" json:"fromVersion"`
	ToVersion   int       `gorm:"default:0;not null" json:"toVersion"`
	OS          string    `gorm:"column:os;type:varchar(32)" json:"os"`
	JavaVersion string    `gorm:"column:java_version;type:varchar(32)" json:"javaVersion"`
	Launcher    string    `gorm:"type:varchar(32)" json:"launcher"`
	DurationMs  int64     `gorm:"column:duration_ms;default:0;not null" json:"durationMs"`
	BootSuccess bool      `gorm:"column:boot_success;default:false;not null" json:"bootSuccess"`
	Error       string    `gorm:"type:varchar(512)" json:"error"`
	CreatedAt   time.Time `gorm:"index:idx_ct_channel_time" json:"createdAt"`
}

// ClientTelemetryDaily 遥测按日聚合（FR-094）。长保留，写时增量；供 FR-095 更新成功率/回退率趋势。
type ClientTelemetryDaily struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Day       string `gorm:"type:char(10);not null;uniqueIndex:idx_ctd_day_chan_result" json:"day"`
	ChannelID string `gorm:"column:channel_id;type:varchar(64);not null;uniqueIndex:idx_ctd_day_chan_result" json:"channelId"`
	Result    string `gorm:"type:varchar(16);not null;uniqueIndex:idx_ctd_day_chan_result" json:"result"`
	Count     int64  `gorm:"default:0;not null" json:"count"`
}
