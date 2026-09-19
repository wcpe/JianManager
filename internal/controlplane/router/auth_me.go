package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AuthMeHandler GET /auth/me（FR-432）：返回当前用户有效权限节点与 roleKey。
type AuthMeHandler struct {
	authz *service.AuthzService
	perm  *service.PermissionService
}

// NewAuthMeHandler 创建 me 处理器。
func NewAuthMeHandler(authz *service.AuthzService, perm *service.PermissionService) *AuthMeHandler {
	return &AuthMeHandler{authz: authz, perm: perm}
}

// RegisterRoutes 注册 /auth/me（需 JWT + LoadAccess）。
func (h *AuthMeHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/auth")
	g.GET("/me", h.Me)
}

func (h *AuthMeHandler) Me(c *gin.Context) {
	uidVal, ok := c.Get(middleware.CtxUserID)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "缺少认证信息"})
		return
	}
	uid, _ := uidVal.(uint)
	access, err := h.authz.LoadUserAccess(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "加载用户权限失败"})
		return
	}
	nodes := make([]string, 0, len(access.Nodes))
	for n := range access.Nodes {
		nodes = append(nodes, n)
	}
	username, _ := c.Get(middleware.CtxUsername)
	c.JSON(http.StatusOK, gin.H{
		"userId":          access.UserID,
		"username":        username,
		"role":            access.Role,
		"roleKey":         access.RoleKey,
		"isPlatformAdmin": access.IsPlatformAdmin,
		"nodes":           nodes,
	})
}
