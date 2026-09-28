package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Handler MCP 传输适配（Streamable HTTP + SSE 兼容）与 JSON-RPC 分发。
//
// 协议会话已随 ADR-096 移除：Streamable HTTP 路径完全无状态——不读也不写
// Mcp-Session-Id，每请求独立处理，授权一律取 middleware.AgentAuth 当次重建的
// principal（无初始化快照，Token 吊销/降权即时生效）。
type Handler struct {
	// conns SSE 兼容路径的传输连接登记；Streamable HTTP 不使用。
	conns *SSEConnRegistry
	deps  ToolDeps
	// callLog 可选；tools/call 记 FR-390 流水。
	callLog *service.AgentCallLogService
}

// NewHandler 创建 MCP 处理器。callLog 可为 nil（不记流水）。
//
// 不持有 AgentTokenService：授权全部由 middleware.AgentAuth 在路由前置完成，
// 处理器只消费当次请求注入的 principal，避免出现第二份鉴权真源。
func NewHandler(conns *SSEConnRegistry, deps ToolDeps, callLog *service.AgentCallLogService) *Handler {
	return &Handler{conns: conns, deps: deps, callLog: callLog}
}

// RegisterMCPRoutes 挂载 MCP 传输路由（须已通过 AgentAuth 并注入 principal）。
// 路径相对 rg：POST ""（Streamable HTTP，无状态）、GET ""/DELETE ""（405）、
// GET /sse 、POST /message。
func (h *Handler) RegisterMCPRoutes(rg *gin.RouterGroup) {
	rg.POST("", h.HandleStreamablePOST)
	rg.GET("", h.HandleStreamableGET)
	rg.DELETE("", h.HandleStreamableDELETE)
	rg.GET("/sse", h.HandleSSE)
	rg.POST("/message", h.HandleSSEMessage)
}

// RegisterActivityRoutes 挂载 Token 维度活动视图（JWT + agent.mcp.read）。
func (h *Handler) RegisterActivityRoutes(rg *gin.RouterGroup) {
	rg.GET("/agent/mcp/activity", h.ListActivity)
}

// writeConnGone 回应「客户端持有的 SSE 连接已不存在」。
//
// SSE 兼容路径的连接是**传输连接**（ADR-096 决策 5），不是协议会话：id 无效即
// 404（MCP SSE 传输要求无法识别的 session id 回 404），客户端须重新连 `/sse`
// 取新 endpoint。Streamable HTTP 路径不再有此类响应（无状态）。
func writeConnGone(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{
		"error":   "CONN_GONE",
		"message": "MCP SSE 连接不存在或已关闭，请重新建立 /mcp/sse 连接",
	})
}

// ---- Streamable HTTP（无状态）----

// HandleStreamablePOST POST /api/v1/mcp — 每个 JSON-RPC 请求独立处理。
//
// 无状态语义（ADR-096）：`initialize` 只回 initializeResult()，不建会话也不下发
// Mcp-Session-Id；携带陈旧/陌生 session 头一律忽略（不拒绝）。此后每次调用都用
// 本请求重建的 principal 授权。
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

	if req.Method == "initialize" {
		if req.IsNotification() {
			c.Status(http.StatusAccepted)
			return
		}
		writeRPC(c, newResult(req.ID, initializeResult()))
		return
	}

	if req.IsNotification() {
		// 通知：处理但不返回 body（accepted）
		h.dispatch(c, c.Request.Context(), p, TransportStreamableHTTP, req, true)
		c.Status(http.StatusAccepted)
		return
	}
	writeRPC(c, h.dispatch(c, c.Request.Context(), p, TransportStreamableHTTP, req, false))
}

// HandleStreamableGET GET /api/v1/mcp — 405：无状态端点没有可保活/可回推的会话流。
//
// 需要服务端推送的客户端应改走 SSE 兼容路径 GET /api/v1/mcp/sse。
func (h *Handler) HandleStreamableGET(c *gin.Context) {
	c.Header("Allow", "POST")
	c.JSON(http.StatusMethodNotAllowed, gin.H{
		"error":   "METHOD_NOT_ALLOWED",
		"message": "MCP Streamable HTTP 为无状态端点，仅支持 POST；服务端推送请改用 /api/v1/mcp/sse",
	})
}

