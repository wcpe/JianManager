package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// Context keys for agent principal。
const (
	CtxAgentPrincipal = "agentPrincipal"
	CtxActorKind      = "actorKind" // "user" | "agent"
	// CtxAgentClient 解析后的调用方（mcp|jmagent|curl|unknown），FR-390。
	CtxAgentClient = "agentClient"
	// CtxAgentCallStart 请求开始时间，供流水 latency_ms。
	CtxAgentCallStart = "agentCallStart"
)

// HeaderAgentClient 客户端自报标识头（FR-390 约定）。
const HeaderAgentClient = "X-JM-Agent-Client"

// AgentAuth 可选：若 Bearer 以 jmat_ 开头则走 Agent Token 鉴权，成功后注入 principal 并跳过 JWT。
// 与 JWTAuth 组合使用：先 AgentAuth（成功则 c.Next 且已 abort 跳过后续 JWT 的写法见 AgentOrJWT）。
// 鉴权成功后解析 X-JM-Agent-Client 写入上下文（401 不写库，由 Ops 层 Record）。
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
		// FR-390：client 标识 + 计时起点（仅成功鉴权后）
		c.Set(CtxAgentClient, service.NormalizeAgentClient(c.GetHeader(HeaderAgentClient)))
		c.Set(CtxAgentCallStart, time.Now())
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

// GetAgentClient 取解析后的 client 标识；缺省 unknown。
func GetAgentClient(c *gin.Context) string {
	v, ok := c.Get(CtxAgentClient)
	if !ok {
		return service.AgentClientUnknown
	}
	s, _ := v.(string)
	if s == "" {
		return service.AgentClientUnknown
	}
	return s
}

// AgentCallLatencyMs 自鉴权成功起的耗时毫秒；无起点返回 0。
func AgentCallLatencyMs(c *gin.Context) uint {
	v, ok := c.Get(CtxAgentCallStart)
	if !ok {
		return 0
	}
	start, ok := v.(time.Time)
	if !ok {
		return 0
	}
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return uint(ms)
}
