package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ServerStateHandler 服务器状态查询路由处理器（FR-076，见 ADR-016）。
// 按需经探针反向 WS 桥取回某实例的全量 Bukkit 内部状态（server/worlds/jvm/classloader/scheduler/listeners），
// 供前端「服务器状态」tab 渲染（FR-077）。轻指标走 /metrics；本端点仅前端开 tab/手动刷新时调用。
type ServerStateHandler struct {
	stateSvc *service.ServerStateService
	authz    *service.AuthzService
}

// NewServerStateHandler 创建服务器状态查询路由处理器。
func NewServerStateHandler(stateSvc *service.ServerStateService, authz *service.AuthzService) *ServerStateHandler {
	return &ServerStateHandler{stateSvc: stateSvc, authz: authz}
}

// State GET /instances/:id/server-state — 按需查询某实例全量服务器状态。
// 权限 instance:read 且实例须可访问。探针未连入/采集超时由 service 层降级（200 + connected/available=false）。
func (h *ServerStateHandler) State(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	res, err := h.stateSvc.QueryState(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询服务器状态失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// RegisterRoutes 注册服务器状态查询路由（加性追加）。
func (h *ServerStateHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/server-state", h.State)
}
