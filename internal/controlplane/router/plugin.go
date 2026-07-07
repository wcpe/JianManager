package router

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

type pluginBatchDeployRequest struct {
	AssetIDs []uint                         `json:"assetIds"`
	IDs      []uint                         `json:"ids"`
	Filter   *service.InstanceBatchFilterIn `json:"filter"`
}

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

// BatchDeploy POST /plugins/batch-deploy — 从制品库批量部署插件到多个实例 plugins/。
func (h *PluginHandler) BatchDeploy(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req pluginBatchDeployRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if len(req.AssetIDs) == 0 || (len(req.IDs) == 0 && req.Filter == nil) || (len(req.IDs) > 0 && req.Filter != nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需指定 assetIds，并在 ids/filter 中二选一"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	svcReq := service.PluginBatchDeployRequest{AssetIDs: req.AssetIDs, IDs: req.IDs}
	if req.Filter != nil {
		f := req.Filter.ToFilter()
		svcReq.Filter = &f
	}
	res, err := h.pluginSvc.BatchDeploy(svcReq, scopeIDs, scope)
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
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
	case errors.Is(err, service.ErrPluginNotFound), errors.Is(err, service.ErrAssetNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrPluginAssetRequired), errors.Is(err, service.ErrPluginTargetRequired), errors.Is(err, service.ErrInvalidPluginAsset):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	case errors.Is(err, service.ErrPluginAssetFileUnavailable):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

// RegisterRoutes 注册插件路由。
func (h *PluginHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/plugins/batch-deploy", h.BatchDeploy)
	plugins := rg.Group("/instances/:id/plugins")
	{
		plugins.GET("", h.List)
		plugins.POST("", h.Upload)
		plugins.DELETE("/:name", h.Delete)
		plugins.POST("/:name/toggle", h.Toggle)
	}
}
