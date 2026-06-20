package router

import (
	"net/http"
	"strconv"
	"strings"

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

type configWriteFieldsRequest struct {
	Path    string            `json:"path" binding:"required"`
	Fields  map[string]string `json:"fields" binding:"required"`
	Message string            `json:"message"`
}

// WriteFields 表单模式保存：字段级补丁回原文（保留注释），生成新版本。
func (h *ConfigHandler) WriteFields(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	var req configWriteFieldsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	versionID, validation, err := h.svc.WriteFields(id, req.Path, req.Fields, req.Message, authorID)
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
	versions, err := h.svc.Versions(id, strings.TrimPrefix(c.Param("file"), "/"))
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
	versionID, err := h.svc.Rollback(id, strings.TrimPrefix(c.Param("file"), "/"), req.VersionID, req.Message, authorID)
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
	cfg.POST("/write-fields", h.WriteFields)
	cfg.POST("/cross-check", h.CrossCheck)
	// *file 通配符必须在路径末尾；文件路径可能含子目录（如 plugins/Foo/config.yml）。
	cfg.GET("/versions/*file", h.Versions)
	cfg.POST("/rollback/*file", h.Rollback)
	cfg.GET("/diff/*file", h.Diff)
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
	fromRaw := c.Query("from")
	if fromRaw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 from 版本 ID"})
		return
	}
	fromID, err := strconv.ParseUint(fromRaw, 10, 64)
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
	res, err := h.svc.Diff(id, strings.TrimPrefix(c.Param("file"), "/"), uint(fromID), uint(toID))
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
