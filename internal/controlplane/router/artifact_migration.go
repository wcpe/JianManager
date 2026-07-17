package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ArtifactMigrationHandler 制品存量迁移路由处理器（FR-348，底座见 ADR-073）。
// 全组限平台管理员（挂 admin 组，与渠道路由同处）；任务本体经全局任务中心可见可停。
type ArtifactMigrationHandler struct {
	svc *service.ArtifactMigrationService
}

// NewArtifactMigrationHandler 创建制品存量迁移处理器。
func NewArtifactMigrationHandler(svc *service.ArtifactMigrationService) *ArtifactMigrationHandler {
	return &ArtifactMigrationHandler{svc: svc}
}

// Start POST /artifact-storages/:id/migrate — 发起「迁移到渠道 :id」后台任务（spec §3.5）。
// 一次一个在途（409 MIGRATION_IN_FLIGHT）；发起时目标真连探测失败即拒（422）。
func (h *ArtifactMigrationHandler) Start(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var createdBy uint
	if access := getAccess(c); access != nil {
		createdBy = access.UserID
	}
	taskID, err := h.svc.Start(id, createdBy)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrArtifactStorageNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
		case errors.Is(err, service.ErrArtifactMigrationInFlight):
			c.JSON(http.StatusConflict, gin.H{"error": "MIGRATION_IN_FLIGHT", "message": err.Error()})
		case errors.Is(err, service.ErrArtifactMigrationTargetUnavailable):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		}
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"taskId": taskID})
}

// Latest GET /artifact-storages/migration — 最近一次迁移状态（任务 + 实时计数；
// 从未迁移过返回双 null）。渠道页在途进度轮询与上次摘要的数据源。
func (h *ArtifactMigrationHandler) Latest(c *gin.Context) {
	status, err := h.svc.Latest()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, status)
}

// Failures GET /artifact-storages/migration/:taskId/failures — 某次迁移的失败明细
// （id 升序，上限 500 条；总失败数看计数 failed）。
func (h *ArtifactMigrationHandler) Failures(c *gin.Context) {
	items, err := h.svc.Failures(c.Param("taskId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, items)
}

// RegisterRoutes 注册迁移路由（挂 admin 组，与制品存储渠道同前缀）。
func (h *ArtifactMigrationHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/artifact-storages")
	{
		g.POST("/:id/migrate", h.Start)
		g.GET("/migration", h.Latest)
		g.GET("/migration/:taskId/failures", h.Failures)
	}
}
