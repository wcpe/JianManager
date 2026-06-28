package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// NotificationFeedHandler 统一通知中心路由处理器（FR-216，见 ADR-048）。
// 把站内信（定向消息）+ 告警（系统警报）合并为一条只读通知流：列表（带来源/筛选/分页）、
// 未读计数、标记已读。站内信按当前用户归属、告警面向全体（与既有 /alerts 可见性一致）。
type NotificationFeedHandler struct {
	svc *service.NotificationFeedService
}

// NewNotificationFeedHandler 创建统一通知中心路由处理器。
func NewNotificationFeedHandler(svc *service.NotificationFeedService) *NotificationFeedHandler {
	return &NotificationFeedHandler{svc: svc}
}

// Feed 统一通知流分页列表。Query: source(message|alert|空)、unread(true)、keyword、page、pageSize。
func (h *NotificationFeedHandler) Feed(c *gin.Context) {
	userID := currentUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未认证"})
		return
	}
	f := service.FeedFilter{
		Source:  c.Query("source"),
		Unread:  c.Query("unread") == "true",
		Keyword: c.Query("keyword"),
	}
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Page = n
		}
	}
	if v := c.Query("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.PageSize = n
		}
	}
	items, total, err := h.svc.Feed(userID, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询通知失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// UnreadCount 统一未读数（本人未读站内信 + 全局未读告警）。
func (h *NotificationFeedHandler) UnreadCount(c *gin.Context) {
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

// MarkRead 标记单条通知为已读。:source = message | alert。
func (h *NotificationFeedHandler) MarkRead(c *gin.Context) {
	userID := currentUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未认证"})
		return
	}
	source := c.Param("source")
	id, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	if err := h.svc.MarkRead(userID, source, id); err != nil {
		if errors.Is(err, service.ErrNotificationNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "通知不存在"})
			return
		}
		// 非法来源等参数错误。
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已标记已读"})
}

// MarkAllRead 全部标记已读（本人站内信 + 全局告警）。
func (h *NotificationFeedHandler) MarkAllRead(c *gin.Context) {
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

// RegisterRoutes 注册统一通知中心路由（认证用户；归属隔离在 service 层收敛）。
// 挂 /notifications/feed/* 子路径，与既有 /notifications/*（站内信）端点并存不冲突。
func (h *NotificationFeedHandler) RegisterRoutes(rg *gin.RouterGroup) {
	f := rg.Group("/notifications/feed")
	f.GET("", h.Feed)
	f.GET("/unread-count", h.UnreadCount)
	f.POST("/read-all", h.MarkAllRead)
	f.POST("/:source/:id/read", h.MarkRead)
}
