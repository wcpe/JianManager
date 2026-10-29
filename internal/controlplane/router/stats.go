package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// StatsHandler 对外只读统计契约路由。
//
// 契约真源：docs/openapi/paths/stats.yaml；规格：docs/specs/external-stats-contract/spec.md。
//
// 边界（spec §2）：**只出通道与基础设施维度，不出任何业务指标**。
// 本 handler 只做参数解析、可见范围收敛与响应头写出，查询全部下沉到 service.StatsService。
type StatsHandler struct {
	stats *service.StatsService
	authz *service.AuthzService
}

// NewStatsHandler 创建统计契约路由处理器。
func NewStatsHandler(stats *service.StatsService, authz *service.AuthzService) *StatsHandler {
	return &StatsHandler{stats: stats, authz: authz}
}

// StatsTokenScopeHeader 保留给未来「限流必须早于鉴权」的部署形态；当前未使用。
//
// 当前实现把限流放在**鉴权之后**，按真实主体（Agent TokenID / 用户 UserID）计费：
// spec §4.4 的口径就是「每 token 每分钟 120 次」，按主体计费与之一一对应，
// 也不会把同一 IP 后的多个合法调用方算成一份配额。
const StatsTokenScopeHeader = "X-JM-Stats-Token"

// RegisterRoutes 注册 /stats 六组只读端点。
//
// 调用方须已挂 statsAuth + statsScreen。
//
// 路径直接写成完整的 `/stats/...`（而不是 `rg.Group("/stats")` + 相对路径）：
// `scripts/check-openapi.sh` 的路由一致性校验按 `rg.GET("<path>")` 的字面量抽取路由，
// 用 Group 前缀会让六条端点全部抽不到，从而一直落在「OpenAPI 声明了但 Gin 未实现」的
// 缺口列表里——那样这个校验对 /stats/* 就完全失效了。
func (h *StatsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/stats/overview", h.Overview)
	rg.GET("/stats/instances", h.Instances)
	rg.GET("/stats/nodes", h.Nodes)
	rg.GET("/stats/logs", h.Logs)
	rg.GET("/stats/crashes", h.Crashes)
	rg.GET("/stats/changes", h.Changes)
}

// statsAuth 是本组端点的鉴权组合：Agent Token 优先，人类 JWT 兜底。
//
// 契约（external.yaml 的 security 是 AgentToken）用于业务台等机器调用方；
// 但运维页面看统计卡片时手里只有浏览器 JWT，故两类合法主体并列，二者都失败才 401。
//
// 【为什么不像 businessStreamAuth 那样直接调用 middleware.AgentAuth / JWTAuth】
// 那三个中间件（AgentAuth / JWTAuth / LoadAccess）都以 `c.Next()` 收尾，是「链上最后一环」
// 的写法。在同一个 handler 里手动调用两个这样的中间件，第一个的 `c.Next()` 会**立刻把后续
// 中间件与最终 handler 全部推进一遍**——于是 handler 在第二个中间件注入上下文**之前**
// 就已执行（实测：JWT 路径下 CtxAccess 尚未写入，`resolveScope` 拿不到授权上下文 → 403）。
// 这里改为直接复用它们底层的**纯校验函数**（AgentTokenService.Authenticate /
// AuthzService.LoadUserAccess / jwt 解析），各自注入同一批上下文键，
// 只在外层调用**一次** c.Next()，语义与「单独挂某一条中间件」完全一致。
func statsAuth(agentSvc *service.AgentTokenService, jwtSecret string, authz *service.AuthzService) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		if strings.HasPrefix(tokenStr, "jmat_") {
			authenticateAgent(c, agentSvc, tokenStr)
		} else {
			authenticateUser(c, jwtSecret, authz)
		}
		if c.IsAborted() {
			return
		}
		c.Next()
	}
}

// authenticateAgent 校验 Agent Token 并注入与 middleware.AgentAuth 相同的上下文键。
func authenticateAgent(c *gin.Context, agentSvc *service.AgentTokenService, tokenStr string) {
	if agentSvc == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": service.StatsCodeUnauthorized, "message": "Agent Token 鉴权未启用",
		})
		return
	}
	p, err := agentSvc.Authenticate(tokenStr)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": service.StatsCodeUnauthorized, "message": "Agent Token 无效或已过期",
		})
		return
	}
	c.Set(middleware.CtxAgentPrincipal, p)
	c.Set(middleware.CtxActorKind, "agent")
	// 兼容审计中间件：用 token id 占位 userId（与 middleware.AgentAuth 同口径）。
	c.Set(middleware.CtxUserID, p.TokenID)
	c.Set(middleware.CtxUsername, "agent:"+p.Name)
	c.Set(middleware.CtxRole, model.UserRole(0))
	c.Set(middleware.CtxAgentClient, service.NormalizeAgentClient(c.GetHeader(middleware.HeaderAgentClient)))
}