// HandleStreamableDELETE DELETE /api/v1/mcp — 405：无状态端点无会话可终止。
//
// Streamable HTTP 规范允许客户端用 DELETE 主动结束会话；本端点在 ADR-096 后
// 每次请求独立处理，没有会话可供终止，故按规范建议回 405（而非落到 NoRoute
// 的通用 404——那会让客户端误判为「路径写错了」而反复重试）。
func (h *Handler) HandleStreamableDELETE(c *gin.Context) {
	c.Header("Allow", "POST")
	c.JSON(http.StatusMethodNotAllowed, gin.H{
		"error":   "METHOD_NOT_ALLOWED",
		"message": "MCP Streamable HTTP 为无状态端点，无会话可终止",
	})
}

// ---- SSE 兼容 ----

// HandleSSE GET /api/v1/mcp/sse — 建立 SSE 传输连接并推送 endpoint 事件。
func (h *Handler) HandleSSE(c *gin.Context) {
	p := getPrincipal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要有效的 Agent Token（jmat_ 前缀）"})
		return
	}
	conn := h.conns.Register(p, c.ClientIP())

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		h.conns.Unregister(conn.ID)
		return
	}

	// endpoint 事件：客户端据此 POST 消息
	endpoint := "/api/v1/mcp/message?sessionId=" + conn.ID
	if !h.writeSSE(c, conn, []byte("event: endpoint\ndata: "+endpoint+"\n\n")) {
		return
	}
	flusher.Flush()

	// 保活 + 读出站消息
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	ch := conn.SSEChannel()
	clientGone := c.Request.Context().Done()
	connDone := conn.Context().Done()

	for {
		select {
		case <-clientGone:
			h.conns.Unregister(conn.ID)
			return
		case <-connDone:
			// 服务关闭/连接注销：发 comment 后结束。
			// 这里仍显式注销一次：Stop() 是「先 cancel 父 ctx、再逐个注销」，若本
			// 连接的 Register 落在 Stop 的快照之后，它就永远不会被那次遍历碰到。
			// Unregister 幂等，重复调用无副作用，代价只是让「connDone ⇒ 已注销」
			// 成为不变量，不给 map 留永久滞留项。
			h.conns.Unregister(conn.ID)
			if !h.writeSSE(c, conn, []byte(": connection closed\n\n")) {
				return
			}
			flusher.Flush()
			return
		case data, open := <-ch:
			if !open {
				return
			}
			if !h.writeSSE(c, conn, []byte("event: message\ndata: ")) ||
				!h.writeSSE(c, conn, data) ||
				!h.writeSSE(c, conn, []byte("\n\n")) {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if !h.writeSSE(c, conn, []byte(": ping\n\n")) {
				return
			}
			flusher.Flush()
		}
	}
}

