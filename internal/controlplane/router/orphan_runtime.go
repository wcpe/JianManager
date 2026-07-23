package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// OrphanRuntimeHandler 无主运行时列表与手动确认处置（FR-326）。
// 全组挂 admin（JWT + 平台管理员）；处置写审计（由 Tracker 内完成）。
type OrphanRuntimeHandler struct {
	svc *service.OrphanRuntimeTracker
}

// NewOrphanRuntimeHandler 创建无主运行时路由处理器。
func NewOrphanRuntimeHandler(svc *service.OrphanRuntimeTracker) *OrphanRuntimeHandler {
	return &OrphanRuntimeHandler{svc: svc}
}

// RegisterRoutes 注册 /orphan-runtimes 路由组（须挂在 admin 组下）。
func (h *OrphanRuntimeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/orphan-runtimes")
	{
		g.GET("", h.List)
		g.GET("/:uuid", h.Get)
		g.POST("/:uuid/dispose", h.Dispose)
	}
}

// List GET /orphan-runtimes?status=&activeOnly=1&limit=
// 默认 activeOnly=true（仅 pending+confirmed）；status 优先；activeOnly=0 可看全部含终态。
func (h *OrphanRuntimeHandler) List(c *gin.Context) {
	status := c.Query("status")
	activeOnly := true
	if status != "" {
		activeOnly = false
	}
	if v := c.Query("activeOnly"); v == "0" || v == "false" {
		activeOnly = false
	} else if v == "1" || v == "true" {
		activeOnly = true
	}
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := h.svc.List(status, activeOnly, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Get GET /orphan-runtimes/:uuid
func (h *OrphanRuntimeHandler) Get(c *gin.Context) {
	rec, err := h.svc.Get(c.Param("uuid"))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rec)
}

// Dispose POST /orphan-runtimes/:uuid/dispose — 管理员手动确认处置。
func (h *OrphanRuntimeHandler) Dispose(c *gin.Context) {
	userID := uint(0)
	if uid, ok := c.Get(middleware.CtxUserID); ok {
		userID, _ = uid.(uint)
	}
	rec, err := h.svc.ConfirmDispose(c.Param("uuid"), userID, c.ClientIP())
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rec)
}

func (h *OrphanRuntimeHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrOrphanRuntimeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ORPHAN_RUNTIME_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrOrphanRuntimeNotActive):
		c.JSON(http.StatusConflict, gin.H{"error": "ORPHAN_RUNTIME_NOT_ACTIVE", "message": err.Error()})
	case errors.Is(err, service.ErrOrphanWorkerOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": err.Error()})
	case errors.Is(err, service.ErrOrphanDisposeFailed):
		c.JSON(http.StatusBadGateway, gin.H{"error": "DISPOSE_FAILED", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}
