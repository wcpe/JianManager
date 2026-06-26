package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// InstanceGroupHandler 实例组织分组树路由（FR-165 / ADR-033）。
// 组织归类正交于用户组（RBAC）与网络群组（部署），权限沿用实例写权限节点
// instance:write（树是实例的人为归类，按实例写权限收敛，不引入新权限节点）。
type InstanceGroupHandler struct {
	svc   *service.InstanceGroupService
	authz *service.AuthzService
}

// NewInstanceGroupHandler 创建实例组织分组路由处理器。
func NewInstanceGroupHandler(svc *service.InstanceGroupService, authz *service.AuthzService) *InstanceGroupHandler {
	return &InstanceGroupHandler{svc: svc, authz: authz}
}

// requireRead 校验读分组树的权限（instance:read）。
func (h *InstanceGroupHandler) requireRead(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// requireWrite 校验写分组树的权限（instance:write）。
func (h *InstanceGroupHandler) requireWrite(c *gin.Context) bool {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return false
	}
	return true
}

// Tree GET /instance-groups
func (h *InstanceGroupHandler) Tree(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	tree, err := h.svc.Tree()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, tree)
}

type createInstanceGroupRequest struct {
	Name     string `json:"name" binding:"required,min=1,max=128"`
	ParentID *uint  `json:"parentId"`
}

// Create POST /instance-groups
func (h *InstanceGroupHandler) Create(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	var req createInstanceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	node, err := h.svc.Create(req.Name, req.ParentID)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusCreated, node)
}

// Update PUT /instance-groups/:id
// 以原始 JSON map 解析，借「parentId 字段是否出现」显式区分三态：
// 字段缺省=不改父；parentId:null=移到根（NULL）；parentId:N=移到 N 下。
func (h *InstanceGroupHandler) Update(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	raw := map[string]json.RawMessage{}
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	var namePtr *string
	if rawName, ok := raw["name"]; ok {
		var name string
		if err := json.Unmarshal(rawName, &name); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "名称格式错误"})
			return
		}
		namePtr = &name
	}

	var parentArg **uint
	if rawParent, ok := raw["parentId"]; ok {
		// 字段出现：本次要改父。值为 null → 移到根；否则解析为 *uint。
		var pid *uint
		if err := json.Unmarshal(rawParent, &pid); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "父分组格式错误"})
			return
		}
		parentArg = &pid
	}

	node, err := h.svc.Update(id, namePtr, parentArg)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, node)
}

// Delete DELETE /instance-groups/:id
func (h *InstanceGroupHandler) Delete(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(id); err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type instanceGroupMembersRequest struct {
	InstanceIDs []uint `json:"instanceIds" binding:"required"`
}

// AddMembers POST /instance-groups/:id/members
func (h *InstanceGroupHandler) AddMembers(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req instanceGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	added, members, err := h.svc.AddMembers(id, req.InstanceIDs)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "members": members})
}

// RemoveMembers DELETE /instance-groups/:id/members
func (h *InstanceGroupHandler) RemoveMembers(c *gin.Context) {
	if !h.requireWrite(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req instanceGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.svc.RemoveMembers(id, req.InstanceIDs); err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Members GET /instance-groups/:id/members
// 返回该组「子树（含自身及后代）去重」的成员实例概要 + 实例 ID 集合，
// 供右列表展示与「按组（含子树）筛选」共用。
func (h *InstanceGroupHandler) MembersSubtree(c *gin.Context) {
	if !h.requireRead(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	ids, err := h.svc.SubtreeInstanceIDs(id)
	if err != nil {
		writeInstanceGroupError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"instanceIds": ids})
}

// RegisterRoutes 注册实例组织分组路由。
func (h *InstanceGroupHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/instance-groups")
	{
		g.GET("", h.Tree)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.GET("/:id/instances", h.MembersSubtree)
		g.POST("/:id/members", h.AddMembers)
		g.DELETE("/:id/members", h.RemoveMembers)
	}
}

func writeInstanceGroupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInstanceGroupNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "INSTANCE_GROUP_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrInstanceGroupParentNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INSTANCE_GROUP_PARENT_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrInstanceGroupCycle):
		c.JSON(http.StatusConflict, gin.H{"error": "INSTANCE_GROUP_CYCLE", "message": err.Error()})
	case errors.Is(err, service.ErrInstanceGroupNotEmpty):
		c.JSON(http.StatusConflict, gin.H{"error": "INSTANCE_GROUP_NOT_EMPTY", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
	}
}
