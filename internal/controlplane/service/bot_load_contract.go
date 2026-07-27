package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	BotLoadScenarioInvalidCode      = "BOT_LOAD_SCENARIO_INVALID"
	BotLoadCapacityChangedCode      = "BOT_LOAD_CAPACITY_CHANGED"
	BotLoadCapacityInsufficientCode = "BOT_LOAD_CAPACITY_INSUFFICIENT"
	BotLoadNodeUnavailableCode      = "BOT_LOAD_NODE_UNAVAILABLE"
	BotLoadProbeRequiredCode        = "BOT_LOAD_PROBE_REQUIRED"

	BotLoadUnavailableNodeOffline     = "NODE_OFFLINE"
	BotLoadUnavailableNodeMaintenance = "NODE_MAINTENANCE"
	BotLoadUnavailableWorkerMissing   = "WORKER_UNREACHABLE"
	BotLoadUnavailableCapacityTimeout = "CAPACITY_RPC_TIMEOUT"
	BotLoadUnavailableCapacityRPC     = "CAPACITY_RPC_FAILED"
	BotLoadUnavailableFleetFeature    = "FLEET_FEATURE_MISSING"
	BotLoadUnavailableLegacyWorker    = "LEGACY_WORKER"
	BotLoadUnavailableAdmission       = "BOT_WORKER_NOT_READY"
	BotLoadUnavailableSnapshotStale   = "CAPACITY_SNAPSHOT_STALE"
)

var (
	// ErrBotLoadCapacityChanged 供 HTTP/start 层稳定映射 409 BOT_LOAD_CAPACITY_CHANGED。
	ErrBotLoadCapacityChanged = errors.New("Bot 负载容量计划已变化")
	// ErrBotLoadCapacityInsufficient 表示即时可用容量已不足，需重新预检。
	ErrBotLoadCapacityInsufficient = errors.New("Bot 负载容量不足")
	// ErrBotLoadNodeUnavailable 表示计划使用的执行节点当前不可用。
	ErrBotLoadNodeUnavailable = errors.New("Bot 负载节点不可用")
	// ErrBotLoadPreflightInvalid 表示纯核心层输入不符合冻结范围。
	ErrBotLoadPreflightInvalid = errors.New("Bot 负载预检参数无效")
)

// BotLoadClock 为缓存、租约和令牌提供可测时钟。
type BotLoadClock interface {
	Now() time.Time
}

type systemBotLoadClock struct{}

func (systemBotLoadClock) Now() time.Time { return time.Now() }

func normalizeBotLoadClock(clock BotLoadClock) BotLoadClock {
	if clock == nil {
		return systemBotLoadClock{}
	}
	return clock
}

// BotLoadNodeCapacity 是 API 冻结的发压节点容量领域 DTO。
type BotLoadNodeCapacity struct {
	NodeID                            uint       `json:"nodeId"`
	NodeUUID                          string     `json:"nodeUuid"`
	NodeName                          string     `json:"nodeName"`
	Online                            bool       `json:"online"`
	TunnelConnected                   bool       `json:"tunnelConnected"`
	BotWorkerReady                    bool       `json:"botWorkerReady"`
	Legacy                            bool       `json:"legacy"`
	MaxBots                           int        `json:"maxBots"`
	ActiveBots                        int        `json:"activeBots"`
	ReservedBots                      int        `json:"reservedBots"`
	AvailableBots                     int        `json:"availableBots"`
	CapacityGeneration                int64      `json:"capacityGeneration"`
	WorkerEpoch                       string     `json:"workerEpoch,omitempty"`
	BotWorkerVersion                  string     `json:"botWorkerVersion,omitempty"`
	RuntimeSource                     string     `json:"runtimeSource,omitempty"`
	RSSBytes                          int64      `json:"rssBytes,omitempty"`
	EventLoopP95MS                    float64    `json:"eventLoopP95Ms,omitempty"`
	WorkerProcessRSSBytes             *int64     `json:"workerProcessRssBytes,omitempty"`
	WorkerProcessRSSUnavailableReason string     `json:"workerProcessRssUnavailableReason,omitempty"`
	LastHeartbeatAt                   *time.Time `json:"lastHeartbeatAt,omitempty"`
	UnavailableReason                 string     `json:"unavailableReason,omitempty"`
}

// BotLoadAllocation 是 API 冻结的单批分片领域 DTO。
type BotLoadAllocation struct {
	BatchID           string    `json:"batchId"`
	Ordinal           int       `json:"ordinal"`
	ExecutorNodeID    uint      `json:"executorNodeId"`
	ExecutorNodeUUID  string    `json:"executorNodeUuid"`
	ExecutorNodeName  string    `json:"executorNodeName"`
	PlannedCount      int       `json:"plannedCount"`
	ConnectStartAt    time.Time `json:"connectStartAt"`
	ConnectIntervalMS int       `json:"connectIntervalMs"`
	IdempotencyKey    string    `json:"idempotencyKey"`
}

