package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// BusinessBatchHandler 业务批量命令与跨区二次确认路由（FR-523 / FR-535 / FR-537）。
//
// 契约见 docs/openapi/paths/business.yaml：
//
//	POST /api/v1/business/batch/preview  解析目标 + 风险评估 + 签发 confirmToken（无副作用）
//	POST /api/v1/business/batch          执行；Accept: text/event-stream 时流式推送进度
//
// 权限：执行走 instance:business:write（批量下发业务命令属于高危写，与单实例写动作同级），
// 预览只解析目标不产生副作用，走 instance:operate 即可。
type BusinessBatchHandler struct {
	batch *service.BusinessBatchService
	authz *service.AuthzService
	audit *service.AuditService
}

// NewBusinessBatchHandler 创建业务批量路由处理器。audit 可为 nil（无审计设施时静默跳过）。
func NewBusinessBatchHandler(batch *service.BusinessBatchService, authz *service.AuthzService, audit *service.AuditService) *BusinessBatchHandler {
	return &BusinessBatchHandler{batch: batch, authz: authz, audit: audit}
}

// RegisterRoutes 注册业务批量路由（加性追加，前缀由调用方的权限组提供）。
func (h *BusinessBatchHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/business/batch/preview", h.Preview)
	rg.POST("/business/batch", h.Execute)
}

// businessTargetIn 目标选择请求体（对应 BatchTarget schema）。
//
// 与 service.BusinessTarget 字段一致，单独定义是为了把 JSON 绑定的关注点留在路由层，
// 服务层结构体不背 json tag 之外的传输语义。
type businessTargetIn struct {
	GroupIDs           []uint                       `json:"groupIds"`
	IncludeDescendants bool                         `json:"includeDescendants"`
	InstanceIDs        []uint                       `json:"instanceIds"`
	Filters            *service.BusinessBatchFilter `json:"filters"`
}

// toService 转为服务层目标结构。
func (in businessTargetIn) toService() service.BusinessTarget {
	return service.BusinessTarget{
		GroupIDs:           in.GroupIDs,
		IncludeDescendants: in.IncludeDescendants,
		InstanceIDs:        in.InstanceIDs,
		Filters:            in.Filters,
	}
}

// businessBatchPreviewRequest 预览请求体（对应 BatchPreviewRequest schema）。
type businessBatchPreviewRequest struct {
	Target businessTargetIn `json:"target"`
	// ActionRisk 动作自身风险等级（READ / WRITE / DESTRUCTIVE），由业务台从 manifest 读取后透传。
	// 缺省按 WRITE 处理（规格 §5：插件未声明时按 WRITE，需要确认但非 critical）。
	ActionRisk string `json:"actionRisk"`
}

// businessBatchExecuteRequest 执行请求体（对应 BatchExecuteRequest schema）。
type businessBatchExecuteRequest struct {
	ConfirmToken string          `json:"confirmToken"`
	Domain       string          `json:"domain"`
	Action       string          `json:"action"`
	Payload      json.RawMessage `json:"payload"`
	RequestID    string          `json:"requestId"`
	Reason       string          `json:"reason"`
	// Target 契约里不存在（执行只认 token），但低风险批量可以不带 token 直接执行，
	// 此时必须能指定目标——因此保留为可选字段，带 token 时一律以 token 正文为准。
	Target *businessTargetIn `json:"target"`
	// ActionRisk 同预览，用于执行期复核风险（无 token 但实际是高风险时拒绝）。
	ActionRisk string `json:"actionRisk"`
}

// businessBatchResolvedView 预览响应中的 resolved 段。
type businessBatchResolvedView struct {
	InstanceCount int    `json:"instanceCount"`
	Online        int    `json:"online"`
	Offline       int    `json:"offline"`
	InstanceIDs   []uint `json:"instanceIds,omitempty"`
	Truncated     bool   `json:"truncated"`
}

