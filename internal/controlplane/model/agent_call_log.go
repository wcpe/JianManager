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
	// varchar(64) 计**字符**：MCP 路径对未知工具名会拼出客户端可控的 "mcp.tool."+toolName，
	// 写入口按 rune 边界截到 64 字符（service.truncateRunes），不得改成按字节——按字节会把多字节
	// 字符切成非法 UTF-8，MySQL 严格模式仍整行拒收，流水静默丢失。
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
	//
	// 此处**刻意不加** not null/default：写路径（service.Record）已用 Select 强制落 0/1，
	// 加约束只对「手工 SQL 写入的 NULL 行」有意义，而代价落在既有库升级上——GORM 的
	// MigrateColumn 判定 default 由「无」变「有」时会调 AlterColumn，而 sqlite 驱动的
	// AlterColumn 是重建整表（recreateTable），即每次 CP 启动都要把本表复制一遍；更糟的是
	// 库里只要存在一行 success IS NULL，重建时的 INSERT ... SELECT 就会报 NOT NULL constraint
	// failed，AutoMigrate 直接返回 error 导致 CP 起不来，且重试同样失败，须人工清数据才能恢复。
	// 「NULL 不得被计成成功」这一审计不变量改由聚合 SQL 承担（service.ActivityByToken）。
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
