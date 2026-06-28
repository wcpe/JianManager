package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// NodeProxyHandler 节点级出站代理路由（FR-185，见 ADR-043）。
// 仅平台管理员可达；设置写审计。生效经心跳下发到 Worker（运行时重建出站 client）。
type NodeProxyHandler struct {
	svc   *service.NodeProxyService
	audit *service.AuditService
}

// NewNodeProxyHandler 创建节点代理路由处理器。audit 可为 nil（审计随之关闭）。
func NewNodeProxyHandler(svc *service.NodeProxyService, audit *service.AuditService) *NodeProxyHandler {
	return &NodeProxyHandler{svc: svc, audit: audit}
}

// Get GET /nodes/:id/proxy — 查看节点代理配置（脱敏，含当前生效值与全局默认）。
func (h *NodeProxyHandler) Get(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	view, err := h.svc.NodeProxyView(id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询节点代理失败"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// updateNodeProxyRequest 设置节点代理：mode=inherit 时 url/noProxy 忽略；custom 时 url 必填。
type updateNodeProxyRequest struct {
	Mode    string `json:"mode" binding:"required"`
	URL     string `json:"url"`
	NoProxy string `json:"noProxy"`
}

// Update PATCH /nodes/:id/proxy — 设置节点继承全局/自定义代理（仅平台管理员，写审计）。
func (h *NodeProxyHandler) Update(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req updateNodeProxyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	node, err := h.svc.UpdateNodeProxy(id, req.Mode, req.URL, req.NoProxy)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNodeNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
		case errors.Is(err, service.ErrSettingValueInvalid):
			// 代理模式/地址非法为可预期校验错误，回 422。
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "设置节点代理失败"})
		}
		return
	}
	// 审计（敏感 URL 不入审计明文，仅记录 mode；详见 ADR-043 脱敏要求）。
	h.recordAudit(c, "node.proxy.set", c.Param("id"), map[string]any{"nodeId": id, "mode": node.ProxyMode})

	// 回视图（脱敏）。设置后节点离线时前端据 view.online 标注「待下发」（下次心跳生效）。
	view, verr := h.svc.NodeProxyView(id)
	if verr != nil {
		c.JSON(http.StatusOK, gin.H{"message": "已保存"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// recordAudit 记录节点代理设置审计；audit 未注入时静默跳过。
func (h *NodeProxyHandler) recordAudit(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	uid, _ := c.Get(middleware.CtxUserID)
	userID, _ := uid.(uint)
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(userID, action, "node", targetID, string(raw), c.ClientIP())
}

// RegisterRoutes 注册节点代理路由（FR-185）。
func (h *NodeProxyHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/nodes/:id/proxy", h.Get)
	rg.PATCH("/nodes/:id/proxy", h.Update)
}
