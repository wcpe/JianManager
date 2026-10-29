// Package ai 是 AI 助手的模型接入层（见 ADR-105）。
//
// 只做一件事：把不同供应商的流式响应归一化成一组内部事件，让上层（运行循环、配置测试）
// 不必关心具体协议。本包**不执行业务工具**——工具调用的参数只解析、校验完整性并上报，
// 由运行循环决定是否/如何执行。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EventKind 归一化流事件类型。
type EventKind string

const (
	// EventText 文本增量。
	EventText EventKind = "text_delta"
	// EventToolCallStart 一个工具调用的开始（携带稳定 callId 与工具名）。
	EventToolCallStart EventKind = "tool_call_start"
	// EventToolArgsDelta 工具参数片段（未保证可解析，需累积到 End 后整体校验）。
	EventToolArgsDelta EventKind = "tool_args_delta"
	// EventToolCallEnd 该工具参数已接收完毕，Args 为完整原文。
	EventToolCallEnd EventKind = "tool_call_end"
	// EventUsage 供应商报告的真实用量；字段缺失保持 nil，不补 0。
	EventUsage EventKind = "usage"
	// EventEnd 正常结束，携带供应商原始结束原因。
	EventEnd EventKind = "end"
)

// Event 归一化流事件。
//
// 设计取舍：这里**没有** Error 事件类型。错误通过 Emit 的返回值之外的方式表达——
// Stream 返回 error，这样调用方不会出现「既收到 error 事件又收到 error 返回」的双重
// 语义歧义，也不必在流中途判断「这个 error 之后还有没有数据」。
type Event struct {
	Kind EventKind
	// Text EventText 的增量文本。
	Text string
	// CallID 工具调用标识（Start/ArgsDelta/End 共用同一个值）。
	CallID string
	// ToolName 工具名，仅 Start 事件保证非空。
	ToolName string
	// Index 供应商侧的工具调用序号，用于无 callId 协议的稳定配对。
	Index int
	// Args 工具参数完整原文，仅 End 事件有值。
	Args string
	// Usage 真实用量，仅 EventUsage 有值；缺测为 nil。
	Usage *Usage
	// FinishReason 供应商原始结束原因（如 stop / tool_use / end_turn / length）。
	FinishReason string
}

// Usage 供应商报告的真实 token 用量。所有字段可空——供应商未必都给。
//
// 为什么用指针而不是零值：0 与「未报告」是两件事。把缺失当 0 会让用量面板显示
// 「本次消耗 0 token」这种明确错误的信息。
type Usage struct {
	InputTokens  *int
	OutputTokens *int
}

// ToolCall 一个完整的工具调用（参数已通过 JSON 对象校验）。
type ToolCall struct {
	CallID   string
	ToolName string
	// Args 原始参数 JSON 文本。
	Args string
}

// Message 对话消息（模型输入侧）。
type Message struct {
	// Role 取值 assistant / user / tool。
	Role string
	// Content 文本正文。
	Content string
	// ToolCalls assistant 消息携带的工具调用。
	ToolCalls []ToolCall
	// ToolCallID tool 消息对应的调用 ID。
	ToolCallID string
	// ToolName tool 消息对应的工具名。
	ToolName string
}

// ToolSpec 暴露给模型的工具定义。
type ToolSpec struct {
	Name        string
	Description string
	// Parameters 参数 JSON Schema（原样透传给供应商）。
	Parameters json.RawMessage
}

// Request 一次模型调用请求。
type Request struct {
	// Model 上游原始模型标识。
	Model  string
	System string
	// Messages 已含历史与本次用户输入。
	Messages []Message
	// Tools 本轮允许暴露的工具；为空表示不提供工具。
	Tools []ToolSpec
	// MaxOutputTokens 输出上限；0 表示不指定（由供应商默认）。
	MaxOutputTokens int
	// ReasoningEffort 思考强度：minimal / low / medium / high，空 = 不指定。
	//
	// 各协议映射不同：OpenAI Chat 是顶层 `reasoning_effort` 字符串，
	// Responses 是 `reasoning.effort`，Anthropic 是 `thinking.budget_tokens`（需换算）。
	// **未知取值一律不发送**：上游不认识的值会 400 掉整轮，
	// 「不发」比「发错」安全——后者表现为「配了个档位就全挂」。
	ReasoningEffort string
}

// Emit 事件回调。返回 error 可中断流（如客户端断开、事件持久化失败）。
type Emit func(Event) error

// Streamer 单一协议的流式适配器。
type Streamer interface {
	// Stream 发起请求并把事件逐个交给 emit。返回 nil 表示流正常结束（调用方应已收到
	// EventEnd）；返回 error 表示流失败——**此时不得**把已产出部分当作完整回复。
	Stream(ctx context.Context, req Request, emit Emit) error
}

// ErrIncompleteToolCall 工具调用参数在流结束时仍不完整或不是 JSON 对象。
var ErrIncompleteToolCall = fmt.Errorf("工具调用参数不完整或不是 JSON 对象")

// ReasoningEffort* 思考强度的合法档位。
const (
	ReasoningEffortMinimal = "minimal"
	ReasoningEffortLow     = "low"
	ReasoningEffortMedium  = "medium"
	ReasoningEffortHigh    = "high"
)

// normalizeReasoningEffort 校验并归一化档位。
//
// 只接受上面四个值；**其余一律返回 ok=false 表示「不发送」**。
// 为什么不用「回退到 medium」这种兜底：管理员配错一个值时，
// 静默按 medium 跑会让他以为配置生效了，而真实效果与预期不同却无从察觉；
// 不发送至少让上游用它自己的默认值，行为可预期。
func normalizeReasoningEffort(effort string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case ReasoningEffortMinimal:
		return ReasoningEffortMinimal, true
	case ReasoningEffortLow:
		return ReasoningEffortLow, true
	case ReasoningEffortMedium:
		return ReasoningEffortMedium, true
	case ReasoningEffortHigh:
		return ReasoningEffortHigh, true
	default:
		return "", false
	}
}

// collectToolCalls 把累积的工具调用做结束校验，产出可直接交给运行器的完整调用。
//
// 为什么必须校验到「是 JSON 对象」才放行：参数片段是逐 token 到达的，任何中间状态
// 都能被拼成一个看似合法的前缀；若不完整就执行，等于把半句话交给业务工具，
// 而业务工具不会预判「这个参数是不是还没收完」。
func collectToolCalls(pending map[int]*ToolCall, order []int) ([]ToolCall, error) {
	out := make([]ToolCall, 0, len(order))
	for _, idx := range order {
		call := pending[idx]
		if call == nil {
			continue
		}
		if call.ToolName == "" {
			return nil, fmt.Errorf("%w: 调用 %s 缺少工具名", ErrIncompleteToolCall, call.CallID)
		}
		if !isJSONObject(call.Args) {
			return nil, fmt.Errorf("%w: 调用 %s(%s) 参数=%q", ErrIncompleteToolCall, call.CallID, call.ToolName, truncateForErr(call.Args))
		}
		out = append(out, *call)
	}
	return out, nil
}

// isJSONObject 判断原文是否为合法 JSON 对象（工具参数必须是对象）。
func isJSONObject(raw string) bool {
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return false
	}
	return true
}

// truncateForErr 把过长参数截断进错误信息，避免日志被工具参数刷屏。
func truncateForErr(s string) string {
	const limit = 128
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// intPtr 构造 *int（用量字段可空）。
func intPtr(v int) *int { return &v }
