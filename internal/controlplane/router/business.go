package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BusinessHandler JBIS 业务对接下发路由处理器（FR-116，见 ADR-026/027）。
// 把前端发起的业务动作（domain.action + payload）经探针桥下发到目标实例的业务对接层；
// CP 插件无关，仅转发信封、透传结果。探针未连/域不可用由 service 层降级（200 + available=false）。
type BusinessHandler struct {
	bizSvc *service.BusinessService
	authz  *service.AuthzService
}

// NewBusinessHandler 创建业务对接下发路由处理器。
func NewBusinessHandler(bizSvc *service.BusinessService, authz *service.AuthzService) *BusinessHandler {
	return &BusinessHandler{bizSvc: bizSvc, authz: authz}
}

// businessDispatchRequest 业务命令下发请求体。
type businessDispatchRequest struct {
	Domain  string `json:"domain"`
	Action  string `json:"action"`
	Payload string `json:"payload"` // 结构化业务参数 JSON 字符串（CP 不解析，原样下发）
}

// Dispatch POST /instances/:id/business — 向某实例下发一条业务命令并取回结果。
// 权限 instance:operate 且实例须可访问（高危写的 per-action 权限与二次确认见 FR-121/ADR-029）。
// 探针未连/域不可用由 service 降级（200 + available=false + error），不返回 5xx。
func (h *BusinessHandler) Dispatch(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req businessDispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.bizSvc.Dispatch(id, req.Domain, req.Action, req.Payload)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInstanceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		case errors.Is(err, service.ErrInvalidBusinessCommand):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 domain 或 action"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "业务命令下发失败"})
		}
		return
	}
	c.JSON(http.StatusOK, res)
}

// RegisterRoutes 注册业务对接下发路由（加性追加）。
func (h *BusinessHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/instances/:id/business", h.Dispatch)
}
