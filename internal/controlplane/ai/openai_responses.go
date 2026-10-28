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
	// Reasoning 思考强度（FR-541 §3.2）。Responses 用对象形态，与 Chat 的顶层字符串不同。
	Reasoning *responsesReasoning `json:"reasoning,omitempty"`
}

// responsesReasoning Responses API 的思考配置。
type responsesReasoning struct {
	Effort string `json:"effort"`
}

// responsesInput 输入项。
//
// 一个结构体承载三类项（message / function_call / function_call_output），靠 Type 区分：
// 它们在同一条 input 数组里按顺序排列，上游要求 function_call 与 function_call_output
// 通过 call_id 配对且顺序一致。
type responsesInput struct {
	Type    string `json:"type,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// function_call 专用：调用标识、工具名与**原始参数 JSON 文本**。
	//
	// 参数必须是字符串而不是对象：Responses 的 function_call.arguments 是
	// 上游自己流式吐出来的一段文本，回放时原样送回最安全——重新序列化会改变
	// 键序与空白，可能被对端判为「与我发出时不一致」。
	//
	// **不要给 function_call 填 `id`**：`id` 是上游为「输出项」分配的标识（形如
	// `fc_…`），而配对靠的是 `call_id`。我们没有保存过上游的项 id，把 callId 塞进
	// `id` 会让上游按一个不存在的项 id 去定位，属于凭空伪造；`id` 在输入项里本就是
	// 可选字段，省略即由对端自行处理。
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output 专用
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
	if effort, ok := normalizeReasoningEffort(req.ReasoningEffort); ok {
		body.Reasoning = &responsesReasoning{Effort: effort}
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

// buildResponsesInput 把内部消息历史翻成 Responses 的 input 数组。
//
// 三类项各有其形，靠 Type 区分：普通文本是 `message`（带 role/content），
// assistant 的工具调用是 `function_call`（带 call_id/name/arguments），
// 工具结果是 `function_call_output`（带 call_id/output）。两处**靠 call_id 配对**，
// 而 function_call 的项 id（`id`）不回填——我们从未保存过上游的项 id，
// 伪造一个只会让对端按不存在的项去定位。
//
// 正确行为有两条硬约束：
//
//  1. **工具调用必须以 function_call 项回放，不能降级成一条文本 message。**
//     一旦写成文本，紧随其后的 function_call_output 就没有可配对的 call_id，
//     上游会判请求不合法；即便某些上游宽容接受，模型读到的也只是「我说过一句话」，
//     拿不到「我调用过什么、拿到了什么」的结构化历史，多轮工具循环的语义随之丢失。
//  2. **顺序按输入线性输出，不分组、不排序、不去重。** 历史里 assistant 在前、
//     tool 结果在后，因此 function_call 天然先于它自己的 output（上游同样要求这个
//     次序）；去重则会掩盖上层「同一次调用被重复登记」的真实缺陷。
//
// 另外：assistant 的文本正文为空时不产出 message 项——空 content 的 message 会被
// 上游拒收，而「只有工具调用」的轮次本来就应该只由 function_call 项构成。
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
			// 有正文才产 message：空正文的 message 会被上游拒绝。
			if m.Content != "" {
				out = append(out, responsesInput{Type: "message", Role: "assistant", Content: m.Content})
			}
			// 调用跟在正文之后，保持「先说话、再调用」的原始顺序。
			for _, call := range m.ToolCalls {
				out = append(out, responsesInput{
					Type:      "function_call",
					CallID:    call.CallID,
					Name:      call.ToolName,
					Arguments: call.Args,
				})
			}
		default:
			out = append(out, responsesInput{Type: "message", Role: "user", Content: m.Content})
		}
	}
	return out
}
