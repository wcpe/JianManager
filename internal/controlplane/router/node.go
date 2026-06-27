package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// NodeHandler 节点路由处理器。
type NodeHandler struct {
	nodeSvc   *service.NodeService
	repairSvc *service.NodeRepairService
	audit     *service.AuditService
}

// NewNodeHandler 创建节点路由处理器。
// repairSvc / audit 可为 nil（坏节点修复入口随之关闭），保证既有装配与测试零改动可用。
func NewNodeHandler(nodeSvc *service.NodeService, repairSvc *service.NodeRepairService, audit *service.AuditService) *NodeHandler {
	return &NodeHandler{nodeSvc: nodeSvc, repairSvc: repairSvc, audit: audit}
}

// requirePlatformAdmin 校验当前用户是否为平台管理员。
func requirePlatformAdmin(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// List 节点列表（仅平台管理员）。
func (h *NodeHandler) List(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	nodes, err := h.nodeSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "查询节点列表失败",
		})
		return
	}
	c.JSON(http.StatusOK, nodes)
}

// Get 节点详情（仅平台管理员）。
func (h *NodeHandler) Get(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}

	node, err := h.nodeSvc.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	c.JSON(http.StatusOK, node)
}

type maintenanceRequest struct {
	// Enabled 置/解维护模式：true=cordon（禁新调度），false=解除。
	Enabled bool `json:"enabled"`
}

// Maintenance 置/解节点维护模式（cordon，仅平台管理员）。参见 FR-048。
func (h *NodeHandler) Maintenance(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}

	var req maintenanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	node, err := h.nodeSvc.SetMaintenance(id, req.Enabled)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "更新维护模式失败"})
		return
	}

	c.JSON(http.StatusOK, node)
}

// Drain 排空节点：停止其上运行实例（仅平台管理员，危险操作）。参见 FR-048。
func (h *NodeHandler) Drain(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}

	result, err := h.nodeSvc.Drain(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// Delete 主动下线节点：解除注册并保留记录（仅平台管理员，危险操作）。参见 FR-048。
func (h *NodeHandler) Delete(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}

	if err := h.nodeSvc.Delete(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已下线"})
}

// repairConfirmRequest 破坏性修复操作的二次确认请求体（见 ADR-039 §2）。
type repairConfirmRequest struct {
	// Confirm 必须显式为 true 才执行破坏性操作。
	Confirm bool `json:"confirm"`
}

// recordRepairAudit 记录坏节点修复审计（FR-015/FR-059）；audit 未注入时静默跳过。
func (h *NodeHandler) recordRepairAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(userID, action, "node", targetID, string(raw), c.ClientIP())
}

// repairUnavailable 当坏节点修复服务未注入时统一回 404（功能未开启）。
func (h *NodeHandler) repairUnavailable(c *gin.Context) bool {
	if h.repairSvc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "坏节点修复未启用"})
		return true
	}
	return false
}

// Suspects GET /nodes/repair/suspects — 列出疑似被串改/重名的节点（只读诊断，仅平台管理员）。参见 ADR-039 §2。
func (h *NodeHandler) Suspects(c *gin.Context) {
	if !requirePlatformAdmin(c) || h.repairUnavailable(c) {
		return
	}
	suspects, err := h.repairSvc.ListSuspects()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "检测坏节点失败"})
		return
	}
	c.JSON(http.StatusOK, suspects)
}

// Orphans GET /nodes/:id/orphans — 统计节点上孤立 JDK/实例数量（只读，仅平台管理员）。参见 ADR-039 §2。
func (h *NodeHandler) Orphans(c *gin.Context) {
	if !requirePlatformAdmin(c) || h.repairUnavailable(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	rep, err := h.repairSvc.OrphanReport(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "统计孤立资源失败"})
		return
	}
	c.JSON(http.StatusOK, rep)
}

// Reenroll POST /nodes/:id/reenroll — 把被挤占机器作为新节点重新 enroll（轮换 UUID/secret，破坏性，
// 需 confirm=true，仅平台管理员，入审计）。参见 ADR-039 §2。
func (h *NodeHandler) Reenroll(c *gin.Context) {
	if !requirePlatformAdmin(c) || h.repairUnavailable(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req repairConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.repairSvc.Reenroll(id, req.Confirm)
	if err != nil {
		h.writeRepairError(c, err)
		return
	}
	h.recordRepairAudit(c, "node.reenroll", res.OldUUID, map[string]any{"nodeId": id, "newUuid": res.NewUUID})
	c.JSON(http.StatusOK, res)
}

// PurgeOrphans POST /nodes/:id/purge-orphans — 清理节点上孤立 JDK/实例（破坏性，需 confirm=true，
// 仅平台管理员，入审计）。参见 ADR-039 §2。
func (h *NodeHandler) PurgeOrphans(c *gin.Context) {
	if !requirePlatformAdmin(c) || h.repairUnavailable(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req repairConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.repairSvc.PurgeOrphans(id, req.Confirm)
	if err != nil {
		h.writeRepairError(c, err)
		return
	}
	h.recordRepairAudit(c, "node.purge_orphans", c.Param("id"),
		map[string]any{"nodeId": id, "jdkDeleted": res.JDKDeleted, "instancesPurged": res.InstancesPurged})
	c.JSON(http.StatusOK, res)
}

// writeRepairError 把修复服务错误映射为 HTTP 响应：未确认→409、节点不存在→404、其它→500。
func (h *NodeHandler) writeRepairError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrRepairNotConfirmed):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFIRM_REQUIRED", "message": err.Error()})
	case errors.Is(err, service.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "修复操作失败"})
	}
}

// RegisterRoutes 注册节点路由。
func (h *NodeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	nodes := rg.Group("/nodes")
	{
		nodes.GET("", h.List)
		// 坏节点修复（见 ADR-039 §2）：诊断只读 + 破坏性修复（二次确认 + 审计）。
		// 静态段 repair 须在 /:id 之前注册，避免被 :id 通配吞掉。
		nodes.GET("/repair/suspects", h.Suspects)
		nodes.GET("/:id", h.Get)
		nodes.GET("/:id/orphans", h.Orphans)
		nodes.POST("/:id/maintenance", h.Maintenance)
		nodes.POST("/:id/drain", h.Drain)
		nodes.POST("/:id/reenroll", h.Reenroll)
		nodes.POST("/:id/purge-orphans", h.PurgeOrphans)
		nodes.DELETE("/:id", h.Delete)
	}
}
