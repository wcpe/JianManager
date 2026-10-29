package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BusinessReconcileHandler 业务对账协调路由。
//
// 契约见 docs/openapi/paths/business.yaml：
//
//	POST /api/v1/business/reconcile  拉取插件全量状态；Accept: text/event-stream 时逐实例推送
//
// 权限：instance:operate（只读动作，与单实例读动作同级；**不要求** instance:business:write）。
// 这是刻意的：对账只读，若要求写权限则只读账号无法做对账，反而会诱导它们复用写通道。
//
// 【JM 不做差异计算】本处理器不读 output 内容，只把它从服务层搬到响应体。
type BusinessReconcileHandler struct {
	reconcile *service.BusinessReconcileService
	authz     *service.AuthzService
	audit     *service.AuditService
}

// NewBusinessReconcileHandler 创建对账路由处理器。audit 可为 nil（无审计设施时静默跳过）。
func NewBusinessReconcileHandler(
	reconcile *service.BusinessReconcileService, authz *service.AuthzService, audit *service.AuditService,
) *BusinessReconcileHandler {
	return &BusinessReconcileHandler{reconcile: reconcile, authz: authz, audit: audit}
}

// RegisterRoutes 注册对账路由（加性追加，前缀由调用方的权限组提供）。
func (h *BusinessReconcileHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/business/reconcile", h.Reconcile)
}

// businessReconcileIn 对账请求体（对应 ReconcileRequest schema）。
//
// `action` 由调用方指定，JM 不预设任何动作名——这里没有默认值、没有枚举校验。
// payload 用 RawMessage 而非 any：JM 不解析业务内容，保留调用方发来的原始形态。
type businessReconcileIn struct {
	Domain    string            `json:"domain"`
	Action    string            `json:"action"`
	Payload   json.RawMessage   `json:"payload"`
	Target    *businessTargetIn `json:"target"`
	RequestID string            `json:"requestId"`
}

// Reconcile POST /business/reconcile — 拉取目标实例的插件全量状态。
//
// 按 Accept 分流：
//   - text/event-stream → SSE 逐实例推送（spec §5「建议流式」）
//   - 其余              → 同步返回完整结果集（HTTP 200，部分失败不整体失败）
//
// **前置错误（参数非法/目标为空/写动作）在任何 SSE 帧写出之前以 JSON 返回**：
// 一旦开帧状态码就固定为 200，错误只能塞进 error 帧，对调用方更不友好。
func (h *BusinessReconcileHandler) Reconcile(c *gin.Context) {
	if h.reconcile == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "RECONCILE_UNAVAILABLE", "message": "业务对账未启用"})
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req businessReconcileIn
	if err := c.ShouldBindJSON(&req); err != nil {
		writeBusinessReconcileError(c, fmt.Errorf("%w: 请求体不是合法 JSON", service.ErrInvalidBusinessCommand))
		return
	}
	if req.Target == nil {
		// 契约里 target 不是 required，但对账没有目标就无从下发；
		// 显式拒绝比「解析成空集再报 TARGET_EMPTY」更容易定位调用方 bug。
		writeBusinessReconcileError(c, fmt.Errorf("%w: target 必填（对账需要明确的目标实例集合）", service.ErrInvalidBusinessCommand))
		return
	}

	scope, ok := h.reconcileTargetScope(c)
	if !ok {
		return
	}

	svcReq := service.BusinessReconcileRequest{
		Target:    req.Target.toService(),
		Domain:    strings.TrimSpace(req.Domain),
		Action:    strings.TrimSpace(req.Action),
		Payload:   req.Payload,
		RequestID: strings.TrimSpace(req.RequestID),
		IssuerKey: businessBatchIssuerKey(c),
	}

	if wantsEventStream(c) {
		h.reconcileStream(c, svcReq, scope)
		return
	}
	h.reconcileSync(c, svcReq, scope)
}