// HandleSSEMessage POST /api/v1/mcp/message?sessionId=
//
// 行为与无状态化前一致（仅会话→连接登记改名）：查连接 → 校验归属 →
// dispatch → SendSSE 回推，HTTP 一律 202（结果经 SSE 流返回）。
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
	conn, ok := h.conns.Get(sessionID)
	if !ok {
		writeConnGone(c)
		return
	}
	if conn.Principal == nil || conn.Principal.TokenID != p.TokenID {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "SSE 连接不属于当前 Token"})
		return
	}

	// 进行中的 tool call 必须绑定在这条 SSE 连接上：结果要回推到 /sse 那条流，
	// 连接被踢/关闭后继续跑只是空转（旧实现用会话 ctx，无状态化时误改成请求 ctx）。
	// 两个来源任一结束都中止：请求 ctx（客户端主动断开这次 POST）与
	// conn.Context()（连接注销、服务关闭）。defer 顺序为 stop 先于 cancel——
	// stop 撤销未触发的回调，cancel 兜底释放，二者不会互相泄漏。
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	stop := context.AfterFunc(conn.Context(), cancel)
	defer stop()

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "读取请求体失败"})
		return
	}
	var req RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusAccepted, gin.H{})
		if sendErr := conn.SendSSE(mustJSON(newError(nil, -32700, "Parse error"))); sendErr != nil {
			slog.Warn("MCP SSE 发送解析错误失败", "connId", conn.ID, "error", sendErr)
		}
		return
	}

	if req.IsNotification() {
		h.dispatch(c, ctx, p, TransportSSE, req, true)
		c.Status(http.StatusAccepted)
		return
	}
	resp := h.dispatch(c, ctx, p, TransportSSE, req, false)
	if err := conn.SendSSE(mustJSON(resp)); err != nil {
		slog.Warn("MCP SSE 推送失败", "connId", conn.ID, "error", err)
	}
	c.Status(http.StatusAccepted)
}

// ---- 管理面：Token 维度活动视图 ----

// mcpActivityItem 活动视图单行（GET /api/v1/agent/mcp/activity）。
type mcpActivityItem struct {
	TokenID        uint             `json:"tokenId"`
	TokenName      string           `json:"tokenName"`
	TokenPrefix    string           `json:"tokenPrefix"`
	LastActivityAt time.Time        `json:"lastActivityAt"`
	LastAction     string           `json:"lastAction"`
	CallCount      int64            `json:"callCount"`
	FailureCount   int64            `json:"failureCount"`
	ClientIPs      []string         `json:"clientIPs"`
	Clients        map[string]int64 `json:"clients"`
}

// 活动窗口允许区间：越界回 400，避免无界扫描 agent_call_logs。
const (
	minActivityWindow = time.Hour
	maxActivityWindow = 7 * 24 * time.Hour
)

// ListActivity GET /api/v1/agent/mcp/activity?window=24h
//
// 按 Token 聚合窗口内的 MCP 活动（ADR-096 决策 6）：进程内会话列表在 CP 重启后
// 即为空，只反映「当前连着谁」；调用流水聚合反映实际行为历史，跨重启连续。
func (h *Handler) ListActivity(c *gin.Context) {
	if h.callLog == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "调用流水未启用"})
		return
	}
	window := 24 * time.Hour
	if v := c.Query("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "window 须为时长字符串（如 24h）"})
			return
		}
		if d < minActivityWindow || d > maxActivityWindow {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "window 允许区间 1h~168h"})
			return
		}
		window = d
	}
	items, err := h.callLog.ActivityByToken(window)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询 MCP 活动失败"})
		return
	}
	out := make([]mcpActivityItem, 0, len(items))
	for _, it := range items {
		ips := it.ClientIPs
		if ips == nil {
			ips = []string{}
		}
		clients := it.Clients
		if clients == nil {
			clients = map[string]int64{}
		}
		out = append(out, mcpActivityItem{
			TokenID:        it.TokenID,
			TokenName:      it.TokenName,
			TokenPrefix:    it.TokenPrefix,
			LastActivityAt: it.LastActivityAt,
			LastAction:     it.LastAction,
			CallCount:      it.CallCount,
			FailureCount:   it.FailureCount,
			ClientIPs:      ips,
			Clients:        clients,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"window":      window.String(),
		"generatedAt": time.Now().UTC(),
		"items":       out,
	})
}

// ---- 内部 ----

