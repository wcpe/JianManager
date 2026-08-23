package router

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// PluginHandler 插件/模组单服管理路由处理器（FR-052）。
type PluginHandler struct {
	pluginSvc *service.PluginService
	authz     *service.AuthzService
	audit     *service.AuditService
}

// NewPluginHandler 创建插件路由处理器。
func NewPluginHandler(pluginSvc *service.PluginService, authz *service.AuthzService, audit *service.AuditService) *PluginHandler {
	return &PluginHandler{pluginSvc: pluginSvc, authz: authz, audit: audit}
}

type pluginBatchDeployRequest struct {
	AssetIDs    []uint              `json:"assetIds"`
	Target      pluginBatchTargetIn `json:"target"`
	Destination string              `json:"destination"`
	Overwrite   bool                `json:"overwrite"`
}

type pluginBatchTargetIn struct {
	IDs    []uint                         `json:"ids"`
	Filter *service.InstanceBatchFilterIn `json:"filter"`
}

// List GET /instances/:id/plugins — 列出受控目录制品，识别启用/禁用。
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

// Upload POST /instances/:id/plugins — multipart 上传受控制品，入制品库后部署到实例。
// 表单字段：file（必填）、dir（受控目录，可选，默认 plugins）、overwrite（可选）。
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

	overwrite := c.PostForm("overwrite") == "true" || c.PostForm("overwrite") == "1"
	asset, err := h.pluginSvc.Upload(id, c.PostForm("dir"), header.Filename, content, service.WithPluginOverwrite(overwrite))
	if err != nil {
		h.respondErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "已上传", "asset": asset})
}

// Delete DELETE /instances/:id/plugins/:name — 删除指定制品（同时匹配启用/禁用文件名）。
// Query: ?dir=受控目录（可选，默认 plugins）。二次确认在前端完成。
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

// Toggle POST /instances/:id/plugins/:name/toggle — 启用/禁用制品（重命名，不删除）。
// Query: ?dir=受控目录（可选，默认 plugins）。
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

// BatchDeploy POST /plugins/batch-deploy — 从制品库批量部署插件到多个实例。
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
	if len(req.AssetIDs) == 0 || (len(req.Target.IDs) == 0 && req.Target.Filter == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需指定插件制品与目标实例"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	svcReq := service.PluginBatchDeployRequest{
		AssetIDs:    req.AssetIDs,
		Target:      service.PluginBatchTarget{IDs: req.Target.IDs},
		Destination: req.Destination,
		Overwrite:   req.Overwrite,
	}
	if req.Target.Filter != nil {
		f := req.Target.Filter.ToFilter()
		svcReq.Target.Filter = &f
	}
	res, err := h.pluginSvc.BatchDeploy(svcReq, scopeIDs, scope)
	if err != nil {
		h.respondBatchErr(c, err)
		return
	}
	h.recordAudit(c, "plugin.batch_deploy", map[string]any{
		"assetIds":           req.AssetIDs,
		"requestedInstances": res.RequestedInstances,
		"requestedAssets":    res.RequestedAssets,
		"succeeded":          res.Succeeded,
		"failed":             res.Failed,
		"skipped":            res.Skipped,
		"destination":        req.Destination,
		"overwrite":          req.Overwrite,
	})
	c.JSON(http.StatusOK, res)
}

// respondErr 将服务层错误映射为合适的 HTTP 状态码。
func (h *PluginHandler) respondErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPluginName):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_NAME", "message": err.Error()})
	case errors.Is(err, service.ErrPluginFileExists):
		c.JSON(http.StatusConflict, gin.H{"error": "FILE_EXISTS", "message": err.Error()})
	case errors.Is(err, service.ErrPluginNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

func (h *PluginHandler) respondBatchErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPluginAsset):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_PLUGIN_ASSET", "message": err.Error()})
	case errors.Is(err, service.ErrAssetNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ASSET_NOT_FOUND", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

func (h *PluginHandler) actorID(c *gin.Context) uint {
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	return id
}

func (h *PluginHandler) recordAudit(c *gin.Context, action string, detail any) {
	if h.audit == nil {
		return
	}
	h.audit.RecordSafe(h.actorID(c), action, "plugin", "", marshalAuditDetail(detail), c.ClientIP())
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
