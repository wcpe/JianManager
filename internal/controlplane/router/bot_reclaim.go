package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BotReclaimHandler 失效 Bot 回收列表与手动确认（FR-460）。
// 全组挂 admin；处置写审计（由 service 内完成）。
type BotReclaimHandler struct {
	svc *service.BotReclaimService
}

// NewBotReclaimHandler 创建失效 Bot 回收路由处理器。
func NewBotReclaimHandler(svc *service.BotReclaimService) *BotReclaimHandler {
	return &BotReclaimHandler{svc: svc}
}

// RegisterRoutes 注册 /bot-reclaims 路由组（须挂在 admin 组下）。
func (h *BotReclaimHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/bot-reclaims")
	{
		g.GET("", h.List)
		g.GET("/:uuid", h.Get)
		g.POST("/:uuid/reclaim", h.Reclaim)
	}
}

// List GET /bot-reclaims?status=&activeOnly=1&limit=
// 默认 activeOnly=true（仅 pending+confirmed）；status 优先；activeOnly=0 可看全部含终态。
func (h *BotReclaimHandler) List(c *gin.Context) {
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

// Get GET /bot-reclaims/:uuid
func (h *BotReclaimHandler) Get(c *gin.Context) {
	rec, err := h.svc.Get(c.Param("uuid"))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rec)
}

// Reclaim POST /bot-reclaims/:uuid/reclaim — 管理员手动确认回收。
func (h *BotReclaimHandler) Reclaim(c *gin.Context) {
	userID := uint(0)
	if uid, ok := c.Get(middleware.CtxUserID); ok {
		userID, _ = uid.(uint)
	}
	rec, err := h.svc.ConfirmReclaim(c.Param("uuid"), userID, c.ClientIP())
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rec)
}

func (h *BotReclaimHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrBotReclaimNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "BOT_RECLAIM_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrBotReclaimNotActive):
		c.JSON(http.StatusConflict, gin.H{"error": "BOT_RECLAIM_NOT_ACTIVE", "message": err.Error()})
	case errors.Is(err, service.ErrBotReclaimNotFleetOwned):
		c.JSON(http.StatusBadRequest, gin.H{"error": "BOT_RECLAIM_NOT_FLEET_OWNED", "message": err.Error()})
	case errors.Is(err, service.ErrBotReclaimWorkerOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": err.Error()})
	case errors.Is(err, service.ErrBotReclaimDisposeFailed):
		c.JSON(http.StatusBadGateway, gin.H{"error": "DISPOSE_FAILED", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}
