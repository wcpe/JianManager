package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Handler MCP 传输适配（Streamable HTTP + SSE）与 JSON-RPC 分发。
type Handler struct {
	sessions *SessionManager
	agent    *service.AgentTokenService
	deps     ToolDeps
	// audit 可选；踢线写审计。
	audit *service.AuditService
	// callLog 可选；tools/call 与会话 open/close/kick 记 FR-390 流水。
	callLog *service.AgentCallLogService
}

// NewHandler 创建 MCP 处理器。callLog 可为 nil（不记流水）。
func NewHandler(sessions *SessionManager, agent *service.AgentTokenService, deps ToolDeps, audit *service.AuditService, callLog *service.AgentCallLogService) *Handler {
	return &Handler{sessions: sessions, agent: agent, deps: deps, audit: audit, callLog: callLog}
}

// Sessions 返回会话管理器（管理员 API 用）。
func (h *Handler) Sessions() *SessionManager {
	return h.sessions
}

// RegisterMCPRoutes 挂载 MCP 传输路由（须已通过 AgentAuth 并注入 principal）。
// 路径相对 rg：POST/GET "" 、GET /sse 、POST /message。
func (h *Handler) RegisterMCPRoutes(rg *gin.RouterGroup) {
	rg.POST("", h.HandleStreamablePOST)
	rg.GET("", h.HandleStreamableGET)
	rg.DELETE("", h.HandleSessionDELETE)
	rg.GET("/sse", h.HandleSSE)
	rg.POST("/message", h.HandleSSEMessage)
}

// RegisterAdminRoutes 管理员会话列表/踢线（JWT + 平台管理员组）。
func (h *Handler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/agent/mcp/sessions")
	g.GET("", h.AdminListSessions)
	g.DELETE("/:id", h.AdminKickSession)
}

// ---- Streamable HTTP ----

// HandleStreamablePOST POST /api/v1/mcp — initialize 或带 session 的 JSON-RPC。
func (h *Handler) HandleStreamablePOST(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "读取请求体失败"})
		return
	}
	var req RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(c, newError(nil, -32700, "Parse error"))
		return
	}

	sessionID := c.GetHeader(HeaderSessionID)
	if sessionID == "" {
		sessionID = c.GetHeader("mcp-session-id")
	}

	// initialize：创建会话
	if req.Method == "initialize" {
		if sessionID != "" {
			// 已有会话时重复 initialize：刷新活动即可
			if s, err := h.sessions.Get(sessionID); err == nil {
				_ = h.sessions.Touch(s.ID, "")
				c.Header(HeaderSessionID, s.ID)
				writeRPC(c, newResult(req.ID, initializeResult()))
				return
			}
		}
		s, err := h.sessions.Create(CreateParams{
			Principal: p,
			ClientIP:  c.ClientIP(),
			Transport: TransportStreamableHTTP,
		})
		if err != nil {
			h.writeSessionLimit(c, err)
			return
		}
		h.recordSessionEvent(s, "mcp.session.open", c.ClientIP(), true, "")
		c.Header(HeaderSessionID, s.ID)
		if !req.IsNotification() {
			writeRPC(c, newResult(req.ID, initializeResult()))
		} else {
			c.Status(http.StatusAccepted)
		}
		return
	}

	// 后续方法需要会话
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "缺少 Mcp-Session-Id；请先 initialize"})
		return
	}
	s, err := h.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "MCP 会话不存在或已关闭"})
		return
	}
	// 会话须归属当前 Token
	if s.TokenID != p.TokenID {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "会话不属于当前 Token"})
		return
	}
	_ = h.sessions.Touch(s.ID, "")

	if req.IsNotification() {
		// 通知：处理但不返回 body（accepted）
		h.dispatch(c, s, req, true)
		c.Status(http.StatusAccepted)
		return
	}
	resp := h.dispatch(c, s, req, false)
	c.Header(HeaderSessionID, s.ID)
	writeRPC(c, resp)
}

// HandleStreamableGET GET /api/v1/mcp — 可选 SSE 流（会话保活）；无会话则 405。
func (h *Handler) HandleStreamableGET(c *gin.Context) {
	sessionID := c.GetHeader(HeaderSessionID)
	if sessionID == "" {
		sessionID = c.GetHeader("mcp-session-id")
	}
	if sessionID == "" {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "METHOD_NOT_ALLOWED", "message": "Streamable HTTP GET 需要 Mcp-Session-Id；新建会话请 POST initialize"})
		return
	}
	s, err := h.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "MCP 会话不存在或已关闭"})
		return
	}
	p := getPrincipal(c)
	if p == nil || s.TokenID != p.TokenID {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "会话不属于当前 Token"})
		return
	}
	// 简单保活：返回 JSON 会话元数据（完整 SSE 多路复用留给兼容端点）
	_ = h.sessions.Touch(s.ID, "")
	c.Header(HeaderSessionID, s.ID)
	c.JSON(http.StatusOK, gin.H{"session": s.Snapshot(), "ok": true})
}

