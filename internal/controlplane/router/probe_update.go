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

// ProbeUpdateHandler 探针在线更新路由处理器（FR-068，见 ADR-016）。
// 复用已有 gRPC DeployServerProbe 把内嵌探针 jar 推到实例，下次重启生效；可选「推送并重启」立即生效。
type ProbeUpdateHandler struct {
	updateSvc   *service.ProbeUpdateService
	instanceSvc *service.InstanceService
	authz       *service.AuthzService
}

// NewProbeUpdateHandler 创建探针在线更新路由处理器。
// instanceSvc 供 restart=true 时复用实例重启逻辑（使新 jar 立即生效）。
func NewProbeUpdateHandler(updateSvc *service.ProbeUpdateService, instanceSvc *service.InstanceService, authz *service.AuthzService) *ProbeUpdateHandler {
	return &ProbeUpdateHandler{updateSvc: updateSvc, instanceSvc: instanceSvc, authz: authz}
}

// Status GET /instances/:id/probe/update — 返回探针更新状态（连接 + 内嵌版本 + 上次推送时间）。
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

// Update POST /instances/:id/probe/update — 推送内嵌探针 jar 到该实例（下次重启生效）。
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
	_ = c.ShouldBindJSON(&req)

	res, err := h.updateSvc.Update(id)
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

// Batch POST /instances/probe/update — 批量推送内嵌探针 jar（按 ids/filter）。
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

	res, err := h.updateSvc.Batch(svcReq, scopeIDs, scope, onDeployed)
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

// RegisterRoutes 注册探针在线更新路由。
// 加性追加：单实例挂 /instances/:id/probe/update，批量挂 /instances/probe/update。
func (h *ProbeUpdateHandler) RegisterRoutes(rg *gin.RouterGroup) {
	// 批量在前注册：/instances/probe/update 的 "probe" 段是字面量，
	// gin 的 radix 路由允许其与 /instances/:id/... 共存（静态段优先匹配）。
	rg.POST("/instances/probe/update", h.Batch)
	rg.GET("/instances/:id/probe/update", h.Status)
	rg.POST("/instances/:id/probe/update", h.Update)
}
