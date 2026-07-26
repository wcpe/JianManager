package model

import (
	"time"
)

// AgentCallLog Agent 调用流水（FR-390 / FR-395，见 ADR-076/080）。
// 覆盖 /api/v1/agent/* Ops 读+写与 MCP tool；与人类 audit 表独立。
// token_name 为签发时名称快照，吊销后仍可读；error 截断短文，禁 Token 明文。
type AgentCallLog struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TokenID 鉴权成功后的 agent token 主键。
	TokenID uint `gorm:"not null;index:idx_agent_call_token_created,priority:1;index" json:"tokenId"`
	// TokenName 冗余快照（吊销后仍可读）。
	TokenName string `gorm:"type:varchar(128);not null" json:"tokenName"`
	// Action 与 service.AgentAction* 或 MCP 事件对齐（如 agent.whoami）。
	Action string `gorm:"type:varchar(64);not null;index:idx_agent_call_action_created,priority:1" json:"action"`
	// Capability 本次授权实际使用的能力标签（V2 capability 或 V1 legacy.*）；会话事件可空。
	Capability string `gorm:"type:varchar(64)" json:"capability,omitempty"`
	// Client 调用方：mcp | jmagent | curl | unknown（优先 X-JM-Agent-Client）。
	Client string `gorm:"type:varchar(32);not null;default:unknown" json:"client"`
	// Transport 可选：streamable_http | sse | http | 空。
	Transport string `gorm:"type:varchar(32)" json:"transport,omitempty"`
	// TargetType 目标类型（instance/node 等，可空）。
	TargetType string `gorm:"type:varchar(32)" json:"targetType,omitempty"`
	// TargetID 目标 ID 字符串（可空）。
	TargetID string `gorm:"type:varchar(64)" json:"targetId,omitempty"`
	// Success 是否成功（策略 403 记 false；业务失败亦 false）。
	Success bool `json:"success"`
	// Error 失败时截断短文（禁 Token 明文）。
	Error string `gorm:"type:varchar(512)" json:"error,omitempty"`
	// LatencyMs 处理耗时毫秒。
	LatencyMs uint `json:"latencyMs"`
	// IP 客户端 IP。
	IP string `gorm:"type:varchar(64)" json:"ip"`
	// CreatedAt 写入时间；复合索引供按 token/时间、action/时间查询。
	CreatedAt time.Time `gorm:"index:idx_agent_call_token_created,priority:2;index:idx_agent_call_action_created,priority:2;index" json:"createdAt"`
}