// BotLoadProbeStatus 是预检结果中的目标实例探针状态。
type BotLoadProbeStatus struct {
	Required     bool   `json:"required"`
	Connected    bool   `json:"connected"`
	InstanceID   uint   `json:"instanceId"`
	InstanceUUID string `json:"instanceUuid"`
	Message      string `json:"message,omitempty"`
}

// BotLoadIssue 是预检 warning/blocker 的冻结结构。
type BotLoadIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  *uint  `json:"nodeId,omitempty"`
}

// BotLoadPreflightResult 是 API 冻结的预检领域 DTO。
type BotLoadPreflightResult struct {
	RunID                    uint                  `json:"runId"`
	RunUUID                  string                `json:"runUuid"`
	Ready                    bool                  `json:"ready"`
	PlanToken                string                `json:"planToken,omitempty"`
	ExpiresAt                *time.Time            `json:"expiresAt,omitempty"`
	TargetBots               int                   `json:"targetBots"`
	TotalAvailable           int                   `json:"totalAvailable"`
	Allocations              []BotLoadAllocation   `json:"allocations"`
	NodeCapacities           []BotLoadNodeCapacity `json:"nodeCapacities"`
	Probe                    BotLoadProbeStatus    `json:"probe"`
	EstimatedDurationSeconds int                   `json:"estimatedDurationSeconds"`
	Warnings                 []BotLoadIssue        `json:"warnings"`
	Blockers                 []BotLoadIssue        `json:"blockers"`
}

// BotLoadWorkerCapacity 是 GetBotCapacity 的 CP 内部快照，避免把 proto 泄漏到算法层。
type BotLoadWorkerCapacity struct {
	Ready                             bool
	Legacy                            bool
	MaxBots                           int
	ActiveBots                        int
	ConnectingBots                    int
	CapacityGeneration                int64
	WorkerEpoch                       string
	WorkerEpochGeneration             int64
	BotWorkerVersion                  string
	RSSBytes                          int64
	EventLoopP95MS                    float64
	WorkerProcessRSSBytes             *int64
	WorkerProcessRSSUnavailableReason string
	ObservedAt                        time.Time
	UnavailableReason                 string
	Features                          []string
}

// BotLoadCapacitySnapshot 是容量目录输出及预留原子校验所需的内部目录快照。
type BotLoadCapacitySnapshot struct {
	NodeCapacities    []BotLoadNodeCapacity `json:"nodeCapacities"`
	ReservationLimits map[uint]int          `json:"-"`
	UpdatedAt         time.Time             `json:"updatedAt"`
}

// BotLoadNodeGeneration 记录计划实际使用节点的容量语义世代。
type BotLoadNodeGeneration struct {
	NodeID             uint  `json:"nodeId"`
	CapacityGeneration int64 `json:"capacityGeneration"`
}

// BotLoadAllocationPlan 是服务端保存到会话的计划正文，客户端不参与回传或校验。
type BotLoadAllocationPlan struct {
	RunID               uint                    `json:"runId"`
	RunUUID             string                  `json:"runUuid"`
	TargetBots          int                     `json:"targetBots"`
	Allocations         []BotLoadAllocation     `json:"allocations"`
	CapacityGenerations []BotLoadNodeGeneration `json:"capacityGenerations"`
}

// DecodeBotLoadAllocationPlan 解码服务端保存的计划正文。
func DecodeBotLoadAllocationPlan(raw string) (*BotLoadAllocationPlan, error) {
	var plan BotLoadAllocationPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, fmt.Errorf("解析 Bot 负载分片计划失败: %w", err)
	}
	if plan.RunID == 0 || plan.TargetBots < 1 || len(plan.Allocations) == 0 {
		return nil, fmt.Errorf("%w: 保存的分片计划不完整", ErrBotLoadPreflightInvalid)
	}
	return &plan, nil
}

// BotLoadCapacityChangedError 携带稳定错误码和不泄漏计划正文的判别原因。
type BotLoadCapacityChangedError struct {
	Code   string
	Reason string
}

func (e *BotLoadCapacityChangedError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrBotLoadCapacityChanged.Error()
	}
	return e.Reason
}

func (e *BotLoadCapacityChangedError) Unwrap() error { return ErrBotLoadCapacityChanged }

func newBotLoadCapacityChanged(reason string) error {
	return &BotLoadCapacityChangedError{Code: BotLoadCapacityChangedCode, Reason: reason}
}

func cloneBotLoadCounts(source map[uint]int) map[uint]int {
	out := make(map[uint]int, len(source))
	for nodeID, count := range source {
		out[nodeID] = count
	}
	return out
}
