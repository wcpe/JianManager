package router

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AssistantHandler AI 助手配置路由处理器（FR-514，见 ADR-105）。
//
// 分两层：普通用户只读可用模型摘要；管理子域做供应商/模型 CRUD。
// 管理面授权用显式 IsPlatformAdmin——既有 requireNodes helper 的实际语义是
// 「持有某个业务节点」，与「平台管理员」不是一回事，不能拿来当管理员门禁。
type AssistantHandler struct {
	svc *service.AssistantService
	// audit 允许为 nil（测试或未装配时）——审计缺失不该让配置面不可用。
	audit *service.AuditService
}

// NewAssistantHandler 构造处理器。
func NewAssistantHandler(svc *service.AssistantService, audit *service.AuditService) *AssistantHandler {
	return &AssistantHandler{svc: svc, audit: audit}
}

// auditProvider 记录供应商维度的审计。
//
// detail 按**白名单逐字段拼**，绝不序列化请求体再裁剪：后者在新增字段时
// （比如哪天给供应商加了个 header 配置）会静默把敏感值带进审计表，
// 而审计表是要被检索、导出、长期保留的。
func (h *AssistantHandler) auditProvider(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	h.audit.RecordResultSafe(currentUserID(c), action, "assistant_provider", targetID,
		marshalAuditDetail(detail), c.ClientIP(), true, "")
}

// auditModel 同上，模型维度。
func (h *AssistantHandler) auditModel(c *gin.Context, action, targetID string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	h.audit.RecordResultSafe(currentUserID(c), action, "assistant_model", targetID,
		marshalAuditDetail(detail), c.ClientIP(), true, "")
}

// requireAssistantAdmin AI 管理面的平台管理员门禁（FR-514，见 ADR-105）。
//
// **不复用 node.go 的 requirePlatformAdmin**：那个函数的名字与实际语义不符——
// 它要求的是 `node.manage` 权限节点，任何被授予该节点的自定义角色都能通过，
// 而 AI 供应商配置掌握着出站地址与凭据引用，必须只对真正的平台管理员开放。
// 同名函数已存在，故此处另起名字以避免覆盖，也避免后来者误用旧的。
func requireAssistantAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		access := getAccess(c)
		if access == nil || !access.IsPlatformAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "FORBIDDEN",
				"message": "需要平台管理员权限",
			})
			return
		}
		c.Next()
	}
}

// RegisterRoutes 注册全部路由。
func (h *AssistantHandler) RegisterRoutes(r *gin.RouterGroup) {
	// 所有登录用户可读：可用模型摘要（不含端点与凭据）。
	r.GET("/models", h.AvailableModels)

	admin := r.Group("/admin")
	admin.Use(requireAssistantAdmin())
	{
		admin.GET("/providers", h.ListProviders)
		admin.POST("/providers", h.CreateProvider)
		admin.GET("/providers/:id", h.GetProvider)
		admin.PUT("/providers/:id", h.UpdateProvider)
		admin.DELETE("/providers/:id", h.DeleteProvider)
		// 显式探测：管理员的按钮触发，绝不自动调用（模型调用按次计费）。
		admin.POST("/providers/:id/test", h.TestProvider)

		// 从上游拉取模型列表（FR-538 §2.2）：只读候选，不自动落库。
		admin.POST("/providers/:id/discover-models", h.DiscoverModels)

		admin.GET("/models", h.ListModels)
		admin.POST("/models", h.CreateModel)
		admin.PUT("/models/:id", h.UpdateModel)
		admin.DELETE("/models/:id", h.DeleteModel)
		admin.POST("/models/:id/test", h.TestModel)

		// 助手全局设置（默认模型 + 出站放行段）按**单例资源**整体读写，
		// 与 /api/v1/settings 同构；拆成两个动作端点会让同一张表出现两种写法。
		admin.GET("/settings", h.GetSettings)
		admin.PUT("/settings", h.UpdateSettings)
	}
}

// AvailableModels GET /assistant/models — 全部登录用户的可用模型摘要。
func (h *AssistantHandler) AvailableModels(c *gin.Context) {
	models, defaultID, err := h.svc.AvailableModels()
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": models, "defaultModelId": defaultID})
}

