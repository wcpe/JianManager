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

// List 节点列表。
func (h *NodeHandler) List(c *gin.Context) {
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

// Get 节点详情。
func (h *NodeHandler) Get(c *gin.Context) {
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

// Delete 删除节点。
func (h *NodeHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	if err := h.nodeSvc.Delete(id); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RegisterRoutes 注册节点路由。
func (h *NodeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	nodes := rg.Group("/nodes")
	{
		nodes.GET("", h.List)
		nodes.GET("/:id", h.Get)
		nodes.DELETE("/:id", h.Delete)
	}
}
