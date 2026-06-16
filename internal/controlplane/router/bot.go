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
	authz  *service.AuthzService
}

// NewBotHandler 创建 Bot 路由处理器。
func NewBotHandler(botSvc *service.BotService, authz *service.AuthzService) *BotHandler {
	return &BotHandler{botSvc: botSvc, authz: authz}
}

// List Bot 列表。
// 平台管理员返回全部；其余按其可访问实例过滤。
func (h *BotHandler) List(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

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

	// 非平台管理员按可访问实例过滤
	if !access.IsPlatformAdmin {
		filtered := make([]model.Bot, 0, len(bots))
		for _, b := range bots {
			ok, err := h.authz.CanAccessInstance(access, b.InstanceID)
			if err != nil || !ok {
				continue
			}
			filtered = append(filtered, b)
		}
		bots = filtered
	}

	c.JSON(http.StatusOK, bots)
}

// Get Bot 详情。
func (h *BotHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanAccessBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
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

// Create 创建 Bot。非平台管理员只能为自己可访问实例创建。
func (h *BotHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req service.CreateBotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if !access.IsPlatformAdmin {
		ok, err := h.authz.CanAccessInstance(access, req.InstanceID)
		if err != nil || !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权为该实例创建 Bot"})
			return
		}
	}

	bot, err := h.botSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建 Bot 失败"})
		return
	}
	c.JSON(http.StatusCreated, bot)
}

// Delete 删除 Bot。
func (h *BotHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanManageBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
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

// UpdateBehavior 切换 Bot 行为模式。
func (h *BotHandler) UpdateBehavior(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN"})
		return
	}
	ok, err := h.authz.CanManageBot(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "Bot 不存在"})
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