// businessBatchPreviewResponse 预览响应体（对应 BatchPreviewResponse schema）。
type businessBatchPreviewResponse struct {
	Resolved businessBatchResolvedView      `json:"resolved"`
	Groups   []service.BusinessGroupRef     `json:"groups,omitempty"`
	Risk     service.BusinessRiskAssessment `json:"risk"`
	// ConfirmToken 仅在风险 high/critical 时签发；低风险为空（前端据此跳过确认步骤）。
	ConfirmToken string `json:"confirmToken,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

// Preview POST /business/batch/preview — 解析目标并评估风险，必要时签发确认令牌。
//
// 无副作用、可重复调用（§7.1）。
func (h *BusinessBatchHandler) Preview(c *gin.Context) {
	if h.batch == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "BATCH_UNAVAILABLE", "message": "业务批量未启用"})
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceOperate) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	var req businessBatchPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeBusinessBatchError(c, fmt.Errorf("%w: 请求体不是合法 JSON", service.ErrInvalidBusinessCommand))
		return
	}

	scope, ok := h.targetScope(c)
	if !ok {
		return
	}
	preview, err := h.batch.Preview(req.Target.toService(), req.ActionRisk, businessBatchIssuerKey(c), scope)
	if err != nil {
		writeBusinessBatchError(c, err)
		return
	}

	view := businessBatchResolvedView{}
	if resolved := preview.Resolved; resolved != nil {
		view.InstanceCount = resolved.Count()
		view.Online, view.Offline = resolved.OnlineOffline()
		view.InstanceIDs, view.Truncated = truncateInstanceIDs(resolved.InstanceIDs)
	}
	resp := businessBatchPreviewResponse{
		Resolved: view,
		Groups:   preview.Resolved.Groups,
		Risk:     preview.Risk,
	}
	if preview.ConfirmToken != "" {
		resp.ConfirmToken = preview.ConfirmToken
		resp.ExpiresAt = preview.ExpiresAt.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, resp)
}

// truncateInstanceIDs 按 §3.1 的口径截断预览里的实例 ID 列表：
// 超过 500 个只返回前 100 个并标记 truncated，避免响应体过大。
func truncateInstanceIDs(ids []uint) ([]uint, bool) {
	if len(ids) <= service.BusinessTargetPreviewMaxIDs() {
		return ids, false
	}
	head := service.BusinessTargetPreviewHeadIDs()
	if head > len(ids) {
		head = len(ids)
	}
	return ids[:head], true
}

// Execute POST /business/batch — 执行批量业务命令。
//
// 按 Accept 分流：
//   - text/event-stream → SSE 推送 progress / done 帧（FR-537）
//   - 其余              → 同步返回完整结果（HTTP 200，部分失败不整体失败）
//
// **token 校验在流开始前完成**：失败一律返回 JSON 错误（不是 SSE 帧），
// 因为确认是「执行前」的门槛，不该进入流式阶段才校验（FR-537 §4）。
func (h *BusinessBatchHandler) Execute(c *gin.Context) {
	if h.batch == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "BATCH_UNAVAILABLE", "message": "业务批量未启用"})
		return
	}
	access := getAccess(c)
	if access == nil || !access.HasPermission(service.PermInstanceBusinessWrite) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	var req businessBatchExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeBusinessBatchError(c, fmt.Errorf("%w: 请求体不是合法 JSON", service.ErrInvalidBusinessCommand))
		return
	}
	if strings.TrimSpace(req.RequestID) == "" {
		writeBusinessBatchError(c, fmt.Errorf("%w: requestId 必填", service.ErrInvalidBusinessCommand))
		return
	}

	scope, ok := h.targetScope(c)
	if !ok {
		return
	}

	svcReq := service.BusinessBatchRequest{
		ConfirmToken: req.ConfirmToken,
		Domain:       strings.TrimSpace(req.Domain),
		Action:       strings.TrimSpace(req.Action),
		Payload:      req.Payload,
		ActionRisk:   req.ActionRisk,
		RequestID:    strings.TrimSpace(req.RequestID),
		Reason:       strings.TrimSpace(req.Reason),
		IssuerKey:    businessBatchIssuerKey(c),
		Operator:     getUsername(c),
		OperatorID:   getUserID(c),
	}
	if req.Target != nil {
		svcReq.Target = req.Target.toService()
	}

	if wantsEventStream(c) {
		h.executeStream(c, svcReq, scope)
		return
	}
	h.executeSync(c, svcReq, scope)
}

// executeSync 同步执行并返回完整 JSON。
func (h *BusinessBatchHandler) executeSync(c *gin.Context, req service.BusinessBatchRequest, scope service.BusinessTargetScope) {
	resp, err := h.batch.Execute(c.Request.Context(), req, scope)
	if err != nil {
		writeBusinessBatchError(c, err)
		return
	}
	// X-Total-Count 是契约声明的响应头，便于调用方在不解包 body 时先拿到规模。
	c.Header("X-Total-Count", strconv.Itoa(resp.Total))
	h.recordAudit(c, req, resp)
	c.JSON(http.StatusOK, resp)
}

// executeStream 流式执行并推送 progress / done（FR-537）。
//
// 断开语义（§5）：客户端断开 **≠ 取消执行**。已下发的实例会继续完成，只是结果不再推送；
// 因此这里的执行使用与请求解耦的 context，写失败只结束推送、不打断执行。
//
// 【并发约束】执行结果只经 `finished` 这一个带缓冲 channel 交回本协程，
// 不通过共享变量传递——否则读结果与执行协程的写结果之间没有 happens-before 关系。
func (h *BusinessBatchHandler) executeStream(c *gin.Context, req service.BusinessBatchRequest, scope service.BusinessTargetScope) {
	// 有界缓冲 + 非阻塞投递：执行协程永远不该被一个慢客户端卡住。
	frames := make(chan service.BusinessBatchProgress, 64)

	// streamResult 是执行协程的最终产物，经 finished 单次交回。
	type streamResult struct {
		resp *service.BusinessBatchResponse
		err  error
	}
	finished := make(chan streamResult, 1)

	ctx := context.Background()
	go func() {
		resp, err := h.batch.ExecuteWithProgress(ctx, req, scope, func(p service.BusinessBatchProgress) {
			select {
			case frames <- p:
			default:
				// 客户端跟不上：丢弃这一帧进度。进度可丢（终态 done 帧必发），
				// 而丢弃绝不允许反压执行本身。
			}
		})
		close(frames)
		finished <- streamResult{resp: resp, err: err}
	}()

	// await 取执行终态。调用前必须已把 frames 排干（channel 已关闭），否则会阻塞。
	await := func() streamResult { return <-finished }

	// 先等到首个进度帧或执行结束——这样「token 校验失败 / 目标为空」这类前置错误
	// 仍然能以 JSON 错误返回（此时还没写过任何 SSE 帧）。
	select {
	case first, ok := <-frames:
		if !ok {
			// 无进度帧就结束了：要么瞬间完成，要么前置失败。按结论分流。
			res := await()
			if res.err != nil {
				writeBusinessBatchError(c, res.err)
				return
			}
			h.streamHeaders(c)
			h.writeSSE(c, "done", donePayload(res.resp))
			h.recordAudit(c, req, res.resp)
			return
		}
		h.streamHeaders(c)
		if !h.writeSSE(c, "progress", first) {
			// 客户端已断：继续让执行跑完（断开 ≠ 取消），只是不再推送。
			h.drainFrames(frames)
			h.recordAudit(c, req, await().resp)
			return
		}
	case <-time.After(businessBatchStreamFirstFrameTimeout):
		// 超时没等到任何帧（大批量首批尚未回执）：仍开帧，后续进度照常推送。
		h.streamHeaders(c)
	}

	for p := range frames {
		if !h.writeSSE(c, "progress", p) {
			// 写失败：不再推送，但等执行收尾以便审计（不取消执行）。
			h.drainFrames(frames)
			h.recordAudit(c, req, await().resp)
			return
		}
	}
	res := await()
	if res.err != nil {
		h.writeSSE(c, "error", errorPayload(res.err))
		return
	}
	h.writeSSE(c, "done", donePayload(res.resp))
	h.recordAudit(c, req, res.resp)
}

// drainFrames 排空并丢弃剩余进度帧，让执行协程能继续投递直到结束。
func (h *BusinessBatchHandler) drainFrames(frames <-chan service.BusinessBatchProgress) {
	for range frames {
	}
}

// businessBatchStreamFirstFrameTimeout 是「等首帧」的窗口。
//
// 太短会让正常但稍慢的批量提前开帧（无害），太长会让调用方盯着空白连接。取 3s：
// 小批量（几台）在此之内必定完成，大批量首批 20 个通常也在 1s 内返回。
const businessBatchStreamFirstFrameTimeout = 3 * time.Second

// donePayload 构造 done 帧正文：终态计数 + 完整逐实例结果。
func donePayload(resp *service.BusinessBatchResponse) map[string]any {
	if resp == nil {
		return map[string]any{"total": 0, "succeeded": 0, "failed": 0, "offline": 0, "results": []any{}}
	}
	return map[string]any{
		"requestId": resp.RequestID,
		"total":     resp.Total,
		"succeeded": resp.Succeeded,
		"failed":    resp.Failed,
		"offline":   resp.Offline,
		"timeout":   resp.Timeout,
		"skipped":   resp.Skipped,
		"results":   resp.Results,
	}
}

// errorPayload 构造 SSE error 帧正文（`{"code":...,"message":...}`）。
func errorPayload(err error) map[string]any {
	code, _ := service.BusinessBatchErrorCode(err)
	return map[string]any{"code": code, "message": err.Error()}
}

// streamHeaders 写出 SSE 响应头。**一旦调用，状态码就已确定为 200**，
// 因此所有需要非 200 的错误都必须在调用它之前返回。
func (h *BusinessBatchHandler) streamHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// 禁用代理缓冲是硬要求：Nginx 默认会缓冲 SSE，导致进度成批到达甚至超时断连。
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
}

// writeSSE 写一帧 `event: <name>\ndata: <json>\n\n` 并 flush，返回是否成功。
func (h *BusinessBatchHandler) writeSSE(c *gin.Context, event string, payload any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("序列化批量 SSE 帧失败", "event", event, "error", err)
		return false
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, raw); err != nil {
		return false
	}
	if err := http.NewResponseController(c.Writer).Flush(); err != nil {
		return false
	}
	return true
}

// wantsEventStream 判断调用方是否要求流式响应。
//
// 按 strings.Contains 而非全等：Accept 可能带 q 值或多类型（`text/event-stream, application/json`）。
func wantsEventStream(c *gin.Context) bool {
	return strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream")
}

// targetScope 解析当前调用方的可访问实例范围。
//
// 返回 (scope, ok)；ok=false 表示已经写过错误响应，调用方应立即返回。
func (h *BusinessBatchHandler) targetScope(c *gin.Context) (service.BusinessTargetScope, bool) {
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

// recordAudit 记录批量执行的 JM 侧审计。
//
// 审计边界（ADR-106）：只记通道事实（domain/action/requestId/reason/风险等级/各项计数），
// **payload 与 output 一律不入审计**——JM 不理解业务语义，也不承担业务数据存储责任。
func (h *BusinessBatchHandler) recordAudit(c *gin.Context, req service.BusinessBatchRequest, resp *service.BusinessBatchResponse) {
	if h.audit == nil || resp == nil {
		return
	}
	detail := map[string]any{
		"domain":    req.Domain,
		"action":    req.Action,
		"requestId": req.RequestID,
		"reason":    req.Reason,
		"risk":      req.ActionRisk,
		"total":     resp.Total,
		"succeeded": resp.Succeeded,
		"failed":    resp.Failed,
		"offline":   resp.Offline,
		"timeout":   resp.Timeout,
		"skipped":   resp.Skipped,
	}
	h.audit.RecordSafe(getUserID(c), "business.batch", "business", req.Domain+"."+req.Action, marshalAuditDetail(detail), c.ClientIP())
}

// businessBatchIssuerKey 构造确认令牌归属键与幂等作用域键。
//
// Agent Token 用 token ID，人类 JWT 用 user ID，前缀区分以避免两个 ID 空间重叠。
func businessBatchIssuerKey(c *gin.Context) string {
	if p := middleware.GetAgentPrincipal(c); p != nil {
		return "agent:" + strconv.FormatUint(uint64(p.TokenID), 10)
	}
	if uid := getUserID(c); uid > 0 {
		return "user:" + strconv.FormatUint(uint64(uid), 10)
	}
	return ""
}

// writeBusinessBatchError 统一输出批量相关错误（§8 错误码表）。
//
// 用 code 而非 error 字段表达机器可读错误码，与 business.yaml 引用的 ErrorBase 一致；
// 同时保留 message 供人阅读。
func writeBusinessBatchError(c *gin.Context, err error) {
	code, status := service.BusinessBatchErrorCode(err)
	if errors.Is(err, service.ErrBusinessTargetUnspecified) {
		code = "TARGET_EMPTY"
	}
	c.JSON(status, gin.H{"code": code, "message": err.Error(), "error": code})
}
