package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 审批中心 HTTP 面（FR-490 / FR-493）。
//
// ## 两组端点的身份边界
//
//   - `/approvals/own/*`：**申请人自查**。范围由 session 归属决定（只能看自己的），
//     对应 Beacon 的 own.list / own.get / own.withdraw。
//   - `/approvals/*`：**人类审批面**。要求 approval.read / approval.decide 权限节点。
//
// 两组都只走人类 JWT。Agent 走的是 `/api/v1/mcp`，不在这些路由组里；
// 但即使有人绕过路由直接把 Agent Token 塞进来，服务层的身份判据也会拦住
// （见 `ApprovalService.Decide` 的第一道闸）。
//
// ## 为什么不把「批准」做成一个 MCP 工具
//
// 那样 Agent 就能自己批准自己提的申请。审批的意义在于**人类介入**，
// 所以批准这条路必须只存在于人类接口上——不是靠工具目录里不放它。

// ApprovalHandler 审批中心处理器。
type ApprovalHandler struct {
	svc *service.ApprovalService
	// exec 批准后触发执行（FR-491）。nil 时只落状态不执行——
	// 但那样批准就成了空操作，因此装配层必须注入。
	exec *service.ApprovalExecutor
	// audit 决策审计。nil 时跳过审计（执行器仍会在执行侧留痕）。
	audit *service.AuditService
}

// NewApprovalHandler 创建处理器。
func NewApprovalHandler(svc *service.ApprovalService, exec *service.ApprovalExecutor, audit *service.AuditService) *ApprovalHandler {
	return &ApprovalHandler{svc: svc, exec: exec, audit: audit}
}

// RegisterRoutes 注册**申请人自查**端点（挂在普通登录组下）。
func (h *ApprovalHandler) RegisterRoutes(r *gin.RouterGroup) {
	own := r.Group("/approvals/own")
	own.GET("", h.ListOwn)
	own.GET("/:id", h.GetOwn)
	own.POST("/:id/withdraw", h.Withdraw)
}

// RegisterAdminRoutes 注册**人类审批**端点（挂在带权限节点的组下）。
func (h *ApprovalHandler) RegisterAdminRoutes(read, decide *gin.RouterGroup) {
	read.GET("/approvals", h.ListPending)
	read.GET("/approvals/:id", h.GetTicket)
	decide.POST("/approvals/:id/approve", h.Approve)
	decide.POST("/approvals/:id/reject", h.Reject)
}

// ListOwn GET /approvals/own
func (h *ApprovalHandler) ListOwn(c *gin.Context) {
	uid := currentUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未授权"})
		return
	}
	items, err := h.svc.ListApprovals(service.ApprovalListFilter{
		ApplicantUserID: &uid,
		Status:          c.Query("status"),
		Limit:           parseIntDefault(c.Query("limit"), 50),
	})
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GetOwn GET /approvals/own/:id
func (h *ApprovalHandler) GetOwn(c *gin.Context) {
	uid := currentUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未授权"})
		return
	}
	id, ok := parseApprovalID(c)
	if !ok {
		return
	}
	t, err := h.svc.GetApproval(id, &uid)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// Withdraw POST /approvals/own/:id/withdraw
func (h *ApprovalHandler) Withdraw(c *gin.Context) {
	uid := currentUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未授权"})
		return
	}
	id, ok := parseApprovalID(c)
	if !ok {
		return
	}
	t, err := h.svc.Withdraw(id, uid)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// ListPending GET /approvals（待审列表，可按 status 过滤看历史）