// authenticateUser 校验用户 JWT 并装载授权上下文（等价 JWTAuth + LoadAccess）。
//
// 两步都在此完成、且都不推进 handler：这正是上段注释说的「只让外层 Next 一次」。
func authenticateUser(c *gin.Context, jwtSecret string, authz *service.AuthzService) {
	auth := c.GetHeader("Authorization")
	if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": service.StatsCodeUnauthorized, "message": "缺少认证信息",
		})
		return
	}
	claims := &service.Claims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(auth, "Bearer "), claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(jwtSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid || claims.TokenType != service.TokenTypeAccess {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": service.StatsCodeUnauthorized, "message": "Token 无效或已过期",
		})
		return
	}
	c.Set(middleware.CtxUserID, claims.UserID)
	c.Set(middleware.CtxUsername, claims.Username)
	c.Set(middleware.CtxRole, claims.Role)
	c.Set(middleware.CtxAuthVersion, claims.AuthVersion)

	if authz == nil {
		return
	}
	access, err := authz.LoadUserAccess(claims.UserID)
	if err != nil || access.AuthVersion != claims.AuthVersion {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": service.StatsCodeUnauthorized, "message": "Token 已失效",
		})
		return
	}
	c.Set(middleware.CtxAccess, access)
}

// statsScreen 按 token 计费的限流中间件（spec §4.4：每 token 每分钟 120 次）。
//
// 必须挂在鉴权之后：限流口径是「每 token」，而主体只有鉴权成功后才能确定。
// 未鉴权请求根本到不了这里（鉴权中间件已 401），不会消耗任何配额。
func statsScreen(svc *service.StatsService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil {
			c.Next()
			return
		}
		remaining, retryAfter, ok := svc.Limiter().Allow(statsRateLimitKey(c))
		c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))
		if !ok {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":          service.StatsCodeRateLimited,
				"message":       "请求过于频繁，请稍后再试",
				"retryAfterSec": retryAfter,
			})
			return
		}
		c.Next()
	}
}

// statsRateLimitKey 派生限流键：Agent Token 按 token id、用户 JWT 按 user id。
//
// 按主体而非 IP：同一个 NAT 出口后可能有多个合法调用方，按 IP 计费会让
// 「每 token 120 次」被稀释成「每 IP 120 次」。两者都取不到时（理论上不可达，
// 因为限流在鉴权之后）回退 IP，保证绝不退化成「不限流」。
func statsRateLimitKey(c *gin.Context) string {
	if p := middleware.GetAgentPrincipal(c); p != nil {
		return "agent:" + strconv.FormatUint(uint64(p.TokenID), 10)
	}
	if v, ok := c.Get(middleware.CtxUserID); ok {
		if uid, ok := v.(uint); ok {
			return "user:" + strconv.FormatUint(uint64(uid), 10)
		}
	}
	return "ip:" + c.ClientIP()
}

// ── handlers ──────────────────────────────────────────────────────────

