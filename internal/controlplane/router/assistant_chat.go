package router

// Phase D：AI 助手对话运行时 HTTP/SSE 层。
//
// 分层关系（阅读顺序）：
// assistant.go 是的**配置面**（供应商/模型/默认模型/出站准入），本文件不碰它；
//   - service/assistant_chat.go 是 Phase C 的**运行时本体**（会话/消息/run/事件的状态机），
//     本文件只调用它已导出的方法，不复制任何状态迁移逻辑；
//   - 本文件只做三件事：参数解析、错误 → HTTP 语义映射、事件流的传输形态。
//
// 三条写在这里的取舍：
//  1. 越权一律 404（不是 403）：run/conversation 的归属判定在 service 层收敛成
//     ErrAssistantChatNotFound。若在 transport 层改成 403，就等于给出一个
//     「这个 id 存在、只是不属于你」的探测接口（spec §6「RUN_NOT_FOUND（404，含越权）」）。
//  2. 事件流的**传输形态**由 §5.3/§6 冻结：`event: <type>` + `data: <json>`，每条事件一个 seq。
//     本文件不发明新事件类型，也不改写载荷内容，只把库里的 data 原样转发。
//  3. 服务端**目前没有订阅机制**（Phase C 的 sink 只写不广播）。因此 SSE 采用「短周期轮询
//     事件表」而不是伪造一个订阅接口——见 StreamRun 的说明。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 助手对话运行时的分页默认值与上限（与 service 层同口径，仅在 handler 侧做入参归一化）。
const (
	assistantChatDefaultLimit = 20
	assistantChatMaxLimit     = 100
)

// SSE 轮询与心跳间隔（Phase D 的传输参数，与业务预算无关）。
const (
	// assistantStreamPollInterval 事件表轮询间隔（**兜底**，不是主路径）。
	//
	// 即时性由 hub 订阅提供（事件落库后即刻推给订阅者），轮询只负责补齐订阅拿不到的部分：
	// 订阅建立前已产生的事件、因订阅者过慢被丢弃的帧、以及进程重启后的空订阅。
	// 因此这里可以放宽到 3 秒——它不再是「首字延迟」的决定因素，再密一档只是把事件表读成热表。
	assistantStreamPollInterval = 3 * time.Second
	// assistantStreamPingInterval 无新事件时的注释帧间隔。
	//
	// 15 秒是常见反向代理的 idle 超时（nginx proxy_read_timeout 默认 60s、
	// 多数云 LB 为 60s 或更短）留出的安全余量：心跳必须显著小于最短的那一个，
	// 否则连接会在「其实还活着」的时候被中间设备单方面掐断。
	assistantStreamPingInterval = 15 * time.Second
)

// AssistantChatHandler 对话运行时路由处理器（Phase D）。
type AssistantChatHandler struct {
	svc *service.AssistantChatService
}

// NewAssistantChatHandler 构造处理器。
//
// svc 为 nil 时整组端点不注册（见 router.go 的挂载条件）：运行时未装配时暴露一堆
// 必然 500 的端点，只会让前端把「没装」误判成「装了但坏了」。
func NewAssistantChatHandler(svc *service.AssistantChatService) *AssistantChatHandler {
	return &AssistantChatHandler{svc: svc}
}

