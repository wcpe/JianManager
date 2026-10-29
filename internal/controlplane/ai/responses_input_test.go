package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Responses 协议的工具历史回放（§7.1 已知缺陷的回归）。
//
// 这组用例守的是一条「自己看不出来、但会必然踩到」的语义：
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
		// 期望：只有 message 项，且不含 id/name/arguments/call_id 这类工具字段。
		for _, forbidden := range []string{`"id"`, `"name"`, `"arguments"`, `"function_call`} {
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

// 单次工具调用：function_call 必须靠 call_id 与 output 配对，且排在其之前。
//
// 刻意**不**给 function_call 填 `id`：`id` 是上游为输出项分配的标识（形如 `fc_…`），
// 我们从未保存过它，拿 callId 冒充属于伪造；配对只依赖 call_id。此用例把「不得出现 id」
// 也固化成断言，防止有人日后又把它加回来。
func TestBuildResponsesInputSingleCallPairsByCallIDAndPrecedesOutput(t *testing.T) {
	got := buildResponsesInput([]Message{
		{Role: "user", Content: "停掉 7 号"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{CallID: "call_1", ToolName: "instance_stop", Args: `{"id":7}`},
		}},
		{Role: "tool", ToolCallID: "call_1", ToolName: "instance_stop", Content: "已停止"},
	})
	if len(got) != 3 {
		t.Fatalf("应为 user + function_call + function_call_output 三项，实际 %+v", got)
	}
	if got[0].Type != "message" || got[0].Role != "user" {
		t.Fatalf("首项应为用户消息，实际 %+v", got[0])
	}

	callIdx, outputIdx := -1, -1
	for i, it := range got {
		switch it.Type {
		case "function_call":
			callIdx = i
		case "function_call_output":
			outputIdx = i
		}
	}
	if callIdx < 0 || outputIdx < 0 {
		t.Fatalf("应同时存在 function_call 与 function_call_output，实际 %+v", got)
	}
	if callIdx >= outputIdx {
		t.Fatalf("function_call 必须排在对应 output 之前，实际 call=%d output=%d", callIdx, outputIdx)
	}

	call, output := got[callIdx], got[outputIdx]
	// 用序列化结果断言 id 不出现：结构体已无该字段，这样写同时守住「将来别再加回来」。
	rawCall, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(rawCall), `"id"`) {
		t.Fatalf("function_call 不应填 id（项 id 由上游分配，我们不伪造），实际 %s", rawCall)
	}
	if call.CallID == "" || call.CallID != output.CallID {
		t.Fatalf("两侧 call_id 必须同源配对，实际 call_id=%q output.call_id=%q",
			call.CallID, output.CallID)
	}
	if call.Name != "instance_stop" || call.Arguments != `{"id":7}` {
		t.Fatalf("工具名与原始参数未原样带回：%+v", call)
	}
	if output.Output != "已停止" {
		t.Fatalf("工具结果正文不符：%q", output.Output)
	}
}

// 多个调用：每项的 name/arguments 必须跟着自己的 call_id 走，不能串味。
//
// 这里刻意让结果**倒序**回灌（先 c2 后 c1）：配对只能靠 call_id，
// 若实现偷偷用「相邻位置」或「按数组下标」配对，这个用例会立刻转红。
func TestBuildResponsesInputMultipleCallsKeepOwnIdentity(t *testing.T) {
	got := buildResponsesInput([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{CallID: "c1", ToolName: "instance_start", Args: `{"id":1}`},
			{CallID: "c2", ToolName: "instance_stop", Args: `{"id":2}`},
		}},
		{Role: "tool", ToolCallID: "c2", ToolName: "instance_stop", Content: "已停止"},
		{Role: "tool", ToolCallID: "c1", ToolName: "instance_start", Content: "已启动"},
	})

	type identity struct{ name, args string }
	calls := map[string]identity{}
	var callOrder []string
	outputs := map[string]string{}
	for _, it := range got {
		switch it.Type {
		case "function_call":
			calls[it.CallID] = identity{name: it.Name, args: it.Arguments}
			callOrder = append(callOrder, it.CallID)
		case "function_call_output":
			outputs[it.CallID] = it.Output
		}
	}
	if len(calls) != 2 || len(outputs) != 2 {
		t.Fatalf("应有 2 个调用与 2 个结果，实际 calls=%v outputs=%v", calls, outputs)
	}
	if callOrder[0] != "c1" || callOrder[1] != "c2" {
		t.Fatalf("调用应按 ToolCalls 原序输出，实际 %v", callOrder)
	}
	if calls["c1"].name != "instance_start" || calls["c1"].args != `{"id":1}` {
		t.Fatalf("c1 的身份串味了：%+v", calls["c1"])
	}
	if calls["c2"].name != "instance_stop" || calls["c2"].args != `{"id":2}` {
		t.Fatalf("c2 的身份串味了：%+v", calls["c2"])
	}
	// 结果按 call_id 各自归位，与回灌顺序无关。
	if outputs["c1"] != "已启动" || outputs["c2"] != "已停止" {
		t.Fatalf("结果与调用配对错误：%v", outputs)
	}
}

// 正文为空、只有工具调用：不得产出空 content 的 message 项。
//
// 上游对空 message 的态度是「直接拒」，而这类轮次本来就不该有文本项——
// 补一条空 message 等于把合法的工具轮次改造成非法请求。
func TestBuildResponsesInputEmptyTextWithToolsEmitsNoMessage(t *testing.T) {
	got := buildResponsesInput([]Message{
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{
			{CallID: "c1", ToolName: "instance_stop", Args: `{"id":1}`},
		}},
	})
	if len(got) != 1 {
		t.Fatalf("空正文只应剩 function_call 一项，实际 %+v", got)
	}
	if got[0].Type != "function_call" {
		t.Fatalf("唯一一项应为 function_call，实际 %q", got[0].Type)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(raw), `"message"`) {
		t.Fatalf("空正文不应产出 message 项，实际 %s", raw)
	}
}

// 线上形状冻结：一次完整工具往返的 JSON 必须逐字节符合约定。
//
// 逐字节比对才能覆盖 omitempty 的真实效果（字段是否出现、键序如何）——
// 只看结构体字段会漏掉「序列化后多/少了一个键」这类只在 wire 层致命的偏差。
func TestBuildResponsesInputToolRoundWireShape(t *testing.T) {
	raw, err := json.Marshal(buildResponsesInput([]Message{
		{Role: "assistant", ToolCalls: []ToolCall{
			{CallID: "call_1", ToolName: "instance_stop", Args: `{"id":1}`},
		}},
		{Role: "tool", ToolCallID: "call_1", ToolName: "instance_stop", Content: "已停止"},
	}))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want := `[{"type":"function_call","call_id":"call_1","name":"instance_stop","arguments":"{\"id\":1}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"已停止"}]`
	if string(raw) != want {
		t.Fatalf("线上形状不符：\n期望 %s\n实际 %s", want, raw)
	}
}
