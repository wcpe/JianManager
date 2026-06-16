package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// Context keys。
const (
	CtxUserID   = "userId"
	CtxUsername = "username"
	CtxRole     = "role"
	CtxAccess   = "access"
)

// JWTAuth JWT 认证中间件。
func JWTAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "缺少认证信息",
			})
			return
		}

		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		claims := &service.Claims{}

		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			return []byte(secret), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "Token 无效或已过期",
			})
			return
		}

		c.Set(CtxUserID, claims.UserID)
		c.Set(CtxUsername, claims.Username)
		c.Set(CtxRole, claims.Role)

		c.Next()
	}
}

// RequireRole 要求最低角色等级的中间件。
func RequireRole(minRole model.UserRole) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleVal, exists := c.Get(CtxRole)
		if !exists {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "无权限",
			})
			return
		}

		userRole, ok := roleVal.(model.UserRole)
		if !ok || userRole < minRole {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}

		c.Next()
	}
}

// LoadAccess 加载当前用户的授权上下文并写入 gin.Context(CtxAccess)。
// 必须在 JWTAuth 之后执行。参见 ADR-004: 用户组替代多租户。
func LoadAccess(authz *service.AuthzService) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, exists := c.Get(CtxUserID)
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "缺少认证信息",
			})
			return
		}
		uid, ok := userID.(uint)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "认证信息异常",
			})
			return
		}

		access, err := authz.LoadUserAccess(uid)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "加载用户权限失败",
			})
			return
		}
		c.Set(CtxAccess, access)
		c.Next()
	}
}

// getAccess 从 context 取出授权上下文，未加载时返回 nil。
func getAccess(c *gin.Context) *service.UserAccess {
	v, ok := c.Get(CtxAccess)
	if !ok {
		return nil
	}
	access, _ := v.(*service.UserAccess)
	return access
}

// RequirePermission 要求当前用户拥有指定权限节点。
// 平台管理员拥有全部权限；其余角色按 HasPermission 判断，资源级隔离由后续中间件收敛。
func RequirePermission(node service.PermissionNode) gin.HandlerFunc {
	return func(c *gin.Context) {
		access := getAccess(c)
		if access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}
		if !access.HasPermission(node) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}
		c.Next()
	}
}

// RequireGroupAccess 要求当前用户能访问路径参数 :id 指定的用户组。
// 平台管理员全量放行；否则必须属于该组（成员或组管理员）。
func RequireGroupAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		access := getAccess(c)
		if access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}
		groupID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": "无效的组 ID",
			})
			return
		}
		if !access.CanAccessGroup(uint(groupID)) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "无权访问该用户组",
			})
			return
		}
		c.Next()
	}
}

// RequireGroupManage 要求当前用户能管理路径参数 :id 指定的用户组（组管理员或平台管理员）。
func RequireGroupManage() gin.HandlerFunc {
	return func(c *gin.Context) {
		access := getAccess(c)
		if access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}
		groupID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": "无效的组 ID",
			})
			return
		}
		if !access.CanManageGroup(uint(groupID)) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "无权管理该用户组",
			})
			return
		}
		c.Next()
	}
}

// RequireInstanceAccess 要求当前用户能访问路径参数 :id 指定的实例。
// 平台管理员全量放行；否则实例必须归属于其可访问的组。
func RequireInstanceAccess(authz *service.AuthzService) gin.HandlerFunc {
	return func(c *gin.Context) {
		access := getAccess(c)
		if access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}
		instanceID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": "无效的实例 ID",
			})
			return
		}
		ok, err := authz.CanAccessInstance(access, uint(instanceID))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "INTERNAL_ERROR",
				"message": "校验实例权限失败",
			})
			return
		}
		if !ok {
			// 未授权访问返回 404 而非 403，避免泄露实例存在性
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "NOT_FOUND",
				"message": "实例不存在",
			})
			return
		}
		c.Next()
	}
}

// RequireInstanceManage 要求当前用户能管理（写/删除）路径参数 :id 指定的实例。
func RequireInstanceManage(authz *service.AuthzService) gin.HandlerFunc {
	return func(c *gin.Context) {
		access := getAccess(c)
		if access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "权限不足",
			})
			return
		}
		instanceID, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": "无效的实例 ID",
			})
			return
		}
		ok, err := authz.CanManageInstance(access, uint(instanceID))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "INTERNAL_ERROR",
				"message": "校验实例权限失败",
			})
			return
		}
		if !ok {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "NOT_FOUND",
				"message": "实例不存在",
			})
			return
		}
		c.Next()
	}
}
