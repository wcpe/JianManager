package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AgentTokenHandler 管理员面：Agent Token 颁发/列表/吊销 + 调用流水查询（JWT + 平台管理员）。
type AgentTokenHandler struct {
	svc     *service.AgentTokenService
	audit   *service.AuditService
	callLog *service.AgentCallLogService
}

// NewAgentTokenHandler 创建处理器（callLog 可为 nil，则列表无 callCount24h、无 call-logs 路由）。
func NewAgentTokenHandler(svc *service.AgentTokenService, audit *service.AuditService, callLog *service.AgentCallLogService) *AgentTokenHandler {
	return &AgentTokenHandler{svc: svc, audit: audit, callLog: callLog}
}

// RegisterAdminRoutes 挂到 JWT protected + 平台管理员组。
func (h *AgentTokenHandler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/agent/tokens")
	g.POST("", h.Issue)
	g.GET("", h.List)
	g.DELETE("/:id", h.Revoke)
	// 调用流水查询（FR-390）
	rg.GET("/agent/call-logs", h.ListCallLogs)
}

type issueAgentTokenRequest struct {
	Name              string          `json:"name"`
	ScopedInstanceIDs []uint          `json:"scopedInstanceIds"`
	ScopedNodeIDs     []uint          `json:"scopedNodeIds"`
	WriteAllowlist    json.RawMessage `json:"writeAllowlist"`
	Capabilities      json.RawMessage `json:"capabilities"`
	PolicyVersion     *int            `json:"policyVersion"`
	TTLDays           int             `json:"ttlDays"`
}

