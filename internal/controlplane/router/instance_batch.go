package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// InstanceBatchHandler 实例批量操作路由处理器（FR-058）。
type InstanceBatchHandler struct {
	batchSvc *service.InstanceBatchService
	authz    *service.AuthzService
}

// NewInstanceBatchHandler 创建实例批量操作路由处理器。
func NewInstanceBatchHandler(batchSvc *service.InstanceBatchService, authz *service.AuthzService) *InstanceBatchHandler {
	return &InstanceBatchHandler{batchSvc: batchSvc, authz: authz}
}

type instanceBatchRequest struct {
	Action  string                          `json:"action"`
	IDs     []uint                          `json:"ids"`
	Filter  *service.InstanceBatchFilterIn  `json:"filter"`
	Command string                          `json:"command"`
}

// Batch 批量执行 command/start/stop/restart/kill，经 gRPC 委托对应 Worker（FR-058）。
// 权限隔离：仅操作有权实例，越权/不存在 id 计入 skipped（存在性隐藏，镜像 Bot 批量）。
// 危险操作（批量 kill/stop）的二次确认在前端完成；服务端依赖审计中间件留痕。
func (h *InstanceBatchHandler) Batch(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req instanceBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	action := service.InstanceBatchAction(req.Action)
	if !service.ValidInstanceBatchAction(action) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "不支持的批量动作"})
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

	scopeIDs, scope, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	svcReq := service.InstanceBatchRequest{
		Action:  action,
		IDs:     req.IDs,
		Command: req.Command,
	}
	if req.Filter != nil {
		f := req.Filter.ToFilter()
		svcReq.Filter = &f
	}

	res, err := h.batchSvc.Batch(svcReq, scopeIDs, scope)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// RegisterRoutes 注册实例批量操作路由。
// 加性追加，挂在 /instances/batch；不改既有 InstanceHandler 路由（避免与 FR-047 冲突）。
func (h *InstanceBatchHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/instances/batch", h.Batch)
}
