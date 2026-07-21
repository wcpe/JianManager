package model

import "time"

// ClientTelemetry 客户端遥测明细（FR-094 / FR-360，见 ADR-023、contract §4.3）。
// **短保留 + 滚动清理**（数据量治理）。仅环境粗粒度 + 不可逆机器码；隐私可关（客户端 opt-out）。
// FR-360 增列：core_version/arch（required）与 java_vendor/locale/timezone/memory_tier（diagnostic）。
type ClientTelemetry struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	ChannelID   string    `gorm:"column:channel_id;type:varchar(64);index:idx_ct_channel_time" json:"channelId"`
	MachineID   string    `gorm:"column:machine_id;type:varchar(128);index" json:"machineId"`
	PlayerName  string    `gorm:"column:player_name;type:varchar(32);index" json:"playerName"`
	IP          string    `gorm:"type:varchar(64)" json:"ip"`
	Result      string    `gorm:"type:varchar(16);not null" json:"result"` // success|fail-static|rolled-back|error
	FromVersion int       `gorm:"default:0;not null" json:"fromVersion"`
	ToVersion   int       `gorm:"default:0;not null" json:"toVersion"`
	CoreVersion string    `gorm:"column:core_version;type:varchar(64)" json:"coreVersion"`
	OS          string    `gorm:"column:os;type:varchar(32)" json:"os"`
	Arch        string    `gorm:"column:arch;type:varchar(16)" json:"arch"`
	JavaVersion string    `gorm:"column:java_version;type:varchar(32)" json:"javaVersion"`
	JavaVendor  string    `gorm:"column:java_vendor;type:varchar(64)" json:"javaVendor"`
	Launcher    string    `gorm:"type:varchar(32)" json:"launcher"`
	Locale      string    `gorm:"column:locale;type:varchar(32)" json:"locale"`
	Timezone    string    `gorm:"column:timezone;type:varchar(64)" json:"timezone"`
	MemoryTier  string    `gorm:"column:memory_tier;type:varchar(16)" json:"memoryTier"`
	DurationMs  int64     `gorm:"column:duration_ms;default:0;not null" json:"durationMs"`
	BootSuccess bool      `gorm:"column:boot_success;default:false;not null" json:"bootSuccess"`
	Error       string    `gorm:"type:varchar(512)" json:"error"`
	CreatedAt   time.Time `gorm:"index:idx_ct_channel_time" json:"createdAt"`
}

// ClientTelemetryDaily 遥测按日聚合（FR-094）。长保留，写时增量；供 FR-095 更新成功率/回退率趋势。
// 不按 FR-360 新维度打爆基数，仍仅 day×channel×result。
type ClientTelemetryDaily struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Day       string `gorm:"type:char(10);not null;uniqueIndex:idx_ctd_day_chan_result" json:"day"`
	ChannelID string `gorm:"column:channel_id;type:varchar(64);not null;uniqueIndex:idx_ctd_day_chan_result" json:"channelId"`
	Result    string `gorm:"type:varchar(16);not null;uniqueIndex:idx_ctd_day_chan_result" json:"result"`
	Count     int64  `gorm:"default:0;not null" json:"count"`
}