// RegisterRoutes 注册全部端点（路径严格按 spec §6）。
//
// 由调用方传入已挂载登录鉴权的 `/assistant` 组：本文件的端点是**所有登录用户**的私有数据面，
// 权限判据是 session 归属（owner_user_id），不是权限节点——组管理员也不是他人会话的所有者
// （spec §3 冻结项），因此这里不挂 permRead。
func (h *AssistantChatHandler) RegisterRoutes(r *gin.RouterGroup) {
	conv := r.Group("/conversations")
	// 路径参数统一用 `:id`：gin 不允许同一位置出现两种参数名，混用会在注册期 panic。
	conv.GET("", h.ListConversations)
	conv.POST("", h.CreateConversation)
	conv.GET("/:id", h.GetConversation)
	conv.PATCH("/:id", h.UpdateConversation)
	conv.DELETE("/:id", h.DeleteConversation)
	conv.GET("/:id/messages", h.ListMessages)
	// 历史工具回显（补缺）：按 messageId 聚合当时的工具调用与结果。
	conv.GET("/:id/tool-calls", h.ListMessageToolCalls)
	conv.POST("/:id/runs", h.SubmitRun)

	runs := r.Group("/runs")
	runs.GET("/:runId/events", h.ListRunEvents)
	runs.GET("/:runId/stream", h.StreamRun)
	runs.POST("/:runId/cancel", h.CancelRun)
	// §7：该 run 的待确认列表（仅所有者）。
	runs.GET("/:runId/confirmations", h.ListConfirmations)

	// §7：逐调用确认。**只接受人类 JWT**——确认端点不能由模型/Agent 调用，
	// 因此它挂在本组（需登录、且判据是 session 归属），而不是任何 Token 鉴权组。
	confirmations := r.Group("/confirmations")
	confirmations.GET("/:id", h.GetConfirmation)
	confirmations.POST("/:id/decide", h.DecideConfirmation)

	// 技能管理：只读列举 + 手动重新扫描。
	//
	// **必须仅平台管理员**：技能内容会拼进系统提示词，能改技能的人就能影响
	// 模型被指示做什么。这里复用 assistant 的管理员门禁（与517 同一口径）。
	// 历史编辑与回退：属会话内容操作，判据是会话归属（不是权限节点）。
	r.PATCH("/messages/:id", h.EditMessage)
	r.POST("/conversations/:id/rollback", h.RollbackConversation)

	admin := r.Group("/admin/skills")
	admin.Use(requireAssistantAdmin())
	admin.GET("", h.ListSkills)
	admin.POST("/reload", h.ReloadSkills)
}

// skillView 技能投影（不含正文：列表页只需要知道有什么，正文在详情里看）。
type skillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	WhenToUse   string `json:"whenToUse,omitempty"`
	Version     string `json:"version,omitempty"`
	Source      string `json:"source,omitempty"`
	// Instructions 正文一并返回：管理页要能预览「模型会被告知什么」，
	// 否则管理员只能靠信任文件名来启停技能。
	Instructions string `json:"instructions"`
	Path         string `json:"path"`
}

// ListSkills GET /assistant/admin/skills
//
// 同时返回 skipped：装载时被跳过的目录与原因。**这是可解释性的落点**——
// 只回「有哪些技能」，管理员改坏一个 SKILL.md 时只会看到它凭空消失。
func (h *AssistantChatHandler) ListSkills(c *gin.Context) {
	skills, skipped := h.svc.SkillsSnapshot()
	out := make([]skillView, 0, len(skills))
	for _, s := range skills {
		out = append(out, skillView{
			Name: s.Name, Description: s.Description, WhenToUse: s.WhenToUse,
			Version: s.Version, Source: s.Source, Instructions: s.Instructions, Path: s.Path,
		})
	}
	c.JSON(http.StatusOK, gin.H{"skills": out, "skipped": skipped})
}

// ReloadSkills POST /assistant/admin/skills/reload
//
// 供管理员放好技能文件后重新扫描，不必重启 CP。
func (h *AssistantChatHandler) ReloadSkills(c *gin.Context) {
	res := h.svc.ReloadSkills()
	c.JSON(http.StatusOK, gin.H{"loaded": len(res.Skills), "skipped": res.Skipped})
}

