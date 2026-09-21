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
	op, err := h.svc.Create(svcReq, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	if h.audit != nil {
		uid, _ := c.Get(middleware.CtxUserID)
		authorID, _ := uid.(uint)
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
	op, err := h.svc.Get(uint(opID))
	if err != nil {
		h.respondOpErr(c, err)
		return
	}
	c.JSON(http.StatusOK, op)
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
