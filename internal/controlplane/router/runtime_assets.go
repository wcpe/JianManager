package router

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// RuntimeAssetsHandler 运行时与制品全局页（FR-082）聚合路由。
// 跨节点 JDK 矩阵 + 每项引用实例、制品按类型占用/去重/冷热统计——平台级共享资源，限平台管理员。
// FR-301 扩展：overview 加性携带多运行时矩阵与每节点同步时间；refresh 强制全节点库存同步（写审计）。
type RuntimeAssetsHandler struct {
	svc *service.RuntimeAssetsService
	// audit 审计服务；为 nil 时刷新审计静默跳过（与 RuntimeLibraryHandler 同约定）。
	audit *service.AuditService
}

// NewRuntimeAssetsHandler 创建运行时与制品聚合路由处理器。audit 可为 nil（审计随之关闭）。
func NewRuntimeAssetsHandler(svc *service.RuntimeAssetsService, audit *service.AuditService) *RuntimeAssetsHandler {
	return &RuntimeAssetsHandler{svc: svc, audit: audit}
}

// Overview GET /runtime-assets/overview — 一次性返回 JDK 矩阵 + 引用关系 + 制品分组统计
//（FR-301 起另含 runtimes 多运行时矩阵 / runtimeSyncs / syncedAt，加性扩展）。
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

// Refresh POST /runtime-assets/refresh — 强制全节点库存 syncFromWorker（FR-301）。
// 失败容忍：单节点同步失败不阻断整体，结果逐节点回报 ok/error（前端据此显旧数据 + 提示），
// 故除服务未装配/查库失败外恒 200。
func (h *RuntimeAssetsHandler) Refresh(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	outcome, err := h.svc.Refresh()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "刷新运行时库存失败"})
		return
	}
	if h.audit != nil {
		uid, _ := c.Get(middleware.CtxUserID)
		userID, _ := uid.(uint)
		okCount := 0
		for _, r := range outcome.Results {
			if r.OK {
				okCount++
			}
		}
		raw, _ := json.Marshal(map[string]any{
			"total": len(outcome.Results), "ok": okCount, "failed": len(outcome.Results) - okCount,
		})
		_ = h.audit.Record(userID, "runtime_assets.refresh", "platform", "", string(raw), c.ClientIP())
	}
	c.JSON(http.StatusOK, outcome)
}

// RegisterRoutes 注册运行时与制品聚合路由。
func (h *RuntimeAssetsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/runtime-assets/overview", h.Overview)
	rg.POST("/runtime-assets/refresh", h.Refresh)
}