// reconcileSync 同步执行并返回完整 JSON。
func (h *BusinessReconcileHandler) reconcileSync(
	c *gin.Context, req service.BusinessReconcileRequest, scope service.BusinessTargetScope,
) {
	resp, err := h.reconcile.Reconcile(c.Request.Context(), req, scope, nil)
	if err != nil {
		writeBusinessReconcileError(c, err)
		return
	}
	h.recordAudit(c, req, resp)
	c.JSON(http.StatusOK, resp)
}

// reconcileStream 流式执行：逐实例推送 result 帧，最后 done 帧带完整结果集。
//
// 断开语义与批量命令一致：客户端断开 **≠ 取消执行**（已下发的实例继续完成），
// 因此执行使用与请求解耦的 context，写失败只结束推送、不打断执行。
//
// 逐实例帧而非按批帧：对账的调用方（业务台）拿到一个实例的快照就能开始算差异，
// 等全批没有意义；这也是规格 §5「逐实例返回而非等全部完成」的直接要求。
func (h *BusinessReconcileHandler) reconcileStream(
	c *gin.Context, req service.BusinessReconcileRequest, scope service.BusinessTargetScope,
) {
	// 有界缓冲 + 非阻塞投递：执行协程永远不该被一个慢客户端卡住。
	frames := make(chan service.BusinessReconcileResult, 64)

	type streamResult struct {
		resp *service.BusinessReconcileResponse
		err  error
	}
	finished := make(chan streamResult, 1)

	go func() {
		resp, err := h.reconcile.Reconcile(context.Background(), req, scope, func(r service.BusinessReconcileResult) {
			select {
			case frames <- r:
			default:
				// 客户端跟不上：丢弃这一帧。done 帧必发（含完整结果集），
				// 而丢弃绝不允许反压执行本身。
			}
		})
		close(frames)
		finished <- streamResult{resp: resp, err: err}
	}()

	await := func() streamResult { return <-finished }

	// 先等到首个结果帧或执行结束：让「目标为空 / 写动作拒绝」这类前置错误
	// 仍能以 JSON 错误返回（此时还没写过任何 SSE 帧）。
	select {
	case first, ok := <-frames:
		if !ok {
			// 无结果帧就结束了：要么瞬间完成（零结果不可能，目标非空），要么前置失败。
			res := await()
			if res.err != nil {
				writeBusinessReconcileError(c, res.err)
				return
			}
			h.streamHeaders(c)
			h.writeReconcileSSE(c, "done", reconcileDonePayload(res.resp))
			h.recordAudit(c, req, res.resp)
			return
		}
		h.streamHeaders(c)
		if !h.writeReconcileSSE(c, "result", first) {
			h.drainReconcileFrames(frames)
			h.recordAudit(c, req, await().resp)
			return
		}
	case <-time.After(businessReconcileStreamFirstFrameTimeout):
		// 超时没等到任何结果（大目标首批尚未回执）：仍开帧，后续照常推送。
		h.streamHeaders(c)
	}

	for r := range frames {
		if !h.writeReconcileSSE(c, "result", r) {
			h.drainReconcileFrames(frames)
			h.recordAudit(c, req, await().resp)
			return
		}
	}
	res := await()
	if res.err != nil {
		h.writeReconcileSSE(c, "error", reconcileErrorPayload(res.err))
		return
	}
	h.writeReconcileSSE(c, "done", reconcileDonePayload(res.resp))
	h.recordAudit(c, req, res.resp)
}

// businessReconcileStreamFirstFrameTimeout 是「等首帧」的窗口。
// 取 5s（略宽于批量命令的 3s）：对账单实例超时 60s，首批回执可能比命令慢。
const businessReconcileStreamFirstFrameTimeout = 5 * time.Second

// drainReconcileFrames 排空并丢弃剩余结果帧，让执行协程能继续投递直到结束。
func (h *BusinessReconcileHandler) drainReconcileFrames(frames <-chan service.BusinessReconcileResult) {
	for range frames {
	}
}

