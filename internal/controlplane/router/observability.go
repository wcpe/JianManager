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
	// 集群健康墙（FR-461）：逐台健康矩阵 + 一键下钻，只读 CP 快照、零 Worker RPC。
	rg.GET("/observability/health-wall", h.HealthWall)
}

// Overview 返回平台管理员首页使用的有界总览读模型。
// 平台级聚合，不向普通成员开放（即使其模板含 monitor.read）。
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

// HealthWall 返回逐台集群健康矩阵（FR-461）。平台级聚合，不向普通成员开放。
// 支持 ?sort=level|cpu|mem|disk|instances。
func (h *ObservabilityHandler) HealthWall(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	result, err := h.svc.HealthWall(c.Query("sort"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询集群健康墙失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}
