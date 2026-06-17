package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/middleware"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

type ConfigHandler struct {
	svc   *service.ConfigService
	authz *service.AuthzService
}

func NewConfigHandler(svc *service.ConfigService, authz *service.AuthzService) *ConfigHandler {
	return &ConfigHandler{svc: svc, authz: authz}
}

func (h *ConfigHandler) List(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	files, err := h.svc.List(id, c.DefaultQuery("path", ""))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, files)
}

func (h *ConfigHandler) Read(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 path 参数"})
		return
	}
	res, err := h.svc.Read(id, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

type configWriteRequest struct {
	Path    string `json:"path" binding:"required"`
	Content string `json:"content" binding:"required"`
	Message string `json:"message"`
}

func (h *ConfigHandler) Write(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req configWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, validation, err := h.svc.Write(id, req.Path, req.Content, req.Message, authorID, nil)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error(), "validation": validation})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versionId": versionID, "validation": validation})
}

func (h *ConfigHandler) Versions(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	versions, err := h.svc.Versions(id, c.Param("file"))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, versions)
}

type rollbackRequest struct {
	VersionID uint   `json:"versionId" binding:"required"`
	Message   string `json:"message"`
}

func (h *ConfigHandler) Rollback(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req rollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, err := h.svc.Rollback(id, c.Param("file"), req.VersionID, req.Message, authorID)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"versionId": versionID})
}

func (h *ConfigHandler) RegisterRoutes(rg *gin.RouterGroup) {
	cfg := rg.Group("/instances/:id/configs")
	cfg.GET("", h.List)
	cfg.GET("/read", h.Read)
	cfg.POST("/write", h.Write)
	cfg.POST("/cross-check", h.CrossCheck)
	// 使用 *file 通配符，使 /configs/plugins/Example/config.yml/versions 这样的子目录路径也能命中。
	cfg.GET("/*file/versions", h.Versions)
	cfg.POST("/*file/rollback", h.Rollback)
	cfg.GET("/*file/versions/:fromId/diff", h.Diff)
}

// Diff 返回 fromID -> toID 的差异。
// toID=0 表示与当前文件内容对比。
func (h *ConfigHandler) Diff(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	fromID, err := strconv.ParseUint(c.Param("fromId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 from 版本 ID"})
		return
	}
	var toID uint64
	if raw := c.Query("to"); raw != "" {
		t, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 to 版本 ID"})
			return
		}
		toID = t
	}
	res, err := h.svc.Diff(id, c.Param("file"), uint(fromID), uint(toID))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// CrossCheck 对提交内容做跨实例一致性校验，返回 warning 列表（不影响写入结果）。
type crossCheckRequest struct {
	Path    string `json:"path" binding:"required"`
	Content string `json:"content" binding:"required"`
}

func (h *ConfigHandler) CrossCheck(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req crossCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	issues, err := h.svc.CheckCrossFile(id, req.Path, req.Content)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"issues": issues})
}
