package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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

// RegisterRoutes 注册 /bot-reclaims 路由组。
//
// readGroup 承载列表/详情（bot.read 或 bot.manage 均可）；writeGroup 承载处置写路由
// （F5：必须仅挂 bot.manage，避免「仅持 bot.read 即可触发停用」）。
func (h *BotReclaimHandler) RegisterRoutes(readGroup, writeGroup *gin.RouterGroup) {
	if readGroup == nil {
		return
	}
	g := readGroup.Group("/bot-reclaims")
	{
		g.GET("", h.List)
		g.GET("/:uuid", h.Get)
	}
	if writeGroup != nil {
		writeGroup.Group("/bot-reclaims").POST("/:uuid/reclaim", h.Reclaim)
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
//
// F5：要求 body 精确确认（{"confirm":"<uuid>"}），避免误触发放置；写路由仅挂 bot.manage。
func (h *BotReclaimHandler) Reclaim(c *gin.Context) {
	uuid := c.Param("uuid")
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != uuid {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "BOT_RECLAIM_CONFIRM_REQUIRED",
			"message": `须在请求体回传 {"confirm":"<uuid>"} 且与路径一致以确认回收`,
		})
		return
	}
	userID := uint(0)
	if uid, ok := c.Get(middleware.CtxUserID); ok {
		userID, _ = uid.(uint)
	}
	rec, err := h.svc.ConfirmReclaim(uuid, userID, c.ClientIP())
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
