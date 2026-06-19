package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// NetworkHandler 群组（Network 软标签）路由（FR-032 / ADR-007）。注册在平台管理员组下。
type NetworkHandler struct {
	svc *service.NetworkService
}

// NewNetworkHandler 创建群组路由处理器。
func NewNetworkHandler(svc *service.NetworkService) *NetworkHandler {
	return &NetworkHandler{svc: svc}
}

// List GET /networks
func (h *NetworkHandler) List(c *gin.Context) {
	networks, err := h.svc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, networks)
}

type createNetworkRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// Create POST /networks
func (h *NetworkHandler) Create(c *gin.Context) {
	var req createNetworkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	n, err := h.svc.Create(req.Name, req.Description)
	if err != nil {
		if errors.Is(err, service.ErrNetworkNameConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "NETWORK_NAME_CONFLICT", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, n)
}

// Get GET /networks/:id
func (h *NetworkHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	detail, err := h.svc.Get(id)
	if err != nil {
		writeNetworkError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

type updateNetworkRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// Update PATCH /networks/:id
func (h *NetworkHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req updateNetworkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	detail, err := h.svc.Update(id, req.Name, req.Description)
	if err != nil {
		writeNetworkError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// Delete DELETE /networks/:id
func (h *NetworkHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		writeNetworkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type addMembersRequest struct {
	InstanceIDs []uint `json:"instanceIds" binding:"required"`
}

// AddMembers POST /networks/:id/members
func (h *NetworkHandler) AddMembers(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req addMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	added, detail, err := h.svc.AddMembers(id, req.InstanceIDs)
	if err != nil {
		writeNetworkError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "members": detail.Members})
}

// RemoveMember DELETE /networks/:id/members/:instanceId
func (h *NetworkHandler) RemoveMember(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	iid, err := strconv.ParseUint(c.Param("instanceId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的实例 ID"})
		return
	}
	if err := h.svc.RemoveMember(id, uint(iid)); err != nil {
		writeNetworkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type networkActionRequest struct {
	Action string `json:"action" binding:"required"`
}

// Actions POST /networks/:id/actions
func (h *NetworkHandler) Actions(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req networkActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	result, err := h.svc.BatchAction(id, req.Action)
	if err != nil {
		if errors.Is(err, service.ErrInvalidBatchAction) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_ACTION", "message": err.Error()})
			return
		}
		writeNetworkError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// RegisterRoutes 注册群组路由。
func (h *NetworkHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/networks")
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.GET("/:id", h.Get)
		g.PATCH("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/members", h.AddMembers)
		g.DELETE("/:id/members/:instanceId", h.RemoveMember)
		g.POST("/:id/actions", h.Actions)
	}
}

func writeNetworkError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNetworkNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NETWORK_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrNetworkNameConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "NETWORK_NAME_CONFLICT", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
	}
}
