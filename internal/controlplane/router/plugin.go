package router

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// PluginHandler 插件/模组单服管理路由处理器（FR-052）。
type PluginHandler struct {
	pluginSvc *service.PluginService
	authz     *service.AuthzService
}

// NewPluginHandler 创建插件路由处理器。
func NewPluginHandler(pluginSvc *service.PluginService, authz *service.AuthzService) *PluginHandler {
	return &PluginHandler{pluginSvc: pluginSvc, authz: authz}
}

// List GET /instances/:id/plugins — 列出 plugins/ 与 mods/ 目录插件，识别启用/禁用。
func (h *PluginHandler) List(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	plugins, err := h.pluginSvc.List(id)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, plugins)
}

// Upload POST /instances/:id/plugins — multipart 上传插件，入制品库后部署到实例。
// 表单字段：file（必填）、dir（plugins|mods，可选，默认 plugins）。
func (h *PluginHandler) Upload(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	file, header, ferr := c.Request.FormFile("file")
	if ferr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少文件"})
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取上传文件失败"})
		return
	}

	asset, err := h.pluginSvc.Upload(id, c.PostForm("dir"), header.Filename, content)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "已上传", "asset": asset})
}

// Delete DELETE /instances/:id/plugins/:name — 删除指定插件（同时匹配启用/禁用文件名）。
// Query: ?dir=plugins|mods（可选，默认 plugins）。二次确认在前端完成。
func (h *PluginHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	if err := h.pluginSvc.Delete(id, c.Query("dir"), c.Param("name")); err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Toggle POST /instances/:id/plugins/:name/toggle — 启用/禁用插件（重命名，不删除）。
// Query: ?dir=plugins|mods（可选，默认 plugins）。
func (h *PluginHandler) Toggle(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canManageInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	enabled, err := h.pluginSvc.Toggle(id, c.Query("dir"), c.Param("name"))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已切换", "enabled": enabled})
}

// respondErr 将服务层错误映射为合适的 HTTP 状态码。
func (h *PluginHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPluginName):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_NAME", "message": err.Error()})
	case errors.Is(err, service.ErrPluginNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

// RegisterRoutes 注册插件路由。
func (h *PluginHandler) RegisterRoutes(rg *gin.RouterGroup) {
	plugins := rg.Group("/instances/:id/plugins")
	{
		plugins.GET("", h.List)
		plugins.POST("", h.Upload)
		plugins.DELETE("/:name", h.Delete)
		plugins.POST("/:name/toggle", h.Toggle)
	}
}
