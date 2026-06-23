package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// StorageHandler 平台存储资源管理器（FR-083）路由。
//
// 对 CP 侧数据根（ADR-010 FHS 布局）只读浏览 + 占用统计 + 制品归档可见 + cache 受控清理。
// 数据根是平台级资源（仅 CP 读写，见架构不变量），故所有端点限平台管理员。
type StorageHandler struct {
	svc *service.StorageService
}

// NewStorageHandler 创建平台存储路由处理器。
func NewStorageHandler(svc *service.StorageService) *StorageHandler {
	return &StorageHandler{svc: svc}
}

// Overview GET /storage/overview — FHS 子目录占用统计 + 制品归档分布。
func (h *StorageHandler) Overview(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	ov, err := h.svc.Overview()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "统计平台存储失败"})
		return
	}
	c.JSON(http.StatusOK, ov)
}

// Files GET /storage/files?path= — 列举数据根内某目录的直接子项（只读）。
func (h *StorageHandler) Files(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	entries, err := h.svc.List(c.Query("path"))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrStoragePathEscape):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_PATH", "message": "路径越出数据根"})
		case errors.Is(err, service.ErrStorageNotDir):
			c.JSON(http.StatusBadRequest, gin.H{"error": "NOT_A_DIR", "message": "目标不是目录"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取目录失败"})
		}
		return
	}
	c.JSON(http.StatusOK, entries)
}

// ClearCache POST /storage/cache/clear — 清空 cache/ 内容（受控清理，二次确认由前端强制）。
func (h *StorageHandler) ClearCache(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	removed, err := h.svc.ClearCache()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "清理缓存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"removed": removed})
}

// RegisterRoutes 注册平台存储路由。
func (h *StorageHandler) RegisterRoutes(rg *gin.RouterGroup) {
	storage := rg.Group("/storage")
	{
		storage.GET("/overview", h.Overview)
		storage.GET("/files", h.Files)
		storage.POST("/cache/clear", h.ClearCache)
	}
}
