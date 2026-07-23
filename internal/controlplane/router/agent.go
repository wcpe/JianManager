package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AgentTokenHandler 管理员面：Agent Token 颁发/列表/吊销（JWT + 平台管理员）。
type AgentTokenHandler struct {
	svc   *service.AgentTokenService
	audit *service.AuditService
}

// NewAgentTokenHandler 创建处理器。
func NewAgentTokenHandler(svc *service.AgentTokenService, audit *service.AuditService) *AgentTokenHandler {
	return &AgentTokenHandler{svc: svc, audit: audit}
}

// RegisterAdminRoutes 挂到 JWT protected + 平台管理员组。
func (h *AgentTokenHandler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/agent/tokens")
	g.POST("", h.Issue)
	g.GET("", h.List)
	g.DELETE("/:id", h.Revoke)
}

type issueAgentTokenRequest struct {
	Name              string   `json:"name"`
	ScopedInstanceIDs []uint   `json:"scopedInstanceIds"`
	ScopedNodeIDs     []uint   `json:"scopedNodeIds"`
	WriteAllowlist    []string `json:"writeAllowlist"`
	TTLDays           int      `json:"ttlDays"`
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
	tok, plain, err := h.svc.Issue(service.IssueAgentTokenRequest{
		Name:              req.Name,
		ScopedInstanceIDs: req.ScopedInstanceIDs,
		ScopedNodeIDs:     req.ScopedNodeIDs,
		WriteAllowlist:    req.WriteAllowlist,
		TTLDays:           req.TTLDays,
		CreatedBy:         userID,
	})
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
		"token":     tok,
		"plaintext": plain, // 仅此一次
	})
}

// List GET /api/v1/agent/tokens
func (h *AgentTokenHandler) List(c *gin.Context) {
	list, err := h.svc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, list)
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

// AgentOpsHandler Agent 运维面（Bearer Agent Token）。
type AgentOpsHandler struct {
	agentSvc    *service.AgentTokenService
	instanceSvc *service.InstanceService
	nodeSvc     *service.NodeService
	audit       *service.AuditService
}

// NewAgentOpsHandler 创建运维处理器。
func NewAgentOpsHandler(agent *service.AgentTokenService, inst *service.InstanceService, node *service.NodeService, audit *service.AuditService) *AgentOpsHandler {
	return &AgentOpsHandler{agentSvc: agent, instanceSvc: inst, nodeSvc: node, audit: audit}
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

// Whoami GET /api/v1/agent/whoami
func (h *AgentOpsHandler) Whoami(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	if err := service.ResolveAction(p, service.AgentActionWhoami, 0, 0); err != nil {
		h.forbid(c, "操作被拒绝")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"kind":              "agent",
		"name":              p.Name,
		"tokenId":           p.TokenID,
		"scopedInstanceIds": p.ScopedInstanceIDs,
		"scopedNodeIds":     p.ScopedNodeIDs,
		"writeAllowlist":    p.WriteAllowlist,
	})
}

// ListNodes GET /api/v1/agent/nodes
func (h *AgentOpsHandler) ListNodes(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	if err := service.ResolveAction(p, service.AgentActionListNodes, 0, 0); err != nil {
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
	c.JSON(http.StatusOK, out)
}

// ListInstances GET /api/v1/agent/instances
func (h *AgentOpsHandler) ListInstances(c *gin.Context) {
	p := h.principal(c)
	if p == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "需要 Agent Token"})
		return
	}
	if err := service.ResolveAction(p, service.AgentActionListInstances, 0, 0); err != nil {
		h.forbid(c, "无实例 scope 或操作被拒绝")
		return
	}
	var out []model.Instance
	for _, id := range p.ScopedInstanceIDs {
		inst, err := h.instanceSvc.GetByID(id)
		if err != nil {
			continue
		}
		if nid := c.Query("nodeId"); nid != "" {
			want, _ := strconv.ParseUint(nid, 10, 64)
			if inst.NodeID != uint(want) {
				continue
			}
		}
		out = append(out, *inst)
	}
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
	if err := service.ResolveAction(p, service.AgentActionGetInstance, uint(id), 0); err != nil {
		h.forbid(c, "实例不在 scope 或操作被拒绝")
		return
	}
	inst, err := h.instanceSvc.GetByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
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
	if err := service.ResolveAction(p, service.AgentActionGetInstanceMetrics, uint(id), 0); err != nil {
		h.forbid(c, "实例不在 scope 或操作被拒绝")
		return
	}
	m, err := h.instanceSvc.GetMetrics(uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询指标失败"})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *AgentOpsHandler) lifecycle(c *gin.Context, action string, fn func(uint) error) {
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
	if err := service.ResolveAction(p, action, uint(id), 0); err != nil {
		h.auditAgent(c, p, action, "instance", c.Param("id"), false, "forbidden")
		h.forbid(c, "写白名单/scope 不足或硬拒绝")
		return
	}
	if err := fn(uint(id)); err != nil {
		h.auditAgent(c, p, action, "instance", c.Param("id"), false, err.Error())
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
		return
	}
	h.auditAgent(c, p, action, "instance", c.Param("id"), true, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Start POST .../start
func (h *AgentOpsHandler) Start(c *gin.Context) {
	h.lifecycle(c, service.AgentActionInstanceStart, h.instanceSvc.Start)
}

// Stop POST .../stop
func (h *AgentOpsHandler) Stop(c *gin.Context) {
	h.lifecycle(c, service.AgentActionInstanceStop, h.instanceSvc.Stop)
}

// Restart POST .../restart
func (h *AgentOpsHandler) Restart(c *gin.Context) {
	h.lifecycle(c, service.AgentActionInstanceRestart, h.instanceSvc.Restart)
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
	if err := service.ResolveAction(p, action, 0, uint(id)); err != nil {
		h.auditAgent(c, p, action, "node", c.Param("id"), false, "forbidden")
		h.forbid(c, "写白名单/scope 不足或硬拒绝")
		return
	}
	if _, err := h.nodeSvc.SetMaintenance(uint(id), enabled); err != nil {
		h.auditAgent(c, p, action, "node", c.Param("id"), false, err.Error())
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
		return
	}
	h.auditAgent(c, p, action, "node", c.Param("id"), true, "")
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
