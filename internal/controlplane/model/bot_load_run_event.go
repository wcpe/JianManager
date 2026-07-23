package model

import "time"

// BotLoadRunEventType 是 append-only 运行事件类型。
type BotLoadRunEventType string

const (
	BotLoadRunEventRunState       BotLoadRunEventType = "run-state"
	BotLoadRunEventStage          BotLoadRunEventType = "stage"
	BotLoadRunEventBarrier        BotLoadRunEventType = "barrier"
	BotLoadRunEventScenarioAction BotLoadRunEventType = "scenario-action"
	BotLoadRunEventCommandSchedule BotLoadRunEventType = "command-schedule"
	BotLoadRunEventCommandSend    BotLoadRunEventType = "command-send"
	BotLoadRunEventWorkerHealth   BotLoadRunEventType = "worker-health"
	BotLoadRunEventExecutorCrash  BotLoadRunEventType = "executor-crash"
	BotLoadRunEventSafetyStop     BotLoadRunEventType = "safety-stop"
	BotLoadRunEventReportReady    BotLoadRunEventType = "report-ready"
)

// BotLoadRunEvent 是 FR-370 运行历史事件（append-only，不随 metric TTL 删除）。
type BotLoadRunEvent struct {
	ID              uint               `gorm:"primaryKey" json:"id"`
	StressSessionID uint               `gorm:"not null;index:idx_bot_load_run_event_session_id,priority:1;index:idx_bot_load_run_event_type,priority:1;index:idx_bot_load_run_event_time,priority:1" json:"stressSessionId"`
	RunUUID         string             `gorm:"type:char(36);not null" json:"runUuid"`
	Type            BotLoadRunEventType `gorm:"type:varchar(32);not null;index:idx_bot_load_run_event_type,priority:2" json:"type"`
	OccurredAt      time.Time          `gorm:"not null;index:idx_bot_load_run_event_time,priority:2" json:"occurredAt"`
	StageIndex      *int               `json:"stageIndex,omitempty"`
	ActionRunID     *string            `gorm:"type:char(36);index:idx_bot_load_run_event_action,priority:2" json:"actionRunId,omitempty"`
	BotUUID         *string            `gorm:"type:char(36);index:idx_bot_load_run_event_bot,priority:2" json:"botUuid,omitempty"`
	ExecutorNodeID  *uint              `gorm:"index:idx_bot_load_run_event_executor,priority:2" json:"executorNodeId,omitempty"`
	StepID          *string            `gorm:"type:varchar(64);index:idx_bot_load_run_event_step,priority:2" json:"stepId,omitempty"`
	PayloadJSON     string             `gorm:"type:longtext;not null" json:"payloadJson"`
	LegacyJSON      *string            `gorm:"type:longtext" json:"legacyJson,omitempty"`
	CreatedAt       time.Time          `json:"createdAt"`

	StressSession BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
}

// TableName 固定表名。
func (BotLoadRunEvent) TableName() string { return "bot_load_run_events" }
