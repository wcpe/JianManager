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

// BusinessHandler JBIS 业务对接下发路由处理器（FR-116/FR-121，见 ADR-026/027/029）。
// 把前端发起的业务动作（domain.action + payload）经探针桥下发到目标实例的业务对接层；
// CP 插件无关，仅转发信封、透传结果。探针未连/域不可用由 service 层降级（200 + available=false）。
//
// 高危写（write=true，对应 manifest readOnly=false）叠加 FR-121 横切硬化：独立权限节点
// instance:business:write + 注入幂等键/操作者上下文（service.DispatchWrite）+ 既有审计留痕。
type BusinessHandler struct {
	bizSvc *service.BusinessService
	authz  *service.AuthzService
	audit  *service.AuditService
}

// NewBusinessHandler 创建业务对接下发路由处理器。
// audit 可为 nil（无审计设施时静默跳过业务写留痕，不阻断下发）。
func NewBusinessHandler(bizSvc *service.BusinessService, authz *service.AuthzService, audit *service.AuditService) *BusinessHandler {
	return &BusinessHandler{bizSvc: bizSvc, authz: authz, audit: audit}
}

// businessDispatchRequest 业务命令下发请求体。
type businessDispatchRequest struct {
	Domain  string `json:"domain"`
	Action  string `json:"action"`
	Payload string `json:"payload"` // 结构化业务参数 JSON 字符串（CP 不解析，原样下发）
	// Write 标记是否为高危写动作（前端据 manifest readOnly 取反，FR-121）。
	// true → 需 instance:business:write + 注入幂等键/操作者上下文 + 审计留痕；
	// false/缺省 → 维持 FR-116 只读行为（instance:operate，不注入、不记业务写审计）。
	Write bool `json:"write"`
	// OperationID 写动作幂等标识（前端生成的稳定 UUID，对同一逻辑操作的重试不变，FR-121）。
	// CP 用作 payload taskId（探针→mce BusinessOrder 幂等键）；缺省时 service 兜底生成（但失去重试去重）。
	OperationID string `json:"operationId"`
	// Reason 操作原因（可选，写动作透传进插件流水 reason + JM 审计，FR-121）。
	Reason string `json:"reason"`
}

// Dispatch POST /instances/:id/business — 向某实例下发一条业务命令并取回结果。
//
// 读动作（write=false/缺省）：权限 instance:operate，payload 原样下发（FR-116）。
// 写动作（write=true）：权限 instance:business:write，注入幂等键 + 操作者上下文并记审计（FR-121/ADR-029）。
// 两者均须实例可访问。探针未连/域不可用由 service 降级（200 + available=false + error），不返回 5xx。
func (h *BusinessHandler) Dispatch(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	var req businessDispatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	access := getAccess(c)
	// 写动作要求独立高危写权限节点；读动作维持 instance:operate（FR-121）。
	requiredPerm := service.PermInstanceOperate
	if req.Write {
		requiredPerm = service.PermInstanceBusinessWrite
	}
	if access == nil || !access.HasPermission(requiredPerm) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}

	var res *service.BusinessResult
	if req.Write {
		res, err = h.bizSvc.DispatchWrite(id, req.Domain, req.Action, req.Payload, service.WriteContext{
			TaskID:     req.OperationID,
			Operator:   getUsername(c),
			OperatorID: getUserID(c),
			Reason:     req.Reason,
		})
	} else {
		res, err = h.bizSvc.Dispatch(id, req.Domain, req.Action, req.Payload)
	}
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInstanceNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		case errors.Is(err, service.ErrInvalidBusinessCommand):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 domain 或 action，或 payload 非法"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "业务命令下发失败"})
		}
		return
	}
	if req.Write {
		h.recordWriteAudit(c, id, req, res)
	}
	c.JSON(http.StatusOK, res)
}

// recordWriteAudit 记录业务高危写的 JM 侧审计（FR-121，复用既有 AuditService）。
// 仅在成功下发后记录；审计失败不阻断主流程（与 PlayerHandler.recordAudit 一致）。
func (h *BusinessHandler) recordWriteAudit(c *gin.Context, instanceID uint, req businessDispatchRequest, res *service.BusinessResult) {
	if h.audit == nil {
		return
	}
	detail := map[string]any{
		"domain":      req.Domain,
		"action":      req.Action,
		"operationId": req.OperationID,
		"reason":      req.Reason,
		"available":   res.Available,
	}
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(getUserID(c), "business.write", "instance", strconv.FormatUint(uint64(instanceID), 10), string(raw), c.ClientIP())
}

// getUserID 取当前操作用户 ID。
func getUserID(c *gin.Context) uint {
	v, _ := c.Get(middleware.CtxUserID)
	id, _ := v.(uint)
	return id
}

// getUsername 取当前操作用户名（用作业务写 operator 透传进插件流水）。
func getUsername(c *gin.Context) string {
	v, _ := c.Get(middleware.CtxUsername)
	name, _ := v.(string)
	return name
}

// Manifest GET /instances/:id/business/manifest — 取某实例的业务能力清单（JBIS 元查询，FR-116）。
// 只读发现，权限 instance:read 且实例须可访问。探针未连/无业务 Provider 由 service 降级（200 + available=false）。
func (h *BusinessHandler) Manifest(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceRead) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if !canAccessInstance(c, h.authz, id) {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
		return
	}
	res, err := h.bizSvc.Manifest(id)
	if err != nil {
		if errors.Is(err, service.ErrInstanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "实例不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "业务能力清单获取失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// RegisterRoutes 注册业务对接路由（加性追加）。
func (h *BusinessHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/instances/:id/business", h.Dispatch)
	rg.GET("/instances/:id/business/manifest", h.Manifest)
}