// streamHeaders 写出 SSE 响应头。
//
// 复用批量命令的响应头约定（Cache-Control / X-Accel-Buffering），但独立实现是为了
// 让对账的帧语义（result 而非 progress）不与批量命令耦合。
func (h *BusinessReconcileHandler) streamHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// 禁用代理缓冲是硬要求：Nginx 默认会缓冲 SSE，导致逐实例结果成批到达甚至超时断连。
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
}

// writeReconcileSSE 写一帧 `event: <name>\ndata: <json>\n\n` 并 flush，返回是否成功。
func (h *BusinessReconcileHandler) writeReconcileSSE(c *gin.Context, event string, payload any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("序列化对账 SSE 帧失败", "event", event, "error", err)
		return false
	}
	if _, err := c.Writer.WriteString("event: " + event + "\ndata: " + string(raw) + "\n\n"); err != nil {
		return false
	}
	if err := http.NewResponseController(c.Writer).Flush(); err != nil {
		return false
	}
	return true
}

// reconcileDonePayload 构造 done 帧正文：requestId + 完整逐实例结果。
//
// 与同步响应的 body 完全一致——流式与同步两条路径给出同一份数据，
// 调用方切换传输方式时不需要改解析逻辑。
func reconcileDonePayload(resp *service.BusinessReconcileResponse) map[string]any {
	if resp == nil {
		return map[string]any{"requestId": "", "results": []any{}}
	}
	return map[string]any{
		"requestId": resp.RequestID,
		"results":   resp.Results,
	}
}

// reconcileErrorPayload 构造 SSE error 帧正文（`{"code":...,"message":...}`）。
func reconcileErrorPayload(err error) map[string]any {
	code, _ := service.BusinessReconcileErrorCode(err)
	return map[string]any{"code": code, "message": err.Error()}
}

// reconcileTargetScope 解析当前调用方的可访问实例范围。
//
// 与批量命令的 targetScope 口径一致（同一次目标解析，同一种范围收敛），
// 但挂在对账处理器上而不复用批量处理器的方法——两者的 authz 依赖独立，
// 复用会迫使对账路由也去构造一个 BusinessBatchHandler。
//
// 返回 (scope, ok)；ok=false 表示已经写过错误响应，调用方应立即返回。
func (h *BusinessReconcileHandler) reconcileTargetScope(c *gin.Context) (service.BusinessTargetScope, bool) {
	access := getAccess(c)
	if h.authz == nil || access == nil {
		// 无 authz 设施（测试/内部路径）：不收敛。
		return service.BusinessTargetScope{}, true
	}
	ids, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询可访问实例失败"})
		return service.BusinessTargetScope{}, false
	}
	return service.BusinessTargetScope{IDs: ids, Scoped: scoped}, true
}

// recordAudit 记录对账的 JM 侧审计。
//
// 审计边界（ADR-106）：只记通道事实（domain/action/requestId/规模/成功数），
// **payload 与 output 一律不入审计**——JM 不理解业务语义，也不承担业务数据存储责任。
func (h *BusinessReconcileHandler) recordAudit(
	c *gin.Context, req service.BusinessReconcileRequest, resp *service.BusinessReconcileResponse,
) {
	if h.audit == nil || resp == nil {
		return
	}
	detail := map[string]any{
		"domain":    req.Domain,
		"action":    req.Action,
		"requestId": req.RequestID,
		"total":     len(resp.Results),
		"succeeded": resp.SuccessfulCount(),
	}
	h.audit.RecordSafe(getUserID(c), "business.reconcile", "business", req.Domain+"."+req.Action, marshalAuditDetail(detail), c.ClientIP())
}

// writeBusinessReconcileError 统一输出对账错误。
//
// 用 code 而非 error 字段表达机器可读错误码，与 business.yaml 引用的 ErrorBase 一致；
// 同时保留 message 供人阅读。
func writeBusinessReconcileError(c *gin.Context, err error) {
	code, status := service.BusinessReconcileErrorCode(err)
	c.JSON(status, gin.H{"code": code, "message": err.Error(), "error": code})
}
