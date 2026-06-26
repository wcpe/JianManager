package router

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AlertHandler 告警路由处理器（FR-011 + FR-085）。
type AlertHandler struct {
	alertSvc   *service.AlertService
	channelSvc *service.AlertChannelService
}

func NewAlertHandler(alertSvc *service.AlertService, channelSvc *service.AlertChannelService) *AlertHandler {
	return &AlertHandler{alertSvc: alertSvc, channelSvc: channelSvc}
}

// ── 告警规则 ──

func (h *AlertHandler) ListRules(c *gin.Context) {
	rules, err := h.alertSvc.ListRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, rules)
}

func (h *AlertHandler) CreateRule(c *gin.Context) {
	var req service.CreateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	rule, err := h.alertSvc.CreateRule(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, rule)
}

func (h *AlertHandler) UpdateRule(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req service.UpdateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	rule, err := h.alertSvc.UpdateRule(id, req)
	if err != nil {
		if errors.Is(err, service.ErrAlertRuleNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rule)
}

func (h *AlertHandler) DeleteRule(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.alertSvc.DeleteRule(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ── 告警事件 ──

func (h *AlertHandler) ListEvents(c *gin.Context) {
	f := service.EventFilter{}
	if v := c.Query("ruleId"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			u := uint(id)
			f.RuleID = &u
		}
	}
	if v := c.Query("resolved"); v != "" {
		b := v == "true"
		f.Resolved = &b
	}
	if v := c.Query("acknowledged"); v != "" {
		b := v == "true"
		f.Acknowledged = &b
	}
	f.Level = c.Query("level")
	f.TriggerType = c.Query("triggerType")
	f.Keyword = c.Query("keyword")
	if v := c.Query("from"); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &ts
		}
	}
	if v := c.Query("to"); v != "" {
		if ts, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &ts
		}
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
	events, total, err := h.alertSvc.ListEvents(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": events, "total": total})
}

// AcknowledgeEvent 确认/认领一条告警事件（FR-085）。
func (h *AlertHandler) AcknowledgeEvent(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	event, err := h.alertSvc.Acknowledge(id, userID)
	if err != nil {
		if errors.Is(err, service.ErrAlertEventNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, event)
}

// MarkEventsRead 标记一条（:id）或全部（无 :id）告警事件为已读（FR-085）。
func (h *AlertHandler) MarkEventsRead(c *gin.Context) {
	var eventID uint
	if v := c.Param("id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			eventID = uint(id)
		}
	}
	if err := h.alertSvc.MarkRead(eventID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已标记已读"})
}

// UnreadCount 返回未读告警数（站内角标，FR-085）。
func (h *AlertHandler) UnreadCount(c *gin.Context) {
	n, err := h.alertSvc.UnreadCount()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": n})
}

// ── 通知通道（FR-085）──

func (h *AlertHandler) ListChannels(c *gin.Context) {
	channels, err := h.channelSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, channels)
}

func (h *AlertHandler) CreateChannel(c *gin.Context) {
	var req service.ChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	ch, err := h.channelSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, ch)
}

func (h *AlertHandler) UpdateChannel(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req service.ChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	ch, err := h.channelSvc.Update(id, req)
	if err != nil {
		if errors.Is(err, service.ErrAlertChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ch)
}

func (h *AlertHandler) DeleteChannel(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.channelSvc.Delete(id); err != nil {
		if errors.Is(err, service.ErrAlertChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		if errors.Is(err, service.ErrAlertChannelInUse) {
			c.JSON(http.StatusConflict, gin.H{"error": "CHANNEL_IN_USE", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// TestChannel 向通道发送测试通知（FR-085）。
func (h *AlertHandler) TestChannel(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.channelSvc.TestSend(id); err != nil {
		if errors.Is(err, service.ErrAlertChannelNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "TEST_SEND_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "测试通知已发送"})
}

func (h *AlertHandler) RegisterRoutes(rg *gin.RouterGroup) {
	alerts := rg.Group("/alerts")
	{
		alerts.GET("/rules", h.ListRules)
		alerts.POST("/rules", h.CreateRule)
		alerts.PUT("/rules/:id", h.UpdateRule)
		alerts.DELETE("/rules/:id", h.DeleteRule)

		alerts.GET("/events", h.ListEvents)
		alerts.GET("/events/unread-count", h.UnreadCount)
		alerts.POST("/events/:id/ack", h.AcknowledgeEvent)
		alerts.POST("/events/:id/read", h.MarkEventsRead)
		alerts.POST("/events/read-all", h.markAllRead)

		alerts.GET("/channels", h.ListChannels)
		alerts.POST("/channels", h.CreateChannel)
		alerts.PUT("/channels/:id", h.UpdateChannel)
		alerts.DELETE("/channels/:id", h.DeleteChannel)
		alerts.POST("/channels/:id/test", h.TestChannel)
	}
}

// markAllRead 标记全部未读为已读（read-all 路由，避免与 :id/read 冲突单独成端点）。
func (h *AlertHandler) markAllRead(c *gin.Context) {
	if err := h.alertSvc.MarkRead(0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "全部已读"})
}
