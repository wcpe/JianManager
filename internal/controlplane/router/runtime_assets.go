package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// RuntimeAssetsHandler 运行时与制品全局页（FR-082）只读聚合路由。
// 跨节点 JDK 矩阵 + 每项引用实例、制品按类型占用/去重/冷热统计——平台级共享资源，限平台管理员。
type RuntimeAssetsHandler struct {
	svc *service.RuntimeAssetsService
}

// NewRuntimeAssetsHandler 创建运行时与制品聚合路由处理器。
func NewRuntimeAssetsHandler(svc *service.RuntimeAssetsService) *RuntimeAssetsHandler {
	return &RuntimeAssetsHandler{svc: svc}
}

// Overview GET /runtime-assets/overview — 一次性返回 JDK 矩阵 + 引用关系 + 制品分组统计。
func (h *RuntimeAssetsHandler) Overview(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	ov, err := h.svc.Overview()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "聚合运行时与制品失败"})
		return
	}
	c.JSON(http.StatusOK, ov)
}

// RegisterRoutes 注册运行时与制品聚合路由。
func (h *RuntimeAssetsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/runtime-assets/overview", h.Overview)
}
