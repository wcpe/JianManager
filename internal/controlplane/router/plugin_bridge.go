package router

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// PluginBridgeHandler 插件桥的 Control Plane HTTP 路由（FR-103 / ADR-012）：
// 实例级 token 签发、插件事件 SSE 代理、指令下发、连接状态查询。
type PluginBridgeHandler struct {
	svc   *service.PluginBridgeService
	authz *service.AuthzService
}

// NewPluginBridgeHandler 创建插件桥路由处理器。
func NewPluginBridgeHandler(svc *service.PluginBridgeService, authz *service.AuthzService) *PluginBridgeHandler {
	return &PluginBridgeHandler{svc: svc, authz: authz}
}

// RegisterRoutes 注册插件桥路由。
func (h *PluginBridgeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/instances/:id/plugin-token", h.IssueToken)
	rg.POST("/instances/:id/plugin-command", h.SendCommand)
	rg.GET("/plugins", h.ListConnections)
	rg.GET("/plugins/events", h.StreamEvents)
}

// IssueToken 为实例签发插件桥连接 token（运维写入插件配置）。需对实例有访问权。
func (h *PluginBridgeHandler) IssueToken(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	token, err := h.svc.IssueToken(id, c.Request.Host, secure)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, token)
}

// sendCommandRequest 指令下发请求体。
type sendCommandRequest struct {
	Action   string `json:"action" binding:"required"`
	ArgsJSON string `json:"argsJson"`
}

// SendCommand 把指令下发给实例当前连入的插件（踢/封/whitelist 等）。需对实例有管理权。
func (h *PluginBridgeHandler) SendCommand(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req sendCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 action"})
		return
	}

	if err := h.svc.SendCommand(id, req.Action, req.ArgsJSON); err != nil {
		// 实例无插件连入 / Worker 未连接均为业务可达失败，用 409 区别于 5xx。
		c.JSON(http.StatusConflict, gin.H{"error": "PLUGIN_COMMAND_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// ListConnections 返回当前已知的插件连接状态（按当前用户可访问实例过滤）。
func (h *PluginBridgeHandler) ListConnections(c *gin.Context) {
	conns := h.svc.Connections()
	access := getAccess(c)
	out := make([]service.PluginConnection, 0, len(conns))
	for _, conn := range conns {
		// 仅返回当前用户有权访问的实例插件连接（跨组隔离）。
		if conn.InstanceID != 0 && access != nil {
			if ok, _ := h.authz.CanAccessInstance(access, conn.InstanceID); !ok {
				continue
			}
		}
		out = append(out, conn)
	}
	c.JSON(http.StatusOK, out)
}

// StreamEvents SSE 推送插件桥事件（连接/断开/玩家事件）。
func (h *PluginBridgeHandler) StreamEvents(c *gin.Context) {
	ch, unsub := h.svc.Subscribe()
	defer unsub()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	fmt.Fprintf(c.Writer, "event: connected\ndata: {}\n\n")
	c.Writer.Flush()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			// data 字段为事件载荷 JSON 原文，整体再包一层便于前端解析。
			data := fmt.Sprintf(`{"instanceUuid":%q,"type":%q,"data":%s,"timestamp":%d}`,
				evt.InstanceUUID, evt.Type, jsonOrEmptyObject(evt.Data), evt.Timestamp)
			if _, err := fmt.Fprintf(c.Writer, "event: plugin\ndata: %s\n\n", data); err != nil {
				slog.Debug("插件桥 SSE 写入失败", "err", err)
				return
			}
			c.Writer.Flush()
		}
	}
}

// jsonOrEmptyObject 保证嵌入 SSE 的 data 是合法 JSON，空值时回退空对象。
func jsonOrEmptyObject(raw string) string {
	if raw == "" {
		return "{}"
	}
	return raw
}