// HandleSessionDELETE DELETE /api/v1/mcp — 客户端主动结束会话。
func (h *Handler) HandleSessionDELETE(c *gin.Context) {
	sessionID := c.GetHeader(HeaderSessionID)
	if sessionID == "" {
		sessionID = c.GetHeader("mcp-session-id")
	}
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "缺少 Mcp-Session-Id"})
		return
	}
	s, err := h.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "MCP 会话不存在或已关闭"})
		return
	}
	p := getPrincipal(c)
	if p == nil || s.TokenID != p.TokenID {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "会话不属于当前 Token"})
		return
	}
	_ = h.sessions.Kick(sessionID, "客户端关闭")
	h.recordSessionEvent(s, "mcp.session.close", c.ClientIP(), true, "客户端关闭")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- SSE 兼容 ----

// HandleSSE GET /api/v1/mcp/sse — 建立 SSE 会话并推送 endpoint 事件。
func (h *Handler) HandleSSE(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
		return
	}
	s, err := h.sessions.Create(CreateParams{
		Principal: p,
		ClientIP:  c.ClientIP(),
		Transport: TransportSSE,
	})
	if err != nil {
		h.writeSessionLimit(c, err)
		return
	}
	h.recordSessionEvent(s, "mcp.session.open", c.ClientIP(), true, "")

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header(HeaderSessionID, s.ID)
	c.Status(http.StatusOK)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		_ = h.sessions.Kick(s.ID, "不支持流式响应")
		return
	}

	// endpoint 事件：客户端据此 POST 消息
	endpoint := "/api/v1/mcp/message?sessionId=" + s.ID
	_, _ = c.Writer.Write([]byte("event: endpoint\ndata: " + endpoint + "\n\n"))
	flusher.Flush()

	// 保活 + 读出站消息
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	ch := s.SSEChannel()
	clientGone := c.Request.Context().Done()
	sessionDone := s.Context().Done()

	for {
		select {
		case <-clientGone:
			_ = h.sessions.Kick(s.ID, "客户端断开")
			return
		case <-sessionDone:
			// 踢线/超时：发 comment 后结束
			_, _ = c.Writer.Write([]byte(": session closed\n\n"))
			flusher.Flush()
			return
		case data, open := <-ch:
			if !open {
				return
			}
			_, _ = c.Writer.Write([]byte("event: message\ndata: "))
			_, _ = c.Writer.Write(data)
			_, _ = c.Writer.Write([]byte("\n\n"))
			flusher.Flush()
		case <-ticker.C:
			_ = h.sessions.Touch(s.ID, "")
			_, _ = c.Writer.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// HandleSSEMessage POST /api/v1/mcp/message?sessionId=
func (h *Handler) HandleSSEMessage(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
		return
	}
	sessionID := c.Query("sessionId")
	if sessionID == "" {
		sessionID = c.GetHeader(HeaderSessionID)
	}
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "缺少 sessionId"})
		return
	}
	s, err := h.sessions.Get(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "MCP 会话不存在或已关闭"})
		return
	}
	if s.TokenID != p.TokenID {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "会话不属于当前 Token"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "读取请求体失败"})
		return
	}
	var req RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusAccepted, gin.H{})
		_ = s.SendSSE(mustJSON(newError(nil, -32700, "Parse error")))
		return
	}
	_ = h.sessions.Touch(s.ID, "")

	if req.IsNotification() {
		h.dispatch(c, s, req, true)
		c.Status(http.StatusAccepted)
		return
	}
	resp := h.dispatch(c, s, req, false)
	if err := s.SendSSE(mustJSON(resp)); err != nil {
		slog.Warn("MCP SSE 推送失败", "sessionId", s.ID, "error", err)
	}
	c.Status(http.StatusAccepted)
}

// ---- 管理员 ----

// AdminListSessions GET /api/v1/agent/mcp/sessions
func (h *Handler) AdminListSessions(c *gin.Context) {
	list := h.sessions.List()
	if list == nil {
		list = []Snapshot{}
	}
	c.JSON(http.StatusOK, gin.H{
		"sessions": list,
		"config": gin.H{
			"idleTimeout":         h.sessions.Config().IdleTimeout.String(),
			"absoluteTimeout":     h.sessions.Config().AbsoluteTimeout.String(),
			"maxGlobalSessions":   h.sessions.Config().MaxGlobalSessions,
			"maxSessionsPerToken": h.sessions.Config().MaxSessionsPerToken,
		},
	})
}