// agentTokenView 管理面 Token 投影：capabilities 以数组返回，兼容旧 writeAllowlist 文本字段。
type agentTokenView struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	TokenPrefix       string     `json:"tokenPrefix"`
	ScopedInstanceIDs string     `json:"scopedInstanceIds"`
	ScopedNodeIDs     string     `json:"scopedNodeIds"`
	WriteAllowlist    string     `json:"writeAllowlist"`
	PolicyVersion     int        `json:"policyVersion"`
	Capabilities      []string   `json:"capabilities"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	Revoked           bool       `json:"revoked"`
	LastUsedAt        *time.Time `json:"lastUsedAt"`
	CreatedAt         time.Time  `json:"createdAt"`
	CreatedBy         uint       `json:"createdBy"`
	CallCount24h      int64      `json:"callCount24h,omitempty"`
}

func projectAgentToken(tok *model.AgentToken) agentTokenView {
	if tok == nil {
		return agentTokenView{}
	}
	caps := []string{}
	if tok.PolicyVersion == service.AgentPolicyVersionV2 && tok.Capabilities != "" {
		_ = json.Unmarshal([]byte(tok.Capabilities), &caps)
		if caps == nil {
			caps = []string{}
		}
	}
	pv := tok.PolicyVersion
	if pv == 0 {
		pv = service.AgentPolicyVersionV1
	}
	return agentTokenView{
		ID:                tok.ID,
		Name:              tok.Name,
		TokenPrefix:       tok.TokenPrefix,
		ScopedInstanceIDs: tok.ScopedInstanceIDs,
		ScopedNodeIDs:     tok.ScopedNodeIDs,
		WriteAllowlist:    tok.WriteAllowlist,
		PolicyVersion:     pv,
		Capabilities:      caps,
		ExpiresAt:         tok.ExpiresAt,
		Revoked:           tok.Revoked,
		LastUsedAt:        tok.LastUsedAt,
		CreatedAt:         tok.CreatedAt,
		CreatedBy:         tok.CreatedBy,
	}
}

// Issue POST /api/v1/agent/tokens
func (h *AgentTokenHandler) Issue(c *gin.Context) {
	var req issueAgentTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "请求体无效"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)

	issueReq := service.IssueAgentTokenRequest{
		Name:              req.Name,
		ScopedInstanceIDs: req.ScopedInstanceIDs,
		ScopedNodeIDs:     req.ScopedNodeIDs,
		TTLDays:           req.TTLDays,
		CreatedBy:         userID,
	}
	if req.PolicyVersion != nil {
		issueReq.PolicyVersion = *req.PolicyVersion
	}
	if len(req.WriteAllowlist) > 0 {
		var allow []string
		if err := json.Unmarshal(req.WriteAllowlist, &allow); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "writeAllowlist 须为字符串数组"})
			return
		}
		issueReq.WriteAllowlist = allow
		issueReq.WriteAllowlistProvided = true
	}
	if len(req.Capabilities) > 0 {
		var caps []string
		if err := json.Unmarshal(req.Capabilities, &caps); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "capabilities 须为字符串数组"})
			return
		}
		issueReq.Capabilities = caps
		issueReq.CapabilitiesProvided = true
	}

	tok, plain, err := h.svc.Issue(issueReq)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": err.Error()})
		return
	}
	if h.audit != nil {
		detail, _ := json.Marshal(map[string]any{
			"tokenId": tok.ID, "tokenPrefix": tok.TokenPrefix, "name": tok.Name,
		})
		_ = h.audit.RecordResult(userID, "agent.token.create", "agent_token", strconv.FormatUint(uint64(tok.ID), 10), string(detail), c.ClientIP(), true, "")
	}
	c.JSON(http.StatusCreated, gin.H{
		"token":     projectAgentToken(tok),
		"plaintext": plain, // 仅此一次
	})
}

// List GET /api/v1/agent/tokens
// 响应含 lastUsedAt（模型字段）与 callCount24h（24h 聚合，FR-390）。
func (h *AgentTokenHandler) List(c *gin.Context) {
	list, err := h.svc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	ids := make([]uint, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	counts := map[uint]int64{}
	if h.callLog != nil {
		if m, err := h.callLog.Count24hMap(ids); err == nil {
			counts = m
		}
	}
	out := make([]agentTokenView, 0, len(list))
	for i := range list {
		view := projectAgentToken(&list[i])
		view.CallCount24h = counts[list[i].ID]
		out = append(out, view)
	}
	c.JSON(http.StatusOK, out)
}

// Revoke DELETE /api/v1/agent/tokens/:id
func (h *AgentTokenHandler) Revoke(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "id 无效"})
		return
	}
	if err := h.svc.Revoke(uint(id)); err != nil {
		if errors.Is(err, service.ErrAgentTokenNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "token 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "吊销失败"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	if h.audit != nil {
		_ = h.audit.RecordResult(userID, "agent.token.revoke", "agent_token", c.Param("id"), "", c.ClientIP(), true, "")
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListCallLogs GET /api/v1/agent/call-logs
// 查询参数：tokenId、action、client、success、from、to、page、pageSize。
func (h *AgentTokenHandler) ListCallLogs(c *gin.Context) {
	if h.callLog == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "调用流水未启用"})
		return
	}
	filter := service.AgentCallLogFilter{
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", 50),
	}
	if v := c.Query("tokenId"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "tokenId 无效"})
			return
		}
		tid := uint(id)
		filter.TokenID = &tid
	}
	if v := c.Query("action"); v != "" {
		filter.Action = &v
	}
	if v := c.Query("client"); v != "" {
		filter.Client = &v
	}
	if v := c.Query("success"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "success 须为 true/false"})
			return
		}
		filter.Success = &b
	}
	if v := c.Query("from"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "from 时间格式无效"})
			return
		}
		filter.From = &t
	}
	if v := c.Query("to"); v != "" {
		t, err := parseQueryTime(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "to 时间格式无效"})
			return
		}
		filter.To = &t
	}
	page, err := h.callLog.List(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询调用流水失败"})
		return
	}
	c.JSON(http.StatusOK, page)
}

func queryInt(c *gin.Context, key string, def int) int {
	v := c.Query(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func parseQueryTime(v string) (time.Time, error) {
	// 支持 RFC3339 与日期 YYYY-MM-DD
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", v, time.Local)
}

// AgentOpsHandler Agent 运维面（Bearer Agent Token）。
type AgentOpsHandler struct {
	agentSvc    *service.AgentTokenService
	instanceSvc *service.InstanceService
	nodeSvc     *service.NodeService
	audit       *service.AuditService
	callLog     *service.AgentCallLogService
}

// NewAgentOpsHandler 创建运维处理器（callLog 可为 nil，则不记流水）。
func NewAgentOpsHandler(agent *service.AgentTokenService, inst *service.InstanceService, node *service.NodeService, audit *service.AuditService, callLog *service.AgentCallLogService) *AgentOpsHandler {
	return &AgentOpsHandler{agentSvc: agent, instanceSvc: inst, nodeSvc: node, audit: audit, callLog: callLog}
}

// RegisterOpsRoutes 挂在已通过 AgentAuth 的组上。
func (h *AgentOpsHandler) RegisterOpsRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/agent")
	g.GET("/whoami", h.Whoami)
	g.GET("/nodes", h.ListNodes)
	g.GET("/instances", h.ListInstances)
	g.GET("/instances/:id", h.GetInstance)
	g.GET("/instances/:id/metrics", h.GetMetrics)
	g.POST("/instances/:id/start", h.Start)
	g.POST("/instances/:id/stop", h.Stop)
	g.POST("/instances/:id/restart", h.Restart)
	g.POST("/nodes/:id/maintenance/enter", h.MaintenanceEnter)
	g.POST("/nodes/:id/maintenance/leave", h.MaintenanceLeave)
}

func (h *AgentOpsHandler) principal(c *gin.Context) *service.AgentPrincipal {
	return middleware.GetAgentPrincipal(c)
}

func (h *AgentOpsHandler) forbid(c *gin.Context, msg string) {
	c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": msg})
}

func (h *AgentOpsHandler) auditAgent(c *gin.Context, p *service.AgentPrincipal, action, targetType, targetID string, ok bool, errMsg string) {
	if h.audit == nil || p == nil {
		return
	}
	detail, _ := json.Marshal(map[string]any{"actorKind": "agent", "agentName": p.Name, "tokenId": p.TokenID})
	_ = h.audit.RecordResult(p.TokenID, action, targetType, targetID, string(detail), c.ClientIP(), ok, errMsg)
}

// recordCall 写入 agent 调用流水（读+写+403）；失败只 WARN 不阻断。仅成功鉴权后调用。
func (h *AgentOpsHandler) recordCall(c *gin.Context, p *service.AgentPrincipal, action, capability, targetType, targetID string, success bool, errMsg string) {
	if h.callLog == nil || p == nil {
		return
	}
	h.callLog.RecordSafe(service.AgentCallRecord{
		TokenID:    p.TokenID,
		TokenName:  p.Name,
		Action:     action,
		Capability: capability,
		Client:     middleware.GetAgentClient(c),
		Transport:  "http",
		TargetType: targetType,
		TargetID:   targetID,
		Success:    success,
		Error:      errMsg,
		LatencyMs:  middleware.AgentCallLatencyMs(c),
		IP:         c.ClientIP(),
	})
}

// Whoami GET /api/v1/agent/whoami
func (h *AgentOpsHandler) Whoami(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	if _, err := service.CanDiscover(p, service.AgentActionWhoami); err != nil {
		h.recordCall(c, p, service.AgentActionWhoami, "", "", "", false, "forbidden")
		h.forbid(c, "操作被拒绝")
		return
	}
	h.recordCall(c, p, service.AgentActionWhoami, "", "", "", true, "")
	c.JSON(http.StatusOK, gin.H{
		"kind":              "agent",
		"name":              p.Name,
		"tokenId":           p.TokenID,
		"policyVersion":     p.PolicyVersion,
		"scopedInstanceIds": p.ScopedInstanceIDs,
		"scopedNodeIds":     p.ScopedNodeIDs,
		"writeAllowlist":    p.WriteAllowlist,
		"capabilities":      p.Capabilities,
	})
}

// ListNodes GET /api/v1/agent/nodes
func (h *AgentOpsHandler) ListNodes(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	auth, err := service.CanDiscover(p, service.AgentActionListNodes)
	if err != nil {
		h.recordCall(c, p, service.AgentActionListNodes, "", "node", "", false, "forbidden")
		h.forbid(c, "无节点 scope 或操作被拒绝")
		return
	}
	var out []model.Node
	for _, id := range p.ScopedNodeIDs {
		n, err := h.nodeSvc.GetByID(id)
		if err != nil {
			continue
		}
		out = append(out, *n)
	}
	h.recordCall(c, p, service.AgentActionListNodes, auth.Capability, "node", "", true, "")
	c.JSON(http.StatusOK, out)
}

// ListInstances GET /api/v1/agent/instances
// V1 仅显式实例 scope；V2 为显式实例 ∪ 授权节点当前实例（FR-395）。
func (h *AgentOpsHandler) ListInstances(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	var nodeFilter *uint
	if nid := c.Query("nodeId"); nid != "" {
		want, err := strconv.ParseUint(nid, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "nodeId 无效"})
			return
		}
		w := uint(want)
		nodeFilter = &w
	}
	out, err := h.agentSvc.ListAccessibleInstances(p, nodeFilter)
	if err != nil {
		h.recordCall(c, p, service.AgentActionListInstances, "", "instance", "", false, "forbidden")
		h.forbid(c, "无实例 scope 或操作被拒绝")
		return
	}
	auth, _ := service.CanDiscover(p, service.AgentActionListInstances)
	h.recordCall(c, p, service.AgentActionListInstances, auth.Capability, "instance", "", true, "")
	c.JSON(http.StatusOK, out)
}

// GetInstance GET /api/v1/agent/instances/:id
func (h *AgentOpsHandler) GetInstance(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "id 无效"})
		return
	}
	auth, inst, err := h.agentSvc.AuthorizeInstanceAction(p, service.AgentActionGetInstance, uint(id))
	if err != nil {
		// scope 外与不存在收敛为同一拒绝语义，不泄露存在性（FR-395）。
		h.recordCall(c, p, service.AgentActionGetInstance, "", "instance", c.Param("id"), false, "forbidden")
		h.forbid(c, "实例不在 scope 或操作被拒绝")
		return
	}
	h.recordCall(c, p, service.AgentActionGetInstance, auth.Capability, "instance", c.Param("id"), true, "")
	c.JSON(http.StatusOK, inst)
}

// GetMetrics GET /api/v1/agent/instances/:id/metrics
func (h *AgentOpsHandler) GetMetrics(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "id 无效"})
		return
	}
	auth, _, err := h.agentSvc.AuthorizeInstanceAction(p, service.AgentActionGetInstanceMetrics, uint(id))
	if err != nil {
		h.recordCall(c, p, service.AgentActionGetInstanceMetrics, "", "instance", c.Param("id"), false, "forbidden")
		h.forbid(c, "实例不在 scope 或操作被拒绝")
		return
	}
	m, err := h.instanceSvc.GetMetrics(uint(id))
	if err != nil {
		h.recordCall(c, p, service.AgentActionGetInstanceMetrics, auth.Capability, "instance", c.Param("id"), false, err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询指标失败"})
		return
	}
	h.recordCall(c, p, service.AgentActionGetInstanceMetrics, auth.Capability, "instance", c.Param("id"), true, "")
	c.JSON(http.StatusOK, m)
}

func (h *AgentOpsHandler) lifecycle(c *gin.Context, action string, fn func(id, expectedNodeID uint) error) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "id 无效"})
		return
	}
	auth, inst, err := h.agentSvc.AuthorizeInstanceAction(p, action, uint(id))
	if err != nil {
		h.auditAgent(c, p, action, "instance", c.Param("id"), false, "forbidden")
		h.recordCall(c, p, action, "", "instance", c.Param("id"), false, "forbidden")
		h.forbid(c, "能力/scope 不足或操作被拒绝")
		return
	}
	// 授权通过后携带可信归属执行；派发前锁内重验归属（FR-395）。
	if err := fn(uint(id), inst.NodeID); err != nil {
		h.auditAgent(c, p, action, "instance", c.Param("id"), false, err.Error())
		h.recordCall(c, p, action, auth.Capability, "instance", c.Param("id"), false, err.Error())
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
		return
	}
	h.auditAgent(c, p, action, "instance", c.Param("id"), true, "")
	h.recordCall(c, p, action, auth.Capability, "instance", c.Param("id"), true, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Start POST .../start
func (h *AgentOpsHandler) Start(c *gin.Context) {
	h.lifecycle(c, service.AgentActionInstanceStart, h.instanceSvc.StartForExpectedNode)
}

// Stop POST .../stop
func (h *AgentOpsHandler) Stop(c *gin.Context) {
	h.lifecycle(c, service.AgentActionInstanceStop, h.instanceSvc.StopForExpectedNode)
}

// Restart POST .../restart
func (h *AgentOpsHandler) Restart(c *gin.Context) {
	h.lifecycle(c, service.AgentActionInstanceRestart, h.instanceSvc.RestartForExpectedNode)
}

func (h *AgentOpsHandler) maintenance(c *gin.Context, action string, enabled bool) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "id 无效"})
		return
	}
	auth, err := h.agentSvc.AuthorizeNodeAction(p, action, uint(id))
	if err != nil {
		h.auditAgent(c, p, action, "node", c.Param("id"), false, "forbidden")
		h.recordCall(c, p, action, "", "node", c.Param("id"), false, "forbidden")
		h.forbid(c, "能力/scope 不足或操作被拒绝")
		return
	}
	if _, err := h.nodeSvc.SetMaintenance(uint(id), enabled); err != nil {
		h.auditAgent(c, p, action, "node", c.Param("id"), false, err.Error())
		h.recordCall(c, p, action, auth.Capability, "node", c.Param("id"), false, err.Error())
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
		return
	}
	h.auditAgent(c, p, action, "node", c.Param("id"), true, "")
	h.recordCall(c, p, action, auth.Capability, "node", c.Param("id"), true, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// MaintenanceEnter POST .../maintenance/enter
func (h *AgentOpsHandler) MaintenanceEnter(c *gin.Context) {
	h.maintenance(c, service.AgentActionNodeMaintenanceEnter, true)
}

// MaintenanceLeave POST .../maintenance/leave
func (h *AgentOpsHandler) MaintenanceLeave(c *gin.Context) {
	h.maintenance(c, service.AgentActionNodeMaintenanceLeave, false)
}
