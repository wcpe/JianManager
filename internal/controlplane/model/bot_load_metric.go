package model

import "time"

// BotLoadMetricSample 是 FR-370 每运行 5 秒一行的聚合指标样本。
type BotLoadMetricSample struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	StressSessionID uint      `gorm:"not null;uniqueIndex:uniq_bot_load_metric_sample,priority:1;index:idx_bot_load_metric_stage,priority:1" json:"stressSessionId"`
	SampledAt       time.Time `gorm:"not null;uniqueIndex:uniq_bot_load_metric_sample,priority:2" json:"sampledAt"`
	StageIndex      int       `gorm:"not null;index:idx_bot_load_metric_stage,priority:2" json:"stageIndex"`
	CountsJSON      string    `gorm:"type:longtext;not null" json:"countsJson"`
	CommandJSON     string    `gorm:"type:longtext;not null" json:"commandJson"`
	BarrierJSON     string    `gorm:"type:longtext;not null" json:"barrierJson"`
	ExecutorJSON    string    `gorm:"type:longtext;not null" json:"executorJson"`
	LatencyJSON     string    `gorm:"type:longtext;not null" json:"latencyJson"`
	ErrorsJSON      string    `gorm:"type:longtext;not null" json:"errorsJson"`
	// TargetLegacyJSON 可选 legacy 指标；缺失时 null，不写 0。
	TargetLegacyJSON *string   `gorm:"type:longtext" json:"targetLegacyJson,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`

	StressSession BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
}

// TableName 固定表名。
func (BotLoadMetricSample) TableName() string { return "bot_load_metric_samples" }
