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

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Streamable HTTP 无状态化（ADR-096）的契约测试。
//
// 覆盖：不读也不写 Mcp-Session-Id、无会话可用、陈旧/陌生 session 头被忽略而非拒绝、
// GET/DELETE 改 405、无凭据仍 401；SSE 兼容路径（/message）的连接语义保持不变；
// 以及传输层的生命周期归属——进行中的 tool call 在 SSE 路径上挂在**连接 ctx**上
// （POST 的请求 ctx 不参与）、在无状态路径上挂在请求 ctx 上；
// 调用流水的 client 取自 X-JM-Agent-Client（FR-390）。

func newStatelessHandler() (*Handler, *SSEConnRegistry) {
	reg := NewSSEConnRegistry()
	return NewHandler(reg, ToolDeps{}, nil), reg
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
	h := NewHandler(reg, ToolDeps{}, nil)

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

// TestStreamableDELETE_Returns405 DELETE /api/v1/mcp 回 405 而非落到 NoRoute 的通用 404。
//
// Streamable HTTP 规范允许客户端用 DELETE 主动结束会话；本端点无状态，没有会话可终止，
// 按规范建议回 405。若让请求落到 NoRoute，客户端会看到 NOT_FOUND 并误判为「路径写错了」
// 而反复重试——这正是 MR 评审要求修掉的契约缺口。
func TestStreamableDELETE_Returns405(t *testing.T) {
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
	assert.True(t, routes["DELETE /api/v1/mcp"], "DELETE 应显式注册为 405，而非落到 NoRoute")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mcp", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code, w.Body.String())
	assert.Equal(t, "POST", w.Header().Get("Allow"))
	assert.Contains(t, w.Body.String(), "METHOD_NOT_ALLOWED")
}

// ---- 传输层生命周期：进行中的 tool call 挂在哪些 ctx 上 ----

// registerBlockingTestTool 注册一个阻塞式假工具（仅测试用），返回：
// started —— 执行器已进入；done —— 执行器结束时的 ctx 结果（nil 表示未被取消）。
//
// 执行器只在 ctx.Done() 或兜底超时后返回，因此「工具是否在中途被取消」可以被直接观测，
// 而不必去猜某个真实工具的耗时。兜底超时故意长于各用例的等待上限：若取消没有传播过来，
// 用例会先超时失败，而不是被兜底超时掩盖。
func registerBlockingTestTool(t *testing.T, name string) (started chan struct{}, done chan error) {
	t.Helper()
	started = make(chan struct{})
	done = make(chan error, 1)
	registerToolSpecs(toolSpec{
		Def:    ToolDef{Name: name, Description: "测试用阻塞工具", InputSchema: map[string]any{"type": "object"}},
		Action: service.AgentActionWhoami,
		Exec: func(ctx context.Context, _ ToolDeps, _ *service.AgentPrincipal, _ string, _ map[string]any) ToolResult {
			close(started)
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return toolErr("MCP 调用已取消")
			case <-time.After(5 * time.Second):
				done <- nil
				return toolOK(map[string]any{"ok": true})
			}
		},
	})
	t.Cleanup(func() {
		// allToolSpecs 是包级目录，用完必须摘掉，否则会污染同包其它用例的工具集合断言。
		for i := range allToolSpecs {
			if allToolSpecs[i].Def.Name == name {
				allToolSpecs = append(allToolSpecs[:i], allToolSpecs[i+1:]...)
				return
			}
		}
	})
	return started, done
}

// toolCallBody 拼一条 tools/call 请求体。
func toolCallBody(name string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
}

// waitToolStarted 等执行器进入（超时即判定工具没跑起来）。
func waitToolStarted(t *testing.T, started chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("假工具未开始执行")
	}
}