func (h *ApprovalHandler) ListPending(c *gin.Context) {
	// status 三段语义：
	//   - 缺省 / "pending"：只看待办（审批台的默认视角）
	//   - "all"：**不加状态条件**，返回全部历史
	//   - 其余：按该状态精确过滤
	//
	// 为什么需要显式的 "all"：服务层用 `Status == ""` 表示不过滤，而缺省值又必须是 pending，
	// 于是「全部」在协议上无可表达——前端只能传一个不存在的状态值，结果恒为空。
	// 这里把 "all" 归一成空串交给服务层，让两种意图各有独立的表达。
	items, err := h.svc.ListApprovals(service.ApprovalListFilter{
		Status: normalizeApprovalListStatus(c.Query("status")),
		Limit:  parseIntDefault(c.Query("limit"), 50),
	})
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// normalizeApprovalListStatus 把查询参数归一成服务层的 status 过滤值。
//
// 服务层用空串表示「不过滤」，而接口缺省值必须是 pending（审批台默认只看待办），
// 两者撞在一起就导致「全部」在协议上无可表达——前端只能传一个不存在的状态，结果恒为空。
// 这里给两种意图各自独立的表达：
//   - 缺省 → pending（默认视角）
//   - "all" → ""（不过滤）
//   - 其余 → 原样精确过滤
func normalizeApprovalListStatus(raw string) string {
	switch raw {
	case "":
		return model.ApprovalStatusPending
	case "all":
		return ""
	default:
		return raw
	}
}

// GetTicket GET /approvals/:id
func (h *ApprovalHandler) GetTicket(c *gin.Context) {
	id, ok := parseApprovalID(c)
	if !ok {
		return
	}
	t, err := h.svc.GetApproval(id, nil)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// Approve POST /approvals/:id/approve
func (h *ApprovalHandler) Approve(c *gin.Context) { h.decide(c, true) }

// Reject POST /approvals/:id/reject
func (h *ApprovalHandler) Reject(c *gin.Context) { h.decide(c, false) }

// decide 批准/驳回的共同实现。
func (h *ApprovalHandler) decide(c *gin.Context, approve bool) {
	uid := currentUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未授权"})
		return
	}
	id, ok := parseApprovalID(c)
	if !ok {
		return
	}
	// 这组端点只可能由人类 JWT 到达（挂登录组），因此 ActorIsAgent=false。
	// 真正的防线在服务层：即使有人把 Agent 身份伪造到这里，服务层的身份判据仍会拒。
	t, err := h.svc.Decide(c.Request.Context(), service.DecideApprovalParams{
		TicketID:     id,
		ActorUserID:  uid,
		ActorIsAgent: false,
		Approve:      approve,
	})
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	// 决策审计：谁在什么时候批准/驳回了哪个高危操作。
	//
	// 执行面另有 approval.executed / approval.execute_failed（见执行器 finish），
	// 两条合起来才回答完整问题：决策是谁做的、最终成没成。
	// 决策审计在这里写而不是服务层：服务层 Decide 也可能被内部调用（如到期巡检），
	// 只有 HTTP 面能确定「这是一个人类的显式决策动作」。
	if h.audit != nil {
		action := "approval.rejected"
		if approve {
			action = "approval.approved"
		}
		h.audit.RecordSafe(uid, action, t.TargetType, t.TargetID,
			fmt.Sprintf(`{"ticketId":%d,"action":%q,"applicantUserId":%d}`, t.ID, t.Action, t.ApplicantUserID), "")
	}
	// 批准后**立即触发执行**。
	//
	// 为什么在请求内触发而不是靠后台轮询：批准是用户的明确动作，它应当马上产生结果；
	// 轮询会引入「批了但过一会儿才生效」的不可解释延迟。
	// 执行器内部是 CAS 认领 +（长操作时）RunAsync，因此这里不会阻塞太久。
	if approve && h.exec != nil {
		// 用**独立 context**：请求结束后仍要继续执行
		//（用户关页面不该中断一个已被批准的动作）。
		go func(ticketID uint) {
			execCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if execErr := h.exec.Execute(execCtx, ticketID); execErr != nil {
				// 失败已写进票据 failure_reason，这里只留日志。
				slog.Warn("审批执行失败", "ticketId", ticketID, "error", execErr)
			}
		}(t.ID)
	}
	c.JSON(http.StatusOK, t)
}

// respondApprovalErr 审批错误映射。
//
// 判序要点：`ErrApprovalNotFound` 与「不是你的票」共用 404——分开回 403/404
// 等于给出「这个 id 存在吗」的探测接口（与 FR-515 会话端点同一取舍）。
func respondApprovalErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrApprovalNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "APPROVAL_NOT_FOUND", "message": "审批不存在"})
	case errors.Is(err, service.ErrApprovalAgentCannotDecide):
		// Agent 身份哪怕拿到权限节点也不可批准。
		c.JSON(http.StatusForbidden, gin.H{"error": "AGENT_CANNOT_DECIDE", "message": err.Error()})
	case errors.Is(err, service.ErrApprovalExpired):
		c.JSON(http.StatusGone, gin.H{"error": "APPROVAL_EXPIRED", "message": err.Error()})
	case errors.Is(err, service.ErrApprovalNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": "APPROVAL_NOT_PENDING", "message": err.Error()})
	case errors.Is(err, service.ErrApprovalTargetDrift),
		errors.Is(err, service.ErrApprovalPayloadTampered):
		c.JSON(http.StatusConflict, gin.H{"error": "APPROVAL_STALE", "message": err.Error()})
	case errors.Is(err, service.ErrApprovalNotRegistered):
		c.JSON(http.StatusBadRequest, gin.H{"error": "APPROVAL_NOT_REGISTERED", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// parseApprovalID 解析路径参数 :id。
func parseApprovalID(c *gin.Context) (uint, bool) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "id 不合法"})
		return 0, false
	}
	return uint(id), true
}
