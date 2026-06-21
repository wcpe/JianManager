package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BackupHandler 备份路由处理器。
type BackupHandler struct {
	backupSvc *service.BackupService
}

func NewBackupHandler(backupSvc *service.BackupService) *BackupHandler {
	return &BackupHandler{backupSvc: backupSvc}
}

func (h *BackupHandler) List(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	backups, err := h.backupSvc.ListByInstance(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, backups)
}

type createBackupRequest struct {
	Name string `json:"name" binding:"required"`
	// Incremental 为 true 时创建增量备份（FR-056），挂到该实例最近一次已完成备份后形成链。
	Incremental bool `json:"incremental"`
	// StorageID 指定远程存储后端；缺省存于节点本地（FR-057）。
	StorageID *uint `json:"storageId"`
}

func (h *BackupHandler) Create(c *gin.Context) {
	instanceID, err := parseID(c)
	if err != nil {
		return
	}
	var req createBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	backup, err := h.backupSvc.CreateWithOptions(instanceID, req.Name, service.CreateOptions{
		Incremental: req.Incremental,
		StorageID:   req.StorageID,
	})
	if err != nil {
		// 增量缺少基准是可预期的业务错误，回 422 便于前端提示先做全量。
		if errors.Is(err, service.ErrNoFullBaseForIncremental) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusCreated, backup)
}

func (h *BackupHandler) Restore(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.backupSvc.Restore(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "恢复中"})
}

func (h *BackupHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.backupSvc.Delete(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

func (h *BackupHandler) RegisterRoutes(rg *gin.RouterGroup) {
	// 实例级备份路由
	rg.GET("/instances/:id/backups", h.List)
	rg.POST("/instances/:id/backups", h.Create)

	// 备份操作路由
	backups := rg.Group("/backups")
	{
		backups.POST("/:id/restore", h.Restore)
		backups.DELETE("/:id", h.Delete)
	}
}
