package ai

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Anthropic 与 Responses 两个适配器的协议测试。
//
// 与 Chat 那组同源关切：分片拼接、截断必失败、缺失用量不补零。额外覆盖
// 两者各自的形状差异（event 字段分发、item_id 与 call_id 的区别）。

func TestAnthropicTextAndUsage(t *testing.T) {
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":0}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"，世界"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","message":{"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":4}}}`,
		"",
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newAnthropicStreamer(srv.Client(), srv.URL, "k")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	var text strings.Builder
	var usage *Usage
	var end *Event
	for i := range events {
		switch events[i].Kind {
		case EventText:
			text.WriteString(events[i].Text)
		case EventUsage:
			usage = events[i].Usage
		case EventEnd:
			end = &events[i]
		}
	}
	if text.String() != "你好，世界" {
		t.Fatalf("文本 = %q", text.String())
	}
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 9 || usage.OutputTokens == nil || *usage.OutputTokens != 4 {
		t.Fatalf("用量应取 message_delta 的真实值，实际 %+v", usage)
	}
	if end == nil || end.FinishReason != "end_turn" {
		t.Fatalf("结束原因 = %+v", end)
	}
}

// message_start 里的 output_tokens=0 是「还没生成」，不能当作真实输出量上报。
func TestAnthropicIgnoresMessageStartUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":0}}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newAnthropicStreamer(srv.Client(), srv.URL, "")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	for _, e := range events {
		if e.Kind == EventUsage {
			t.Fatalf("不应从 message_start 上报用量，实际 %+v", e.Usage)
		}
	}
}

func TestAnthropicToolCallSplitAcrossChunks(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"node_get"}}`,
		"",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"id\":"}}`,
		"",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"7}"}}`,
		"",
		`data: {"type":"message_delta","message":{"stop_reason":"tool_use"}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newAnthropicStreamer(srv.Client(), srv.URL, "")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	var ends []Event
	for _, e := range events {
		if e.Kind == EventToolCallEnd {
			ends = append(ends, e)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("应产出 1 个完整调用，实际 %d", len(ends))
	}
	if ends[0].ToolName != "node_get" || ends[0].Args != `{"id":7}` || ends[0].CallID != "tu_1" {
		t.Fatalf("调用解析错误: %+v", ends[0])
	}
}

// 参数片段先于 content_block_start 到达属协议违例，必须报错而不是产出空参数调用。
func TestAnthropicArgsBeforeStartFails(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		"",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newAnthropicStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); err == nil {
		t.Fatal("未声明的参数片段必须报错")
	}
}

// 上游 error 事件必须中断且不冒充完整回复。
func TestAnthropicErrorEventFails(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"error","message":"overloaded"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newAnthropicStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); err == nil {
		t.Fatal("error 事件必须报错")
	}
}

// 无 message_stop 也无 stop_reason ⇒ 截断。
func TestAnthropicTruncatedFails(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半句"}}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newAnthropicStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); err == nil {
		t.Fatal("截断的流必须报错")
	}
}

// tool 结果必须并入 user 消息的 tool_result 块，且保持角色交替。
func TestBuildAnthropicMessagesMergesToolResults(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "重启一下"},
		{Role: "assistant", ToolCalls: []ToolCall{{CallID: "c1", ToolName: "instance_restart", Args: `{"id":1}`}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "instance_restart", Content: "ok"},
		{Role: "tool", ToolCallID: "c2", ToolName: "instance_restart", Content: "ok2"},
	}
	out := buildAnthropicMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("连续两条 tool 结果应并入同一条 user 消息，实际 %d 条", len(out))
	}
	blocks, ok := out[2].Content.([]anthropicBlock)
	if !ok || len(blocks) != 2 {
		t.Fatalf("末条 user 消息应含两个 tool_result 块，实际 %#v", out[2].Content)
	}
	if blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "c1" {
		t.Fatalf("块解析错误: %+v", blocks[0])
	}
}

func TestOpenAIResponsesTextAndUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"你好"}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"，世界"}`,
		"",
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":3}}}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIResponsesStreamer(srv.Client(), srv.URL, "")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	var text strings.Builder
	var usage *Usage
	var end *Event
	for i := range events {
		switch events[i].Kind {
		case EventText:
			text.WriteString(events[i].Text)
		case EventUsage:
			usage = events[i].Usage
		case EventEnd:
			end = &events[i]
		}
	}
	if text.String() != "你好，世界" {
		t.Fatalf("文本 = %q", text.String())
	}
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 5 {
		t.Fatalf("用量 = %+v", usage)
	}
	if end == nil || end.FinishReason != "completed" {
		t.Fatalf("结束 = %+v", end)
	}
}

