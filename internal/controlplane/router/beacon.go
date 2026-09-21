package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BeaconHandler 是 FR-444「从 Beacon 拉取拓扑」的手动触发端点。
//
// 设计要点（ADR-090）：
//   - **不自动建树**：拉取只由此端点手动触发，避免与人工已建的分组冲突。
//   - **可选协同**：未配置 beacon.endpoint 时返回 503 + 明确提示，而非 500；
//     未部署 Beacon 时本平台其余功能完全不受影响。
//   - 权限沿用实例写权限 instance:write（拉取会改分组树与实例归属，是实例域的写操作）。
//   - 成功/失败均写审计（instance.beacon_pull_ok / instance.beacon_pull_fail）。
type BeaconHandler struct {
	svc   *service.BeaconSyncService
	authz *service.AuthzService
}

// NewBeaconHandler 创建 Beacon 拉取路由处理器。
func NewBeaconHandler(svc *service.BeaconSyncService, authz *service.AuthzService) *BeaconHandler {
	return &BeaconHandler{svc: svc, authz: authz}
}

// requireWrite 校验拉取权限（instance:write）。
func (h *BeaconHandler) requireWrite(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// Pull POST /beacon/topology/pull
// 从 Beacon 拉取区服结构树与 server 归属，映射为本地分组树并补 region:/zone:/role: 标签。
// Beacon 不可达时返回 502 且本地零改动（先全量拉取校验、后单事务写入）。
func (h *BeaconHandler) Pull(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	if h.svc == nil || !h.svc.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "BEACON_SYNC_DISABLED",
			"message": "Beacon 拓扑拉取未启用（须配置 beacon.endpoint 并开启 beacon.pull-enabled）",
		})
		return
	}
	result, err := h.svc.PullTopology(c.Request.Context(), currentUserID(c), c.ClientIP())
	if err != nil {
		writeBeaconError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// Status GET /beacon/topology/status
// 返回拉取可用性，供前端在未配置协同时隐藏入口而非报错（可选协同，绝非依赖）。
func (h *BeaconHandler) Status(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"configured": h.svc != nil && h.svc.Configured(),
		"enabled":    h.svc != nil && h.svc.Enabled(),
	})
}

// RegisterRoutes 注册 Beacon 拉取路由。
func (h *BeaconHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/beacon/topology")
	{
		g.GET("/status", h.Status)
		g.POST("/pull", h.Pull)
	}
}

// writeBeaconError 把拉取错误映射为可区分的 HTTP 状态码：
// 未配置/未启用 → 503（能力未开），拓扑不合法 → 422，其余（不可达、写入失败）→ 502。
func writeBeaconError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrBeaconSyncDisabled),
		errors.Is(err, service.ErrBeaconNotConfigured),
		errors.Is(err, service.ErrBeaconPullDisabled):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "BEACON_SYNC_DISABLED", "message": err.Error()})
	case errors.Is(err, service.ErrBeaconTopologyInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BEACON_TOPOLOGY_INVALID", "message": err.Error()})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "BEACON_UNREACHABLE", "message": err.Error()})
	}
}
