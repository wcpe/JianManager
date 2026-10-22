package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openAIChatStreamer OpenAI 兼容 Chat Completions 流式适配器。
//
// 这是覆盖面最广的一种协议：OpenAI 自身与大量兼容网关（自托管、聚合网关、
// 国内厂商的兼容层）都用同一形状，因此优先支持收益最大。
type openAIChatStreamer struct {
	client   *http.Client
	baseURL  string
	apiKey   string
	endpoint string
}

// newOpenAIChatStreamer 构造适配器。endpoint 为相对 baseURL 的路径（默认 /chat/completions）。
func newOpenAIChatStreamer(client *http.Client, baseURL, apiKey string) *openAIChatStreamer {
	return &openAIChatStreamer{client: client, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, endpoint: "/chat/completions"}
}

// chatStreamRequest Chat Completions 请求体。
type chatStreamRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Tools         []chatTool     `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions 请求在流末尾附带用量统计（部分兼容网关不支持该字段，故仅当需要时下发）。
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage Chat Completions 消息。
type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

// chatToolCall 历史消息里的工具调用。
type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

// chatFunctionCall 工具调用的函数部分。
type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatTool 工具定义。
type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

// chatFunction 工具函数定义。
type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// chatStreamChunk 流式响应分片。
type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				// Index 分片按 index 归属同一个调用；部分网关不给 id，需按 index 兜底。
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

// Stream 实现 Streamer。
func (s *openAIChatStreamer) Stream(ctx context.Context, req Request, emit Emit) error {
	body := chatStreamRequest{
		Model:         req.Model,
		Messages:      buildChatMessages(req),
		Stream:        true,
		MaxTokens:     req.MaxOutputTokens,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	for _, tool := range req.Tools {
		body.Tools = append(body.Tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+s.endpoint, bytes.NewReader(payload))
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

	// 累积工具调用：index → 调用；order 保持出现顺序（供应商不保证按 index 递增到达）。
	pending := map[int]*ToolCall{}
	var order []int
	finishReason := ""
	sawEnd := false

	reader := newSSEReader(resp.Body)
	for {
		frame, ok, rerr := reader.Next()
		if rerr != nil {
			// 读失败必须**中断**而不是当作正常结束：半截的工具调用不得执行。
			return fmt.Errorf("读取上游流失败: %w", rerr)
		}
		if !ok {
			break
		}
		if strings.TrimSpace(frame.Data) == "[DONE]" {
			sawEnd = true
			break
		}
		var chunk chatStreamChunk
		if jerr := json.Unmarshal([]byte(frame.Data), &chunk); jerr != nil {
			// 单帧解析失败视为协议不符：不静默跳过，否则会得到「看起来正常但内容缺失」的回答。
			return fmt.Errorf("解析上游分片失败: %w", jerr)
		}
		if chunk.Usage != nil {
			if eerr := emit(Event{Kind: EventUsage, Usage: &Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}}); eerr != nil {
				return eerr
			}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if eerr := emit(Event{Kind: EventText, Text: choice.Delta.Content}); eerr != nil {
					return eerr
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				call, exists := pending[tc.Index]
				if !exists {
					call = &ToolCall{CallID: tc.ID}
					pending[tc.Index] = call
					order = append(order, tc.Index)
					if eerr := emit(Event{
						Kind:     EventToolCallStart,
						CallID:   tc.ID,
						ToolName: tc.Function.Name,
						Index:    tc.Index,
					}); eerr != nil {
						return eerr
					}
				}
				// 名与 id 可能在后续分片才补齐（首片常只有 index）。
				if tc.Function.Name != "" && call.ToolName == "" {
					call.ToolName = tc.Function.Name
				}
				if tc.ID != "" && call.CallID == "" {
					call.CallID = tc.ID
				}
				if tc.Function.Arguments != "" {
					call.Args += tc.Function.Arguments
					if eerr := emit(Event{
						Kind:   EventToolArgsDelta,
						CallID: call.CallID,
						Index:  tc.Index,
						Text:   tc.Function.Arguments,
					}); eerr != nil {
						return eerr
					}
				}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
	}

	// 结束前校验全部工具调用参数完整，校验失败即整体失败。
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
	// 未收到 [DONE] 也未必是错误：部分兼容网关直接关闭连接。但若既无 [DONE]
	// 也无 finish_reason，说明流是被截断的，按失败处理而非冒充完整回复。
	if !sawEnd && finishReason == "" {
		return fmt.Errorf("上游流意外结束（既无 [DONE] 也无 finish_reason）")
	}
	return emit(Event{Kind: EventEnd, FinishReason: finishReason})
}

// buildChatMessages 把内部消息转成 Chat Completions 形状。
func buildChatMessages(req Request) []chatMessage {
	out := make([]chatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		out = append(out, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msg := chatMessage{Role: m.Role, Content: m.Content}
		if m.Role == "tool" {
			msg.ToolCallID = m.ToolCallID
			msg.Name = m.ToolName
		}
		for _, call := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
				ID:       call.CallID,
				Type:     "function",
				Function: chatFunctionCall{Name: call.ToolName, Arguments: call.Args},
			})
		}
		out = append(out, msg)
	}
	return out
}

// upstreamError 把非 2xx 响应转成**已分类且脱敏**的错误。
//
// 只回状态码与**截断后的**上游消息：上游错误体可能回显请求内容（含我们自己发出去的
// 密钥——"Incorrect API key provided: sk-xxx" 是常见形态），不能原样透传进日志或 UI。
// 因此先按模式掩蔽疑似凭据，再截断；同时带上分类码，
// 使配置页的「测试连接」能区分密钥错、限流与服务端故障。
func upstreamError(resp *http.Response) error {
	const limit = 512
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	msg := RedactUpstreamBody(strings.TrimSpace(string(raw)))
	if len(msg) > limit {
		msg = msg[:limit] + "…"
	}
	return NewUpstreamError(ClassifyStatus(resp.StatusCode), resp.StatusCode, msg)
}