// ListConfirmations GET /assistant/runs/:runId/confirmations
func (h *AssistantChatHandler) ListConfirmations(c *gin.Context) {
	owner, ok := h.requireOwner(c)
	if !ok {
		return
	}
	runID, ok := parseAssistantUintParam(c, "runId")
	if !ok {
		return
	}
	items, err := h.svc.ListPendingConfirmations(owner, runID)
	if err != nil {
		writeAssistantChatError(c, err, "RUN_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// GetConfirmation GET /assistant/confirmations/:id
func (h *AssistantChatHandler) GetConfirmation(c *gin.Context) {
	owner, ok := h.requireOwner(c)
	if !ok {
		return
	}
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	view, err := h.svc.GetConfirmation(owner, id)
	if err != nil {
		writeAssistantChatError(c, err, "CONFIRMATION_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, view)
}

// decideConfirmationRequest 决策请求体。
//
// 刻意只有 decision 一个字段：spec §3 冻结「只接受 approve / reject，
// 不接受新参数或新目标」——加字段就是给「确认即改参」开口子。
type decideConfirmationRequest struct {
	Decision string `json:"decision" binding:"required"`
}

// DecideConfirmation POST /assistant/confirmations/:id/decide
func (h *AssistantChatHandler) DecideConfirmation(c *gin.Context) {
	owner, ok := h.requireOwner(c)
	if !ok {
		return
	}
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	var req decideConfirmationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "decision 必填"})
		return
	}
	ticket, err := h.svc.DecideConfirmation(c.Request.Context(), owner, id, req.Decision)
	if err != nil {
		writeConfirmError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": ticket.ID, "status": ticket.Status})
}

// writeConfirmError 确认票据的错误映射（§7）。
//
// 与对话错误分开：票据有自己的 410 / 409 语义（过期 / 已消费 / 已失效），
// 混进通用映射会让客户端分不清「该重新出票」还是「该重新提交」。
func writeConfirmError(c *gin.Context, err error) {
	// 判序：先判「已消费、已尝试执行但领域失败」。它同时会命中下面的 ErrAssistantInvalid
	// 等分支（领域错误可能被包装成那些），所以必须放在最前，否则会被误映射成 400/409
	// ——那会让客户端以为「参数错了，改一下再确认」，而票据已经被消费，重试只会得到 409。
	var execErr *service.AssistantToolExecError
	if errors.As(err, &execErr) {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":   "TOOL_EXEC_FAILED",
			"message": execErr.Error(),
			// 明确告诉调用方不要原样重试：票据已消费，动作可能已部分生效。
			"retryable": false,
		})
		return
	}
	switch {
	case errors.Is(err, service.ErrAssistantConfirmExpired):
		c.JSON(http.StatusGone, gin.H{"error": "CONFIRMATION_EXPIRED", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantConfirmConsumed):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFIRMATION_CONSUMED", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantConfirmStale):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFIRMATION_STALE", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantForbidden):
		// 非所有者或非人类身份。
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantChatNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CONFIRMATION_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		writeAssistantChatError(c, err, "CONFIRMATION_NOT_FOUND")
	}
}

// requireOwner 取当前登录用户 id。
//
// 复用既有 `currentUserID(c)`（内部从 UserAccess 取，所有登录用户端点都用它），
// 而不是自己读某个上下文键：键名散落在中间件里，硬编码字符串一旦对不上，
// 这里会静默变成「所有确认都 401」——那是最难查的一类 bug。
//
// 确认端点只走人类 JWT：本路由组要求登录（同会话端点），
// Agent Token 走的是 /api/v1/mcp，不在本组内。
func (h *AssistantChatHandler) requireOwner(c *gin.Context) (uint, bool) {
	id := currentUserID(c)
	if id == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "未授权"})
		return 0, false
	}
	return id, true
}

// parseAssistantUintParam 解析路径上的正整数参数。
func parseAssistantUintParam(c *gin.Context, name string) (uint, bool) {
	n, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "参数不合法"})
		return 0, false
	}
	return uint(n), true
}

// ── 错误映射 ────────────────────────────────────────────────────────────

