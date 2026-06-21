package router

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AuditHandler 审计日志路由处理器。
type AuditHandler struct {
	auditSvc *service.AuditService
}

func NewAuditHandler(auditSvc *service.AuditService) *AuditHandler {
	return &AuditHandler{auditSvc: auditSvc}
}

// List 审计日志列表。
func (h *AuditHandler) List(c *gin.Context) {
	filter := service.AuditFilter{}

	if v := c.Query("userId"); v != "" {
		id, _ := strconv.ParseUint(v, 10, 64)
		u := uint(id)
		filter.UserID = &u
	}
	if v := c.Query("action"); v != "" {
		filter.Action = &v
	}
	if v := c.Query("targetType"); v != "" {
		filter.TargetType = &v
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.From = &t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.To = &t
		}
	}

	logs, err := h.auditSvc.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// RegisterRoutes 注册审计日志路由。
func (h *AuditHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/audit", h.List)
}
