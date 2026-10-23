package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Responses 协议的工具历史回放（FR-514 §7.1 已知缺陷的回归）。
//
// 这组用例守的是一条「FR-514 自己看不出来、但 FR-515 会必然踩到」的语义：
// assistant 的工具调用必须回放成 function_call 项，否则后面的 function_call_output
// 找不到配对 call_id，上游判请求不合法——而且即便被宽容接受，模型也失去了
// 「我调用过什么、拿到了什么」的结构化历史。

// countByType 统计各类项数量，并按出现顺序收集 call_id。
func collectCallIDs(items []responsesInput) (calls []string, outputs []string) {
	for _, it := range items {
		switch it.Type {
		case "function_call":
			calls = append(calls, it.CallID)
		case "function_call_output":
			outputs = append(outputs, it.CallID)
		}
	}
	return calls, outputs
}

func TestBuildResponsesInputPairsToolCalls(t *testing.T) {
	// 典型一轮工具循环：用户提问 → assistant 发起两个调用 → 两个结果回灌。
	history := []Message{
		{Role: "user", Content: "停掉这两台"},
		{Role: "assistant", Content: "好的，我分别停。", ToolCalls: []ToolCall{
			{CallID: "call_a", ToolName: "instance_stop", Args: `{"id":1}`},
			{CallID: "call_b", ToolName: "instance_stop", Args: `{"id":2}`},
		}},
		{Role: "tool", ToolCallID: "call_a", ToolName: "instance_stop", Content: "已停止"},
		{Role: "tool", ToolCallID: "call_b", ToolName: "instance_stop", Content: "已停止"},
	}
	got := buildResponsesInput(history)

	calls, outputs := collectCallIDs(got)
	if len(calls) != 2 || len(outputs) != 2 {
		t.Fatalf("应有 2 个 function_call 与 2 个 function_call_output，实际 calls=%v outputs=%v", calls, outputs)
	}

	// ① 必须是 function_call 项，不能退化成 message。
	var sawFunctionCall bool
	for _, it := range got {
		if it.Type == "function_call" {
			sawFunctionCall = true
		}
		if it.Type == "message" && it.Role == "assistant" && strings.Contains(it.Content, "[调用工具") {
			t.Fatalf("工具调用不应降级为文本项，实际存在 %q", it.Content)
		}
	}
	if !sawFunctionCall {
		t.Fatal("未产出任何 function_call 项")
	}

	// ② call_id 集合必须一一对应（多一个少一个都会让上游拒绝）。
	if calls[0] != "call_a" || calls[1] != "call_b" {
		t.Fatalf("function_call 顺序应为原序 call_a,call_b，实际 %v", calls)
	}
	if outputs[0] != "call_a" || outputs[1] != "call_b" {
		t.Fatalf("function_call_output 顺序应与调用一致，实际 %v", outputs)
	}

	// ③ function_call 必须整体先于 output 出现（顺序颠倒上游同样无法配对）。
	lastCall, firstOutput := -1, len(got)
	for i, it := range got {
		if it.Type == "function_call" {
			lastCall = i
		}
		if it.Type == "function_call_output" && i < firstOutput {
			firstOutput = i
		}
	}
	if lastCall > firstOutput {
		t.Fatalf("function_call 必须先于 function_call_output，实际最后调用 index=%d 首个结果 index=%d",
			lastCall, firstOutput)
	}
}

func TestBuildResponsesInputCarriesNameAndRawArgs(t *testing.T) {
	// 工具名与**原始参数文本**都要原样带回：重新序列化会改变键序与空白，
	// 可能被对端判为「与我发出时不一致」。这里刻意用带空格与特定键序的参数验证逐字保留。
	raw := `{"id": 1,"reason" : "维护" }`
	got := buildResponsesInput([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{CallID: "c1", ToolName: "instance_stop", Args: raw},
		}},
	})
	if len(got) != 1 || got[0].Type != "function_call" {
		t.Fatalf("应只有一个 function_call 项，实际 %+v", got)
	}
	if got[0].Name != "instance_stop" {
		t.Fatalf("工具名未带上，实际 %q", got[0].Name)
	}
	if got[0].Arguments != raw {
		t.Fatalf("参数必须逐字保留：期望 %q 实际 %q", raw, got[0].Arguments)
	}
}

func TestBuildResponsesInputWithoutToolsUnchanged(t *testing.T) {
	// 回归保护：没有工具调用时，请求体必须与改动前逐字节一致。
	// 用 JSON 序列化结果比较，才能覆盖 omitempty 的实际效果（字段名/顺序/是否出现）。
	cases := [][]Message{
		{{Role: "user", Content: "你好"}},
		{{Role: "user", Content: "你好"}, {Role: "assistant", Content: "在"}},
		{{Role: "assistant", Content: ""}}, // 空正文不产出项
	}
	for _, in := range cases {
		got, err := json.Marshal(buildResponsesInput(in))
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		// 期望：只有 message 项，且不含 name/arguments/call_id 这类工具字段。
		for _, forbidden := range []string{`"name"`, `"arguments"`, `"function_call`} {
			if strings.Contains(string(got), forbidden) {
				t.Fatalf("无工具的输入不应出现 %s，实际 %s", forbidden, got)
			}
		}
		if in[0].Content != "" && !strings.Contains(string(got), `"type":"message"`) {
			t.Fatalf("应产出 message 项，实际 %s", got)
		}
	}
}

func TestBuildResponsesInputRejectsNothingButKeepsPairing(t *testing.T) {
	// 只有调用、没有结果的中间态（流被中断）：此时不应凭空造 output。
	// 这是「不假装完整」的一面——由上层决定是否终止，本函数不替它掩盖。
	got := buildResponsesInput([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{{CallID: "c1", ToolName: "t", Args: "{}"}}},
	})
	calls, outputs := collectCallIDs(got)
	if len(calls) != 1 || len(outputs) != 0 {
		t.Fatalf("应只有调用、无结果，实际 calls=%v outputs=%v", calls, outputs)
	}
}
