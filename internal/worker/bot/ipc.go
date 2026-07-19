/**
 * Go 侧 Bot 管理层 IPC 类型定义。
 * 与 apps/bot-worker/src/ipc/types.ts 保持同步。
 */

package bot

import "encoding/json"

// IpcCommand Worker Node → Bot Worker 的命令。
type IpcCommand struct {
	Cmd       string `json:"cmd"`
	RequestID string `json:"requestId,omitempty"`
}

// CreateBotsCommand 批量创建 Bot 命令。
type CreateBotsCommand struct {
	Cmd            string      `json:"cmd"`
	RequestID      string      `json:"requestId,omitempty"`
	BatchID        string      `json:"batchId,omitempty"`
	IdempotencyKey string      `json:"idempotencyKey,omitempty"`
	Bots           []BotConfig `json:"bots"`
}

// StopBotsCommand 批量停止 Bot 命令。
type StopBotsCommand struct {
	Cmd        string   `json:"cmd"`
	RequestID  string   `json:"requestId,omitempty"`
	BotIds     []string `json:"botIds"`
	Generation int64    `json:"generation,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// SignalActionsCommand 批量投递外部动作信号。
type SignalActionsCommand struct {
	Cmd       string         `json:"cmd"`
	RequestID string         `json:"requestId"`
	Signals   []ActionSignal `json:"signals"`
}

// GetFleetSnapshotCommand 请求当前 Bot fleet 快照。
type GetFleetSnapshotCommand struct {
	Cmd       string `json:"cmd"`
	RequestID string `json:"requestId"`
}

// SetBehaviorCommand 切换行为模式命令。
type SetBehaviorCommand struct {
	Cmd      string      `json:"cmd"`
	BotID    string      `json:"botId"`
	Behavior string      `json:"behavior"`
	Target   string      `json:"target,omitempty"`
	Config   interface{} `json:"config,omitempty"`
}

// SendBotCommand 向 Bot 发送命令。
type SendBotCommand struct {
	Cmd     string `json:"cmd"`
	BotID   string `json:"botId"`
	Command string `json:"command"`
}

// RunScriptCommand 执行脚本命令。
type RunScriptCommand struct {
	Cmd      string       `json:"cmd"`
	ScriptID string       `json:"scriptId"`
	Steps    []ScriptStep `json:"steps"`
	BotIds   []string     `json:"botIds"`
}

// StopScriptCommand 停止脚本命令。
type StopScriptCommand struct {
	Cmd      string `json:"cmd"`
	ScriptID string `json:"scriptId"`
}

// BotConfig Bot 配置（下发给 Bot Worker）。
type BotConfig struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Host             string          `json:"host"`
	Port             int             `json:"port"`
	Username         string          `json:"username,omitempty"`
	Version          string          `json:"version,omitempty"`
	Auth             string          `json:"auth,omitempty"`
	Behavior         string          `json:"behavior,omitempty"`
	BehaviorConfig   interface{}     `json:"behaviorConfig,omitempty"`
	Server           string          `json:"server,omitempty"`
	SessionID        string          `json:"sessionId,omitempty"`
	Generation       int64           `json:"generation,omitempty"`
	ConfigHash       string          `json:"configHash,omitempty"`
	CohortKey        string          `json:"cohortKey,omitempty"`
	Scenario         json.RawMessage `json:"scenario,omitempty"`
	ResumeStepID     string          `json:"resumeStepId,omitempty"`
	ConnectNotBefore int64           `json:"connectNotBefore,omitempty"`
	CorrelationSeed  string          `json:"correlationSeed,omitempty"`
}

// ActionSignal 外部 probe/barrier/cancel 动作信号。
type ActionSignal struct {
	SignalID         string          `json:"signalId"`
	BotID            string          `json:"botId"`
	SessionID        string          `json:"sessionId"`
	Generation       int64           `json:"generation"`
	ActionRunID      string          `json:"actionRunId"`
	StepID           string          `json:"stepId"`
	Type             string          `json:"type"`
	CorrelationToken string          `json:"correlationToken,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ObservedAt       int64           `json:"observedAt,omitempty"`
}

// BotItemResult create-bots/stop-bots 的逐 Bot 回执。
type BotItemResult struct {
	BotID     string `json:"botId"`
	Accepted  bool   `json:"accepted"`
	Skipped   bool   `json:"skipped"`
	Status    string `json:"status,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SignalItemResult signal-actions 的逐信号回执。
type SignalItemResult struct {
	SignalID  string `json:"signalId"`
	Accepted  bool   `json:"accepted"`
	Skipped   bool   `json:"skipped"`
	Status    string `json:"status,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ScriptStep 脚本步骤。
type ScriptStep struct {
	Action   string `json:"action"`
	Message  string `json:"message,omitempty"`
	Pos      *Vec3  `json:"pos,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Command  string `json:"command,omitempty"`
	Text     string `json:"text,omitempty"`
}

// Vec3 坐标。
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}
