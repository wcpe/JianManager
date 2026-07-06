package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BotStressSessionHandler Bot 压测会话路由处理器。
type BotStressSessionHandler struct {
	svc   *service.BotStressSessionService
	authz *service.AuthzService
}

// NewBotStressSessionHandler 创建 Bot 压测会话路由处理器。
func NewBotStressSessionHandler(svc *service.BotStressSessionService, authz *service.AuthzService) *BotStressSessionHandler {
	return &BotStressSessionHandler{svc: svc, authz: authz}
}

// Create 创建压测会话。
func (h *BotStressSessionHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req service.CreateBotStressSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if !access.IsPlatformAdmin {
		ok, err := h.authz.CanAccessInstance(access, req.InstanceID)
		if err != nil || !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权为该实例创建压测会话"})
			return
		}
	}

	view, err := h.svc.Create(req)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, view)
}

// List 查询压测会话列表。
func (h *BotStressSessionHandler) List(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("pageSize"))
	res, err := h.svc.List(service.BotStressSessionListQuery{Page: page, PageSize: pageSize}, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// Get 查询压测会话详情。
func (h *BotStressSessionHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.canReadSession(c, id) {
		return
	}
	view, err := h.svc.Get(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Start 启动压测会话。
func (h *BotStressSessionHandler) Start(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.canManageSession(c, id) {
		return
	}
	view, err := h.svc.Start(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Stop 停止压测会话。
func (h *BotStressSessionHandler) Stop(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !h.canManageSession(c, id) {
		return
	}
	view, err := h.svc.Stop(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *BotStressSessionHandler) canManageSession(c *gin.Context, id uint) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	view, err := h.svc.Get(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return false
	}
	ok, err := h.authz.CanAccessInstance(access, view.InstanceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return false
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
		return false
	}
	return true
}

func (h *BotStressSessionHandler) canReadSession(c *gin.Context, id uint) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermBotRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	view, err := h.svc.Get(id)
	if err != nil {
		writeBotStressSessionError(c, err)
		return false
	}
	ok, err := h.authz.CanAccessInstance(access, view.InstanceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return false
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
		return false
	}
	return true
}

func writeBotStressSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrBotStressSessionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "压测会话不存在"})
	case errors.Is(err, service.ErrBotStressSessionInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "压测会话操作失败"})
	}
}

// RegisterRoutes 注册压测会话路由。
func (h *BotStressSessionHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/bots/stress-test", h.Create)
	sessions := rg.Group("/bots/stress-sessions")
	{
		sessions.POST("", h.Create)
		sessions.GET("", h.List)
		sessions.GET("/:id", h.Get)
		sessions.POST("/:id/start", h.Start)
		sessions.POST("/:id/stop", h.Stop)
	}
}
