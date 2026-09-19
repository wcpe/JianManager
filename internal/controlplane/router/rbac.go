package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// RBACHandler 权限树管理（FR-432）。
type RBACHandler struct {
	perm  *service.PermissionService
	users *service.UserService
}

// NewRBACHandler 创建 RBAC 处理器。
func NewRBACHandler(perm *service.PermissionService, users *service.UserService) *RBACHandler {
	return &RBACHandler{perm: perm, users: users}
}

// RegisterRoutes 注册 /rbac 路由（需认证；写操作要求 rbac.manage）。
func (h *RBACHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/rbac")
	{
		g.GET("/catalog", h.Catalog)
		g.GET("/roles", middleware.RequireAnyPerm("rbac.read", "rbac.manage"), h.ListRoles)
		g.GET("/roles/:id", middleware.RequireAnyPerm("rbac.read", "rbac.manage"), h.GetRole)
		g.POST("/roles", middleware.RequirePerm("rbac.manage"), h.CreateRole)
		g.PATCH("/roles/:id", middleware.RequirePerm("rbac.manage"), h.UpdateRole)
		g.DELETE("/roles/:id", middleware.RequirePerm("rbac.manage"), h.DeleteRole)
		g.PUT("/roles/:id/permissions", middleware.RequirePerm("rbac.manage"), h.SetRolePermissions)
		g.GET("/users/:id/permissions", middleware.RequireAnyPerm("rbac.read", "rbac.manage"), h.GetUserPermissions)
		g.PUT("/users/:id/role", middleware.RequirePerm("rbac.manage"), h.SetUserRole)
		g.PUT("/users/:id/overrides", middleware.RequirePerm("rbac.manage"), h.SetUserOverrides)
	}
}

func (h *RBACHandler) Catalog(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"domains": h.perm.Catalog()})
}

func (h *RBACHandler) ListRoles(c *gin.Context) {
	roles, err := h.perm.ListRoles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "服务器内部错误"})
		return
	}
	out := make([]gin.H, 0, len(roles))
	for _, r := range roles {
		nodes, err := h.perm.RoleNodes(r.ID)
		if err != nil {
			// 单角色节点读取失败不吞：列表返回空节点并继续，避免整页 500
			nodes = []string{}
		}
		locked := r.Key == model.RoleKeyPlatformAdmin
		out = append(out, gin.H{
			"id": r.ID, "key": r.Key, "name": r.Name, "description": r.Description,
			"isSystem": r.IsSystem, "locked": locked,
			"nodes": nodes, "createdAt": r.CreatedAt, "updatedAt": r.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *RBACHandler) GetRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	role, err := h.perm.GetRole(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "角色不存在"})
		return
	}
	nodes, err := h.perm.RoleNodes(role.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取角色节点失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": role.ID, "key": role.Key, "name": role.Name, "description": role.Description,
		"isSystem": role.IsSystem, "locked": role.Key == model.RoleKeyPlatformAdmin,
		"nodes": nodes, "createdAt": role.CreatedAt, "updatedAt": role.UpdatedAt,
	})
}

type createRoleRequest struct {
	Name        string   `json:"name" binding:"required"`
	Description string   `json:"description"`
	Nodes       []string `json:"nodes"`
}

// rbacSafeMessage 将服务端错误映射为可展示文案，避免 err.Error() 泄露内部细节（R-09）。
func rbacSafeMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrSuperAdminLocked):
		return err.Error()
	case errors.Is(err, service.ErrRoleSystem):
		return err.Error()
	case errors.Is(err, service.ErrRoleExists):
		return err.Error()
	case errors.Is(err, service.ErrOverrideInvalid):
		return "非法权限覆盖项"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "记录不存在"
	default:
		return "操作失败"
	}
}

