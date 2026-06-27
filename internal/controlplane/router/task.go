package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TaskHandler 全局任务中心路由处理器（FR-183，见 ADR-040）。
type TaskHandler struct {
	svc *service.TaskService
}

// NewTaskHandler 创建任务中心路由处理器。
func NewTaskHandler(svc *service.TaskService) *TaskHandler {
	return &TaskHandler{svc: svc}
}

// List 列出任务（非平台管理员只见自己发起的，平台管理员见全部）。
func (h *TaskHandler) List(c *gin.Context) {
	access := getAccess(c)
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	tasks, err := h.svc.List(access, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询任务列表失败"})
		return
	}
	c.JSON(http.StatusOK, tasks)
}

// Get 查单个任务（含日志）；越权或不存在返回 404。
func (h *TaskHandler) Get(c *gin.Context) {
	access := getAccess(c)
	taskID := c.Param("taskId")
	task, logs, err := h.svc.Get(access, taskID)
	if err != nil {
		if errors.Is(err, service.ErrTaskNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "任务不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询任务失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"task": task, "logs": logs})
}

// RegisterRoutes 注册任务中心路由（认证用户；归属隔离在 service 层收敛）。
func (h *TaskHandler) RegisterRoutes(rg *gin.RouterGroup) {
	tasks := rg.Group("/tasks")
	tasks.GET("", h.List)
	tasks.GET("/:taskId", h.Get)
}

// NotificationHandler 站内信路由处理器（FR-183，见 ADR-040）。
type NotificationHandler struct {
	svc *service.NotificationService
}

// NewNotificationHandler 创建站内信路由处理器。
func NewNotificationHandler(svc *service.NotificationService) *NotificationHandler {
	return &NotificationHandler{svc: svc}
}

// List 列出当前用户的站内信。?unread=true 仅未读。
func (h *NotificationHandler) List(c *gin.Context) {
	userID := currentUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未认证"})
		return
	}
	onlyUnread := c.Query("unread") == "true"
	limit := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	items, err := h.svc.List(userID, onlyUnread, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询站内信失败"})
		return
	}
	c.JSON(http.StatusOK, items)
}

// UnreadCount 返回当前用户未读站内信数。
func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	userID := currentUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未认证"})
		return
	}
	n, err := h.svc.UnreadCount(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "统计未读失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": n})
}

// MarkRead 标记一条站内信为已读。
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	userID := currentUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未认证"})
		return
	}
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.MarkRead(userID, id); err != nil {
		if errors.Is(err, service.ErrNotificationNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "站内信不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "标记已读失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已标记已读"})
}

// MarkAllRead 标记当前用户所有未读站内信为已读。
func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	userID := currentUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未认证"})
		return
	}
	n, err := h.svc.MarkAllRead(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "标记全部已读失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": n})
}

// RegisterRoutes 注册站内信路由（认证用户；只操作自己的站内信）。
func (h *NotificationHandler) RegisterRoutes(rg *gin.RouterGroup) {
	n := rg.Group("/notifications")
	n.GET("", h.List)
	n.GET("/unread-count", h.UnreadCount)
	n.POST("/:id/read", h.MarkRead)
	n.POST("/read-all", h.MarkAllRead)
}

// currentUserID 从授权上下文取当前用户 ID（auth 中间件注入 access），未认证返回 0。
func currentUserID(c *gin.Context) uint {
	if access := getAccess(c); access != nil {
		return access.UserID
	}
	return 0
}
