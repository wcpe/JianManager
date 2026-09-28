package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Streamable HTTP 无状态化（ADR-096）的契约测试。
//
// 覆盖：不读也不写 Mcp-Session-Id、无会话可用、陈旧/陌生 session 头被忽略而非拒绝、
// GET 改 405、无凭据仍 401；以及 SSE 兼容路径（/message）的连接语义保持不变。

func newStatelessHandler() (*Handler, *SSEConnRegistry) {
	reg := NewSSEConnRegistry()
	return NewHandler(reg, nil, ToolDeps{}, nil), reg
}

// newMCPContext 构造带 principal 的 gin 上下文（绕过 AgentAuth 中间件）。
// principal 为 nil 时不注入，等价于「未鉴权」。
func newMCPContext(method, path, body, sessionID string, principal *service.AgentPrincipal) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		c.Request.Header.Set(HeaderSessionID, sessionID)
	}
	if principal != nil {
		c.Set("agentPrincipal", principal)
	}
	return c, w
}

func decodeRPCResult(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var resp map[string]any
	require.NoError(t, json.Unmarshal(body, &resp))
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "响应应含 result 对象: %s", string(body))
	return result
}

// TestStreamablePOST_SessionlessCalls 无会话可连续调用：不携带也拿不到 Mcp-Session-Id。
func TestStreamablePOST_SessionlessCalls(t *testing.T) {
	h, _ := newStatelessHandler()
	p := testPrincipal(1, "ci")

	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "", p)
	h.HandleStreamablePOST(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, w.Header().Get(HeaderSessionID), "无状态路径不得下发会话头")
	assert.NotEmpty(t, decodeRPCResult(t, w.Body.Bytes())["protocolVersion"])

	// 未先 initialize、也不带任何 session 头，tools/list 仍可用。
	c, w = newMCPContext(http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, "", p)
	h.HandleStreamablePOST(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, w.Header().Get(HeaderSessionID))
	assert.NotNil(t, decodeRPCResult(t, w.Body.Bytes())["tools"])
}

// TestStreamablePOST_StaleSessionHeaderIgnored 携带陈旧 session id 不被拒绝。
//
// 这正是无状态化要修掉的故障面：会话语义下客户端会收到 404 SESSION_GONE 并永久卡死。
func TestStreamablePOST_StaleSessionHeaderIgnored(t *testing.T) {
	h, reg := newStatelessHandler()
	p := testPrincipal(1, "ci")

	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, "mcps_stale_session", p)
	h.HandleStreamablePOST(c)

	require.Equal(t, http.StatusOK, w.Code, "陈旧会话头应被忽略，而不是 404/403")
	assert.Empty(t, w.Header().Get(HeaderSessionID))
	_, ok := reg.Get("mcps_stale_session")
	assert.False(t, ok, "不得因请求头而登记连接")
}

// TestStreamableGET_Returns405WithAllowPOST GET /api/v1/mcp 无会话可保活。
func TestStreamableGET_Returns405WithAllowPOST(t *testing.T) {
	h, _ := newStatelessHandler()

	c, w := newMCPContext(http.MethodGet, "/api/v1/mcp", "", "", testPrincipal(1, "ci"))
	h.HandleStreamableGET(c)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, "POST", w.Header().Get("Allow"))
	assert.Contains(t, w.Body.String(), "METHOD_NOT_ALLOWED")
}

// TestStreamablePOST_MissingPrincipalUnauthorized 无凭据仍回 401（不因无状态化而放宽）。
func TestStreamablePOST_MissingPrincipalUnauthorized(t *testing.T) {
	h, _ := newStatelessHandler()

	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "", nil)
	h.HandleStreamablePOST(c)

	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

// TestStreamablePOST_InitializeNotificationAccepted initialize 作为通知时不返回 body。
func TestStreamablePOST_InitializeNotificationAccepted(t *testing.T) {
	h, _ := newStatelessHandler()

	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","method":"initialize"}`, "", testPrincipal(1, "ci"))
	h.HandleStreamablePOST(c)

	// 直接调 handler（未经 gin 引擎收尾）时 recorder 不会落状态码，故读 gin 侧状态。
	assert.Equal(t, http.StatusAccepted, c.Writer.Status())
	assert.Empty(t, w.Body.Bytes())
}

// ---- SSE 兼容路径（传输连接语义不变）----

// sseTestWriter 线程安全的 SSE 响应写入器（httptest.ResponseRecorder 不支持并发读写）。
type sseTestWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	hdr http.Header
}

func newSSETestWriter() *sseTestWriter { return &sseTestWriter{hdr: http.Header{}} }

func (w *sseTestWriter) Header() http.Header { return w.hdr }

func (w *sseTestWriter) WriteHeader(int) {}

