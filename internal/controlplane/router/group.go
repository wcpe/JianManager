package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/middleware"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// GroupHandler 用户组路由处理器。
type GroupHandler struct {
	groupSvc *service.GroupService
	authz    *service.AuthzService
}

// NewGroupHandler 创建用户组路由处理器。
func NewGroupHandler(groupSvc *service.GroupService, authz *service.AuthzService) *GroupHandler {
	return &GroupHandler{groupSvc: groupSvc, authz: authz}
}

// List 用户组列表。
// 平台管理员返回全部用户组；组管理员/成员仅返回其可访问的组。
func (h *GroupHandler) List(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	if !access.HasPermission(service.PermGroupRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	groups, err := h.groupSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "查询用户组列表失败",
		})
		return
	}

	// 非平台管理员按可访问组过滤
	if !access.IsPlatformAdmin {
		filtered := make([]model.Group, 0, len(groups))
		for i := range groups {
			if access.CanAccessGroup(groups[i].ID) {
				filtered = append(filtered, groups[i])
			}
		}
		groups = filtered
	}

	c.JSON(http.StatusOK, groups)
}

// Get 用户组详情。
func (h *GroupHandler) Get(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil || !access.CanAccessGroup(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户组不存在"})
		return
	}

	group, err := h.groupSvc.GetByID(id)
	if err != nil {
		if errors.Is(err, service.ErrGroupNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户组不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	c.JSON(http.StatusOK, group)
}

type createGroupRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=128"`
	Description string `json:"description"`
}

// Create 创建用户组（仅平台管理员）。
func (h *GroupHandler) Create(c *gin.Context) {
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermGroupManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req createGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	group, err := h.groupSvc.Create(req.Name, req.Description)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建用户组失败"})
		return
	}

	c.JSON(http.StatusCreated, group)
}

type updateGroupRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// Update 更新用户组（组管理员或平台管理员）。
func (h *GroupHandler) Update(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil || !access.CanManageGroup(id) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权管理该用户组"})
		return
	}

	var req updateGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	group, err := h.groupSvc.Update(id, req.Name, req.Description)
	if err != nil {
		if errors.Is(err, service.ErrGroupNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户组不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "更新用户组失败"})
		return
	}

	c.JSON(http.StatusOK, group)
}

// Delete 删除用户组（仅平台管理员）。
func (h *GroupHandler) Delete(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermGroupManage) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	if err := h.groupSvc.Delete(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "删除用户组失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

type addMemberRequest struct {
	UserID uint                  `json:"userId" binding:"required"`
	Role   model.GroupMemberRole `json:"role"`
}

// AddMember 添加组成员（组管理员或平台管理员）。
func (h *GroupHandler) AddMember(c *gin.Context) {
	groupID, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil || !access.CanManageGroup(groupID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权管理该用户组"})
		return
	}

	var req addMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if err := h.groupSvc.AddMember(groupID, req.UserID, req.Role); err != nil {
		if errors.Is(err, service.ErrAlreadyMember) {
			c.JSON(http.StatusConflict, gin.H{"error": "ALREADY_MEMBER", "message": "已经是组成员"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "添加成员失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已添加"})
}

// RemoveMember 移除组成员（组管理员或平台管理员）。
func (h *GroupHandler) RemoveMember(c *gin.Context) {
	groupID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的组 ID"})
		return
	}

	access := getAccess(c)
	if access == nil || !access.CanManageGroup(uint(groupID)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权管理该用户组"})
		return
	}

	userID, err := strconv.ParseUint(c.Param("userId"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的用户 ID"})
		return
	}

	if err := h.groupSvc.RemoveMember(uint(groupID), uint(userID)); err != nil {
		if errors.Is(err, service.ErrNotMember) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_MEMBER", "message": "不是组成员"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "移除成员失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已移除"})
}

type updateQuotaRequest struct {
	MaxInstances *int `json:"maxInstances"`
	MaxBots      *int `json:"maxBots"`
	MaxStorageMB *int `json:"maxStorageMb"`
}

// UpdateQuota 更新组配额（仅平台管理员）。
func (h *GroupHandler) UpdateQuota(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermGroupQuotaWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req updateQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	if err := h.groupSvc.UpdateQuota(id, req.MaxInstances, req.MaxBots, req.MaxStorageMB); err != nil {
		if errors.Is(err, service.ErrGroupNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户组不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "更新配额失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已更新"})
}

// GetQuota 查询组配额及当前用量。
// 组成员可查看本组配额；组管理员/平台管理员同。
func (h *GroupHandler) GetQuota(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	access := getAccess(c)
	if access == nil || !access.CanAccessGroup(id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户组不存在"})
		return
	}

	usage, err := h.authz.GetQuotaUsage(id)
	if err != nil {
		if errors.Is(err, service.ErrGroupNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户组不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询配额失败"})
		return
	}

	c.JSON(http.StatusOK, usage)
}

// RegisterRoutes 注册用户组路由。
func (h *GroupHandler) RegisterRoutes(rg *gin.RouterGroup) {
	groups := rg.Group("/groups")
	{
		groups.GET("", h.List)
		groups.POST("", h.Create)
		groups.GET("/:id", h.Get)
		groups.PUT("/:id", h.Update)
		groups.DELETE("/:id", h.Delete)
		groups.POST("/:id/members", h.AddMember)
		groups.DELETE("/:id/members/:userId", h.RemoveMember)
		groups.PUT("/:id/quota", h.UpdateQuota)
		groups.GET("/:id/quota", h.GetQuota)
	}
}

// parseID 从路径参数解析 ID。
func parseID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的 ID"})
		return 0, err
	}
	return uint(id), nil
}

// getAccess 从 gin.Context 取出授权上下文。
func getAccess(c *gin.Context) *service.UserAccess {
	v, ok := c.Get(middleware.CtxAccess)
	if !ok {
		return nil
	}
	access, _ := v.(*service.UserAccess)
	return access
}
