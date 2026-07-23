package mcp

import "time"

// Config MCP 会话与并发限制（FR-389）。
// 全部有合理默认；可经 control-plane.yml 的 mcp 段或环境变量覆盖。
type Config struct {
	// IdleTimeout 空闲超时：自 lastActivityAt 起无 tool/消息活动超过该时长则踢会话。
	IdleTimeout time.Duration
	// AbsoluteTimeout 绝对超时：自 connectedAt 起总存活上限。
	AbsoluteTimeout time.Duration
	// MaxGlobalSessions 全局并发会话上限。
	MaxGlobalSessions int
	// MaxSessionsPerToken 同一 Token 并发会话上限。
	MaxSessionsPerToken int
}

// DefaultConfig 返回规格建议默认值：空闲 30m、绝对 24h、全局 32、每 Token 4。
func DefaultConfig() Config {
	return Config{
		IdleTimeout:         30 * time.Minute,
		AbsoluteTimeout:     24 * time.Hour,
		MaxGlobalSessions:   32,
		MaxSessionsPerToken: 4,
	}
}

// Normalize 将零值/非法值回落到默认。
func (c Config) Normalize() Config {
	d := DefaultConfig()
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = d.IdleTimeout
	}
	if c.AbsoluteTimeout <= 0 {
		c.AbsoluteTimeout = d.AbsoluteTimeout
	}
	if c.MaxGlobalSessions <= 0 {
		c.MaxGlobalSessions = d.MaxGlobalSessions
	}
	if c.MaxSessionsPerToken <= 0 {
		c.MaxSessionsPerToken = d.MaxSessionsPerToken
	}
	return c
}
