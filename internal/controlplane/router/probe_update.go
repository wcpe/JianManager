package router

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ProbeUpdateHandler 探针在线更新路由处理器（FR-068/409，见 ADR-083）。
// 复用已有 gRPC DeployServerProbe 下发 CP-local jar 引用，下次重启生效；可选「推送并重启」立即生效。
type ProbeUpdateHandler struct {
	updateSvc   *service.ProbeUpdateService
	instanceSvc *service.InstanceService
	authz       *service.AuthzService
	artifacts   *service.ArtifactVersionService
}

// NewProbeUpdateHandler 创建探针在线更新路由处理器。
// instanceSvc 供 restart=true 时复用实例重启逻辑（使新 jar 立即生效）。
func NewProbeUpdateHandler(updateSvc *service.ProbeUpdateService, instanceSvc *service.InstanceService, authz *service.AuthzService, artifacts ...*service.ArtifactVersionService) *ProbeUpdateHandler {
	var artifactSvc *service.ArtifactVersionService
	if len(artifacts) > 0 {
		artifactSvc = artifacts[0]
	}
	return &ProbeUpdateHandler{updateSvc: updateSvc, instanceSvc: instanceSvc, authz: authz, artifacts: artifactSvc}
}

// Status GET /instances/:id/probe/update — 返回探针更新状态（连接 + 解析版本 + 上次推送时间）。
func (h *ProbeUpdateHandler) Status(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	st, err := h.updateSvc.Status(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询探针状态失败"})
		return
	}
	c.JSON(http.StatusOK, st)
}

type probeUpdateRequest struct {
	Restart bool `json:"restart"`
}

// Update POST /instances/:id/probe/update — 推送已选探针版本到该实例（下次重启生效）。
// 权限 instance:operate；危险/操作经审计中间件留痕（probe.update）。restart=true 时推送后重启。
func (h *ProbeUpdateHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var req probeUpdateRequest
	// 请求体可选；非法 JSON 不阻断（按默认 restart=false 处理）。
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Debug("探针更新请求体按零值处理", "error", err)
	}

	res, err := h.updateSvc.UpdateWithBaseURL(id, selfUpdateRequestBaseURL(c))
	if err != nil {
		h.respondUpdateErr(c, err)
		return
	}

	if req.Restart {
		if rerr := h.instanceSvc.Restart(id); rerr != nil {
			// 推送已成功；重启失败不回滚 jar（已就位下次重启仍生效），仅在响应里标注。
			slog.Warn("探针推送后重启失败（jar 已就位，下次手动重启生效）", "instanceId", id, "err", rerr)
			res.Message = "探针 jar 已就位；自动重启失败，请手动重启使其生效：" + rerr.Error()
		} else {
			res.Restarted = true
			res.Message = "探针 jar 已就位，正在重启实例使其生效"
		}
	}
	c.JSON(http.StatusOK, res)
}

// respondUpdateErr 将更新错误映射为 HTTP 状态码。
func (h *ProbeUpdateHandler) respondUpdateErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
	case errors.Is(err, service.ErrProbeNotEmbedded):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "PROBE_NOT_EMBEDDED", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

type probeUpdateBatchRequest struct {
	IDs     []uint                         `json:"ids"`
	Filter  *service.InstanceBatchFilterIn `json:"filter"`
	Restart bool                           `json:"restart"`
}

// Batch POST /instances/probe/update — 批量推送已选探针版本（按 ids/filter）。
// 权限 instance:operate；资源隔离（越权/不存在计入 skipped，镜像 FR-058）；审计 probe.update.batch。
func (h *ProbeUpdateHandler) Batch(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req probeUpdateBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需指定 ids 或 filter"})
		return
	}

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	svcReq := service.ProbeUpdateBatchRequest{IDs: req.IDs, Restart: req.Restart}
	if req.Filter != nil {
		f := req.Filter.ToFilter()
		svcReq.Filter = &f
	}

	// restart=true 时，对每个推送成功的实例异步重启（与单实例 restart 语义一致，不阻塞批量计数）。
	var onDeployed func(inst *model.Instance)
	if req.Restart {
		onDeployed = func(inst *model.Instance) {
			if rerr := h.instanceSvc.Restart(inst.ID); rerr != nil {
				slog.Warn("批量探针推送后重启失败（jar 已就位，下次重启生效）", "instanceId", inst.ID, "err", rerr)
			}
		}
	}

	res, err := h.updateSvc.BatchWithBaseURL(svcReq, scopeIDs, scope, selfUpdateRequestBaseURL(c), onDeployed)
	if err != nil {
		if errors.Is(err, service.ErrProbeNotEmbedded) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "PROBE_NOT_EMBEDDED", "message": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

type setInstanceProbeVersionRequest struct {
	// VersionID 为 0 时恢复继承；保存后仍立即按解析后的有效版本通知 Worker 拉取。
	VersionID uint `json:"versionId"`
}

// Version GET /instances/:id/probe-version — 返回实例显式覆盖和最终继承解析结果。
func (h *ProbeUpdateHandler) Version(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	if h.artifacts == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ARTIFACT_VERSION_LIBRARY_UNAVAILABLE"})
		return
	}
	explicit, err := h.artifacts.InstanceProbeVersion(id)
	if err != nil {
		h.respondProbeVersionError(c, err)
		return
	}
	resolved, origin, err := h.artifacts.ResolveInstanceProbeVersion(id)
	if err != nil {
		h.respondProbeVersionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"instanceId": id, "versionId": explicit, "resolvedVersion": resolved, "origin": origin})
}

// SetVersion PUT /instances/:id/probe-version — 手动升级/回滚/恢复继承，并立即通知 Worker 拉取 jar。
func (h *ProbeUpdateHandler) SetVersion(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	if h.artifacts == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ARTIFACT_VERSION_LIBRARY_UNAVAILABLE"})
		return
	}
	var req setInstanceProbeVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.artifacts.SetInstanceProbeVersion(id, req.VersionID); err != nil {
		h.respondProbeVersionError(c, err)
		return
	}
	result, err := h.updateSvc.UpdateWithBaseURL(id, selfUpdateRequestBaseURL(c))
	if err != nil {
		h.respondUpdateErr(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ProbeUpdateHandler) respondProbeVersionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInstanceNotFound), errors.Is(err, service.ErrArtifactVersionNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrArtifactVersionNotCached):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "VERSION_NOT_CACHED", "message": err.Error()})
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
	}
}

// RegisterRoutes 注册探针在线更新路由。
// 加性追加：单实例挂 /instances/:id/probe/update，批量挂 /instances/probe/update。
func (h *ProbeUpdateHandler) RegisterRoutes(rg *gin.RouterGroup) {
	// 批量在前注册：/instances/probe/update 的 "probe" 段是字面量，
	// gin 的 radix 路由允许其与 /instances/:id/... 共存（静态段优先匹配）。
	rg.POST("/instances/probe/update", h.Batch)
	rg.GET("/instances/:id/probe-version", h.Version)
	rg.PUT("/instances/:id/probe-version", h.SetVersion)
	rg.GET("/instances/:id/probe/update", h.Status)
	rg.POST("/instances/:id/probe/update", h.Update)
}
