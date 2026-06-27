package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// SelfUpdateHandler 面板自更新路由（FR-081，见 ADR-020 §4）。
// 挂在平台管理员组下（仅平台管理员）；升级类操作写审计（detail 仅含版本，绝不含下载凭据）。
type SelfUpdateHandler struct {
	svc   *service.SelfUpdateService
	audit *service.AuditService
}

// NewSelfUpdateHandler 创建自更新路由处理器。
func NewSelfUpdateHandler(svc *service.SelfUpdateService, audit *service.AuditService) *SelfUpdateHandler {
	return &SelfUpdateHandler{svc: svc, audit: audit}
}

// upgradeRequest 升级请求体（CP / 单节点共用，全部可选）。
type upgradeRequest struct {
	// Version 目标版本；留空取 feed 最新版本。
	Version string `json:"version"`
}

// upgradeAllRequest 全网升级请求体（全部可选）。
type upgradeAllRequest struct {
	Version string `json:"version"`
	// NodeIDs 限定升级的节点；留空=全部在线节点。
	NodeIDs []uint `json:"nodeIds"`
}

// Check GET /self-update/check — 检查更新（CP + 各节点版本对比）。
func (h *SelfUpdateHandler) Check(c *gin.Context) {
	res, err := h.svc.CheckUpdate(c.Request.Context())
	if err != nil {
		if errors.Is(err, service.ErrUpdateNotConfigured) {
			c.JSON(http.StatusConflict, gin.H{"error": "UPDATE_NOT_CONFIGURED", "message": "未配置更新源"})
			return
		}
		if errors.Is(err, service.ErrUpdateRateLimited) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "UPDATE_RATE_LIMITED", "message": "GitHub API 限流，请稍后重试或配置 github_token"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "UPDATE_CHECK_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// UpgradeControlPlane POST /self-update/control-plane/upgrade — 升级 CP 自身。
func (h *SelfUpdateHandler) UpgradeControlPlane(c *gin.Context) {
	var req upgradeRequest
	_ = c.ShouldBindJSON(&req)

	from, to, err := h.svc.UpgradeControlPlane(c.Request.Context(), req.Version)
	if err != nil {
		h.respondUpgradeError(c, err)
		return
	}
	h.recordAudit(c, "self_update.control_plane", map[string]any{"fromVersion": from, "toVersion": to})
	c.JSON(http.StatusAccepted, gin.H{"status": "restarting", "fromVersion": from, "toVersion": to})
}

// UpgradeNode POST /self-update/nodes/:id/upgrade — 升级单个 Worker 节点。
func (h *SelfUpdateHandler) UpgradeNode(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req upgradeRequest
	_ = c.ShouldBindJSON(&req)

	from, to, err := h.svc.UpgradeNode(c.Request.Context(), id, req.Version)
	if err != nil {
		h.respondUpgradeError(c, err)
		return
	}
	h.recordAudit(c, "self_update.node", map[string]any{"nodeId": id, "fromVersion": from, "toVersion": to})
	c.JSON(http.StatusAccepted, gin.H{"status": "upgrading", "nodeId": id, "fromVersion": from, "toVersion": to})
}

// RollbackControlPlane POST /self-update/control-plane/rollback — 回滚 CP 自身到升级前备份（FR-182）。
func (h *SelfUpdateHandler) RollbackControlPlane(c *gin.Context) {
	from, to, err := h.svc.RollbackControlPlane(c.Request.Context())
	if err != nil {
		h.respondUpgradeError(c, err)
		return
	}
	h.recordAudit(c, "self_update.control_plane_rollback", map[string]any{"fromVersion": from, "toVersion": to})
	c.JSON(http.StatusAccepted, gin.H{"status": "restarting", "fromVersion": from, "toVersion": to})
}

// RollbackNode POST /self-update/nodes/:id/rollback — 回滚单个节点到其升级前备份（FR-182）。
func (h *SelfUpdateHandler) RollbackNode(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	from, to, err := h.svc.RollbackNode(c.Request.Context(), id)
	if err != nil {
		h.respondUpgradeError(c, err)
		return
	}
	h.recordAudit(c, "self_update.node_rollback", map[string]any{"nodeId": id, "fromVersion": from, "toVersion": to})
	c.JSON(http.StatusAccepted, gin.H{"status": "rolling-back", "nodeId": id, "fromVersion": from, "toVersion": to})
}

// UpgradeAll POST /self-update/nodes/upgrade-all — 全网逐节点升级编排（异步）。
func (h *SelfUpdateHandler) UpgradeAll(c *gin.Context) {
	var req upgradeAllRequest
	_ = c.ShouldBindJSON(&req)

	ro, err := h.svc.StartRollout(c.Request.Context(), req.NodeIDs, req.Version)
	if err != nil {
		if errors.Is(err, service.ErrUpdateNotConfigured) {
			c.JSON(http.StatusConflict, gin.H{"error": "UPDATE_NOT_CONFIGURED", "message": "未配置更新源"})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "ROLLOUT_BUSY", "message": err.Error()})
		return
	}
	h.recordAudit(c, "self_update.rollout", map[string]any{"targetVersion": req.Version, "total": ro.Total})
	c.JSON(http.StatusAccepted, ro)
}

// Rollout GET /self-update/rollout — 查询当前/最近一次全网升级进度。
func (h *SelfUpdateHandler) Rollout(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.RolloutSnapshot())
}