// TestSSEMessage_ConnUnregisterCancelsInFlightToolCall SSE 连接注销时取消进行中的 tool call。
//
// 旧实现用会话 ctx，无状态化时被改成 /message 的请求 ctx——请求 ctx 在「发完 POST 就断开、
// 只从 /sse 读结果」这类 SSE 常规用法下会提前结束，于是连接还在、结果却推不回去（行为回退）。
// 这里锁住恢复后的语义：取消源是连接 ctx，连接注销（客户端断开、Stop 收尾）必须把取消传下去。
//
// 断言的是**取消已传播**，不是「工具必然立刻停手」：本用例的假工具显式消费 ctx，故取消可见；
// 真工具多数不读 ctx（tools.go 只在入口做 select 检查，各家 tool 实现基本 `_ = ctx`），
// 已进入执行的调用可能照旧跑到结束。连接注销能保证的是：ctx 已 done，尚未进入 CallTool
// 的调用会被入口直接拦下，且结果不再可能推到已关闭的流上。
func TestSSEMessage_ConnUnregisterCancelsInFlightToolCall(t *testing.T) {
	const toolName = "zz_test_blocking_conn_cancel"
	gin.SetMode(gin.TestMode)
	reg := NewSSEConnRegistry()
	h := NewHandler(reg, ToolDeps{}, nil)
	started, done := registerBlockingTestTool(t, toolName)

	conn := reg.Register(testPrincipal(1, "owner"), "127.0.0.1")
	c, _ := newMCPContext(http.MethodPost, "/api/v1/mcp/message?sessionId="+conn.ID,
		toolCallBody(toolName), "", testPrincipal(1, "owner"))

	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		h.HandleSSEMessage(c)
	}()

	waitToolStarted(t, started)
	// 请求 ctx 全程未取消，所以这里唯一可能的取消源就是连接 ctx。
	reg.Unregister(conn.ID)

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, "连接注销后进行中的 tool call 应收到取消")
	case <-time.After(2 * time.Second):
		t.Fatal("连接注销后进行中的 tool call 未被取消（取消语义回退）")
	}
	<-httpDone
}

// TestSSEMessage_RequestCancelDoesNotCancelInFlightToolCall SSE 路径的取消只绑连接，不绑 POST 请求。
//
// 与上一条互补的另一半：把请求 ctx 并进工具 ctx 会让「发完 POST 就断开、只从 /sse 读结果」的
// 常规 SSE 客户端（以及 POST 因长耗时工具而超时的情形）中途取消一个本可继续完成、结果本可
// 推到流上的调用。故这里断言取消**没有**传播；收尾时再注销连接，确认真正的边界（连接）
// 依然能把取消传下去——两条用例一起把「取消源只有连接 ctx」钉死。
func TestSSEMessage_RequestCancelDoesNotCancelInFlightToolCall(t *testing.T) {
	const toolName = "zz_test_blocking_sse_request_cancel_ignored"
	gin.SetMode(gin.TestMode)
	reg := NewSSEConnRegistry()
	h := NewHandler(reg, ToolDeps{}, nil)
	started, done := registerBlockingTestTool(t, toolName)

	conn := reg.Register(testPrincipal(1, "owner"), "127.0.0.1")
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	c, _ := newMCPContext(http.MethodPost, "/api/v1/mcp/message?sessionId="+conn.ID,
		toolCallBody(toolName), "", testPrincipal(1, "owner"))
	c.Request = c.Request.WithContext(reqCtx)

	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		h.HandleSSEMessage(c)
	}()

	waitToolStarted(t, started)
	cancelReq()

	// 窗口取 300ms：并上请求 ctx 的实现会在毫秒级就把取消传过去（必然落在窗口内），
	// 而只绑连接的实现要等 5s 兜底超时才会结束——两侧余量都足够，不是靠运气取胜。
	select {
	case err := <-done:
		t.Fatalf("POST 请求 ctx 取消不得中止在途 tool call（结果仍要回推到 SSE 流上），实际取消原因: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	// 收尾并验证真正的取消源：注销连接后取消必须传下去（否则本用例会以 `done` 从未就绪而失败）。
	reg.Unregister(conn.ID)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, "连接注销仍应把取消传给在途调用")
	case <-time.After(2 * time.Second):
		t.Fatal("连接注销后进行中的 tool call 未被取消")
	}
	<-httpDone
}

// TestStreamablePOST_RequestCancelCancelsInFlightToolCall 无状态路径仍只受请求 ctx 约束。
//
// 与上两条互补：Streamable HTTP 没有连接可绑，客户端断开（请求 ctx 取消）必须照旧中止
// 工具调用——SSE 路径改绑连接 ctx 不能顺手把这条路径的取消源也弄丢。
func TestStreamablePOST_RequestCancelCancelsInFlightToolCall(t *testing.T) {
	const toolName = "zz_test_blocking_request_cancel"
	gin.SetMode(gin.TestMode)
	h := NewHandler(NewSSEConnRegistry(), ToolDeps{}, nil)
	started, done := registerBlockingTestTool(t, toolName)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	c, _ := newMCPContext(http.MethodPost, "/api/v1/mcp", toolCallBody(toolName), "", testPrincipal(1, "ci"))
	c.Request = c.Request.WithContext(reqCtx)

	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		h.HandleStreamablePOST(c)
	}()

	waitToolStarted(t, started)
	cancelReq()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled, "请求 ctx 取消后进行中的 tool call 应收到取消")
	case <-time.After(2 * time.Second):
		t.Fatal("请求 ctx 取消后进行中的 tool call 未被取消")
	}
	<-httpDone
}

