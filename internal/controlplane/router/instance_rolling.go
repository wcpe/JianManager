package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// InstanceRollingHandler 实例滚动/分批/灰度编排路由处理器（FR-457）。
type InstanceRollingHandler struct {
	svc   *service.InstanceRollingService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewInstanceRollingHandler 创建滚动编排路由处理器。
func NewInstanceRollingHandler(svc *service.InstanceRollingService, authz *service.AuthzService, audit *service.AuditService) *InstanceRollingHandler {
	return &InstanceRollingHandler{svc: svc, authz: authz, audit: audit}
}

type rollingCreateRequest struct {
	Action           string                         `json:"action"`
	IDs              []uint                         `json:"ids"`
	Filter           *service.InstanceBatchFilterIn `json:"filter"`
	Command          string                         `json:"command"`
	BatchSize        int                            `json:"batchSize"`
	BatchIntervalSec int                            `json:"batchIntervalSec"`
	FailFast         bool                           `json:"failFast"`
	Ratio            float64                        `json:"ratio"`
}

// Create 创建并启动滚动编排。
func (h *InstanceRollingHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	var req rollingCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	action := service.InstanceBatchAction(req.Action)
	if !service.ValidInstanceBatchAction(action) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "不支持的滚动动作"})
		return
	}
	if len(req.IDs) == 0 && req.Filter == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "需指定 ids 或 filter"})
		return
	}
	if action == service.InstanceBatchCommand && req.Command == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "command 动作需指定 command"})
		return
	}
	if req.BatchSize < 0 || req.BatchIntervalSec < 0 || req.Ratio < 0 || req.Ratio > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "策略参数越界"})
		return
	}
	if h.authz == nil {
		// fail-closed：无授权服务时无法计算可访问集合，拒绝而非放行（避免越权编排）。
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "授权服务不可用"})
		return
	}
	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	svcReq := service.RollingRequest{
		Action:  action,
		IDs:     req.IDs,
		Command: req.Command,
		Policy: model.RollingPolicy{
			BatchSize:        req.BatchSize,
			BatchIntervalSec: req.BatchIntervalSec,
			FailFast:         req.FailFast,
			Ratio:            req.Ratio,
		},
	}
	if req.Filter != nil {
		f := req.Filter.ToFilter()
		svcReq.Filter = &f
	}
	uid, _ := c.Get(middleware.CtxUserID)
	authorID, _ := uid.(uint)
	op, err := h.svc.Create(svcReq, scopeIDs, scope, authorID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	if h.audit != nil {
		h.audit.RecordSafe(authorID, "instance_rolling_create", "instance_rolling",
			strconv.FormatUint(uint64(op.ID), 10),
			"action="+string(action)+" requested="+strconv.Itoa(op.Requested), c.ClientIP())
	}
	c.JSON(http.StatusOK, op)
}

// Get 查询编排会话。
func (h *InstanceRollingHandler) Get(c *gin.Context) {
	if !requireNodes(c, "instance.read", "instance.operate") {
		return
	}
	opID, err := strconv.ParseUint(c.Param("opId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的编排 ID"})
		return
	}
	if _, ok := h.authorizeOp(c, uint(opID)); !ok {
		return
	}
	op, err := h.svc.Get(uint(opID))
	if err != nil {
		h.respondOpErr(c, err)
		return
	}
	c.JSON(http.StatusOK, op)
}

// authorizeOp 加载编排会话并校验调用者对其全部目标有访问权（FR-457 越权修复，IDOR）。
// 平台管理员放行；非管理员要求 op.Targets ⊆ 可访问实例集合，否则以 404 隐藏存在性。
func (h *InstanceRollingHandler) authorizeOp(c *gin.Context, opID uint) (*model.InstanceRollingOp, bool) {
	op, err := h.svc.Get(opID)
	if err != nil {
		h.respondOpErr(c, err)
		return nil, false
	}
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return nil, false
	}
	if access.IsPlatformAdmin {
		return op, true
	}
	if h.authz == nil {
		// fail-closed：无法判定归属时拒绝，避免退化为放行（与空目标集真空通过同源）。
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限校验不可用"})
		return nil, false
	}
	scopeIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return nil, false
	}
	if !scoped {
		return op, true
	}
	// 空目标集无法用「op.Targets ⊆ 可访问」判定归属（循环会真空通过）：仅创建者可见，其余以 404 隐藏存在性。
	if len(op.Targets) == 0 {
		if op.CreatedBy != access.UserID {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "编排会话不存在"})
			return nil, false
		}
		return op, true
	}
	allow := make(map[uint]struct{}, len(scopeIDs))
	for _, id := range scopeIDs {
		allow[id] = struct{}{}
	}
	for _, t := range op.Targets {
		if _, ok := allow[t]; !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "编排会话不存在"})
			return nil, false
		}
	}
	return op, true
}

// Pause 暂停编排。
func (h *InstanceRollingHandler) Pause(c *gin.Context) {
	h.control(c, h.svc.Pause, "instance_rolling_pause")
}

// Resume 继续编排。
func (h *InstanceRollingHandler) Resume(c *gin.Context) {
	h.control(c, h.svc.Resume, "instance_rolling_resume")
}

// Cancel 取消编排。
func (h *InstanceRollingHandler) Cancel(c *gin.Context) {
	h.control(c, h.svc.Cancel, "instance_rolling_cancel")
}

func (h *InstanceRollingHandler) control(c *gin.Context, fn func(uint) error, action string) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	opID, err := strconv.ParseUint(c.Param("opId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的编排 ID"})
		return
	}
	// 越权校验（FR-457 IDOR）：非管理员仅能控制其可访问实例集合内的编排。
	if _, ok := h.authorizeOp(c, uint(opID)); !ok {
		return
	}
	if err := fn(uint(opID)); err != nil {
		h.respondOpErr(c, err)
		return
	}
	if h.audit != nil {
		uid, _ := c.Get(middleware.CtxUserID)
		authorID, _ := uid.(uint)
		h.audit.RecordSafe(authorID, action, "instance_rolling", c.Param("opId"), "", c.ClientIP())
	}
	op, err := h.svc.Get(uint(opID))
	if err != nil {
		h.respondOpErr(c, err)
		return
	}
	c.JSON(http.StatusOK, op)
}

func (h *InstanceRollingHandler) respondOpErr(c *gin.Context, err error) {
	if errors.Is(err, service.ErrRollingOpNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "编排会话不存在"})
		return
	}
	c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
}

// RegisterRoutes 注册滚动编排路由（加性追加，不动既有批量端点）。
func (h *InstanceRollingHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/instances/rolling")
	g.POST("", h.Create)
	g.GET("/:opId", h.Get)
	g.POST("/:opId/pause", h.Pause)
	g.POST("/:opId/resume", h.Resume)
	g.POST("/:opId/cancel", h.Cancel)
}
