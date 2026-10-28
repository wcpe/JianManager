package assistantmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/**
 * FR-517 客户端协议层验收（spec §8 里能在此层验证的条目）。
 *
 * 重点守三件「看起来通过、实际留了口子」的事：
 *  1. 响应体超限必须**失败**而不是截断后当成功；
 *  2. 请求范围 SSE 要能读完并终止，不挂成长连接；
 *  3. 工具数超 200 必须**整次发现失败**，不静默截断。
 */

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := New(Options{
		HTTPClient: srv.Client(),
		Endpoint:   srv.URL,
	})
	if err != nil {
		t.Fatalf("构造客户端失败: %v", err)
	}
	return c
}

// rpcOK 构造一条成功响应。
func rpcOK(id int, result any) string {
	b, _ := json.Marshal(result)
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, id, b)
}

func TestDiscoverTools_JSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rpcOK(1, map[string]any{
			"tools": []map[string]any{
				{"name": "search", "description": "查找", "inputSchema": map[string]any{"type": "object"}},
			},
		})))
	}))
	defer srv.Close()

	tools, err := newTestClient(t, srv).DiscoverTools(context.Background())
	if err != nil {
		t.Fatalf("发现失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("工具解析错误: %+v", tools)
	}
}

func TestDiscoverTools_SSEResponse(t *testing.T) {
	// 请求范围 SSE：读到带 result 的帧即结束，不挂成长连接。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// 先发一个通知帧（无 id），客户端应跳过。
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: " + rpcOK(1, map[string]any{
			"tools": []map[string]any{{"name": "a"}, {"name": "b"}},
		}) + "\n\n"))
	}))
	defer srv.Close()

	tools, err := newTestClient(t, srv).DiscoverTools(context.Background())
	if err != nil {
		t.Fatalf("SSE 发现失败: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("应从 SSE 读到 2 个工具，得到 %d", len(tools))
	}
}

func TestCall_ResponseTooLargeRejected(t *testing.T) {
	// 响应体超 1 MiB → 502 语义（ErrTooLarge），**不截断后当成功**。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		big := strings.Repeat("x", DefaultResponseMax+1024)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"blob":"` + big + `"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	var out map[string]any
	err := c.call(context.Background(), "tools/call", map[string]any{}, &out)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超限响应应报 ErrTooLarge，得到 %v", err)
	}
}

func TestDiscoverTools_ToolsLimitFailsWholeDiscovery(t *testing.T) {
	// 工具数 > 200 → 整次发现失败（不静默截断、不部分提交）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tools := make([]map[string]any, 0, MaxToolsPerConnection+1)
		for i := 0; i <= MaxToolsPerConnection; i++ {
			tools = append(tools, map[string]any{"name": fmt.Sprintf("tool_%d", i)})
		}
		_, _ = w.Write([]byte(rpcOK(1, map[string]any{"tools": tools})))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).DiscoverTools(context.Background())
	if !errors.Is(err, ErrToolsLimit) {
		t.Fatalf("超上限应报 ErrToolsLimit，得到 %v", err)
	}
}

func TestDiscoverTools_ExplicitSchemaMismatchIsError(t *testing.T) {
	// 结果结构不符（tools 不是数组）必须报错，而不是静默当「没有工具」。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":"not-an-array"}}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).DiscoverTools(context.Background())
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("结构不符应报 ErrProtocol，得到 %v", err)
	}
}

func TestCall_UpstreamErrorSurfaced(t *testing.T) {
	// 上游 JSON-RPC 错误必须上抛，不能当成功。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).DiscoverTools(context.Background())
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("上游错误应报 ErrProtocol，得到 %v", err)
	}
}

func TestCall_Non2xxIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	var out map[string]any
	err := newTestClient(t, srv).call(context.Background(), "initialize", nil, &out)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("非 2xx 应报 ErrUnreachable，得到 %v", err)
	}
}

func TestNew_RequiresGuardedHTTPClient(t *testing.T) {
	// 不传 HTTP 客户端必须拒绝：否则调用方塞个裸 client 就绕过全部 SSRF 防护。
	if _, err := New(Options{Endpoint: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("缺少受保护出站客户端时应拒绝构造")
	}
}

func TestCallTool_ErrorFlagSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rpcOK(1, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "boom"}},
			"isError": true,
		})))
	}))
	defer srv.Close()

	text, err := newTestClient(t, srv).CallTool(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("isError=true 应返回错误")
	}
	// 文本仍应带回：用户需要看到上游说了什么。
	if !strings.Contains(text, "boom") {
		t.Fatalf("错误时仍应返回上游文本，得到 %q", text)
	}
}

func TestApplyAuth_BearerAndHeader(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rpcOK(1, map[string]any{})))
	}))
	defer srv.Close()

	// bearer
	c1, _ := New(Options{HTTPClient: srv.Client(), Endpoint: srv.URL, AuthStyle: "bearer", AuthValue: "tok"})
	var out map[string]any
	_ = c1.call(context.Background(), "initialize", nil, &out)
	if gotAuth != "Bearer tok" {
		t.Fatalf("bearer 注入错误: %q", gotAuth)
	}

	// header（自定义头名）
	c2, _ := New(Options{
		HTTPClient: srv.Client(), Endpoint: srv.URL,
		AuthStyle: "header", AuthHeaderName: "X-Api-Key", AuthValue: "k1",
	})
	_ = c2.call(context.Background(), "initialize", nil, &out)
	if gotCustom != "k1" {
		t.Fatalf("自定义头注入错误: %q", gotCustom)
	}

	// none：不带任何凭据
	gotAuth = "sentinel"
	c3, _ := New(Options{HTTPClient: srv.Client(), Endpoint: srv.URL, AuthStyle: "none", AuthValue: "should-not-send"})
	_ = c3.call(context.Background(), "initialize", nil, &out)
	if gotAuth == "Bearer should-not-send" {
		t.Fatal("authStyle=none 不该注入凭据")
	}
}