// dispatch 处理一条 JSON-RPC 请求。
//
// transport 仅用于流水标注（streamable_http 无状态请求 / sse 回推请求）；
// 授权取入参 p（每请求重建的 principal，见 ADR-096 决策 2）。
// ctx 由传输层传入并透传给工具执行：Streamable HTTP 用请求 ctx，SSE 用
// 「请求 ctx ∪ 连接 ctx」（见 HandleSSEMessage）。
func (h *Handler) dispatch(c *gin.Context, ctx context.Context, p *service.AgentPrincipal, transport string, req RPCRequest, notification bool) RPCResponse {
	switch req.Method {
	case "initialize":
		// SSE 兼容路径的 /message 也会走到这里：无状态化后没有任何会话要建。
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
		return newResult(req.ID, map[string]any{"tools": ToolsForPrincipal(p)})
	case "tools/call":
		if notification {
			return RPCResponse{}
		}
		return h.handleToolsCall(c, ctx, p, transport, req)
	case "shutdown":
		if notification {
			return RPCResponse{}
		}
		// 无会话可终止（ADR-096）：仅确认收到。
		return newResult(req.ID, map[string]any{})
	default:
		if notification {
			return RPCResponse{}
		}
		return newError(req.ID, -32601, "Method not found: "+req.Method)
	}
}

func (h *Handler) handleToolsCall(c *gin.Context, ctx context.Context, p *service.AgentPrincipal, transport string, req RPCRequest) RPCResponse {
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
	start := time.Now()
	result := CallTool(ctx, h.deps, p, params.Name, params.Arguments)
	h.recordToolCall(c, p, transport, params.Name, params.Arguments, result, time.Since(start))
	return newResult(req.ID, result)
}

// recordToolCall 记 FR-390 调用流水。
//
// Client 取本次请求经 AgentAuth 归一后的 X-JM-Agent-Client：运维要靠它区分调用来自
// MCP 代理还是 curl/脚本，硬编码成 mcp 会把客户端分布抹平成一根柱子。
//
// 归一化结论如实落库：缺省、超长、含非法字符或不在白名单都是 unknown，不再按
// 「MCP 路径」改写成 mcp（理由见 agentClientOf）。
func (h *Handler) recordToolCall(c *gin.Context, p *service.AgentPrincipal, transport, toolName string, args map[string]any, result ToolResult, d time.Duration) {
	if h == nil || h.callLog == nil || p == nil {
		return
	}
	action, ok := toolActionByName(toolName)
	if !ok {
		action = "mcp.tool." + toolName
	}
	targetType, targetID := toolTargetByName(toolName, args)
	capability := service.CapabilityForCallLog(p, action)
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
		TokenID:    p.TokenID,
		TokenName:  p.Name,
		Action:     action,
		Capability: capability,
		Client:     agentClientOf(c),
		Transport:  transport,
		TargetType: targetType,
		TargetID:   targetID,
		Success:    !result.IsError,
		Error:      errMsg,
		LatencyMs:  uint(ms),
		IP:         c.ClientIP(),
	})
}

// agentClientOf 取本次请求归一化后的客户端标识（FR-390）。
//
// 值由 middleware.AgentAuth 归一后写入上下文：自报 X-JM-Agent-Client 且在白名单内取其值，
// 缺省、超长、含非法字符或不在白名单一律为 unknown——与 Ops HTTP 面同一口径。
//
// 此处**不**按「MCP 路径」兜底为 mcp：本端点聚合的是该 Token 的全部调用流水（未按
// transport 过滤，同一 Token 在 MCP 与 Ops 两条路径都可用），兜底为 mcp 会把 Ops 侧
// 未自报的调用一并算作 MCP 客户端，掩盖真实来源。
func agentClientOf(c *gin.Context) string {
	return middleware.GetAgentClient(c)
}

func (h *Handler) writeSSE(c *gin.Context, conn *SSEConn, data []byte) bool {
	if _, err := c.Writer.Write(data); err != nil {
		slog.Debug("MCP SSE 写入失败", "connId", conn.ID, "error", err)
		h.conns.Unregister(conn.ID)
		return false
	}
	return true
}

func getPrincipal(c *gin.Context) *service.AgentPrincipal {
	// 经 middleware 的 ctx 契约取值（mcp → middleware → service，无环）：键名只此一处定义，
	// 避免两边各写一份字面量后静默漂移。鉴权本身仍由 AgentAuth 前置完成，本包不碰 Token。
	return middleware.GetAgentPrincipal(c)
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
