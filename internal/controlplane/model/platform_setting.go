package model

import "time"

// PlatformSetting 平台配置的 DB 覆盖项（FR-063 / ADR-015）。
//
// 只存「被显式覆盖过的白名单键」：未覆盖的键不落库，沿用 env/YAML 基线。
// 生效优先级 DB 覆盖 > 环境变量 > YAML 默认，由 SettingsService 解析有效配置时叠加。
type PlatformSetting struct {
	// Key 配置键，与 SettingsService 白名单一致（如 log.level、graceful_stop.timeout）。
	Key string `gorm:"type:varchar(128);primaryKey" json:"key"`
	// Value 覆盖值的文本表示（按键各自的语义解析，如 duration 文本、整数文本）。
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}