// AdminKickSession DELETE /api/v1/agent/mcp/sessions/:id
func (h *Handler) AdminKickSession(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "id 无效"})
		return
	}
	// 踢线前取快照用于流水（Kick 后会话不可再 Get）
	var snap *Session
	if s, err := h.sessions.Get(id); err == nil {
		snap = s
	}
	if err := h.sessions.Kick(id, "管理员踢线"); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "MCP 会话不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "踢线失败"})
		return
	}
	if h.audit != nil {
		// 与 JWT 中间件 CtxUserID 键一致（"userId"）
		uid, _ := c.Get("userId")
		userID, _ := uid.(uint)
		_ = h.audit.RecordResult(userID, "mcp.session.kick", "mcp_session", id, "", c.ClientIP(), true, "")
	}
	if snap != nil {
		h.recordSessionEvent(snap, "mcp.session.kick", c.ClientIP(), true, "管理员踢线")
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- 内部 ----

func (h *Handler) dispatch(c *gin.Context, s *Session, req RPCRequest, notification bool) RPCResponse {
	_ = c
	switch req.Method {
	case "initialize":
		return newResult(req.ID, initializeResult())
	case "notifications/initialized", "initialized":
		return RPCResponse{} // 通知
	case "ping":
		if notification {
			return RPCResponse{}
		}
		return newResult(req.ID, map[string]any{})
	case "tools/list":
		if notification {
			return RPCResponse{}
		}
		return newResult(req.ID, map[string]any{"tools": ToolsForPrincipal(s.Principal)})
	case "tools/call":
		if notification {
			return RPCResponse{}
		}
		return h.handleToolsCall(s, req)
	case "shutdown":
		if notification {
			return RPCResponse{}
		}
		_ = h.sessions.Kick(s.ID, "客户端 shutdown")
		return newResult(req.ID, map[string]any{})
	default:
		if notification {
			return RPCResponse{}
		}
		return newError(req.ID, -32601, "Method not found: "+req.Method)
	}
}

func (h *Handler) handleToolsCall(s *Session, req RPCRequest) RPCResponse {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return newError(req.ID, -32602, "Invalid params: "+err.Error())
		}
	}
	if params.Name == "" {
		return newError(req.ID, -32602, "Invalid params: 缺少 name")
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}
	_ = h.sessions.Touch(s.ID, params.Name)
	start := time.Now()
	result := CallTool(s.Context(), h.deps, s.Principal, params.Name, params.Arguments)
	h.recordToolCall(s, params.Name, params.Arguments, result, time.Since(start))
	return newResult(req.ID, result)
}

func (h *Handler) recordToolCall(s *Session, toolName string, args map[string]any, result ToolResult, d time.Duration) {
	if h == nil || h.callLog == nil || s == nil || s.Principal == nil {
		return
	}
	action, ok := toolActionByName(toolName)
	if !ok {
		action = "mcp.tool." + toolName
	}
	targetType, targetID := toolTargetByName(toolName, args)
	capability := service.CapabilityForCallLog(s.Principal, action)
	errMsg := ""
	if result.IsError {
		if len(result.Content) > 0 {
			errMsg = result.Content[0].Text
		} else {
			errMsg = "tool error"
		}
	}
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	h.callLog.RecordSafe(service.AgentCallRecord{
		TokenID:    s.Principal.TokenID,
		TokenName:  s.Principal.Name,
		Action:     action,
		Capability: capability,
		Client:     service.AgentClientMCP,
		Transport:  s.Transport,
		TargetType: targetType,
		TargetID:   targetID,
		Success:    !result.IsError,
		Error:      errMsg,
		LatencyMs:  uint(ms),
		IP:         s.ClientIP,
	})
}

func (h *Handler) recordSessionEvent(s *Session, action, ip string, success bool, errMsg string) {
	if h == nil || h.callLog == nil || s == nil {
		return
	}
	tokenID := s.TokenID
	tokenName := s.TokenName
	if s.Principal != nil {
		tokenID = s.Principal.TokenID
		tokenName = s.Principal.Name
	}
	if ip == "" {
		ip = s.ClientIP
	}
	h.callLog.RecordSafe(service.AgentCallRecord{
		TokenID:    tokenID,
		TokenName:  tokenName,
		Action:     action,
		Client:     service.AgentClientMCP,
		Transport:  s.Transport,
		TargetType: "mcp_session",
		TargetID:   s.ID,
		Success:    success,
		Error:      errMsg,
		IP:         ip,
	})
}

func (h *Handler) writeSessionLimit(c *gin.Context, err error) {
	msg := err.Error()
	code := "SESSION_LIMIT"
	if errors.Is(err, ErrSessionLimitGlobal) {
		msg = "MCP 全局并发会话已达上限，请稍后重试或联系管理员"
	} else if errors.Is(err, ErrSessionLimitToken) {
		msg = "该 Token 的 MCP 并发会话已达上限，请关闭空闲会话后重试"
	} else {
		code = "BAD_REQUEST"
	}
	c.JSON(http.StatusTooManyRequests, gin.H{"error": code, "message": msg})
}

func getPrincipal(c *gin.Context) *service.AgentPrincipal {
	// 与 middleware 解耦：用 key 字符串避免循环依赖时由 router 注入
	v, ok := c.Get("agentPrincipal")
	if !ok {
		return nil
	}
	p, _ := v.(*service.AgentPrincipal)
	return p
}

func writeRPC(c *gin.Context, resp RPCResponse) {
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusOK, resp)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal"}}`)
	}
	return b
}

// ExtractBearerToken 从 Authorization 提取 Bearer token（测试/辅助）。
func ExtractBearerToken(auth string) string {
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}