func (w *sseTestWriter) Flush() {}

func (w *sseTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *sseTestWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// waitForSSEEndpoint 轮询 SSE 响应体，返回 endpoint 事件里的连接 id（超时即失败）。
func waitForSSEEndpoint(t *testing.T, w *sseTestWriter) string {
	t.Helper()
	const marker = "sessionId="
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if body := w.body(); strings.Contains(body, marker) {
			rest := body[strings.Index(body, marker)+len(marker):]
			if end := strings.IndexAny(rest, "\n\r"); end > 0 {
				return rest[:end]
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("未等到 SSE endpoint 事件: %q", w.body())
	return ""
}

// TestHandleSSE_RegistersAndUnregistersConn /sse 建连即登记、客户端断开即注销。
//
// 连接 id 仍编进 endpoint URL（`/api/v1/mcp/message?sessionId=mcps_...`），
// 但不再下发 Mcp-Session-Id 响应头（协议会话已移除，ADR-096）。
func TestHandleSSE_RegistersAndUnregistersConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := NewSSEConnRegistry()
	h := NewHandler(reg, nil, ToolDeps{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newSSETestWriter()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/sse", nil).WithContext(ctx)
	c.Set("agentPrincipal", testPrincipal(1, "ci"))

	go h.HandleSSE(c)

	connID := waitForSSEEndpoint(t, w)
	require.True(t, strings.HasPrefix(connID, "mcps_"), "连接 id 应保持 mcps_ 前缀: %s", connID)
	conn, ok := reg.Get(connID)
	require.True(t, ok, "建连后应已登记，/message 才能按 id 回推")
	assert.Equal(t, "ci", conn.Principal.Name)
	assert.Empty(t, w.Header().Get(HeaderSessionID), "SSE 建连也不再下发协议会话头")

	// 客户端断开：处理循环退出并注销连接（无残留登记）。
	cancel()
	require.Eventually(t, func() bool {
		_, ok := reg.Get(connID)
		return !ok
	}, 2*time.Second, 5*time.Millisecond, "客户端断开后连接应已注销")
}

// TestSSEMessage_UnknownConnReturns404 无法识别的连接 id 仍回 404（SSE 传输要求）。
func TestSSEMessage_UnknownConnReturns404(t *testing.T) {
	h, _ := newStatelessHandler()

	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp/message?sessionId=mcps_gone", "{}", "", testPrincipal(1, "ci"))
	h.HandleSSEMessage(c)

	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "CONN_GONE")
}

// TestSSEMessage_OwnershipStillForbidden 连接归属校验仍回 403。
func TestSSEMessage_OwnershipStillForbidden(t *testing.T) {
	h, reg := newStatelessHandler()
	conn := reg.Register(testPrincipal(1, "owner"), "127.0.0.1")

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp/message?sessionId="+conn.ID, body, "", testPrincipal(2, "intruder"))
	h.HandleSSEMessage(c)

	assert.Equal(t, http.StatusForbidden, w.Code, "连接不属于当前 Token 应回 403")
}

// TestSSEMessage_PushesResponseToStream /message 的响应经 SSE 流回推，HTTP 只回 202。
func TestSSEMessage_PushesResponseToStream(t *testing.T) {
	h, reg := newStatelessHandler()
	conn := reg.Register(testPrincipal(1, "owner"), "127.0.0.1")

	body := `{"jsonrpc":"2.0","id":7,"method":"ping"}`
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp/message?sessionId="+conn.ID, body, "", testPrincipal(1, "owner"))
	h.HandleSSEMessage(c)

	// 直接调 handler（未经 gin 引擎收尾）时 recorder 不会落状态码，故读 gin 侧状态。
	require.Equal(t, http.StatusAccepted, c.Writer.Status())
	assert.Empty(t, w.Body.Bytes())
	select {
	case data := <-conn.SSEChannel():
		var resp map[string]any
		require.NoError(t, json.Unmarshal(data, &resp))
		assert.EqualValues(t, 7, resp["id"])
		assert.NotNil(t, resp["result"])
	default:
		t.Fatal("响应应已回推到 SSE 通道")
	}
}

// TestHandlerRegistersNoDELETERoute DELETE 不再注册（无会话可终止）。
func TestHandlerRegistersNoDELETERoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h, _ := newStatelessHandler()
	h.RegisterMCPRoutes(r.Group("/api/v1/mcp"))

	routes := map[string]bool{}
	for _, route := range r.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	assert.True(t, routes["POST /api/v1/mcp"])
	assert.True(t, routes["GET /api/v1/mcp/sse"])
	assert.True(t, routes["POST /api/v1/mcp/message"])
	assert.False(t, routes["DELETE /api/v1/mcp"], "DELETE 不应注册")
}
