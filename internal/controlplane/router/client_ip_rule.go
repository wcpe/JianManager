package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientIPRuleHandler 客户端分发端点 IP 防护规则管理（FR-096，见 ADR-023）。运行时可改、入审计；限平台管理员。
type ClientIPRuleHandler struct {
	svc   *service.ClientIPGuardService
	audit *service.AuditService
}

// NewClientIPRuleHandler 创建 IP 防护规则处理器。
func NewClientIPRuleHandler(svc *service.ClientIPGuardService, audit *service.AuditService) *ClientIPRuleHandler {
	return &ClientIPRuleHandler{svc: svc, audit: audit}
}

// List GET /client-dist/ip-rules — 列出全部 IP 规则。
func (h *ClientIPRuleHandler) List(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	rules, err := h.svc.ListRules()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, rules)
}

// addIPRuleRequest 新增 IP 规则请求体。
type addIPRuleRequest struct {
	CIDR string `json:"cidr"`
	Mode string `json:"mode"` // deny | allow
	Note string `json:"note"`
}

// Add POST /client-dist/ip-rules — 新增 IP 规则（运行时生效，入审计）。
func (h *ClientIPRuleHandler) Add(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body addIPRuleRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	createdBy, _ := uid.(uint)
	rule, err := h.svc.AddRule(body.CIDR, body.Mode, body.Note, createdBy)
	if err != nil {
		if errors.Is(err, service.ErrInvalidIPRule) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_IP_RULE", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建失败"})
		return
	}
	h.recordAudit(c, "client_ip_rule.add", map[string]any{"cidr": rule.CIDR, "mode": rule.Mode})
	c.JSON(http.StatusCreated, rule)
}

// Remove DELETE /client-dist/ip-rules/:id — 删除 IP 规则（运行时生效，入审计）。
func (h *ClientIPRuleHandler) Remove(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "非法 id"})
		return
	}
	if err := h.svc.RemoveRule(uint(id)); err != nil {
		if errors.Is(err, service.ErrIPRuleNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "IP_RULE_NOT_FOUND", "message": "规则不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "删除失败"})
		return
	}
	h.recordAudit(c, "client_ip_rule.remove", map[string]any{"id": id})
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Stats GET /client-dist/protection-stats — 防护拦截计数（可观测；内存计数、不写库）。
func (h *ClientIPRuleHandler) Stats(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	c.JSON(http.StatusOK, h.svc.Stats())
}

func (h *ClientIPRuleHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, action, "client_ip_rule", "", string(raw), c.ClientIP())
}

// RegisterRoutes 注册 IP 防护规则管理路由（须挂 JWT 平台管理员组）。
func (h *ClientIPRuleHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/ip-rules", h.List)
	rg.POST("/client-dist/ip-rules", h.Add)
	rg.DELETE("/client-dist/ip-rules/:id", h.Remove)
	rg.GET("/client-dist/protection-stats", h.Stats)
}
