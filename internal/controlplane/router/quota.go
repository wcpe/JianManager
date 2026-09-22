package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// QuotaHandler 实例运行期配额路由处理器（FR-467）。
// 只读视图：限额来源 + 实时用量 + 强制状态。限额的写入沿用既有路径
// （实例级走 PUT /instances/:id 的 CPULimit/MemLimitMB/DiskLimitMB，组级走组配额端点）。
type QuotaHandler struct {
	svc   *service.QuotaEnforcer
	authz *service.AuthzService
}

// NewQuotaHandler 创建配额路由处理器。
func NewQuotaHandler(svc *service.QuotaEnforcer, authz *service.AuthzService) *QuotaHandler {
	return &QuotaHandler{svc: svc, authz: authz}
}

// Get GET /instances/:id/quota — 实例配额与实时用量（含强制状态）。
func (h *QuotaHandler) Get(c *gin.Context) {
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
	status, err := h.svc.Status(id)
	if err != nil {
		if errors.Is(err, service.ErrQuotaInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询配额失败"})
		return
	}
	c.JSON(http.StatusOK, status)
}

// RegisterRoutes 注册配额路由（加性追加）。
func (h *QuotaHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/quota", h.Get)
}
