package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openAIResponsesStreamer OpenAI Responses API 流式适配器。
//
// 与 Chat Completions 的关键差异：响应是一串**带类型的事件**（response.output_text.delta、
// response.function_call_arguments.delta 等），工具调用是 response 输出项（item）而非
// delta 里的 tool_calls 数组；因此不能复用 Chat 的解析路径。
type openAIResponsesStreamer struct {
	client  *http.Client
	baseURL string
	apiKey  string
}

// newOpenAIResponsesStreamer 构造适配器。
func newOpenAIResponsesStreamer(client *http.Client, baseURL, apiKey string) *openAIResponsesStreamer {
	return &openAIResponsesStreamer{client: client, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey}
}

// responsesRequest Responses API 请求体。
type responsesRequest struct {
	Model           string           `json:"model"`
	Instructions    string           `json:"instructions,omitempty"`
	Input           []responsesInput `json:"input"`
	Tools           []responsesTool  `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
}

// responsesInput 输入项。
type responsesInput struct {
	Type    string `json:"type,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// function_call_output 专用
	CallID string `json:"call_id,omitempty"`
	Output string `json:"output,omitempty"`
}

// responsesTool 工具定义（Responses 用扁平结构，非嵌套 function）。
type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// responsesEvent Responses 流式事件。
type responsesEvent struct {
	Type string `json:"type"`
	// 文本增量
	Delta string `json:"delta"`
	// 输出项相关
	ItemID string `json:"item_id"`
	// function_call_arguments.done 携带完整 arguments
	Arguments string `json:"arguments"`
	// response.completed 里的 usage 与状态
	Response *struct {
		Status string `json:"status"`
		Usage  *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
	// 输出项起始时的名称（response.output_item.added）
	Item *struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Name   string `json:"name"`
		CallID string `json:"call_id"`
	} `json:"item"`
	// 错误事件
	Message string `json:"message"`
}

// Stream 实现 Streamer。
func (s *openAIResponsesStreamer) Stream(ctx context.Context, req Request, emit Emit) error {
	body := responsesRequest{
		Model:           req.Model,
		Instructions:    req.System,
		Input:           buildResponsesInput(req.Messages),
		Stream:          true,
		MaxOutputTokens: req.MaxOutputTokens,
	}
	for _, tool := range req.Tools {
		body.Tools = append(body.Tools, responsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造 HTTP 请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if s.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("请求上游失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upstreamError(resp)
	}

	// Responses 的工具调用按 item_id 标识；用出现顺序做 order。
	pending := map[string]*ToolCall{}
	var order []string
	stopReason := ""
	sawCompleted := false

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
		var ev responsesEvent
		if jerr := json.Unmarshal([]byte(data), &ev); jerr != nil {
			return fmt.Errorf("解析上游事件失败: %w", jerr)
		}

		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				if eerr := emit(Event{Kind: EventText, Text: ev.Delta}); eerr != nil {
					return eerr
				}
			}
		case "response.output_item.added":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				key := ev.Item.ID
				if key == "" {
					key = ev.Item.CallID
				}
				call := &ToolCall{CallID: ev.Item.CallID, ToolName: ev.Item.Name}
				pending[key] = call
				order = append(order, key)
				if eerr := emit(Event{
					Kind:     EventToolCallStart,
					CallID:   call.CallID,
					ToolName: call.ToolName,
					Index:    len(order) - 1,
				}); eerr != nil {
					return eerr
				}
			}
		case "response.function_call_arguments.delta":
			call := pending[ev.ItemID]
			if call == nil {
				return fmt.Errorf("收到未声明输出项的参数片段 item_id=%s", ev.ItemID)
			}
			call.Args += ev.Delta
			if eerr := emit(Event{
				Kind:   EventToolArgsDelta,
				CallID: call.CallID,
				Text:   ev.Delta,
			}); eerr != nil {
				return eerr
			}
		case "response.function_call_arguments.done":
			// done 事件带完整 arguments，作为权威值覆盖累积结果（避免丢尾片段）。
			if call := pending[ev.ItemID]; call != nil && ev.Arguments != "" {
				call.Args = ev.Arguments
			}
		case "response.completed":
			sawCompleted = true
			if ev.Response != nil {
				stopReason = ev.Response.Status
				if ev.Response.Usage != nil {
					if eerr := emit(Event{Kind: EventUsage, Usage: &Usage{
						InputTokens:  ev.Response.Usage.InputTokens,
						OutputTokens: ev.Response.Usage.OutputTokens,
					}}); eerr != nil {
						return eerr
					}
				}
			}
		case "response.failed", "error":
			msg := ev.Message
			if msg == "" {
				msg = truncateForErr(data)
			}
			return fmt.Errorf("上游返回失败事件: %s", msg)
		}
	}

	// item_id 与 call_id 可能不一致（前者是输出项标识），这里统一成 Map 的 key 顺序。
	calls := make([]ToolCall, 0, len(order))
	for _, key := range order {
		call := pending[key]
		if call == nil {
			continue
		}
		if call.ToolName == "" {
			return fmt.Errorf("%w: 调用 %s 缺少工具名", ErrIncompleteToolCall, call.CallID)
		}
		if !isJSONObject(call.Args) {
			return fmt.Errorf("%w: 调用 %s(%s) 参数=%q", ErrIncompleteToolCall, call.CallID, call.ToolName, truncateForErr(call.Args))
		}
		calls = append(calls, *call)
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
	if !sawCompleted {
		return fmt.Errorf("上游流意外结束（未收到 response.completed）")
	}
	return emit(Event{Kind: EventEnd, FinishReason: stopReason})
}

// buildResponsesInput 把内部消息转成 Responses 输入项。
//
// 工具结果在 Responses 里是独立的 function_call_output 项，不是 role=tool 的消息。
func buildResponsesInput(in []Message) []responsesInput {
	out := make([]responsesInput, 0, len(in))
	for _, m := range in {
		switch m.Role {
		case "tool":
			out = append(out, responsesInput{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: m.Content,
			})
		case "assistant":
			if m.Content != "" {
				out = append(out, responsesInput{Type: "message", Role: "assistant", Content: m.Content})
			}
			for _, call := range m.ToolCalls {
				// 历史工具调用以 message + 文本形式回放会丢语义，这里用注释性文本
				// 保留「曾调用过什么」的可追溯性；真正配对靠 function_call_output。
				out = append(out, responsesInput{
					Type:    "message",
					Role:    "assistant",
					Content: fmt.Sprintf("[调用工具 %s 参数 %s]", call.ToolName, call.Args),
				})
			}
		default:
			out = append(out, responsesInput{Type: "message", Role: "user", Content: m.Content})
		}
	}
	return out
}