// Responses 的工具调用按 item_id 归属；done 事件里的完整 arguments 是权威值。
func TestOpenAIResponsesToolCallUsesDoneArguments(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_1","call_id":"call_9","name":"instance_stop"}}`,
		"",
		`data: {"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"id\":"}`,
		"",
		`data: {"type":"response.function_call_arguments.done","item_id":"item_1","arguments":"{\"id\":42}"}`,
		"",
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIResponsesStreamer(srv.Client(), srv.URL, "")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	var end *Event
	for i := range events {
		if events[i].Kind == EventToolCallEnd {
			end = &events[i]
		}
	}
	if end == nil {
		t.Fatal("应产出完整工具调用")
	}
	if end.Args != `{"id":42}` {
		t.Fatalf("参数应以 done 的完整值为准，实际 %q", end.Args)
	}
	if end.CallID != "call_9" || end.ToolName != "instance_stop" {
		t.Fatalf("调用标识错误: %+v", end)
	}
}

func TestOpenAIResponsesFailedEventFails(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.failed","message":"quota exceeded"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIResponsesStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); err == nil {
		t.Fatal("response.failed 必须报错")
	}
}

// 无 response.completed ⇒ 截断。
func TestOpenAIResponsesTruncatedFails(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"半句"}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIResponsesStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); err == nil {
		t.Fatal("截断的流必须报错")
	}
}

func TestNewStreamerUnknownProtocol(t *testing.T) {
	if _, err := NewStreamer("nope", http.DefaultClient, "https://x.example.com", ""); err == nil {
		t.Fatal("未知协议应报错")
	}
	if !ProtocolSupported(model.AssistantProtocolAnthropic) {
		t.Fatal("anthropic 应受支持")
	}
	if len(SupportedProtocols()) != 3 {
		t.Fatalf("受支持协议应为 3 个，实际 %d", len(SupportedProtocols()))
	}
}

// 三协议都必须拒绝"半截参数"，用同一组输入保证不会出现某个协议漏校验。
func TestAllProtocolsRejectIncompleteArgs(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		body     string
	}{
		{
			name:     "chat",
			protocol: model.AssistantProtocolOpenAIChat,
			body: strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":"{\"x\""}}]}}]}`,
				"",
				`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				"",
				`data: [DONE]`,
				"",
			}, "\n"),
		},
		{
			name:     "anthropic",
			protocol: model.AssistantProtocolAnthropic,
			body: strings.Join([]string{
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t0","name":"a"}}`,
				"",
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\""}}`,
				"",
				`data: {"type":"message_stop"}`,
				"",
			}, "\n"),
		},
		{
			name:     "responses",
			protocol: model.AssistantProtocolOpenAIResponses,
			body: strings.Join([]string{
				`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"i0","call_id":"c0","name":"a"}}`,
				"",
				`data: {"type":"response.function_call_arguments.delta","item_id":"i0","delta":"{\"x\""}`,
				"",
				`data: {"type":"response.completed","response":{"status":"completed"}}`,
				"",
			}, "\n"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newSSEServer(t, c.body, http.StatusOK)
			s, err := NewStreamer(c.protocol, srv.Client(), srv.URL, "")
			if err != nil {
				t.Fatalf("构造适配器失败: %v", err)
			}
			if _, err := collect(t, s, Request{Model: "m"}); !errors.Is(err, ErrIncompleteToolCall) {
				t.Fatalf("%s 应拒绝不完整参数，实际 %v", c.name, err)
			}
		})
	}
}

// TestNormalizeReasoningEffort 只认四个合法档位，其余「不发送」。
//
// 为什么不是「非法值回退到 medium」：静默回退会让管理员以为配置生效，
// 而真实效果与预期不同却无从察觉。不发送至少行为可预期。
func TestNormalizeReasoningEffort(t *testing.T) {
	for _, ok := range []string{"minimal", "low", "medium", "high", " HIGH "} {
		if _, valid := normalizeReasoningEffort(ok); !valid {
			t.Fatalf("%q 应合法", ok)
		}
	}
	for _, bad := range []string{"", "max", "turbo", "1", "thinking"} {
		if _, valid := normalizeReasoningEffort(bad); valid {
			t.Fatalf("%q 不应合法（未知值不得发送给上游）", bad)
		}
	}
}

// TestAnthropicThinkingBudget 档位换算为 token 预算，未知档位不启用。
func TestAnthropicThinkingBudget(t *testing.T) {
	want := map[string]int{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384}
	for effort, budget := range want {
		got, ok := anthropicThinkingBudget(effort)
		if !ok || got != budget {
			t.Fatalf("%s 预算应为 %d，实际 %d ok=%v", effort, budget, got, ok)
		}
	}
	if _, ok := anthropicThinkingBudget("unknown"); ok {
		t.Fatal("未知档位不得启用 thinking（会被上游 400）")
	}
}
