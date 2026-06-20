package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// NodeHandler 节点路由处理器。
type NodeHandler struct {
	nodeSvc *service.NodeService
}

// NewNodeHandler 创建节点路由处理器。
func NewNodeHandler(nodeSvc *service.NodeService) *NodeHandler {
	return &NodeHandler{nodeSvc: nodeSvc}
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

// RegisterRoutes 注册节点路由。
func (h *NodeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	nodes := rg.Group("/nodes")
	{
		nodes.GET("", h.List)
		nodes.GET("/:id", h.Get)
		nodes.POST("/:id/maintenance", h.Maintenance)
		nodes.POST("/:id/drain", h.Drain)
		nodes.DELETE("/:id", h.Delete)
	}
}