// ListProviders GET /assistant/admin/providers
func (h *AssistantHandler) ListProviders(c *gin.Context) {
	items, err := h.svc.ListProviders()
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// providerRequest 供应商创建/编辑请求体。
//
// credentialSecret 只入不出：响应里永远不会回它，也无法查回已保存的值。
type providerRequest struct {
	Name     string `json:"name" binding:"required"`
	Protocol string `json:"protocol" binding:"required"`
	BaseURL  string `json:"baseUrl" binding:"required"`
	Enabled  *bool  `json:"enabled"`
	// CredentialMode none / env / stored
	CredentialMode string `json:"credentialMode" binding:"required"`
	CredentialEnv  string `json:"credentialEnv"`
	// CredentialSecret 空表示保留原值（编辑态）或必填（创建态，由 service 判定）。
	CredentialSecret string `json:"credentialSecret"`
	// ClearCredential 显式清除已保存密文。
	ClearCredential bool `json:"clearCredential"`
	// ProxyMode 出站代理模式（FR-539）：direct / env / custom；空串表示不改动。
	ProxyMode string `json:"proxyMode"`
	// ProxySecret 自定义代理地址。含 userinfo 时整串落库且**永不回显**。
	ProxySecret string `json:"proxyUrl"`
	Version     int    `json:"version"`
}

// toProviderParams 转 service 参数。
func toProviderParams(req providerRequest) service.SaveProviderParams {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return service.SaveProviderParams{
		Name:             req.Name,
		Protocol:         req.Protocol,
		BaseURL:          req.BaseURL,
		Enabled:          enabled,
		CredentialMode:   req.CredentialMode,
		CredentialEnv:    req.CredentialEnv,
		CredentialSecret: req.CredentialSecret,
		ClearCredential:  req.ClearCredential,
		ProxyMode:        req.ProxyMode,
		ProxyURL:         req.ProxySecret,
		Version:          req.Version,
	}
}

// CreateProvider POST /assistant/admin/providers
func (h *AssistantHandler) CreateProvider(c *gin.Context) {
	var req providerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	created, err := h.svc.CreateProvider(toProviderParams(req))
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	h.auditProvider(c, "assistant.provider.create", itoaID(created.ID), map[string]any{
		"name": created.Name, "protocol": created.Protocol, "credentialMode": created.CredentialMode,
	})
	c.JSON(http.StatusCreated, created)
}

// GetProvider GET /assistant/admin/providers/:id
func (h *AssistantHandler) GetProvider(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	view, err := h.svc.GetProvider(id)
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// UpdateProvider PUT /assistant/admin/providers/:id
func (h *AssistantHandler) UpdateProvider(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	var req providerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	updated, err := h.svc.UpdateProvider(id, toProviderParams(req))
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	// 只记「凭据是否被动过」的布尔，不记任何值：审计表会被检索与长期保留。
	h.auditProvider(c, "assistant.provider.update", itoaID(updated.ID), map[string]any{
		"name": updated.Name, "protocol": updated.Protocol,
		"credentialMode":    updated.CredentialMode,
		"credentialChanged": strings.TrimSpace(req.CredentialSecret) != "" || req.ClearCredential,
	})
	c.JSON(http.StatusOK, updated)
}

// DeleteProvider DELETE /assistant/admin/providers/:id
func (h *AssistantHandler) DeleteProvider(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	// 删除前先取一次名字：删完就查不到了，而审计里「删了哪个」比「删了 id=7」有用得多。
	name := ""
	if existing, gerr := h.svc.GetProvider(id); gerr == nil {
		name = existing.Name
	}
	if err := h.svc.DeleteProvider(id); err != nil {
		respondAssistantErr(c, err)
		return
	}
	h.auditProvider(c, "assistant.provider.delete", itoaID(id), map[string]any{"name": name})
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListModels GET /assistant/admin/models
func (h *AssistantHandler) ListModels(c *gin.Context) {
	items, err := h.svc.ListModels()
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// modelRequest 模型创建/编辑请求体。
type modelRequest struct {
	ProviderID      uint   `json:"providerId" binding:"required"`
	ModelID         string `json:"modelId" binding:"required"`
	DisplayName     string `json:"displayName"`
	Streaming       *bool  `json:"streaming"`
	Tools           *bool  `json:"tools"`
	ContextWindow   *int   `json:"contextWindow"`
	MaxOutputTokens *int   `json:"maxOutputTokens"`
	Enabled         *bool  `json:"enabled"`
	Version         int    `json:"version"`
}

// toModelParams 转 service 参数。
func toModelParams(req modelRequest) service.SaveModelParams {
	streaming := true
	if req.Streaming != nil {
		streaming = *req.Streaming
	}
	tools := false
	if req.Tools != nil {
		tools = *req.Tools
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return service.SaveModelParams{
		ProviderID:      req.ProviderID,
		ModelID:         req.ModelID,
		DisplayName:     req.DisplayName,
		Streaming:       streaming,
		Tools:           tools,
		ContextWindow:   req.ContextWindow,
		MaxOutputTokens: req.MaxOutputTokens,
		Enabled:         enabled,
		Version:         req.Version,
	}
}

// CreateModel POST /assistant/admin/models
func (h *AssistantHandler) CreateModel(c *gin.Context) {
	var req modelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	created, err := h.svc.CreateModel(toModelParams(req))
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	h.auditModel(c, "assistant.model.create", itoaID(created.ID), map[string]any{
		"providerId": created.ProviderID, "modelId": created.ModelID, "enabled": created.Enabled,
	})
	c.JSON(http.StatusCreated, created)
}

// UpdateModel PUT /assistant/admin/models/:id
func (h *AssistantHandler) UpdateModel(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	var req modelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	updated, err := h.svc.UpdateModel(id, toModelParams(req))
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	h.auditModel(c, "assistant.model.update", itoaID(updated.ID), map[string]any{
		"providerId": updated.ProviderID, "modelId": updated.ModelID, "enabled": updated.Enabled,
	})
	c.JSON(http.StatusOK, updated)
}

// DeleteModel DELETE /assistant/admin/models/:id
func (h *AssistantHandler) DeleteModel(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	// 审计里只记 id：模型没有独立 GET 端点，为一个审计字段新增公开查询方法不值得。
	// 供应商那边有 GetProvider，故能记下名字。
	if err := h.svc.DeleteModel(id); err != nil {
		respondAssistantErr(c, err)
		return
	}
	h.auditModel(c, "assistant.model.delete", itoaID(id), map[string]any{"id": id})
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// TestProvider POST /assistant/admin/providers/:id/test — 显式探测供应商连通性。
//
// **上游失败仍回 200**：`ok:false` 是探测的**结论**，不是本次请求失败。
// 若把「密钥错了」映射成 401，前端会误以为自己的登录态失效并触发重新登录；
// 把「上游 5xx」映射成 502，则会让管理员以为是面板自己的服务出问题。
// 真正的请求级错误（供应商不存在、出站未初始化）仍走 respondAssistantErr。
func (h *AssistantHandler) TestProvider(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	res, err := h.svc.TestProvider(c.Request.Context(), id)
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	// 探测会产生一次真实的上游调用（可能计费），必须留痕；
	// 只记结论元数据，不记 message（它可能含上游回显的片段）。
	h.auditProvider(c, "assistant.provider.test", itoaID(id), map[string]any{
		"ok": res.OK, "classifiedError": res.ClassifiedError, "latencyMs": res.LatencyMs,
	})
	c.JSON(http.StatusOK, res)
}

// TestModel POST /assistant/admin/models/:id/test — 用指定模型跑一次最小补全。
//
// 请求体可选 `{"withTools":true}`：额外验证工具调用能否解析出完整参数（不执行任何工具）。
func (h *AssistantHandler) TestModel(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	// 请求体允许为空（前端只点按钮时不必构造 body），因此不把绑定错误当致命。
	var req modelTestRequest
	_ = c.ShouldBindJSON(&req)

	res, err := h.svc.TestModel(c.Request.Context(), id, req.WithTools)
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	// 同上：model.test 直连上游且可能计费，留痕但只记元数据。
	h.auditModel(c, "assistant.model.test", itoaID(id), map[string]any{
		"ok": res.OK, "classifiedError": res.ClassifiedError, "latencyMs": res.LatencyMs,
		"withTools": req.WithTools, "toolCallParsed": res.ToolCallParsed,
	})
	c.JSON(http.StatusOK, res)
}

// modelTestRequest 单模型测试的请求体。全字段可选。
type modelTestRequest struct {
	// WithTools 额外要求一次工具调用并验证参数完整性。
	WithTools bool `json:"withTools"`
}

// assistantSettingsRequest 助手全局设置请求体（**整体替换**语义）。
//
// 未提供的字段等于置空：不传 defaultModelId 即清空默认模型，allowPrivateCidrs 传空
// 即不放行任何私网段。这样「请求体即最终状态」，不必区分「省略」与「显式 null」。
type assistantSettingsRequest struct {
	DefaultModelID    *uint    `json:"defaultModelId"`
	AllowPrivateCIDRs []string `json:"allowPrivateCidrs"`
	// SystemPrompt 自定义系统提示词（FR-541 §3.1）。
	//
	// 用 **string 而非 *string**：本接口是整体替换语义（PUT 单例资源），
	// 省略即「清空」，与 allowPrivateCidrs 的空数组语义一致。
	SystemPrompt string `json:"systemPrompt"`
	// BuiltinToolsEnabled 平台自带工具开关（FR-540）。用指针以便「不传=不改」。
	BuiltinToolsEnabled *bool `json:"builtinToolsEnabled"`
	Version             int   `json:"version"`
}

// GetSettings GET /assistant/admin/settings
func (h *AssistantHandler) GetSettings(c *gin.Context) {
	view, err := h.svc.GetSettings()
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// UpdateSettings PUT /assistant/admin/settings
//
// 保存后 service 会在放行段变化时**立即重建出站工厂**：AllowPrivate 在构造 Guard 时
// 被编译进拨号器，不重建就等于「保存成功但行为不变」——那是最难自查的一类故障。
func (h *AssistantHandler) UpdateSettings(c *gin.Context) {
	var req assistantSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	if err := h.svc.SaveSettings(service.SaveSettingsParams{
		DefaultModelID:      req.DefaultModelID,
		AllowPrivateCIDRs:   req.AllowPrivateCIDRs,
		SystemPrompt:        req.SystemPrompt,
		BuiltinToolsEnabled: req.BuiltinToolsEnabled,
		Version:             req.Version,
	}); err != nil {
		respondAssistantErr(c, err)
		return
	}
	if h.audit != nil {
		// 一个端点承载两类设置，故用单一 action + 记录**两类值**：
		// 事后能回答「那一刻默认模型与放行段各是什么」，而不只是「有人改过设置」。
		// 清空必须与「没设过」区分开，故 null 显式记为 "null"。
		defaultModel := "null"
		if req.DefaultModelID != nil {
			defaultModel = itoaID(*req.DefaultModelID)
		}
		h.audit.RecordResultSafe(currentUserID(c), "assistant.settings.update", "assistant_settings", "1",
			marshalAuditDetail(map[string]any{
				"defaultModelId":    defaultModel,
				"allowPrivateCidrs": req.AllowPrivateCIDRs,
			}), c.ClientIP(), true, "")
	}
	view, err := h.svc.GetSettings()
	if err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// respondAssistantErr 把 service 错误映射成 HTTP 语义。
//
// 注意 409 与 422 的区分：前者是「和别人的写入撞了，重试或刷新后再来」，
// 后者是「你的输入本身不对」——把两者混成 400 会让前端无法给出正确引导。
func respondAssistantErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAssistantNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_CONFIG", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantEncryptorMissing):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ENCRYPTOR_UNAVAILABLE", "message": err.Error()})
	case errors.Is(err, service.ErrAssistantInUse):
		c.JSON(http.StatusConflict, gin.H{"error": "IN_USE", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
	}
}

// parseAssistantID 解析路径 id 参数；失败时已写响应。
func parseAssistantID(c *gin.Context) (uint, bool) {
	id, err := parseIDParam(c, "id")
	if err != nil {
		return 0, false
	}
	return id, true
}

// DiscoverModels POST /assistant/admin/providers/:id/discover-models
//
// 只回候选清单，不写库：`contextWindow` / `tools` 这类能力声明不能从模型名推断，
// 必须由管理员确认后走 POST /admin/models 落库。自动导入会把「能力未声明」的
// 模型塞进默认可选集，之后表现为「聊天报不支持工具」，而问题实际在配置阶段。
func (h *AssistantHandler) DiscoverModels(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "id 无效"})
		return
	}
	res, derr := h.svc.DiscoverModels(c.Request.Context(), uint(id))
	if derr != nil {
		respondAssistantErr(c, derr)
		return
	}
	c.JSON(http.StatusOK, res)
}
