package assistantmcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 响应 id 校验与「上游能力比我们多」时的行为（FR-517 能力面）。
//
// ## 为什么必须校验响应 id
//
// MCP 允许服务端在同一条流里发起**反向请求**：`sampling/createMessage`
// （借用客户端模型）、`roots/list`、`elicitation/create`。这些是「服务器问你」的
// 消息，**带自己的 id，但没有 result / error 字段**。
//
// 不校验 id 就会把它们当成本次调用的结果，而 `finishRPC` 解一个既无 result
// 又无 error 的响应会解出**零值**——于是 `tools/list` 得到空工具列表，
// 表现为「上游一切正常但工具数为 0」这种极难定位的静默失败。
//
// 这个缺陷是在真实 MCP 服务器上发现的：服务端发 `id: 999777` 的
// `sampling/createMessage`，客户端把工具数读成 0。用字符串 id 时反而「侥幸正确」
// （`rpcResponse.ID` 是 int，类型不匹配导致整帧解析失败被跳过），
// 所以用例**必须用数字 id**，否则测不到真正的问题。

// TestCall_RejectsMismatchedResponseID 响应 id 与请求 id 不一致时必须报错。
//
// 断言「报错」而不是「跳过继续等」：单个 JSON 响应已经到齐，流已结束，
// 不会再有第二条——此时沉默地返回零值比报错危险得多。
func TestCall_RejectsMismatchedResponseID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID int `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			// 回一个**不是**本次请求的 id。
			"id":     req.ID + 1000,
			"result": map[string]any{"tools": []any{}},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.DiscoverTools(t.Context()); err == nil {
		t.Fatal("响应 id 不匹配必须报错——否则会把上游的反向请求当成本次结果，静默解出零值")
	}
}

// TestDiscoverTools_SkipsServerInitiatedSampling SSE 流里的 sampling 请求必须被跳过。
//
// 这是上面那个缺陷的**直接回归用例**：上游先发一条数字 id 的
// `sampling/createMessage`，再发真正的 tools/list 结果；
// 客户端必须拿到 2 个工具，而不是 0 个。
func TestDiscoverTools_SkipsServerInitiatedSampling(t *testing.T) {
	const toolCount = 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		// ① 服务端发起的反向请求（数字 id，与自己请求的 id 不同）。
		sampling := map[string]any{
			"jsonrpc": "2.0",
			"id":      999777,
			"method":  "sampling/createMessage",
			"params": map[string]any{
				"messages":  []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "借用你的模型"}}},
				"maxTokens": 64,
			},
		}
		b1, _ := json.Marshal(sampling)
		_, _ = w.Write([]byte("event: message\ndata: " + string(b1) + "\n\n"))

		// ② 真正的响应。
		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"tools": []any{
					map[string]any{"name": "tool_a", "inputSchema": map[string]any{"type": "object"}},
					map[string]any{"name": "tool_b", "inputSchema": map[string]any{"type": "object"}},
				},
			},
		}
		b2, _ := json.Marshal(resp)
		_, _ = w.Write([]byte("event: message\ndata: " + string(b2) + "\n\n"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	tools, err := c.DiscoverTools(t.Context())
	if err != nil {
		t.Fatalf("发现工具失败: %v", err)
	}
	if len(tools) != toolCount {
		t.Fatalf("应发现 %d 个工具，实际 %d（0 说明把上游的 sampling 请求当成了结果）",
			toolCount, len(tools))
	}
}

// TestDiscoverTools_IgnoresUnknownCapabilities 上游多声明能力不应影响工具发现。
//
// MCP 里「客户端没声明某能力 = 不支持它」。上游声明 resources/prompts/sampling
// 而客户端只做 tools 时，正确行为是**照常工作**，而不是报错或降级。
func TestDiscoverTools_IgnoresUnknownCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": mcpProtocolVersion,
					"serverInfo":      map[string]any{"name": "caps-rich"},
					// 上游能力比客户端多：这三项本客户端都不实现。
					"capabilities": map[string]any{
						"tools":     map[string]any{"listChanged": false},
						"resources": map[string]any{"listChanged": false, "subscribe": false},
						"prompts":   map[string]any{"listChanged": false},
						"sampling":  map[string]any{},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"tools": []any{
						map[string]any{"name": "tool_a", "inputSchema": map[string]any{"type": "object"}},
					},
				},
			})
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Initialize(t.Context()); err != nil {
		t.Fatalf("上游多声明能力不应导致握手失败: %v", err)
	}
	tools, err := c.DiscoverTools(t.Context())
	if err != nil {
		t.Fatalf("上游多声明能力不应导致工具发现失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "tool_a" {
		t.Fatalf("应发现 tool_a，实际 %+v", tools)
	}
}
