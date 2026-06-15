package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// BotHandler Bot 路由处理器。
type BotHandler struct {
	botSvc *service.BotService
}

func NewBotHandler(botSvc *service.BotService) *BotHandler {
	return &BotHandler{botSvc: botSvc}
}

func (h *BotHandler) List(c *gin.Context) {
	var instanceID *uint
	if v := c.Query("instanceId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		instanceID = &u
	}
	var status *model.BotStatus
	if v := c.Query("status"); v != "" {
		s := model.BotStatus(v)
		status = &s
	}
	bots, err := h.botSvc.List(instanceID, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, bots)
}

func (h *BotHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	bot, err := h.botSvc.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, bot)
}

func (h *BotHandler) Create(c *gin.Context) {
	var req service.CreateBotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	bot, err := h.botSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建 Bot 失败"})
		return
	}
	c.JSON(http.StatusCreated, bot)
}

func (h *BotHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.botSvc.Delete(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

type updateBehaviorRequest struct {
	Behavior string `json:"behavior" binding:"required"`
}

func (h *BotHandler) UpdateBehavior(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req updateBehaviorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	if err := h.botSvc.UpdateBehavior(id, req.Behavior); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

func (h *BotHandler) RegisterRoutes(rg *gin.RouterGroup) {
	bots := rg.Group("/bots")
	{
		bots.GET("", h.List)
		bots.POST("", h.Create)
		bots.GET("/:id", h.Get)
		bots.DELETE("/:id", h.Delete)
		bots.POST("/:id/behavior", h.UpdateBehavior)
	}
}
