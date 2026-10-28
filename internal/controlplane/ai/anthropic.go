package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropicStreamer Anthropic Messages API 流式适配器。
//
// 与 OpenAI 形状的主要差异（决定了这里不能简单复用同一套解析）：
//   - 鉴权是 `x-api-key` + `anthropic-version` 头，不是 Bearer；
//   - 系统提示是顶层 `system` 字段，不是一条 system 消息；
//   - 事件类型由 SSE 的 `event:` 字段给出，而非靠 JSON 形状猜；
//   - 工具参数以 `input_json_delta` 的 partial_json 片段到达；
//   - 结束原因是 `stop_reason`（tool_use / end_turn / max_tokens）。
type anthropicStreamer struct {
	client     *http.Client
	baseURL    string
	apiKey     string
	apiVersion string
	maxTokens  int
}

// anthropicDefaultVersion 固定的 API 版本头。
//
// 固定而非可配：版本决定响应形状，让管理员随意改会把「适配器按哪个形状解析」
// 变成运行期未知量；升级版本应伴随适配器改动。
const anthropicDefaultVersion = "2023-06-01"

// anthropicDefaultMaxTokens Anthropic 要求 max_tokens 必填，未指定时的默认值。
const anthropicDefaultMaxTokens = 2048

// anthropicThinkingOutputReserve 启用思考时给「正式回复」预留的输出预算。
//
// 上游要求 max_tokens > budget_tokens（思考 token 也占用 max_tokens）。
// 若只把 max_tokens 抬到等于预算，回复会被思考吃光、直接截断成空文本。
const anthropicThinkingOutputReserve = 2048

// newAnthropicStreamer 构造适配器。
func newAnthropicStreamer(client *http.Client, baseURL, apiKey string) *anthropicStreamer {
	return &anthropicStreamer{
		client:     client,
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		apiVersion: anthropicDefaultVersion,
	}
}

// anthropicRequest Messages API 请求体。
type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	// Thinking 思考强度（FR-541 §3.2）。仅档位非空时发送。
	//
	// 与 temperature **互斥**：同时发送会被上游 400。本适配器不发 temperature，
	// 所以这里不需要额外互斥处理——但若将来加了 temperature，必须在此判空。
	Thinking *anthropicThinking `json:"thinking,omitempty"`
}

// anthropicThinking 启用思考并给出预算。
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// anthropicThinkingBudget 把档位映射成 token 预算。
//
// 为什么是「预算」而不是像 OpenAI 那样只给档位：Anthropic 的思考是按 token 计的，
// 档位必须换算成一个正整数；这些数值取自其文档的常见档（1k/2k/8k/16k）。
func anthropicThinkingBudget(effort string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return 1024, true
	case "low":
		return 2048, true
	case "medium":
		return 8192, true
	case "high":
		return 16384, true
	default:
		// 未知档位一律不发送：发一个上游不认识的值会被 400 拒掉整轮。
		return 0, false
	}
}

