package router

import (
	"net/http"

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
	versionID, validation, err := h.svc.Write(id, req.Path, req.Content, req.Message, authorID)
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
	cfg.GET("/:file/versions", h.Versions)
	cfg.POST("/:file/rollback", h.Rollback)
}
