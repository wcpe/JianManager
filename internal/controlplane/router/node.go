package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// NodeHandler 节点路由处理器。
type NodeHandler struct {
	nodeSvc   *service.NodeService
	audit     *service.AuditService
}

// NewNodeHandler 创建节点路由处理器。
// audit 可为 nil（审计未注入时静默跳过），保证既有装配与测试零改动可用。
func NewNodeHandler(nodeSvc *service.NodeService, audit *service.AuditService) *NodeHandler {
	return &NodeHandler{nodeSvc: nodeSvc, audit: audit}
}

// requireNodeRead 节点只读。
func requireNodeRead(c *gin.Context) bool {
	return requireNodes(c, "node.read", "node.manage")
}

// requireNodeManage 节点写/维护。
func requireNodeManage(c *gin.Context) bool {
	return requireNodes(c, "node.manage")
}

// requirePlatformAdmin 平台管理域**写/危险**默认门禁（F-01）。
// 历史误用为 node.read；现严格为 node.manage（超管恒过）。
// 只读 handler 请改用 requireNodeRead 或域读节点。
func requirePlatformAdmin(c *gin.Context) bool {
	return requireNodes(c, "node.manage")
}

// List 节点列表（仅平台管理员）。
func (h *NodeHandler) List(c *gin.Context) {
	if !requireNodeRead(c) {
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

// StartupProgress GET /nodes/startup-progress — 正在启动的 Worker 进度。
//
// 单独成端点而不并入 GET /nodes：启动中的 Worker 尚未注册，在 nodes 表里**没有记录**
// （首装连 UUID 都还没有）。若硬塞进节点列表，会凭空出现「存在但从未上线」的节点；
// 分开返回让前端能明确区分「已注册的节点」与「正在启动、还没注册的 Worker」。
func (h *NodeHandler) StartupProgress(c *gin.Context) {
	if !requireNodeRead(c) {
		return
	}
	items, err := h.nodeSvc.ListStartupProgress()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "查询启动进度失败",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Get 节点详情（仅平台管理员）。
func (h *NodeHandler) Get(c *gin.Context) {
	if !requireNodeRead(c) {
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

// Maintenance 置/解节点维护模式（cordon，仅平台管理员）。参见。
// Maintenance 维护模式（写）。
func (h *NodeHandler) Maintenance(c *gin.Context) {
	if !requireNodeManage(c) {
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

// Drain 排空节点：停止其上运行实例（仅平台管理员，危险操作）。参见。
// Drain 排空节点（写）。
func (h *NodeHandler) Drain(c *gin.Context) {
	if !requireNodeManage(c) {
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

// Delete 主动下线节点：解除注册并保留记录（仅平台管理员，危险操作）。参见。
// 实例守卫：名下有实例回 409 并携实例清单；离线节点可 `?force=true` 显式级联
// 删除实例平台记录（不清理远端文件）；在线节点无论 force 一律拒绝。
// Delete 删除节点（写）。
func (h *NodeHandler) Delete(c *gin.Context) {
	if !requireNodeManage(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}

	force := false
	if raw := c.Query("force"); raw != "" {
		force, err = strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "force 参数格式错误"})
			return
		}
	}

	result, err := h.nodeSvc.Delete(id, force)
	if err != nil {
		var hasInst *service.NodeHasInstancesError
		if errors.As(err, &hasInst) {
			c.JSON(http.StatusConflict, gin.H{
				"error":     "NODE_HAS_INSTANCES",
				"message":   err.Error(),
				"instances": hasInst.Instances,
			})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已下线", "instancesPurged": result.InstancesPurged})
}

// ListArchived 已下线（软删）节点列表（仅平台管理员）。
func (h *NodeHandler) ListArchived(c *gin.Context) {
	if !requireNodeRead(c) {
		return
	}
	nodes, err := h.nodeSvc.ListArchived()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询归档节点失败"})
		return
	}
	c.JSON(http.StatusOK, nodes)
}

// GetArchived 单个归档节点详情（仅平台管理员）。
func (h *NodeHandler) GetArchived(c *gin.Context) {
	if !requireNodeRead(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	node, err := h.nodeSvc.GetArchived(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "归档节点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, node)
}

// PurgeArchived 彻底清理归档节点（硬删库记录，不清理远端文件）。
// PurgeArchived 清理归档节点（写）。
func (h *NodeHandler) PurgeArchived(c *gin.Context) {
	if !requireNodeManage(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	force := false
	if raw := c.Query("force"); raw != "" {
		force, err = strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "force 参数格式错误"})
			return
		}
	}

	result, err := h.nodeSvc.Purge(id, force)
	if err != nil {
		var hasInst *service.NodeHasInstancesError
		if errors.As(err, &hasInst) {
			c.JSON(http.StatusConflict, gin.H{
				"error":     "NODE_HAS_INSTANCES",
				"message":   err.Error(),
				"instances": hasInst.Instances,
			})
			return
		}
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "归档节点不存在"})
			return
		}
		if errors.Is(err, service.ErrNodeNotArchived) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	h.recordNodeAudit(c, "node.purge", strconv.FormatUint(uint64(id), 10), map[string]any{
		"force":           force,
		"instancesPurged": result.InstancesPurged,
	})
	c.JSON(http.StatusOK, gin.H{"message": "已清理", "instancesPurged": result.InstancesPurged})
}

// recordNodeAudit 记录节点操作的审计；audit 未注入时静默跳过。
func (h *NodeHandler) recordNodeAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	h.audit.RecordSafe(userID, action, "node", targetID, marshalAuditDetail(detail), c.ClientIP())
}

// Metrics 返回节点实时指标快照（仅平台管理员）。
func (h *NodeHandler) Metrics(c *gin.Context) {
	if !requireNodeRead(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}

	metrics, err := h.nodeSvc.GetMetrics(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "METRICS_UNAVAILABLE", "message": "无法获取节点指标"})
		return
	}

	c.JSON(http.StatusOK, metrics)
}

// RegisterRoutes 注册节点路由。
func (h *NodeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	nodes := rg.Group("/nodes")
	{
		nodes.GET("", h.List)
		// 归档列表/详情/清理（394）：静态段 archived 须在 /:id 之前。
		nodes.GET("/archived", h.ListArchived)
		nodes.GET("/archived/:id", h.GetArchived)
		// 正在启动的 Worker 进度：静态段 startup-progress 须在 /:id 之前。
		nodes.GET("/startup-progress", h.StartupProgress)
		nodes.DELETE("/archived/:id", h.PurgeArchived)
		nodes.GET("/:id", h.Get)
		nodes.GET("/:id/metrics", h.Metrics)
		nodes.POST("/:id/maintenance", h.Maintenance)
		nodes.POST("/:id/drain", h.Drain)
		nodes.DELETE("/:id", h.Delete)
	}
}