// TestHandleSSE_ConnCloseUnregistersEntry connDone 分支也必须注销连接。
//
// Stop() 是「先 cancel 父 ctx、再逐个注销」：若某连接的 Register 落在 Stop 的快照
// 之后，它就永远不会被那次遍历碰到；此时唯一能收尾的就是 /sse 处理循环自己。
// 这里直接 Close 连接（不注销）复现该窗口，断言登记表里不留残项。
func TestHandleSSE_ConnCloseUnregistersEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := NewSSEConnRegistry()
	h := NewHandler(reg, ToolDeps{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newSSETestWriter()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/sse", nil).WithContext(ctx)
	c.Set(middleware.CtxAgentPrincipal, testPrincipal(1, "ci"))

	go h.HandleSSE(c)

	connID := waitForSSEEndpoint(t, w)
	conn, ok := reg.Get(connID)
	require.True(t, ok, "建连后应已登记")
	// 只关连接、不注销：请求 ctx 仍活着，处理循环只能从 connDone 分支退出。
	conn.Close()

	require.Eventually(t, func() bool {
		_, ok := reg.Get(connID)
		return !ok
	}, 2*time.Second, 5*time.Millisecond, "connDone 分支也应注销，否则连接会永久滞留在 map 中")
}

// ---- 调用流水的 client 标识（FR-390）----

// TestToolsCall_RecordsClientFromAgentClientHeader 流水记录请求头声明的客户端。
//
// 走真实链路（AgentAuth 归一 + handler 落库）：硬编码 mcp 会把活动视图的客户端
// 分布抹平，运维分不清调用来自 MCP 代理还是 curl/脚本。
func TestToolsCall_RecordsClientFromAgentClientHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newStatelessTestDB(t)
	agentSvc, plain := newStatelessTestToken(t, db)
	h := NewHandler(NewSSEConnRegistry(), ToolDeps{}, service.NewAgentCallLogService(db))

	r := gin.New()
	h.RegisterMCPRoutes(r.Group("/api/v1/mcp", middleware.AgentAuth(agentSvc)))

	call := func(clientHeader string) model.AgentCallLog {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp",
			strings.NewReader(toolCallBody("agent_whoami")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+plain)
		if clientHeader != "" {
			req.Header.Set(middleware.HeaderAgentClient, clientHeader)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())

		var row model.AgentCallLog
		require.NoError(t, db.Order("id desc").First(&row).Error, "tools/call 应落一条流水")
		return row
	}

	curl := call(service.AgentClientCurl)
	assert.Equal(t, service.AgentClientCurl, curl.Client, "client 应取请求头归一后的取值，而不是硬编码 mcp")
	assert.Equal(t, TransportStreamableHTTP, curl.Transport)
	assert.Equal(t, service.AgentActionWhoami, curl.Action)
	assert.True(t, curl.Success)

	// 白名单外的取值由 AgentAuth 归一为 unknown，handler 必须原样落库：
	// 无论「已被归一为 unknown」还是「上下文里根本没有 client」，都不回退 mcp（理由见 agentClientOf）。
	garbage := call("not-in-allowlist")
	assert.Equal(t, service.AgentClientUnknown, garbage.Client,
		"中间件已归一为 unknown，handler 不得再改写成 mcp")
}

// TestToolsCall_ClientUnreportedIsUnknown 未经 AgentAuth 注入 client 时归 unknown。
//
// 与 Ops HTTP 面同一口径（FR-390）：缺省、不在白名单一律归 unknown。handler 不按
// 「MCP 路径」兜底为 mcp——本端点同样接受 curl/脚本直连，硬编码会把这类未自报的调用
// 记成 MCP 客户端，抹平活动视图的客户端分布（理由详见 agentClientOf）。
func TestToolsCall_ClientUnreportedIsUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newStatelessTestDB(t)
	h := NewHandler(NewSSEConnRegistry(), ToolDeps{}, service.NewAgentCallLogService(db))

	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", toolCallBody("agent_whoami"), "", testPrincipal(1, "ci"))
	h.HandleStreamablePOST(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var row model.AgentCallLog
	require.NoError(t, db.Order("id desc").First(&row).Error)
	assert.Equal(t, service.AgentClientUnknown, row.Client, "无 client 上下文时归 unknown")
}
