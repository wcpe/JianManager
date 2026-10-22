package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 协议适配测试（FR-514）。
//
// 关注三类真实会出错的地方：
//  1. 工具参数跨多个分片到达（真实供应商几乎总是这样）；
//  2. 流被截断时**不能**把半截参数当完整调用执行；
//  3. 用量缺失时不能补 0。
//
// 全部用 httptest 起真实 HTTP 服务回放 SSE，而不是 mock 掉 http.Client——
// 后者会把「SSE 帧怎么被切开」这类最容易出错的细节一起 mock 掉。

// newSSEServer 起一个回放给定 SSE 文本的测试服务。
func newSSEServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// collect 跑一次 Stream 并收集事件。
func collect(t *testing.T, s Streamer, req Request) ([]Event, error) {
	t.Helper()
	var events []Event
	err := s.Stream(context.Background(), req, func(e Event) error {
		events = append(events, e)
		return nil
	})
	return events, err
}

func TestOpenAIChatTextAndUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"你好"}}]}`,
		"",
		`data: {"choices":[{"delta":{"content":"，世界"}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)

	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "test-key")
	events, err := collect(t, s, Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
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
	if got := text.String(); got != "你好，世界" {
		t.Fatalf("文本拼接 = %q，期望 %q", got, "你好，世界")
	}
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 11 {
		t.Fatalf("应取到真实 input tokens=11，实际 %+v", usage)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 7 {
		t.Fatalf("应取到真实 output tokens=7，实际 %+v", usage)
	}
	if end == nil || end.FinishReason != "stop" {
		t.Fatalf("应收到 end 事件且 finish_reason=stop，实际 %+v", end)
	}
}

// 参数跨分片是常态：必须等全部分片到齐、整体可解析后才产出 ToolCallEnd。
func TestOpenAIChatToolCallSplitAcrossChunks(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"instance_start","arguments":"{\"id\":"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"12}"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)

	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "")
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
		t.Fatalf("应产出 1 个完整工具调用，实际 %d", len(ends))
	}
	if ends[0].ToolName != "instance_start" {
		t.Fatalf("工具名 = %q", ends[0].ToolName)
	}
	if ends[0].Args != `{"id":12}` {
		t.Fatalf("参数拼装 = %q，期望 %q", ends[0].Args, `{"id":12}`)
	}
	if ends[0].CallID != "call_a" {
		t.Fatalf("callId = %q", ends[0].CallID)
	}
}

// 一个分片里同时出现多个调用（并列工具调用）时必须各自独立配对。
func TestOpenAIChatParallelToolCalls(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":"{}"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","function":{"name":"b","arguments":"{}"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	var names []string
	for _, e := range events {
		if e.Kind == EventToolCallEnd {
			names = append(names, e.ToolName)
		}
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("应各自配对成 a、b，实际 %v", names)
	}
}

// 参数不完整（截断在半个 JSON）时必须**整体失败**，不能当完整调用交出去。
func TestOpenAIChatRejectsIncompleteToolArgs(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":"{\"id\":12"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "")

	_, err := collect(t, s, Request{Model: "m"})
	if !errors.Is(err, ErrIncompleteToolCall) {
		t.Fatalf("参数不完整应返回 ErrIncompleteToolCall，实际 %v", err)
	}
}

// 工具参数是数组等非对象 JSON 时同样拒绝：工具契约要求对象参数。
func TestOpenAIChatRejectsNonObjectToolArgs(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":"[1,2]"}}]}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); !errors.Is(err, ErrIncompleteToolCall) {
		t.Fatalf("非对象参数应被拒绝，实际 %v", err)
	}
}

// 用量缺失时保持 nil，不能补 0：否则面板会显示「消耗 0 token」这种明确的错误信息。
func TestOpenAIChatUsageAbsentStaysNil(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"ok"}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "")

	events, err := collect(t, s, Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}
	for _, e := range events {
		if e.Kind == EventUsage && e.Usage != nil {
			if e.Usage.InputTokens != nil || e.Usage.OutputTokens != nil {
				t.Fatalf("上游未报用量时不得补值，实际 %+v", e.Usage)
			}
		}
	}
}

// 既无 [DONE] 也无 finish_reason 时说明流被截断，必须报错而非冒充完整回复。
func TestOpenAIChatTruncatedStreamFails(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"半句"}}]}`,
		"",
	}, "\n")
	srv := newSSEServer(t, body, http.StatusOK)
	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "")

	if _, err := collect(t, s, Request{Model: "m"}); err == nil {
		t.Fatal("截断的流必须报错")
	}
}

// 上游非 2xx 时错误信息要脱敏（不原样透传上游体）。
func TestOpenAIChatUpstreamError(t *testing.T) {
	srv := newSSEServer(t, `{"error":"invalid api key sk-secret-value"}`, http.StatusUnauthorized)
	s := newOpenAIChatStreamer(srv.Client(), srv.URL, "k")

	_, err := collect(t, s, Request{Model: "m"})
	if err == nil {
		t.Fatal("401 必须报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误应包含状态码，实际 %v", err)
	}
}

// SSE 解析：多行 data 用 \n 拼接，注释行忽略，[DONE] 哨兵可识别。
func TestSSEReaderFrames(t *testing.T) {
	raw := strings.Join([]string{
		": heartbeat",
		`event: message`,
		`data: line1`,
		`data: line2`,
		"",
		`data: {"k":1}`,
		"",
	}, "\n")
	r := newSSEReader(strings.NewReader(raw))

	f1, ok, err := r.Next()
	if err != nil || !ok {
		t.Fatalf("第一帧应有数据，err=%v ok=%v", err, ok)
	}
	if f1.Event != "message" || f1.Data != "line1\nline2" {
		t.Fatalf("第一帧解析错误: %+v", f1)
	}
	f2, ok, err := r.Next()
	if err != nil || !ok || f2.Data != `{"k":1}` {
		t.Fatalf("第二帧解析错误: %+v err=%v ok=%v", f2, err, ok)
	}
	if _, ok, _ = r.Next(); ok {
		t.Fatal("流结束后不应再产出帧")
	}
}

// 超长单行 data（工具参数很长）不能被 Scanner 上限截断成错误。
func TestSSEReaderLongLine(t *testing.T) {
	long := strings.Repeat("a", 200*1024)
	r := newSSEReader(strings.NewReader("data: " + long + "\n\n"))
	frame, ok, err := r.Next()
	if err != nil || !ok {
		t.Fatalf("超长帧应能读取，err=%v ok=%v", err, ok)
	}
	if len(frame.Data) != len(long) {
		t.Fatalf("超长帧被截断：len=%d，期望 %d", len(frame.Data), len(long))
	}
}