// writeAssistantChatError 把运行时错误映射成 HTTP 语义（spec §6 统一错误码）。
//
// notFoundCode 由调用方给出（`CONVERSATION_NOT_FOUND` / `RUN_NOT_FOUND`）：两种对象的
// **状态码相同**（都是 404），只有错误码字符串不同，便于前端区分「我这条链接坏了」
// 与「这一轮已经不存在」。语义本身不区分「不存在」与「无权限」，理由见文件头注释。
func writeAssistantChatError(c *gin.Context, err error, notFoundCode string) {
	// 判序：先判带数据的 busy 错误。AssistantConversationBusyError 同时满足
	// errors.Is(err, ErrAssistantConversationBusy)，所以必须先 As 再 Is，
	// 否则会丢掉活跃 runId——而前端正是靠它接续观察那一轮（spec §5.2 末段）。
	var busy *service.AssistantConversationBusyError
	if errors.As(err, &busy) {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "CONVERSATION_BUSY",
			"message": "该会话已有进行中的运行",
			// 带上当前活跃 runId：让前端能直接转去观察那一轮，
			// 而不是只知道"忙"却不知道在忙什么。
			"runId": busy.RunID,
		})
		return
	}
	switch {
	case errors.Is(err, service.ErrAssistantChatNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": notFoundCode, "message": err.Error()})
	case errors.Is(err, service.ErrAssistantIdempotencyConflict):
		// 同一个 Idempotency-Key 被用于内容不同的请求：这是客户端的用法错误，
		// 但对客户端来说**不可自愈**（它必须换一个键或换一份内容），故不并进 400。
		c.JSON(http.StatusConflict, gin.H{"error": "IDEMPOTENCY_CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantChatConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantTooManyRuns):
		// 402 与 429 都在 spec 允许范围（§7）；取 429 是因为它准确表达了
		// 「你现在别再来，等一会儿再来」的重试语义，而 402 会让通用 HTTP 客户端
		// 误以为是计费问题并弹出付费引导。
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "BUDGET_EXCEEDED", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInvalidCursor):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CURSOR", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantContextTooLarge):
		// §6：整个上下文超 4 KiB 属「请求体异常」，必须拒收而不是裁剪后继续——
		// 继续裁剪会让用户以为上下文完整送达。
		c.JSON(http.StatusBadRequest, gin.H{"error": "CONTEXT_TOO_LARGE", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInvalid):
		// 输入本身不合法（正文为空/标题超长等）。不并进 500：这是调用方的错。
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantMessageNotEditable):
		// 编辑越界（非 user 消息 / 已被回退取代）：这是**有意的拒绝**，不是系统故障。
		// 落到 default 会变成 500，让前端把「不允许改这条」误显示成「服务坏了」。
		c.JSON(http.StatusBadRequest, gin.H{"error": "MESSAGE_NOT_EDITABLE", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantRollbackBlocked):
		// 有进行中的 run：语义同 CONVERSATION_BUSY，409 让前端提示「先等这一轮结束」。
		c.JSON(http.StatusConflict, gin.H{"error": "CONVERSATION_BUSY", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantModelUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MODEL_UNAVAILABLE", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// ── 会话 ────────────────────────────────────────────────────────────────

// ListConversations GET /assistant/conversations?cursor=&limit=
func (h *AssistantChatHandler) ListConversations(c *gin.Context) {
	limit, ok := parseAssistantLimit(c)
	if !ok {
		return
	}
	// `q` 为服务端检索（§5.1）：命中标题或消息正文，不在前端过滤全量。
	items, next, err := h.svc.ListConversationsFiltered(currentUserID(c),
		service.AssistantConversationFilter{Keyword: c.Query("q")}, c.Query("cursor"), limit)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	// items 为 nil 时显式回 []：JSON 里 null 与 [] 对前端是两种分支，
	// 「没有会话」不该逼前端写 `for (x of items || [])`。
	if items == nil {
		items = []model.AssistantConversation{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "nextCursor": next})
}

// createConversationRequest 新建会话的请求体。两字段均可省略。
type createConversationRequest struct {
	// Title 省略时由 service 用「新会话」占位，首轮提交时再按用户消息截取。
	Title *string `json:"title"`
	// ModelID 省略表示跟随全局默认模型（而不是固化成创建时的默认值快照）。
	ModelID *uint `json:"modelId"`
}

// CreateConversation POST /assistant/conversations
func (h *AssistantChatHandler) CreateConversation(c *gin.Context) {
	var req createConversationRequest
	// 允许空 body：前端可以先建一个空会话再让用户选模型。
	_ = c.ShouldBindJSON(&req)
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	conv, err := h.svc.CreateConversation(currentUserID(c), title, req.ModelID)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	c.JSON(http.StatusCreated, conv)
}

// GetConversation GET /assistant/conversations/:id
//
// 会话详情连同**最近若干条消息**一起返回（spec §6）：一次来回即可渲染首屏，
// 不必让前端在两个请求之间处理「会话到了但消息还没到」的中间态。
func (h *AssistantChatHandler) GetConversation(c *gin.Context) {
	id, ok := h.parseConversationID(c)
	if !ok {
		return
	}
	owner := currentUserID(c)
	conv, err := h.svc.GetConversation(owner, id)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	limit, ok := parseAssistantLimit(c)
	if !ok {
		return
	}
	msgs, err := h.svc.ListMessages(owner, id, nil, limit)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	if msgs == nil {
		msgs = []model.AssistantMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"conversation": conv, "messages": msgs})
}

// updateConversationRequest 改标题 / 换模型的请求体。
//
// modelId 用 json.RawMessage 保留三态：缺省（不改）、null（显式清空、回跟随默认模型）、
// 数字（改成该模型）。用 *uint 会把「null」与「缺省」压成同一个 nil，
// 于是「清空模型」这个操作在下发端根本表达不出来。
type updateConversationRequest struct {
	Title   *string         `json:"title"`
	ModelID json.RawMessage `json:"modelId"`
}

// UpdateConversation PATCH /assistant/conversations/:id
func (h *AssistantChatHandler) UpdateConversation(c *gin.Context) {
	id, ok := h.parseConversationID(c)
	if !ok {
		return
	}
	var req updateConversationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	var modelID **uint
	if len(req.ModelID) > 0 {
		var inner *uint
		if err := json.Unmarshal(req.ModelID, &inner); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "modelId 不合法"})
			return
		}
		modelID = &inner
	}
	conv, err := h.svc.UpdateConversation(currentUserID(c), id, req.Title, modelID)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, conv)
}

// DeleteConversation DELETE /assistant/conversations/:id
//
// 软删除（§4.1）：消息与事件随之不可读但不物理删除，属操作追责材料。
// 已有活跃 run 时由 service 回 CONVERSATION_BUSY（带 runId），语义是「先取消再删」。
func (h *AssistantChatHandler) DeleteConversation(c *gin.Context) {
	id, ok := h.parseConversationID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteConversation(currentUserID(c), id); err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ListMessages GET /assistant/conversations/:id/messages?before=&limit=
func (h *AssistantChatHandler) ListMessages(c *gin.Context) {
	id, ok := h.parseConversationID(c)
	if !ok {
		return
	}
	limit, ok := parseAssistantLimit(c)
	if !ok {
		return
	}
	var before *uint
	if raw := c.Query("before"); raw != "" {
		// before 是「比这条 id 更早」的游标，非法即 400 INVALID_CURSOR（§6.2 同口径）。
		// 不降级成「忽略该参数」：那样前端会把重复的一页当成新的一页，翻页永远不动。
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CURSOR", "message": "before 必须是正整数"})
			return
		}
		u := uint(v)
		before = &u
	}
	msgs, err := h.svc.ListMessages(currentUserID(c), id, before, limit)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	if msgs == nil {
		msgs = []model.AssistantMessage{}
	}
	c.JSON(http.StatusOK, gin.H{"items": msgs})
}

// ── 提交与取消 run ──────────────────────────────────────────────────────

// submitRunRequest 提交一轮的请求体。
type submitRunRequest struct {
	// Text 本轮用户消息正文（必填，长度上限由 runtime 判定）。
	Text string `json:"text"`
	// ContextSnapshot 页面上下文快照（写入）：本层只原样透传，不解析。
	ContextSnapshot *string `json:"contextSnapshot"`
}

// SubmitRun POST /assistant/conversations/:id/runs
//
// **202 Accepted** 而不是 201：这里返回的不是「一个已完成的资源」，而是「一个已被受理、
// 正在后台推进的运行」。用 201 会让客户端以为可以立刻读到结果，而实际上结果要靠
// /runs/:runId/stream 或 /events 观察。
//
// Idempotency-Key 走请求头（不是 body 字段）：它是传输层的重试语义，与业务内容无关，
// 放 body 会让「同一份内容 + 不同键」与「同键 + 不同内容」两种情形在代码里长得一样。
func (h *AssistantChatHandler) SubmitRun(c *gin.Context) {
	id, ok := h.parseConversationID(c)
	if !ok {
		return
	}
	var req submitRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	run, err := h.svc.SubmitRun(c.Request.Context(), currentUserID(c), id, service.SubmitRunBody{
		Text:            req.Text,
		ContextSnapshot: req.ContextSnapshot,
	}, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"runId":          run.ID,
		"conversationId": run.ConversationID,
		"status":         run.Status,
	})
}