// Overview 返回集群总览快照。
func (h *StatsHandler) Overview(c *gin.Context) {
	scope, ok := h.resolveScope(c)
	if !ok {
		return
	}
	res, err := h.stats.Overview(scope)
	if err != nil {
		writeStatsInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// Instances 返回实例资源指标时序（走 rollup 表）。
func (h *StatsHandler) Instances(c *gin.Context) {
	q, stErr := h.parseMetricQuery(c, service.StatsMetricsMaxRange, service.StatsMetricsMaxRangeLabel)
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	ids, stErr := parseStatsIDList(c.Query("instanceIds"))
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	q.InstanceIDs = ids
	scope, ok := h.resolveScope(c)
	if !ok {
		return
	}
	res, err := h.stats.QueryInstanceSeries(q, scope)
	if err != nil {
		writeStatsInternalError(c, err)
		return
	}
	writeStatsPaginationHeaders(c, res.Pagination)
	c.JSON(http.StatusOK, res)
}

// Nodes 返回节点资源指标时序（走 rollup 表）。
func (h *StatsHandler) Nodes(c *gin.Context) {
	q, stErr := h.parseMetricQuery(c, service.StatsMetricsMaxRange, service.StatsMetricsMaxRangeLabel)
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	ids, stErr := parseStatsIDList(c.Query("nodeIds"))
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	q.NodeIDs = ids
	scope, ok := h.resolveScope(c)
	if !ok {
		return
	}
	res, err := h.stats.QueryNodeSeries(q, scope)
	if err != nil {
		writeStatsInternalError(c, err)
		return
	}
	writeStatsPaginationHeaders(c, res.Pagination)
	c.JSON(http.StatusOK, res)
}

// Logs 检索实例或节点日志。
func (h *StatsHandler) Logs(c *gin.Context) {
	from, to, stErr := parseStatsTimeRange(c, service.StatsLogsDefaultRange,
		service.StatsLogsMaxRange, service.StatsLogsMaxRangeLabel)
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	page, pageSize, stErr := parseStatsPagination(c)
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	q := service.StatsLogQuery{
		Level:    c.Query("level"),
		Keyword:  c.Query("q"),
		From:     from,
		To:       to,
		Page:     page,
		PageSize: pageSize,
	}
	instID, hasInst, stErr := parseStatsOptionalID(c.Query("instanceId"))
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	if hasInst {
		q.InstanceID = &instID
	}
	nodeID, hasNode, stErr := parseStatsOptionalID(c.Query("nodeId"))
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	if hasNode {
		q.NodeID = &nodeID
	}
	scope, ok := h.resolveScope(c)
	if !ok {
		return
	}
	res, err := h.stats.QueryLogs(q, scope)
	if err != nil {
		if stErr, ok := err.(*service.StatsError); ok {
			writeStatsError(c, stErr)
			return
		}
		writeStatsInternalError(c, err)
		return
	}
	writeStatsPaginationHeaders(c, res.Pagination)
	c.JSON(http.StatusOK, res)
}

// Crashes 返回崩溃趋势。
func (h *StatsHandler) Crashes(c *gin.Context) {
	days := 0
	if raw := strings.TrimSpace(c.Query("days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeStatsError(c, &service.StatsError{
				Code: service.StatsCodeInvalidArgument, Message: "days 必须为整数",
			})
			return
		}
		days = n
	}
	normalizedDays, stErr := service.NormalizeStatsCrashDays(days)
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	instID, hasInst, stErr := parseStatsOptionalID(c.Query("instanceId"))
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	q := service.StatsCrashQuery{Days: normalizedDays}
	if hasInst {
		q.InstanceID = instID
	}
	scope, ok := h.resolveScope(c)
	if !ok {
		return
	}
	res, err := h.stats.QueryCrashTrend(q, scope)
	if err != nil {
		writeStatsInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// Changes 返回在途变更（不含配置内容）。
func (h *StatsHandler) Changes(c *gin.Context) {
	page, pageSize, stErr := parseStatsPagination(c)
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	statuses, stErr := service.ParseStatsChangeStatuses(c.Query("status"))
	if stErr != nil {
		writeStatsError(c, stErr)
		return
	}
	res, err := h.stats.ListInFlightChanges(service.StatsChangeQuery{
		Statuses: statuses, Page: page, PageSize: pageSize,
	})
	if err != nil {
		writeStatsInternalError(c, err)
		return
	}
	writeStatsPaginationHeaders(c, res.Pagination)
	c.JSON(http.StatusOK, res)
}

// ── 参数解析 ──────────────────────────────────────────────────────────

// parseMetricQuery 解析 instances / nodes 共用的查询参数。
func (h *StatsHandler) parseMetricQuery(c *gin.Context, maxSpan time.Duration, maxLabel string) (service.StatsMetricQuery, *service.StatsError) {
	from, to, stErr := parseStatsTimeRange(c, service.StatsMetricsDefaultRange, maxSpan, maxLabel)
	if stErr != nil {
		return service.StatsMetricQuery{}, stErr
	}
	page, pageSize, stErr := parseStatsPagination(c)
	if stErr != nil {
		return service.StatsMetricQuery{}, stErr
	}
	res, stErr := service.NormalizeStatsResolution(c.Query("resolution"), to.Sub(from))
	if stErr != nil {
		return service.StatsMetricQuery{}, stErr
	}
	return service.StatsMetricQuery{
		From: from, To: to, Resolution: res, Page: page, PageSize: pageSize,
	}, nil
}

// parseStatsTimeRange 解析 from/to（RFC3339），并按接口上限校验。
func parseStatsTimeRange(c *gin.Context, defaultSpan, maxSpan time.Duration, maxLabel string) (time.Time, time.Time, *service.StatsError) {
	var fromPtr, toPtr *time.Time
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, &service.StatsError{
				Code: service.StatsCodeInvalidArgument, Message: "from 必须为 RFC3339 时间",
			}
		}
		fromPtr = &t
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, &service.StatsError{
				Code: service.StatsCodeInvalidArgument, Message: "to 必须为 RFC3339 时间",
			}
		}
		toPtr = &t
	}
	return service.NormalizeStatsTimeRange(fromPtr, toPtr, defaultSpan, maxSpan, maxLabel, time.Now())
}

