package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 外部 MCP 连接管理端点（§6）。
//
// **全部仅平台管理员**：外部连接是平台级配置，且 V1 里所有连接的工具都只对管理员
// 可见/可用（§9.1：无法证明上游按调用者区分身份时，任何共享连接都会把上游权限
// 借给普通用户）。因此这里不区分「查看」与「修改」，一律过同一个门禁。

// AssistantMCPHandler 外部连接管理处理器。
type AssistantMCPHandler struct {
	svc *service.AssistantMCPService
}

// NewAssistantMCPHandler 构造处理器。
func NewAssistantMCPHandler(svc *service.AssistantMCPService) *AssistantMCPHandler {
	return &AssistantMCPHandler{svc: svc}
}

// RegisterRoutes 注册端点（挂到已鉴权的 /assistant/admin 组）。
func (h *AssistantMCPHandler) RegisterRoutes(r *gin.RouterGroup) {
	conn := r.Group("/connections")
	conn.GET("", h.List)
	conn.POST("", h.Create)
	conn.PUT("/:id", h.Update)
	conn.DELETE("/:id", h.Delete)
	conn.POST("/:id/probe", h.Probe)
	conn.POST("/:id/discover", h.Discover)
	// 工具启停 + 风险分类。用 PATCH：只改给出的字段。
	conn.PATCH("/:id/tools/:toolId", h.UpdateTool)
	conn.GET("/:id/tools", h.ListTools)
}

// mcpOriginRequest 一条 origin 放行规则。
type mcpOriginRequest struct {
	Scheme string   `json:"scheme"`
	Host   string   `json:"host"`
	Port   int      `json:"port"`
	CIDRs  []string `json:"cidrs"`
}

// mcpConnectionRequest 创建/更新连接的请求体。
type mcpConnectionRequest struct {
	Slug           string             `json:"slug"`
	Name           string             `json:"name"`
	Endpoint       string             `json:"endpoint"`
	AuthStyle      string             `json:"authStyle"`
	AuthHeaderName string             `json:"authHeaderName"`
	AuthSource     string             `json:"authSource"`
	AuthEnv        string             `json:"authEnv"`
	AuthSecret     string             `json:"authSecret"`
	ClearCredential bool              `json:"clearCredential"`
	AllowedOrigins []mcpOriginRequest `json:"allowedOrigins"`
	Enabled        bool               `json:"enabled"`
	Version        int                `json:"version"`
}

func (r mcpConnectionRequest) toInput() service.MCPConnectionInput {
	origins := make([]service.MCPAllowedOriginInput, 0, len(r.AllowedOrigins))
	for _, o := range r.AllowedOrigins {
		origins = append(origins, service.MCPAllowedOriginInput{
			Scheme: o.Scheme, Host: o.Host, Port: o.Port, CIDRs: o.CIDRs,
		})
	}
	return service.MCPConnectionInput{
		Slug:            r.Slug,
		Name:            r.Name,
		Endpoint:        r.Endpoint,
		AuthStyle:       r.AuthStyle,
		AuthHeaderName:  r.AuthHeaderName,
		AuthSource:      r.AuthSource,
		AuthEnv:         r.AuthEnv,
		AuthValue:       r.AuthSecret,
		ClearCredential: r.ClearCredential,
		AllowedOrigins:  origins,
		Enabled:         r.Enabled,
		Version:         r.Version,
	}
}

// List GET /assistant/admin/connections
func (h *AssistantMCPHandler) List(c *gin.Context) {
	items, err := h.svc.ListConnections()
	if err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Create POST /assistant/admin/connections
func (h *AssistantMCPHandler) Create(c *gin.Context) {
	var req mcpConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体不合法"})
		return
	}
	view, err := h.svc.CreateConnection(req.toInput())
	if err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusCreated, view)
}

// Update PUT /assistant/admin/connections/:id
func (h *AssistantMCPHandler) Update(c *gin.Context) {
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	var req mcpConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体不合法"})
		return
	}
	view, err := h.svc.UpdateConnection(id, req.toInput())
	if err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Delete DELETE /assistant/admin/connections/:id
func (h *AssistantMCPHandler) Delete(c *gin.Context) {
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.DeleteConnection(id); err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Probe POST /assistant/admin/connections/:id/probe
func (h *AssistantMCPHandler) Probe(c *gin.Context) {
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	latency, err := h.svc.ProbeConnection(c.Request.Context(), id)
	if err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "latencyMs": latency.Milliseconds()})
}

// Discover POST /assistant/admin/connections/:id/discover
func (h *AssistantMCPHandler) Discover(c *gin.Context) {
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	items, err := h.svc.DiscoverTools(c.Request.Context(), id)
	if err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListTools GET /assistant/admin/connections/:id/tools
func (h *AssistantMCPHandler) ListTools(c *gin.Context) {
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	items, err := h.svc.ListTools(id)
	if err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// updateMCPToolRequest 工具启停 + 风险分类。
//
// 两个字段都是**指针**：PATCH 语义要求「没传 = 不改」，用值类型会把
// 「只想改风险」的请求顺带把 enabled 置成 false。
type updateMCPToolRequest struct {
	Enabled *bool  `json:"enabled"`
	Risk    string `json:"risk"`
}

// UpdateTool PATCH /assistant/admin/connections/:id/tools/:toolId
func (h *AssistantMCPHandler) UpdateTool(c *gin.Context) {
	connID, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	toolID, ok := parseAssistantUintParam(c, "toolId")
	if !ok {
		return
	}
	var req updateMCPToolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体不合法"})
		return
	}
	if err := h.svc.UpdateTool(connID, toolID, req.Enabled, req.Risk); err != nil {
		writeMCPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// writeMCPError 把外部连接相关错误映射成 spec §6 的错误码。
func writeMCPError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAssistantMCPSelfLoop):
		// 端点指向本平台自身：拒绝保存，否则模型可借外部工具回打自身 API。
		c.JSON(http.StatusBadRequest, gin.H{"error": "MCP_SELF_LOOP", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMCPToolsLimit):
		c.JSON(http.StatusConflict, gin.H{"error": "MCP_TOOLS_LIMIT", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMCPTooLarge):
		c.JSON(http.StatusBadGateway, gin.H{"error": "MCP_TOO_LARGE", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMCPUnreachable):
		c.JSON(http.StatusBadGateway, gin.H{"error": "MCP_UNREACHABLE", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMCPProtocol):
		c.JSON(http.StatusBadGateway, gin.H{"error": "MCP_PROTOCOL_ERROR", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMCPConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMCPNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantEncryptorMissing):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ENCRYPTOR_MISSING", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_CONFIG", "message": err.Error()})
	default:
		// 端点准入类错误（ErrBlockedTarget / ErrProxyUnsupported）落在 422：
		// 那是配置问题，不是上游故障，管理员据此改配置即可。
		if isBlockedTargetErr(err) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "EGRESS_DENIED", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// isBlockedTargetErr 判断是否为出站准入拒绝（不 import 具体错误包以免循环）。
func isBlockedTargetErr(err error) bool {
	msg := err.Error()
	return containsAny(msg, "拒绝访问", "未被放行", "仅支持 https", "不得内嵌凭据",
		"不得带 query", "地址缺少主机", "不允许代理", "受保护出站")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}

// atoiPort 解析端口（测试用）。
func atoiPort(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