// CancelRun POST /runs/:runId/cancel
//
// 幂等：已终态再取消由 service 回 nil（「已经停住了」就是达成了目的）。
func (h *AssistantChatHandler) CancelRun(c *gin.Context) {
	runID, ok := h.parseRunID(c)
	if !ok {
		return
	}
	if err := h.svc.Cancel(currentUserID(c), runID); err != nil {
		writeAssistantChatError(c, err, "RUN_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, gin.H{"cancelled": true, "runId": runID})
}

// ── 事件补读与 SSE ──────────────────────────────────────────────────────

// assistantEventFrame 事件对外的传输形态。
//
// 为什么不直接把 model.AssistantEvent 序列化出去：它的 Data 是 string（库内是 TEXT）。
// 那样前端拿到的 `data` 是一段"编码在字符串里的 JSON"，每次消费都要二次 parse。
// 这里把它还原成真正的 JSON 值（RawMessage），HTTP 与 SSE 两条路径共用同一个形态——
// 前端补读与实时流可以用同一个 reducer。
type assistantEventFrame struct {
	Seq       int             `json:"seq"`
	Type      string          `json:"type"`
	MessageID *uint           `json:"messageId,omitempty"`
	CallID    *string         `json:"callId,omitempty"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
}

// toAssistantEventFrame 把库内事件转成传输帧。
func toAssistantEventFrame(ev model.AssistantEvent) assistantEventFrame {
	payload := json.RawMessage(ev.Data)
	if !json.Valid(payload) {
		// 库内 data 理论上一定是合法 JSON（写入侧 marshalBoundedEventPayload 保证）。
		// 万一不是，也**不能让整条流断掉**：退化成把原文当字符串，让前端至少能看到内容，
		// 而不是收到一个 500 或一条被吞掉的事件。
		quoted, _ := json.Marshal(ev.Data)
		payload = json.RawMessage("{\"raw\":" + string(quoted) + "}")
	}
	return assistantEventFrame{
		Seq:       ev.Seq,
		Type:      ev.Type,
		MessageID: ev.MessageID,
		CallID:    ev.CallID,
		Data:      payload,
		CreatedAt: ev.CreatedAt,
	}
}

// ListRunEvents GET /runs/:runId/events?after=<seq>
//
// 补读语义（§6.2）：返回该 run 中 seq > after 的事件，**JSON 数组**。
// after 大于该 run 的 maxSeq 时由 service 回 ErrAssistantInvalidCursor（400），不是 410：
// 事件永不因清理而消失，游标失效只可能是伪造或跨 run 误用。
func (h *AssistantChatHandler) ListRunEvents(c *gin.Context) {
	runID, ok := h.parseRunID(c)
	if !ok {
		return
	}
	after, ok := parseAssistantAfter(c)
	if !ok {
		return
	}
	events, err := h.svc.ListRunEvents(currentUserID(c), runID, after)
	if err != nil {
		writeAssistantChatError(c, err, "RUN_NOT_FOUND")
		return
	}
	frames := make([]assistantEventFrame, 0, len(events))
	for _, ev := range events {
		frames = append(frames, toAssistantEventFrame(ev))
	}
	// 返回裸数组（spec §6「返回 JSON 数组」）。frames 已用 make 初始化，
	// 空结果序列化成 [] 而不是 null。
	c.JSON(http.StatusOK, frames)
}

// StreamRun GET /runs/:runId/stream?after=<seq>
//
// SSE 观察流（fetch + Bearer，spec §6 / shared-contracts §8：不用 EventSource，
// 不把 JWT 放进 URL）。
//
// **服务端目前没有事件订阅/广播机制**：Phase C 的 assistantEventSink 只写库。
// 因此这里采取「短周期轮询事件表」的**临时实现**（间隔见 assistantStreamPollInterval）：
//   - 不伪造一个看似实时的订阅接口——假订阅会让上层误以为有推送语义，
//     等真正实现订阅时反而要先拆掉这层伪装；
//   - 轮询的读路径与 /events 补读**完全同一条**，所以「先补发历史再跟新」天然一致，
//     不存在两套 seq 判定的漂移风险；
//   - 待运行时提供订阅后，只需替换 flushOnce 的取数来源，帧格式与断线补读语义不变。
//
// 结束条件（三者之一）：客户端断开、读到 run_end（每 run 恰好一条终态事件）、
// 或 run 已终态且事件已补齐。
func (h *AssistantChatHandler) StreamRun(c *gin.Context) {
	runID, ok := h.parseRunID(c)
	if !ok {
		return
	}
	after, ok := parseAssistantAfter(c)
	if !ok {
		return
	}
	owner := currentUserID(c)

	// 先做一次读再写 SSE 头：一旦写了 text/event-stream，状态码就已经发出去了，
	// 那时「不存在」只能塞进一条 error 事件，前端要维护两套错误模型。
	// 提前试读让越权/非法游标仍然是一个干净的 404 / 400 JSON。
	if _, err := h.svc.ListRunEvents(owner, runID, after); err != nil {
		writeAssistantChatError(c, err, "RUN_NOT_FOUND")
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	// Connection 在 HTTP/2 下是禁用头，但这里保持与既有 SSE 端点一致：
	// 反向代理与浏览器在 HTTP/1.1 下依赖它（对齐 bot_stress_session.go 的范式）。
	c.Header("Connection", "keep-alive")
	// X-Accel-Buffering: no 关掉 nginx 的响应缓冲。不关的话事件会被代理攒着发，
	// 表现为「流式但成批到达」——SSE 的全部意义就没了。
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	writeSSE := func(event string, payload any) bool {
		raw, mErr := json.Marshal(payload)
		if mErr != nil {
			return false
		}
		if _, wErr := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, raw); wErr != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}

	lastSeq := after
	// lastPingSeq 记录上次心跳时已补发到哪一条：只有「确实没有新事件」才发注释帧，
	// 有数据在流的连接不需要额外心跳（数据本身就是活性证明）。
	lastPingSeq := lastSeq

	// 先写一条注释帧再进循环：run 刚启动时往往还没有任何事件，如果此时不写字节，
	// 响应头可能一直留在缓冲区里，浏览器与中间代理会认为"连接尚未建立"（部分代理
	// 还会因此先建一个空响应）。注释帧被 SSE 客户端规范忽略，不构成任何事件语义。
	if _, err := fmt.Fprint(c.Writer, ": stream-open\n\n"); err != nil {
		return
	}
	c.Writer.Flush()

	// flushOnce 补发 seq > lastSeq 的事件；返回 true 表示这条流应当结束。
	flushOnce := func() bool {
		events, err := h.svc.ListRunEvents(owner, runID, lastSeq)
		if err != nil {
			// 流已经开始，无法再改状态码；用一条 error 事件把「读失败」告诉前端，
			// 让它可以走「按 seq 重连补读」的既有路径，而不是无限等一个永不到来的 run_end。
			_ = writeSSE("error", gin.H{"code": "INTERNAL_ERROR", "message": "读取运行事件失败"})
			return true
		}
		for _, ev := range events {
			if !writeSSE(ev.Type, toAssistantEventFrame(ev)) {
				return true
			}
			lastSeq = ev.Seq
			// run_end 是每 run 恰好一条的终态事件（§5.3）：读到它，这条流就完成了它的使命。
			if ev.Type == model.AssistantEventTypeRunEnd {
				return true
			}
		}
		if len(events) > 0 {
			return false
		}
		// 本次没有新事件：若 run 已终态且事件已补齐（seq 连续无空洞，§5.3），
		// 说明这条流已经没有后续内容，直接结束而不是把客户端挂在这里。
		run, err := h.svc.GetRun(owner, runID)
		if err != nil {
			return true
		}
		return isTerminalAssistantRunStatus(run.Status)
	}

	ctx := c.Request.Context()
	// 订阅即时事件：hub 在事件**落库之后**广播，因此收到的帧一定能被补读到。
	// 订阅是尽力而为的（慢订阅者会被丢弃），故仍保留轮询作为兜底：
	//   - 订阅建立前已产生的事件
	//   - 因缓冲满被丢弃的那几条
	//   - 进程重启后 hub 里没有任何订阅者
	// 两者是「快」与「全」的分工，不能互相替代。
	live, unsubscribe := h.svc.SubscribeRunEvents(runID)
	defer unsubscribe()

	poll := time.NewTicker(assistantStreamPollInterval)
	defer poll.Stop()
	ping := time.NewTicker(assistantStreamPingInterval)
	defer ping.Stop()

	for {
		if flushOnce() {
			return
		}
		select {
		case <-ctx.Done():
			// 客户端断开：不写任何东西（连接已不可写），直接返回让 gin 收尾。
			return
		case ev, ok := <-live:
			if !ok {
				// 订阅通道被关闭（unsubscribe 由本函数 defer 触发时不会走到这里；
				// 走到这里说明 hub 侧已清理），退化为纯轮询，不结束这条流。
				live = nil
				continue
			}
			// 跳过已发过的 seq：广播与轮询可能拿到同一条。
			if ev.Seq <= lastSeq {
				continue
			}
			// 跳号说明中间有事件被丢弃：不直接发这条，先让 flushOnce 从库里补齐，
			// 否则前端会看到一个永久空洞（seq 连续性是其补读判据，§5.3）。
			if ev.Seq > lastSeq+1 {
				continue
			}
			if !writeSSE(ev.Type, toAssistantEventFrame(ev)) {
				return
			}
			lastSeq = ev.Seq
			if ev.Type == model.AssistantEventTypeRunEnd {
				return
			}
		case <-poll.C:
		case <-ping.C:
			if lastSeq == lastPingSeq {
				if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
					return
				}
				c.Writer.Flush()
			}
			lastPingSeq = lastSeq
		}
	}
}

// ── 解析 helper ────────────────────────────────────────────────────────

// parseConversationID 解析会话 id；失败时已写响应。
func (h *AssistantChatHandler) parseConversationID(c *gin.Context) (uint, bool) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		return 0, false
	}
	return id, true
}

// parseRunID 解析 runId；失败时已写响应。
func (h *AssistantChatHandler) parseRunID(c *gin.Context) (uint, bool) {
	id, err := parseIDParam(c, "runId")
	if err != nil {
		return 0, false
	}
	return id, true
}

// parseAssistantLimit 解析 limit 入参；非法时回 400 并由调用方直接 return。
//
// limit 非法按 INVALID_REQUEST 而不是 INVALID_CURSOR：§6.2 的 INVALID_CURSOR 专指
// **游标**不合法（cursor/before/after），limit 是页大小，混进同一个码会让前端
// 误以为自己该重置游标。
func parseAssistantLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return assistantChatDefaultLimit, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "limit 必须是正整数"})
		return 0, false
	}
	if v > assistantChatMaxLimit {
		v = assistantChatMaxLimit
	}
	return v, true
}

// parseAssistantAfter 解析 ?after=<seq>；非法回 400 INVALID_CURSOR（§6.2）。
func parseAssistantAfter(c *gin.Context) (int, bool) {
	raw := c.Query("after")
	if raw == "" {
		// 缺省 = 从头补发。SSE 与 /events 共用这一默认值，避免两条路径起手位置不同。
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CURSOR", "message": "after 必须是非负整数"})
		return 0, false
	}
	return v, true
}

// isTerminalAssistantRunStatus 判断 run 是否已到终态（§5.1）。
//
// 复用 model 的状态常量而不是自己拼字符串：终态集合是状态机的一部分，
// transport 层若各写一份字面量，状态机新增终态时这里会静默漏判（表现为流不结束）。
func isTerminalAssistantRunStatus(status string) bool {
	switch status {
	case model.AssistantRunStatusCompleted, model.AssistantRunStatusFailed,
		model.AssistantRunStatusCancelled, model.AssistantRunStatusTimedOut,
		model.AssistantRunStatusInterrupted:
		return true
	}
	return false
}

// editMessageRequest 编辑用户消息（§6.2）。
type editMessageRequest struct {
	Content string `json:"content"`
}

// EditMessage PATCH /assistant/messages/:id
//
// 只允许改自己的 user 消息；**不自动重跑**——编辑是「改错别字」，不是「重新生成」。
func (h *AssistantChatHandler) EditMessage(c *gin.Context) {
	owner, ok := h.requireOwner(c)
	if !ok {
		return
	}
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	var req editMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	msg, err := h.svc.EditUserMessage(owner, id, req.Content)
	if err != nil {
		writeAssistantChatError(c, err, "MESSAGE_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, msg)
}

// assistantRollbackRequest 回退到某条消息（§6.3）。
type assistantRollbackRequest struct {
	MessageID uint `json:"messageId"`
}

// RollbackConversation POST /assistant/conversations/:id/rollback
//
// **只回退会话内容，不撤销已发生的副作用**。回滚到「启动实例」之前，
// 那个实例仍然是启动着的——前端必须把这句话显式写给用户看。
func (h *AssistantChatHandler) RollbackConversation(c *gin.Context) {
	owner, ok := h.requireOwner(c)
	if !ok {
		return
	}
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	var req assistantRollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.MessageID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "messageId 必填"})
		return
	}
	affected, err := h.svc.RollbackToMessage(owner, id, req.MessageID)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	c.JSON(http.StatusOK, gin.H{"superseded": affected})
}

// ListMessageToolCalls GET /assistant/conversations/:id/tool-calls
//
// 回「按 messageId 分组」的工具调用记录。历史页据此在每条回复下展示
// 「当时调了哪些工具、各自什么状态」——这些数据一直在事件流里，
// 此前只是没有读取面，导致回看历史看不到任何工具痕迹。
func (h *AssistantChatHandler) ListMessageToolCalls(c *gin.Context) {
	owner, ok := h.requireOwner(c)
	if !ok {
		return
	}
	id, ok := parseAssistantUintParam(c, "id")
	if !ok {
		return
	}
	grouped, err := h.svc.ListMessageToolCalls(owner, id)
	if err != nil {
		writeAssistantChatError(c, err, "CONVERSATION_NOT_FOUND")
		return
	}
	// 没有工具调用时回空对象而不是 null：前端据此跳过渲染，
	// 不必为「字段是 null 还是 {}」写两套判断。
	if grouped == nil {
		grouped = map[uint][]service.AssistantToolCallRecord{}
	}
	c.JSON(http.StatusOK, gin.H{"byMessage": grouped})
}