// respondUpgradeError 把升级错误映射为合适的 HTTP 状态码。
func (h *SelfUpdateHandler) respondUpgradeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrUpdateNotConfigured):
		c.JSON(http.StatusConflict, gin.H{"error": "UPDATE_NOT_CONFIGURED", "message": "未配置更新源"})
	case errors.Is(err, service.ErrUpdateRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "UPDATE_RATE_LIMITED", "message": "GitHub API 限流，请稍后重试或配置 github_token"})
	case errors.Is(err, service.ErrUpdateAlreadyLatest):
		c.JSON(http.StatusConflict, gin.H{"error": "UPDATE_ALREADY_LATEST", "message": "已是最新版本"})
	case errors.Is(err, service.ErrNoBackup):
		c.JSON(http.StatusConflict, gin.H{"error": "UPDATE_NO_BACKUP", "message": "无可回滚的备份（尚未升级过或备份缺失）"})
	case errors.Is(err, service.ErrUpdateNoArtifact):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "UPDATE_NO_ARTIFACT", "message": "更新源无匹配本平台的制品"})
	case errors.Is(err, service.ErrNodeOffline):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "NODE_OFFLINE", "message": "节点未连接"})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "UPDATE_FAILED", "message": err.Error()})
	}
}

// currentUserID 取当前登录用户 ID（审计用）；缺失返回 0。
func (h *SelfUpdateHandler) currentUserID(c *gin.Context) uint {
	v, _ := c.Get(middleware.CtxUserID)
	id, _ := v.(uint)
	return id
}

// recordAudit 记录自更新操作审计（detail 仅含版本/节点元数据，绝不含下载 url 或凭据）。
func (h *SelfUpdateHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(h.currentUserID(c), action, "self_update", "", string(raw), c.ClientIP())
}

// RegisterRoutes 注册自更新路由（挂在平台管理员组下，无需再判 IsPlatformAdmin）。
func (h *SelfUpdateHandler) RegisterRoutes(rg *gin.RouterGroup) {
	su := rg.Group("/self-update")
	{
		su.GET("/check", h.Check)
		su.POST("/control-plane/upgrade", h.UpgradeControlPlane)
		su.POST("/control-plane/rollback", h.RollbackControlPlane)
		su.POST("/nodes/:id/upgrade", h.UpgradeNode)
		su.POST("/nodes/:id/rollback", h.RollbackNode)
		su.POST("/nodes/upgrade-all", h.UpgradeAll)
		su.GET("/rollout", h.Rollout)
	}
}
