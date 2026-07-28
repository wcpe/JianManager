package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ObservabilityHandler 提供平台管理员的有界全景观测读接口。
type ObservabilityHandler struct {
	svc *service.PlatformObservabilityService
}

// NewObservabilityHandler 创建平台全景观测路由处理器。
func NewObservabilityHandler(svc *service.PlatformObservabilityService) *ObservabilityHandler {
	return &ObservabilityHandler{svc: svc}
}

// RegisterRoutes 注册平台全景观测只读路由。
func (h *ObservabilityHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/observability/overview", h.Overview)
}

// Overview 返回平台管理员首页使用的有界总览读模型。
func (h *ObservabilityHandler) Overview(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	result, err := h.svc.Overview()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询平台观测总览失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}
