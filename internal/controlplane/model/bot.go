package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BotStatus Bot 状态。
type BotStatus string

const (
	BotStatusPending      BotStatus = "pending"
	BotStatusConnecting   BotStatus = "connecting"
	BotStatusConnected    BotStatus = "connected"
	BotStatusDisconnected BotStatus = "disconnected"
	BotStatusError        BotStatus = "error"
	BotStatusStopped      BotStatus = "stopped"
)

// Bot Mineflayer Bot。
type Bot struct {
	ID                     uint       `gorm:"primaryKey" json:"id"`
	UUID                   string     `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID             uint       `gorm:"not null;index" json:"instanceId"`
	StressSessionID        *uint      `gorm:"index" json:"stressSessionId,omitempty"`
	ExecutorNodeID         *uint      `gorm:"index;index:idx_bots_executor_generation,priority:1" json:"executorNodeId,omitempty"`
	LoadBatchID            *uint      `gorm:"index" json:"loadBatchId,omitempty"`
	Name                   string     `gorm:"type:varchar(128);not null" json:"name"`
	Status                 BotStatus  `gorm:"type:varchar(32);default:pending" json:"status"`
	WorkerEpoch            string     `gorm:"type:varchar(36);not null;default:''" json:"workerEpoch"`
	WorkerEpochGeneration  int64      `gorm:"not null;default:0" json:"workerEpochGeneration"`
	LastEventSeq           int64      `gorm:"not null;default:0" json:"lastEventSeq"`
	LastSeenAt             *time.Time `json:"lastSeenAt,omitempty"`
	ConnectedAt            *time.Time `json:"connectedAt,omitempty"`
	DesiredStateGeneration int64      `gorm:"not null;default:1;index:idx_bots_executor_generation,priority:2" json:"desiredStateGeneration"`
	ConfigHash             string     `gorm:"type:char(64);not null;default:''" json:"configHash"`
	CohortKey              string     `gorm:"type:varchar(64);not null;default:''" json:"cohortKey"`
	// LastError 最近一次委托 Worker 失败的原因（如 bot-worker 依赖未装、节点未连）。
	// 委托成功即清空；status=error 时前端据此显示可操作指引，杜绝「创建 201 但永远 pending 零反馈」。
	LastError string         `gorm:"type:text" json:"lastError,omitempty"`
	Config    string         `gorm:"type:text" json:"config"` // JSON: server, port, auth
	Behavior  string         `gorm:"type:varchar(64)" json:"behavior"`
	WorkerID  string         `gorm:"type:varchar(128)" json:"workerId"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Instance 表示被测目标实例和权限归属，不代表 Bot 实际执行位置。
	Instance Instance `gorm:"foreignKey:InstanceID" json:"-"`
	// ExecutorNode 是实际运行 Bot Worker 的节点；为空时兼容回退 Instance.NodeID。
	ExecutorNode *Node `gorm:"foreignKey:ExecutorNodeID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"executorNode,omitempty"`
	// LoadBatch 是分布式运行所属批次；删除批次时保留 Bot 并清空关联。
	LoadBatch *BotLoadBatch `gorm:"foreignKey:LoadBatchID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
	// StressSession 所属压测会话；删除会话时保留 Bot，并清空关联。
	StressSession *BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL" json:"-"`
}

// BeforeCreate 创建前自动生成 UUID。
func (b *Bot) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}

// BotStressSessionStatus 压测会话状态。
type BotStressSessionStatus string

const (
	BotStressSessionPending BotStressSessionStatus = "pending"
	BotStressSessionRunning BotStressSessionStatus = "running"
	BotStressSessionStopped BotStressSessionStatus = "stopped"
	BotStressSessionError   BotStressSessionStatus = "error"
)

// BotStressSession Bot 压测会话。
type BotStressSession struct {
	ID                   uint                   `gorm:"primaryKey" json:"id"`
	UUID                 string                 `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	InstanceID           uint                   `gorm:"not null;index" json:"instanceId"`
	Name                 string                 `gorm:"type:varchar(128);not null" json:"name"`
	NamePrefix           string                 `gorm:"type:varchar(64);not null" json:"namePrefix"`
	Status               BotStressSessionStatus `gorm:"type:varchar(32);default:pending;index" json:"status"`
	BotCount             int                    `gorm:"not null" json:"botCount"`
	Behavior             string                 `gorm:"type:varchar(64)" json:"behavior"`
	Config               string                 `gorm:"type:text" json:"config"`
	OrchestrationYAML    string                 `gorm:"type:text" json:"orchestrationYaml,omitempty"`
	OrchestrationSummary string                 `gorm:"type:text" json:"orchestrationSummary,omitempty"`
	ScenarioSnapshot     string                 `gorm:"type:longtext" json:"scenarioSnapshot,omitempty"`
	AllocationPlan       string                 `gorm:"type:text" json:"allocationPlan,omitempty"`
	Succeeded            int                    `gorm:"default:0" json:"succeeded"`
	Failed               int                    `gorm:"default:0" json:"failed"`
	LastError            string                 `gorm:"type:text" json:"lastError,omitempty"`
	StartedAt            *time.Time             `json:"startedAt,omitempty"`
	EndedAt              *time.Time             `json:"endedAt,omitempty"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
	DeletedAt            gorm.DeletedAt         `gorm:"index" json:"-"`

	Instance Instance `gorm:"foreignKey:InstanceID" json:"instance,omitempty"`
}

