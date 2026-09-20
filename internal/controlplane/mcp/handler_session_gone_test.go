package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 控制面重启后客户端持有的 sessionId 全部失效。
//
// 响应码必须是 404：MCP Streamable HTTP 规范（2025-06-18, Session Management）规定
// 服务器终止会话后对携带该 ID 的请求 MUST 回 404，客户端收 404 后 MUST 重新发
// InitializeRequest。此处额外带上 SESSION_GONE 错误码与中文提示，便于人工排障时
// 一眼区分「会话失效」与「路由不存在」（两者同为 404）。

func newMCPHandlerForTest(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(
		NewSessionManager(Config{
			IdleTimeout:         time.Hour,
			AbsoluteTimeout:     24 * time.Hour,
			MaxGlobalSessions:   10,
			MaxSessionsPerToken: 4,
		}),
		nil, ToolDeps{}, nil, nil,
	)
}

// newMCPContext 构造带 principal 的 gin 上下文（绕过 AgentAuth 中间件）。
func newMCPContext(method, path, body, sessionID string, principal *service.AgentPrincipal) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		c.Request.Header.Set(HeaderSessionID, sessionID)
	}
	c.Set("agentPrincipal", principal)
	return c, w
}

// TestUnknownSession_Returns404WithSessionGone 覆盖 POST（JSON-RPC 调用）路径：
// 携带未知 sessionId 必须回 404（规范要求）+ SESSION_GONE 错误码。
func TestUnknownSession_Returns404WithSessionGone(t *testing.T) {
	h := newMCPHandlerForTest(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", body, "mcps_does_not_exist", testPrincipal(1, "ci"))

	h.HandleStreamablePOST(c)

	require.Equal(t, http.StatusNotFound, w.Code, "会话失效须回 404（MCP 规范 MUST），客户端据此重新 initialize")
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "SESSION_GONE", resp["error"], "须带 SESSION_GONE 以便与「路由不存在」区分")
	assert.Contains(t, resp["message"], "initialize", "错误信息应指引客户端重新初始化")
}

// TestUnknownSession_GETReturns404 覆盖 GET（SSE 保活）路径。
func TestUnknownSession_GETReturns404(t *testing.T) {
	h := newMCPHandlerForTest(t)
	c, w := newMCPContext(http.MethodGet, "/api/v1/mcp", "", "mcps_gone", testPrincipal(1, "ci"))

	h.HandleStreamableGET(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestUnknownSession_DELETEReturns404 覆盖 DELETE（关会话）路径。
func TestUnknownSession_DELETEReturns404(t *testing.T) {
	h := newMCPHandlerForTest(t)
	c, w := newMCPContext(http.MethodDelete, "/api/v1/mcp", "", "mcps_gone", testPrincipal(1, "ci"))

	h.HandleSessionDELETE(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestUnknownSession_SSEMessageReturns404 覆盖 SSE /message 路径。
func TestUnknownSession_SSEMessageReturns404(t *testing.T) {
	h := newMCPHandlerForTest(t)
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp/message?sessionId=mcps_gone", "{}", "", testPrincipal(1, "ci"))

	h.HandleSSEMessage(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestInitializeAfterSessionGone_CreatesFreshSession 验证自愈闭环：
// 客户端收到 401 后重发 initialize（即便仍带着旧 sessionId）应重建会话并返回新 ID。
func TestInitializeAfterSessionGone_CreatesFreshSession(t *testing.T) {
	h := newMCPHandlerForTest(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	// 刻意带一个不存在的 sessionId：模拟客户端沿用失效会话。
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", body, "mcps_stale_session", testPrincipal(1, "ci"))

	h.HandleStreamablePOST(c)

	require.Equal(t, http.StatusOK, w.Code, "initialize 应无视失效 sessionId 直接建新会话")
	newID := w.Header().Get(HeaderSessionID)
	require.NotEmpty(t, newID, "必须下发新会话 ID")
	assert.NotEqual(t, "mcps_stale_session", newID)
	_, err := h.sessions.Get(newID)
	assert.NoError(t, err, "新会话应已在管理器中可查")
}

// TestValidSession_StillWorks 回归保护：有效会话不受本次改动影响。
func TestValidSession_StillWorks(t *testing.T) {
	h := newMCPHandlerForTest(t)
	s, err := h.sessions.Create(CreateParams{
		Principal: testPrincipal(1, "ci"), ClientIP: "127.0.0.1", Transport: TransportStreamableHTTP,
	})
	require.NoError(t, err)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", body, s.ID, testPrincipal(1, "ci"))

	h.HandleStreamablePOST(c)

	assert.Equal(t, http.StatusOK, w.Code, "有效会话应正常处理")
}

// TestSessionOwnership_StillForbidden 回归保护：会话归属校验仍回 403（不被 401 改动吞掉）。
func TestSessionOwnership_StillForbidden(t *testing.T) {
	h := newMCPHandlerForTest(t)
	s, err := h.sessions.Create(CreateParams{
		Principal: testPrincipal(1, "owner"), ClientIP: "127.0.0.1", Transport: TransportStreamableHTTP,
	})
	require.NoError(t, err)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	// 用另一个 token 的 principal 访问该会话。
	c, w := newMCPContext(http.MethodPost, "/api/v1/mcp", body, s.ID, testPrincipal(2, "intruder"))

	h.HandleStreamablePOST(c)

	assert.Equal(t, http.StatusForbidden, w.Code, "会话不属于当前 Token 应回 403")
}
