package router

import (
	"errors"
	"net/http"

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
}

// NewAssistantHandler 构造处理器。
func NewAssistantHandler(svc *service.AssistantService) *AssistantHandler {
	return &AssistantHandler{svc: svc}
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

		admin.GET("/models", h.ListModels)
		admin.POST("/models", h.CreateModel)
		admin.PUT("/models/:id", h.UpdateModel)
		admin.DELETE("/models/:id", h.DeleteModel)
		admin.POST("/models/:id/test", h.TestModel)

		admin.PUT("/settings/default-model", h.SetDefaultModel)
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
	Version         int  `json:"version"`
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
	c.JSON(http.StatusOK, updated)
}

// DeleteProvider DELETE /assistant/admin/providers/:id
func (h *AssistantHandler) DeleteProvider(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteProvider(id); err != nil {
		respondAssistantErr(c, err)
		return
	}
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
	c.JSON(http.StatusOK, updated)
}

// DeleteModel DELETE /assistant/admin/models/:id
func (h *AssistantHandler) DeleteModel(c *gin.Context) {
	id, ok := parseAssistantID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteModel(id); err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// defaultModelRequest 设置默认模型请求体（modelId 为 null 表示清空）。
type defaultModelRequest struct {
	ModelID *uint `json:"modelId"`
	Version int   `json:"version"`
}

// SetDefaultModel PUT /assistant/admin/settings/default-model
func (h *AssistantHandler) SetDefaultModel(c *gin.Context) {
	var req defaultModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体格式不正确"})
		return
	}
	if err := h.svc.SetDefaultModel(req.ModelID, req.Version); err != nil {
		respondAssistantErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
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
	c.JSON(http.StatusOK, res)
}

// modelTestRequest 单模型测试的请求体。全字段可选。
type modelTestRequest struct {
	// WithTools 额外要求一次工具调用并验证参数完整性。
	WithTools bool `json:"withTools"`
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