func (h *RBACHandler) CreateRole(c *gin.Context) {
	var req createRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	role, err := h.perm.CreateRole(req.Name, req.Description, req.Nodes)
	if err != nil {
		if errors.Is(err, service.ErrRoleExists) {
			c.JSON(http.StatusConflict, gin.H{"error": "ROLE_EXISTS", "message": rbacSafeMessage(err)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建角色失败"})
		return
	}
	nodes, err := h.perm.RoleNodes(role.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取角色节点失败"})
		return
	}
	c.JSON(http.StatusCreated, service.RoleDetail{RoleTemplate: *role, Nodes: nodes})
}

type updateRoleRequest struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Nodes       []string `json:"nodes"`
}

func (h *RBACHandler) UpdateRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	var req updateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	role, err := h.perm.UpdateRole(id, req.Name, req.Description, req.Nodes, req.Nodes != nil)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "角色不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "更新角色失败"})
		return
	}
	nodes, err := h.perm.RoleNodes(role.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取角色节点失败"})
		return
	}
	c.JSON(http.StatusOK, service.RoleDetail{RoleTemplate: *role, Nodes: nodes})
}

func (h *RBACHandler) DeleteRole(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.perm.DeleteRole(id); err != nil {
		if errors.Is(err, service.ErrRoleSystem) || errors.Is(err, service.ErrSuperAdminLocked) {
			c.JSON(http.StatusForbidden, gin.H{"error": "ROLE_LOCKED", "message": rbacSafeMessage(err)})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "角色不存在"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "删除失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *RBACHandler) SetRolePermissions(c *gin.Context) {
	id, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	var req struct {
		Nodes []string `json:"nodes" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	role, err := h.perm.UpdateRole(id, nil, nil, req.Nodes, true)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "角色不存在"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": rbacSafeMessage(err)})
		return
	}
	nodes, err := h.perm.RoleNodes(role.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "读取角色节点失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": role.ID, "key": role.Key, "name": role.Name,
		"isSystem": role.IsSystem, "locked": role.Key == model.RoleKeyPlatformAdmin,
		"nodes": nodes,
	})
}

func (h *RBACHandler) GetUserPermissions(c *gin.Context) {
	uid, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	legacy := model.UserRole(0)
	// 用户可能不存在于 users 表的场景由调用方保证；此处查用户拿 role
	if h.users != nil {
		if u, err := h.users.GetByID(uid); err == nil && u != nil {
			legacy = u.Role
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户不存在"})
			return
		}
	}
	nodes, roleKey, err := h.perm.EffectiveForUser(uid, legacy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "服务器内部错误"})
		return
	}
	ovs, _ := h.perm.UserOverrides(uid)
	list := make([]string, 0, len(nodes))
	for n := range nodes {
		list = append(list, n)
	}
	view := service.UserPermissionView{
		UserID:          uid,
		RoleKey:         roleKey,
		Nodes:           list,
		Overrides:       ovs,
		IsPlatformAdmin: legacy == model.RolePlatformAdmin || roleKey == model.RoleKeyPlatformAdmin,
	}
	// 角色 ID
	if roles, err := h.perm.ListRoles(); err == nil {
		for i := range roles {
			if roles[i].Key == roleKey {
				id := roles[i].ID
				view.RoleID = &id
				break
			}
		}
	}
	c.JSON(http.StatusOK, view)
}

func (h *RBACHandler) SetUserRole(c *gin.Context) {
	uid, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	var req struct {
		RoleID uint `json:"roleId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.perm.BindUserRole(uid, req.RoleID); err != nil {
		if errors.Is(err, service.ErrSuperAdminLocked) {
			c.JSON(http.StatusForbidden, gin.H{"error": "SUPER_ADMIN_LOCKED", "message": rbacSafeMessage(err)})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "用户或角色不存在"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": rbacSafeMessage(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *RBACHandler) SetUserOverrides(c *gin.Context) {
	uid, err := parseUintParam(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	var req struct {
		Overrides []model.UserPermissionOverride `json:"overrides"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	if err := h.perm.SetUserOverrides(uid, req.Overrides); err != nil {
		if errors.Is(err, service.ErrSuperAdminLocked) {
			c.JSON(http.StatusForbidden, gin.H{"error": "SUPER_ADMIN_LOCKED", "message": rbacSafeMessage(err)})
			return
		}
		if errors.Is(err, service.ErrOverrideInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": rbacSafeMessage(err)})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": rbacSafeMessage(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
