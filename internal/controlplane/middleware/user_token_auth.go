package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Context keys for user token principal。
const (
	CtxUserTokenPrincipal = "userTokenPrincipal"
	// CtxUserID / CtxAuthVersion 复用 JWT 侧既有键，使 RequirePerm/LoadAccess 无需感知凭证来源。
	CtxUserTokenID = "userTokenId"
)

// UserTokenAuth 用户令牌鉴权。
//
// 设计为 JWTAuth 之前的「可选分流」：
//   - 只处理 Authorization: Bearer jmut_*
//   - 校验通过后注入 CtxUserID / CtxAuthVersion / CtxUserTokenPrincipal，并 abort 后续 JWT 校验
//   - 非 jmut_ 前缀原样放过，交给 JWTAuth 处理（jmat_ 交给 AgentAuth）
//
// 这样既让 jm 能用人类令牌访问常规业务路由，又不需要复制一份路由树。
func UserTokenAuth(svc *service.UserTokenService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			c.Next()
			return
		}
		auth := c.GetHeader("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			c.Next()
			return
		}
		plaintext := strings.TrimPrefix(auth, "Bearer ")
		// 不是 jmut_ 前缀 → 交给后续中间件，保持 JWT / AgentToken 既有行为不变。
		if !strings.HasPrefix(plaintext, service.UserTokenPrefix) {
			c.Next()
			return
		}

		principal, err := svc.Authenticate(plaintext)
		if err != nil {
			// 统一 401，不区分「不存在/已吊销/已过期」，避免成为令牌状态探针。
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "用户令牌无效或已失效",
			})
			return
		}

		c.Set(CtxUserTokenPrincipal, principal)
		// 复用既有键：下游 LoadAccess 用它加载权限并比对 AuthVersion。
		c.Set(CtxUserID, principal.UserID)
		c.Set(CtxUsername, principal.Username)
		c.Set(CtxRole, principal.Role)
		c.Set(CtxAuthVersion, principal.AuthVersion)
		c.Next()
	}
}

// NarrowAccessForUserToken 把 UserAccess 收窄到令牌声明的能力与资源 scope，
// 使 RequirePerm / RequirePermission / CanAccessInstance 等下游判定自动生效。
//
// 收窄规则（与 spec 对齐：能力不放大、scope 取交、越权 403）：
//   - Capabilities 非空：权限树 Nodes 收敛到令牌声明的节点集合（含冒号→点号兼容）。
//     平台管理员令牌不裁能力（服务层语义：admin 能力不裁剪，scope 仍生效）。
//   - NodeIDs / InstanceIDs 非空：写入 access.TokenScoped*，由 AuthzService 的
//     实例/节点判定与集合投影消费。
func NarrowAccessForUserToken(access *service.UserAccess, p *service.UserTokenPrincipal) {
	if access == nil || p == nil {
		return
	}
	if !access.IsPlatformAdmin && len(p.Capabilities) > 0 {
		allowed := make(map[string]struct{}, len(p.Capabilities)*2)
		for _, c := range p.Capabilities {
			allowed[c] = struct{}{}
			allowed[strings.ReplaceAll(c, ":", ".")] = struct{}{}
		}
		nodes := make(map[string]struct{}, len(access.Nodes))
		for n := range access.Nodes {
			if _, ok := allowed[n]; ok {
				nodes[n] = struct{}{}
			}
		}
		access.Nodes = nodes
	}
	if len(p.NodeIDs) > 0 {
		access.TokenScopedNodeIDs = make(map[uint]struct{}, len(p.NodeIDs))
		for _, id := range p.NodeIDs {
			access.TokenScopedNodeIDs[id] = struct{}{}
		}
	}
	if len(p.InstanceIDs) > 0 {
		access.TokenScopedInstanceIDs = make(map[uint]struct{}, len(p.InstanceIDs))
		for _, id := range p.InstanceIDs {
			access.TokenScopedInstanceIDs[id] = struct{}{}
		}
	}
}

// GetUserTokenPrincipal 取出用户令牌主体。
func GetUserTokenPrincipal(c *gin.Context) *service.UserTokenPrincipal {
	v, ok := c.Get(CtxUserTokenPrincipal)
	if !ok {
		return nil
	}
	p, _ := v.(*service.UserTokenPrincipal)
	return p
}

// GetUserID 从上下文取当前用户 ID（JWT 与用户令牌路径共用）。
func GetUserID(c *gin.Context) uint {
	v, ok := c.Get(CtxUserID)
	if !ok {
		return 0
	}
	id, _ := v.(uint)
	return id
}

// IsPlatformAdmin 判断当前请求主体是否平台管理员。
//
// 依赖 LoadAccess 已注入的 UserAccess；未加载时返回 false（默认从严）。
func IsPlatformAdmin(c *gin.Context) bool {
	access := getAccess(c)
	return access != nil && access.IsPlatformAdmin
}