// anthropicMessage Messages API 消息（content 允许字符串或块数组）。
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicTool 工具定义。
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicBlock 内容块（文本 / 工具调用 / 工具结果）。
type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// 工具调用块
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// 工具结果块
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// anthropicStreamEvent SSE 事件载荷。按 event 类型解释 data。
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	// content_block_start
	ContentBlock *anthropicBlock `json:"content_block"`
	// content_block_delta
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	// message_delta 携带 stop_reason 与 usage
	Message *struct {
		StopReason *string `json:"stop_reason"`
		Usage      *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage *struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

// Stream 实现 Streamer。
func (s *anthropicStreamer) Stream(ctx context.Context, req Request, emit Emit) error {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	body := anthropicRequest{
		Model:     req.Model,
		System:    req.System,
		Messages:  buildAnthropicMessages(req.Messages),
		MaxTokens: maxTokens,
		Stream:    true,
	}
	// 思考强度（FR-541 §3.2）：预算必须小于 max_tokens，否则上游直接 400。
	// 冲突时的取舍：把 max_tokens 抬到「预算 + 输出余量」，而不是把预算压小——
	// 管理员设了 high 却拿到 minimal 的效果，比多花 token 更难排查。
	if budget, ok := anthropicThinkingBudget(req.ReasoningEffort); ok {
		if body.MaxTokens <= budget {
			body.MaxTokens = budget + anthropicThinkingOutputReserve
		}
		body.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
	}
	for _, tool := range req.Tools {
		body.Tools = append(body.Tools, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造 HTTP 请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", s.apiVersion)
	if s.apiKey != "" {
		httpReq.Header.Set("x-api-key", s.apiKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("请求上游失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upstreamError(resp)
	}

	// Anthropic 用 index 标识内容块；工具调用与文本块共享同一个 index 空间。
	pending := map[int]*ToolCall{}
	var order []int
	stopReason := ""
	sawMessageStop := false

	reader := newSSEReader(resp.Body)
	for {
		frame, ok, rerr := reader.Next()
		if rerr != nil {
			return fmt.Errorf("读取上游流失败: %w", rerr)
		}
		if !ok {
			break
		}
		data := strings.TrimSpace(frame.Data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev anthropicStreamEvent
		if jerr := json.Unmarshal([]byte(data), &ev); jerr != nil {
			return fmt.Errorf("解析上游事件失败: %w", jerr)
		}

		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				call := &ToolCall{CallID: ev.ContentBlock.ID, ToolName: ev.ContentBlock.Name}
				pending[ev.Index] = call
				order = append(order, ev.Index)
				if eerr := emit(Event{
					Kind:     EventToolCallStart,
					CallID:   call.CallID,
					ToolName: call.ToolName,
					Index:    ev.Index,
				}); eerr != nil {
					return eerr
				}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					if eerr := emit(Event{Kind: EventText, Text: ev.Delta.Text}); eerr != nil {
						return eerr
					}
				}
			case "input_json_delta":
				call := pending[ev.Index]
				if call == nil {
					// 参数片段先于 start 到达属协议违例，不能静默丢弃（会得到空参数调用）。
					return fmt.Errorf("收到未声明内容块的工具参数片段 index=%d", ev.Index)
				}
				call.Args += ev.Delta.PartialJSON
				if eerr := emit(Event{
					Kind:   EventToolArgsDelta,
					CallID: call.CallID,
					Index:  ev.Index,
					Text:   ev.Delta.PartialJSON,
				}); eerr != nil {
					return eerr
				}
			}
		case "message_delta":
			if ev.Message != nil && ev.Message.StopReason != nil {
				stopReason = *ev.Message.StopReason
			}
			if uerr := emitAnthropicUsage(ev.Message, emit); uerr != nil {
				return uerr
			}
		case "message_start":
			// message_start 也带 usage（输入侧），但 output_tokens 此时通常为 0；
			// 只在 message_delta 上报，避免把「还没生成」的 0 当成真实输出量。
		case "message_stop":
			sawMessageStop = true
		case "error":
			// 上游显式错误事件：携带的是协议层错误，按失败处理。
			return fmt.Errorf("上游返回错误事件: %s", truncateForErr(data))
		}
	}

	calls, cerr := collectToolCalls(pending, order)
	if cerr != nil {
		return cerr
	}
	for _, call := range calls {
		if eerr := emit(Event{
			Kind:     EventToolCallEnd,
			CallID:   call.CallID,
			ToolName: call.ToolName,
			Args:     call.Args,
		}); eerr != nil {
			return eerr
		}
	}
	if !sawMessageStop && stopReason == "" {
		return fmt.Errorf("上游流意外结束（未收到 message_stop 与 stop_reason）")
	}
	return emit(Event{Kind: EventEnd, FinishReason: stopReason})
}

// emitAnthropicUsage 从 message_delta 里取用量（两处形状都兼容）。
func emitAnthropicUsage(msg *struct {
	StopReason *string `json:"stop_reason"`
	Usage      *struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}, emit Emit) error {
	if msg == nil || msg.Usage == nil {
		return nil
	}
	return emit(Event{Kind: EventUsage, Usage: &Usage{
		InputTokens:  msg.Usage.InputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}})
}

// buildAnthropicMessages 把内部消息转成 Messages API 形状。
//
// 关键差异：tool 结果不是独立 role，而是 user 消息里的 tool_result 块；
// assistant 的工具调用是 tool_use 块。角色必须交替，因此连续的 tool 结果要合并到
// 同一条 user 消息里，否则上游会因「角色不交替」拒绝请求。
func buildAnthropicMessages(in []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case "tool":
			block := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			// 若上一条已是 user 且装的正是工具结果块，则追加（保持角色交替）。
			if n := len(out); n > 0 && out[n-1].Role == "user" {
				if blocks, ok := out[n-1].Content.([]anthropicBlock); ok {
					out[n-1].Content = append(blocks, block)
					continue
				}
			}
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicBlock{block}})
		case "assistant":
			var blocks []anthropicBlock
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				var input json.RawMessage = []byte("{}")
				if strings.TrimSpace(call.Args) != "" {
					input = json.RawMessage(call.Args)
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    call.CallID,
					Name:  call.ToolName,
					Input: input,
				})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		default:
			out = append(out, anthropicMessage{Role: "user", Content: m.Content})
		}
	}
	return out
}