// parseStatsPagination 解析 page/pageSize。
func parseStatsPagination(c *gin.Context) (int, int, *service.StatsError) {
	page := 0
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, 0, &service.StatsError{Code: service.StatsCodeInvalidArgument, Message: "page 必须为整数"}
		}
		page = n
	}
	pageSize := 0
	if raw := strings.TrimSpace(c.Query("pageSize")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, 0, &service.StatsError{Code: service.StatsCodeInvalidArgument, Message: "pageSize 必须为整数"}
		}
		pageSize = n
	}
	return service.NormalizeStatsPagination(page, pageSize)
}

// parseStatsOptionalID 解析可选的单个正整数 ID。
func parseStatsOptionalID(raw string) (uint, bool, *service.StatsError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || id == 0 {
		return 0, false, &service.StatsError{
			Code: service.StatsCodeInvalidArgument, Message: "ID 必须为正整数",
		}
	}
	return uint(id), true, nil
}

// parseStatsIDList 解析逗号分隔的 ID 列表。空串返回 nil（表示「不指定」）。
func parseStatsIDList(raw string) ([]uint, *service.StatsError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	seen := map[uint]struct{}{}
	out := make([]uint, 0, 8)
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseUint(part, 10, 64)
		if err != nil || id == 0 {
			return nil, &service.StatsError{
				Code: service.StatsCodeInvalidArgument, Message: "ID 列表必须为逗号分隔的正整数",
			}
		}
		v := uint(id)
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// resolveScope 解析调用者可见范围。Agent 主体与用户主体走各自的收敛规则。
func (h *StatsHandler) resolveScope(c *gin.Context) (service.StatsScope, bool) {
	if principal := middleware.GetAgentPrincipal(c); principal != nil {
		scope, err := h.stats.StatsScopeForAgent(principal)
		if err != nil {
			writeStatsInternalError(c, err)
			return service.StatsScope{}, false
		}
		return scope, true
	}
	access := getAccess(c)
	if access == nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"code": "FORBIDDEN", "message": "权限不足",
		})
		return service.StatsScope{}, false
	}
	// 节点读权限用与节点端点相同的权限节点判定，保证「能看节点列表的账号也能看节点指标」。
	canReadAllNodes := access.IsPlatformAdmin || access.HasNode("node.read") || access.HasNode("node.manage")
	scope, err := h.stats.StatsScopeForUser(access, h.authz, canReadAllNodes)
	if err != nil {
		writeStatsInternalError(c, err)
		return service.StatsScope{}, false
	}
	return scope, true
}

// ── 响应写出 ──────────────────────────────────────────────────────────

// writeStatsPaginationHeaders 按契约写分页响应头（spec §4.5）。
func writeStatsPaginationHeaders(c *gin.Context, p service.StatsPagination) {
	c.Header("X-Total-Count", strconv.Itoa(p.Total))
	c.Header("X-Page", strconv.Itoa(p.Page))
	c.Header("X-Page-Size", strconv.Itoa(p.PageSize))
}

// writeStatsError 按错误码写出契约错误体。
//
// 附加字段（maxPageSize / maxRange）只在对应错误码下出现，避免无差别铺字段
// 让调用方误以为该字段对所有错误都有意义。
func writeStatsError(c *gin.Context, e *service.StatsError) {
	body := gin.H{"code": e.Code, "message": e.Message}
	switch e.Code {
	case service.StatsCodePageSizeExceeded:
		body["maxPageSize"] = e.MaxPageSize
	case service.StatsCodeTimeRangeExceeded:
		body["maxRange"] = e.MaxRange
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, body)
}

// writeStatsInternalError 写 500。错误细节只进日志，不进响应体。
func writeStatsInternalError(c *gin.Context, err error) {
	var stErr *service.StatsError
	if errors.As(err, &stErr) {
		writeStatsError(c, stErr)
		return
	}
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
		"code": "INTERNAL_ERROR", "message": "查询统计失败",
	})
}