// BeforeCreate 创建前自动生成 UUID。
func (s *BotStressSession) BeforeCreate(tx *gorm.DB) error {
	if s.UUID == "" {
		s.UUID = uuid.New().String()
	}
	return nil
}

// BotLoadBatchState 分布式 Bot 批次状态。
type BotLoadBatchState string

const (
	BotLoadBatchPlanned     BotLoadBatchState = "planned"
	BotLoadBatchDispatching BotLoadBatchState = "dispatching"
	BotLoadBatchRunning     BotLoadBatchState = "running"
	BotLoadBatchStopped     BotLoadBatchState = "stopped"
	BotLoadBatchFailed      BotLoadBatchState = "failed"
)

// BotLoadBatch 记录一次压测运行在单个执行节点上的确定性分片。
type BotLoadBatch struct {
	ID                uint              `gorm:"primaryKey" json:"id"`
	UUID              string            `gorm:"type:char(36);uniqueIndex;not null" json:"uuid"`
	StressSessionID   uint              `gorm:"not null;index;uniqueIndex:uniq_bot_load_batch_ordinal" json:"stressSessionId"`
	ExecutorNodeID    uint              `gorm:"not null;index" json:"executorNodeId"`
	Ordinal           int               `gorm:"not null;uniqueIndex:uniq_bot_load_batch_ordinal" json:"ordinal"`
	PlannedCount      int               `gorm:"not null" json:"plannedCount"`
	AcceptedCount     int               `gorm:"default:0" json:"acceptedCount"`
	ConnectedCount    int               `gorm:"default:0" json:"connectedCount"`
	FailedCount       int               `gorm:"default:0" json:"failedCount"`
	State             BotLoadBatchState `gorm:"type:varchar(16);default:planned;index" json:"state"`
	IdempotencyKey    string            `gorm:"type:varchar(128);uniqueIndex;not null" json:"idempotencyKey"`
	ConnectStartAt    time.Time         `json:"connectStartAt"`
	ConnectIntervalMS int               `gorm:"not null" json:"connectIntervalMs"`
	LastError         string            `gorm:"type:text" json:"lastError,omitempty"`
	StartedAt         *time.Time        `json:"startedAt,omitempty"`
	EndedAt           *time.Time        `json:"endedAt,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`

	StressSession BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ExecutorNode  Node             `gorm:"foreignKey:ExecutorNodeID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"executorNode,omitempty"`
}

// BeforeCreate 创建前自动生成批次 UUID。
func (b *BotLoadBatch) BeforeCreate(tx *gorm.DB) error {
	if b.UUID == "" {
		b.UUID = uuid.New().String()
	}
	return nil
}

// BotLoadActionResultStatus 是场景动作结果状态。
type BotLoadActionResultStatus string

const (
	BotLoadActionRunning   BotLoadActionResultStatus = "running"
	BotLoadActionSucceeded BotLoadActionResultStatus = "succeeded"
	BotLoadActionFailed    BotLoadActionResultStatus = "failed"
	BotLoadActionTimedOut  BotLoadActionResultStatus = "timed_out"
	BotLoadActionCancelled BotLoadActionResultStatus = "cancelled"
)

// BotLoadActionResult 持久化场景动作开始与首个终态。
type BotLoadActionResult struct {
	ID               uint                      `gorm:"primaryKey" json:"id"`
	StressSessionID  uint                      `gorm:"not null;index" json:"stressSessionId"`
	BotID            uint                      `gorm:"not null;index" json:"botId"`
	CohortKey        string                    `gorm:"type:varchar(64);not null" json:"cohortKey"`
	StepID           string                    `gorm:"type:varchar(64);not null" json:"stepId"`
	ActionRunID      string                    `gorm:"type:char(36);not null;uniqueIndex" json:"actionRunId"`
	Attempt          int                       `gorm:"not null" json:"attempt"`
	Status           BotLoadActionResultStatus `gorm:"type:varchar(16);not null;index" json:"status"`
	ErrorCode        string                    `gorm:"type:varchar(64)" json:"errorCode,omitempty"`
	Message          string                    `gorm:"type:text" json:"message,omitempty"`
	DurationMS       int64                     `gorm:"not null;default:0" json:"durationMs"`
	CorrelationToken string                    `gorm:"type:varchar(128);index" json:"correlationToken,omitempty"`
	StartedAt        time.Time                 `gorm:"not null" json:"startedAt"`
	EndedAt          *time.Time                `json:"endedAt,omitempty"`
	ResultJSON       string                    `gorm:"type:longtext" json:"resultJson,omitempty"`
	CreatedAt        time.Time                 `json:"createdAt"`
	UpdatedAt        time.Time                 `json:"updatedAt"`

	StressSession BotStressSession `gorm:"foreignKey:StressSessionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	Bot           Bot              `gorm:"foreignKey:BotID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
}
