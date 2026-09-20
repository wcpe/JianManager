package mcp

import "time"

// Config MCP 会话与并发限制（FR-389）。
// 全部有合理默认；可经 control-plane.yml 的 mcp 段或环境变量覆盖。
type Config struct {
	// IdleTimeout 空闲超时：自 lastActivityAt 起无 tool/消息活动超过该时长则踢会话。
	IdleTimeout time.Duration
	// AbsoluteTimeout 绝对超时：自 connectedAt 起总存活上限。
	AbsoluteTimeout time.Duration
	// MaxGlobalSessions 全局并发会话上限；<=0 表示不限制（默认不限制）。
	//
	// 默认不限制的原因：会话生命周期已由空闲/绝对超时兜底（30m/24h），
	// 而并发上限在「客户端重连不主动关旧会话」时会把配额耗尽、
	// 之后连 initialize 都建不了新会话（真机事故：连接方反复重连累积到上限后永久 429，
	// 只能靠重启控制面清空内存会话恢复）。需要限流的环境可显式设正值。
	MaxGlobalSessions int
	// MaxSessionsPerToken 同一 Token 并发会话上限；<=0 表示不限制（默认不限制）。
	MaxSessionsPerToken int
}

// DefaultConfig 返回规格建议默认值：空闲 30m、绝对 24h、并发不限制。
func DefaultConfig() Config {
	return Config{
		IdleTimeout:         30 * time.Minute,
		AbsoluteTimeout:     24 * time.Hour,
		MaxGlobalSessions:   0, // 不限制
		MaxSessionsPerToken: 0, // 不限制
	}
}

// Normalize 将零值/非法值回落到默认。
//
// 并发上限的语义与超时不同：<=0 即「不限制」，是合法且默认的取值，
// 故此处不回落到任何正数默认（回落会让「不限制」无法表达）。
func (c Config) Normalize() Config {
	d := DefaultConfig()
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = d.IdleTimeout
	}
	if c.AbsoluteTimeout <= 0 {
		c.AbsoluteTimeout = d.AbsoluteTimeout
	}
	return c
}
