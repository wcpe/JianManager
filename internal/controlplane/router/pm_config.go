package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// PMConfigHandler 节点包管理器与 registry 配置（FR-306）。
type PMConfigHandler struct {
	svc   *service.PMConfigService
	audit *service.AuditService
}

// NewPMConfigHandler 创建处理器。
func NewPMConfigHandler(svc *service.PMConfigService, audit *service.AuditService) *PMConfigHandler {
	return &PMConfigHandler{svc: svc, audit: audit}
}

// Get GET /nodes/:id/pm-config —— 读取节点 PM 偏好 + registry（脱敏）+ corepack 状态。
func (h *PMConfigHandler) Get(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	view, err := h.svc.Get(nodeID)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Put PUT /nodes/:id/pm-config —— 设置 PM 偏好（corepack 激活）+ registry（写托管 .npmrc）。
func (h *PMConfigHandler) Put(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var in service.SetPMConfigInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	view, err := h.svc.Set(nodeID, in)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	if h.audit != nil {
		uid, _ := c.Get(middleware.CtxUserID)
		userID, _ := uid.(uint)
		h.audit.RecordSafe(userID, "node.pm.config", "node", c.Param("id"), marshalAuditDetail(map[string]any{"pm": view.PM, "registries": len(view.Registries)}), c.ClientIP())
	}
	c.JSON(http.StatusOK, view)
}

// ListPackages GET /nodes/:id/packages —— 列出托管全局目录已装包（含可更新标记，FR-307）。
func (h *PMConfigHandler) ListPackages(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	pkgs, pm, err := h.svc.ListGlobalPackages(nodeID)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pm": pm, "packages": pkgs})
}

// InstallPackage POST /nodes/:id/packages —— 异步安装/升级全局包（202+taskId，FR-307）。
func (h *PMConfigHandler) InstallPackage(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	var in struct {
		Name    string `json:"name" binding:"required,min=1,max=214"`
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误（name 必填）"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	task, err := h.svc.InstallGlobalPackageAsync(nodeID, in.Name, in.Version, userID)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	if h.audit != nil {
		h.audit.RecordSafe(userID, "node.pkg.install", "node", c.Param("id"), marshalAuditDetail(map[string]any{"name": in.Name, "version": in.Version}), c.ClientIP())
	}
	c.JSON(http.StatusAccepted, gin.H{"taskId": task.TaskID, "task": task})
}

// RemovePackage DELETE /nodes/:id/packages?name=<pkg> —— 卸载全局包（同步，FR-307）。
// 包名经 query 传（@scope/name 含斜杠，路径参数会破路由）。
func (h *PMConfigHandler) RemovePackage(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodeID, err := parseUintParam(c, "id")
	if err != nil {
		return
	}
	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 name"})
		return
	}
	if err := h.svc.RemoveGlobalPackage(nodeID, name); err != nil {
		h.mapErr(c, err)
		return
	}
	if h.audit != nil {
		uid, _ := c.Get(middleware.CtxUserID)
		userID, _ := uid.(uint)
		h.audit.RecordSafe(userID, "node.pkg.remove", "node", c.Param("id"), marshalAuditDetail(map[string]any{"name": name}), c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"message": "已卸载"})
}

func (h *PMConfigHandler) mapErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
	case errors.Is(err, service.ErrNodeOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接，无法配置包管理器"})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

// RegisterRoutes 注册 PM 配置与全局包路由（平台管理员）。
func (h *PMConfigHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/nodes/:id/pm-config", h.Get)
	rg.PUT("/nodes/:id/pm-config", h.Put)
	rg.GET("/nodes/:id/packages", h.ListPackages)
	rg.POST("/nodes/:id/packages", h.InstallPackage)
	rg.DELETE("/nodes/:id/packages", h.RemovePackage)
}
