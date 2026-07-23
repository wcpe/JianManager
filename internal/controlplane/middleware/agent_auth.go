package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Context keys for agent principal。
const (
	CtxAgentPrincipal = "agentPrincipal"
	CtxActorKind      = "actorKind" // "user" | "agent"
)

// AgentAuth 可选：若 Bearer 以 jmat_ 开头则走 Agent Token 鉴权，成功后注入 principal 并跳过 JWT。
// 与 JWTAuth 组合使用：先 AgentAuth（成功则 c.Next 且已 abort 跳过后续 JWT 的写法见 AgentOrJWT）。
func AgentAuth(agentSvc *service.AgentTokenService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if agentSvc == nil {
			c.Next()
			return
		}
		auth := c.GetHeader("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			c.Next()
			return
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		if !strings.HasPrefix(tokenStr, "jmat_") {
			c.Next()
			return
		}
		p, err := agentSvc.Authenticate(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "Agent Token 无效或已过期",
			})
			return
		}
		c.Set(CtxAgentPrincipal, p)
		c.Set(CtxActorKind, "agent")
		// 兼容审计中间件：用 token id 占位 userId（审计 service 需识别 actor_kind）
		c.Set(CtxUserID, p.TokenID)
		c.Set(CtxUsername, "agent:"+p.Name)
		c.Set(CtxRole, 0)
		c.Next()
	}
}

// GetAgentPrincipal 从上下文取 agent 主体。
func GetAgentPrincipal(c *gin.Context) *service.AgentPrincipal {
	v, ok := c.Get(CtxAgentPrincipal)
	if !ok {
		return nil
	}
	p, _ := v.(*service.AgentPrincipal)
	return p
}
