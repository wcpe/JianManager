package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// AlertHandler 告警路由处理器。
type AlertHandler struct {
	alertSvc *service.AlertService
}

func NewAlertHandler(alertSvc *service.AlertService) *AlertHandler {
	return &AlertHandler{alertSvc: alertSvc}
}

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusCreated, rule)
}

func (h *AlertHandler) UpdateRule(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req struct {
		Enabled   *bool    `json:"enabled"`
		Threshold *float64 `json:"threshold"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	rule, err := h.alertSvc.UpdateRule(id, req.Enabled, req.Threshold)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND"})
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

func (h *AlertHandler) ListEvents(c *gin.Context) {
	var ruleID *uint
	if v := c.Query("ruleId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		ruleID = &u
	}
	var resolved *bool
	if v := c.Query("resolved"); v != "" {
		b := v == "true"
		resolved = &b
	}
	events, err := h.alertSvc.ListEvents(ruleID, resolved)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, events)
}

func (h *AlertHandler) RegisterRoutes(rg *gin.RouterGroup) {
	alerts := rg.Group("/alerts")
	{
		alerts.GET("/rules", h.ListRules)
		alerts.POST("/rules", h.CreateRule)
		alerts.PUT("/rules/:id", h.UpdateRule)
		alerts.DELETE("/rules/:id", h.DeleteRule)
		alerts.GET("/events", h.ListEvents)
	}
}
