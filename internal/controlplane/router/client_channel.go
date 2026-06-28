package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientChannelHandler 客户端分发频道与拉取密钥路由（FR-086，见 ADR-022）。
// 平台级共享资源，限平台管理员。创建/吊销/轮换写审计，审计 detail 绝不含密钥明文。
type ClientChannelHandler struct {
	svc   *service.ClientChannelService
	audit *service.AuditService
}

// NewClientChannelHandler 创建客户端分发路由处理器。
func NewClientChannelHandler(svc *service.ClientChannelService, audit *service.AuditService) *ClientChannelHandler {
	return &ClientChannelHandler{svc: svc, audit: audit}
}

// createChannelRequest 创建频道请求体。
type createChannelRequest struct {
	ChannelID   string `json:"channelId" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// updateChannelRequest 更新频道请求体（channelId 不可改）。
type updateChannelRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// createKeyRequest 创建拉取密钥请求体。
type createKeyRequest struct {
	Name      string `json:"name" binding:"required"`
	ExpiresAt string `json:"expiresAt"`
}

// ListChannels GET /client-channels — 列出全部频道。
func (h *ClientChannelHandler) ListChannels(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	list, err := h.svc.ListChannels()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询频道失败"})
		return
	}
	c.JSON(http.StatusOK, list)
}

// CreateChannel POST /client-channels — 创建频道。
func (h *ClientChannelHandler) CreateChannel(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body createChannelRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	ch, err := h.svc.CreateChannel(body.ChannelID, body.Name, body.Description)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidChannelID):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_CHANNEL_ID", "message": err.Error()})
		case errors.Is(err, service.ErrChannelExists):
			c.JSON(http.StatusConflict, gin.H{"error": "CHANNEL_EXISTS", "message": "频道已存在"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建频道失败"})
		}
		return
	}
	c.JSON(http.StatusCreated, ch)
}

// GetChannel GET /client-channels/:id — 频道详情（含密钥元数据，无明文）。
func (h *ClientChannelHandler) GetChannel(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	detail, err := h.svc.GetChannel(c.Param("id"))
	if err != nil {
		h.respondChannelErr(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// UpdateChannel PUT /client-channels/:id — 更新名称/描述。
func (h *ClientChannelHandler) UpdateChannel(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body updateChannelRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	ch, err := h.svc.UpdateChannel(c.Param("id"), body.Name, body.Description)
	if err != nil {
		h.respondChannelErr(c, err)
		return
	}
	c.JSON(http.StatusOK, ch)
}

// DeleteChannel DELETE /client-channels/:id — 删除频道及其密钥。
func (h *ClientChannelHandler) DeleteChannel(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	if err := h.svc.DeleteChannel(channelID); err != nil {
		h.respondChannelErr(c, err)
		return
	}
	h.recordAudit(c, "client_channel.delete", map[string]any{"channelId": channelID})
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ListKeys GET /client-channels/:id/keys — 列出密钥（仅元数据）。
func (h *ClientChannelHandler) ListKeys(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	keys, err := h.svc.ListKeys(c.Param("id"))
	if err != nil {
		h.respondChannelErr(c, err)
		return
	}
	c.JSON(http.StatusOK, keys)
}

// CreateKey POST /client-channels/:id/keys — 创建密钥；明文一次性返回。
func (h *ClientChannelHandler) CreateKey(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	var body createKeyRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	expiresAt, err := parseOptionalTime(body.ExpiresAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "expiresAt 格式错误"})
		return
	}
	channelID := c.Param("id")
	key, plaintext, err := h.svc.CreateKey(channelID, body.Name, expiresAt)
	if err != nil {
		h.respondKeyErr(c, err)
		return
	}
	// 审计 detail 仅含可公开元数据，绝不含明文。
	h.recordAudit(c, "client_key.create", map[string]any{
		"channelId": channelID, "keyId": key.ID, "name": key.Name, "keyPrefix": key.KeyPrefix,
	})
	c.JSON(http.StatusCreated, keyWithPlaintext(key, plaintext))
}

// RotateKey POST /client-channels/:id/keys/:kid/rotate — 轮换密钥；新明文一次性返回。
func (h *ClientChannelHandler) RotateKey(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	kid, err := parseUintParam(c, "kid")
	if err != nil {
		return
	}
	key, plaintext, err := h.svc.RotateKey(channelID, kid)
	if err != nil {
		h.respondKeyErr(c, err)
		return
	}
	h.recordAudit(c, "client_key.rotate", map[string]any{
		"channelId": channelID, "keyId": key.ID, "keyPrefix": key.KeyPrefix,
	})
	c.JSON(http.StatusOK, keyWithPlaintext(key, plaintext))
}

// RevealKey GET /client-channels/:id/keys/:kid/reveal — 查看密钥明文（FR-192，见 ADR-044）。
// 仅平台管理员 + 审计 client_key.reveal（detail 绝不含明文）。无 KeyEnc → 404 KEY_NOT_REVEALABLE。
func (h *ClientChannelHandler) RevealKey(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	kid, err := parseUintParam(c, "kid")
	if err != nil {
		return
	}
	plaintext, err := h.svc.RevealKey(channelID, kid)
	if err != nil {
		if errors.Is(err, service.ErrPullKeyNotRevealable) {
			c.JSON(http.StatusNotFound, gin.H{
				"error":   "KEY_NOT_REVEALABLE",
				"message": "此密钥创建于可查看功能之前或未配置加密，明文不可找回",
			})
			return
		}
		h.respondKeyErr(c, err)
		return
	}
	// 审计：记录查看动作以便追溯；detail 仅含元数据，绝不含明文。
	h.recordAudit(c, "client_key.reveal", map[string]any{"channelId": channelID, "keyId": kid})
	c.JSON(http.StatusOK, gin.H{"key": plaintext})
}

// RevokeKey DELETE /client-channels/:id/keys/:kid — 吊销密钥。
func (h *ClientChannelHandler) RevokeKey(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	kid, err := parseUintParam(c, "kid")
	if err != nil {
		return
	}
	if err := h.svc.RevokeKey(channelID, kid); err != nil {
		h.respondKeyErr(c, err)
		return
	}
	h.recordAudit(c, "client_key.revoke", map[string]any{"channelId": channelID, "keyId": kid})
	c.JSON(http.StatusOK, gin.H{"message": "已吊销"})
}

// respondChannelErr 统一频道错误映射。
func (h *ClientChannelHandler) respondChannelErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrInvalidChannelID):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// respondKeyErr 统一密钥错误映射（含频道不存在）。
func (h *ClientChannelHandler) respondKeyErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrChannelNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
	case errors.Is(err, service.ErrPullKeyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "KEY_NOT_FOUND", "message": "拉取密钥不存在"})
	case errors.Is(err, service.ErrInvalidChannelID):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "操作失败"})
	}
}

// recordAudit 记录客户端分发破坏性操作审计（detail 仅含可公开元数据，绝不含明文）。
func (h *ClientChannelHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, action, "client_channel", "", string(raw), c.ClientIP())
}

// parseOptionalTime 解析可选 RFC3339 时间；空串返回 nil。
func parseOptionalTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// keyWithPlaintext 把一次性明文并入密钥响应（仅创建/轮换返回；列表/详情不含）。
func keyWithPlaintext(key any, plaintext string) gin.H {
	raw, _ := json.Marshal(key)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["key"] = plaintext
	return m
}

// RegisterRoutes 注册客户端分发路由。
func (h *ClientChannelHandler) RegisterRoutes(rg *gin.RouterGroup) {
	ch := rg.Group("/client-channels")
	{
		ch.GET("", h.ListChannels)
		ch.POST("", h.CreateChannel)
		ch.GET("/:id", h.GetChannel)
		ch.PUT("/:id", h.UpdateChannel)
		ch.DELETE("/:id", h.DeleteChannel)
		ch.GET("/:id/keys", h.ListKeys)
		ch.POST("/:id/keys", h.CreateKey)
		ch.GET("/:id/keys/:kid/reveal", h.RevealKey)
		ch.POST("/:id/keys/:kid/rotate", h.RotateKey)
		ch.DELETE("/:id/keys/:kid", h.RevokeKey)
	}
}
