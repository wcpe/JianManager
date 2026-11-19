package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// DeviceFlowHandler 设备授权（FR-562 / RFC 8628 / ADR-112）。
//
// 三个端点分两类鉴权：
//   - /device/code 与 /device/token 供 CLI 使用，匿名（CLI 此时还没有凭证）
//   - /device/authorize 供浏览器使用，需 JWT（用户必须已登录才能确认授权）
type DeviceFlowHandler struct {
	svc *service.DeviceFlowService
}

// NewDeviceFlowHandler 创建处理器。
func NewDeviceFlowHandler(svc *service.DeviceFlowService) *DeviceFlowHandler {
	return &DeviceFlowHandler{svc: svc}
}

// RegisterAnonymousRoutes 挂匿名端点（CLI 侧）。
func (h *DeviceFlowHandler) RegisterAnonymousRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/oauth/device")
	g.POST("/code", h.Code)
	g.POST("/token", h.Token)
}

// RegisterAuthorizeRoutes 挂需 JWT 的确认端点（浏览器侧）。
func (h *DeviceFlowHandler) RegisterAuthorizeRoutes(rg *gin.RouterGroup) {
	rg.POST("/oauth/device/authorize", h.Authorize)
}

type deviceCodeRequest struct {
	// DeviceLabel CLI 上报的设备标识，展示给用户确认是自己发起的。
	DeviceLabel string `json:"deviceLabel"`
}

// Code POST /api/v1/oauth/device/code
func (h *DeviceFlowHandler) Code(c *gin.Context) {
	var req deviceCodeRequest
	// 允许空 body。
	_ = c.ShouldBindJSON(&req)

	// verificationURI 指向 CP 自己的设备授权页；用请求 Host 拼装以适配反代。
	uri := buildBaseURL(c) + "/device"
	res, err := h.svc.Start(req.DeviceLabel, uri)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "生成设备码失败"})
		return
	}
	// RFC 8628 字段名为 snake_case。
	c.JSON(http.StatusOK, gin.H{
		"device_code":      res.DeviceCode,
		"user_code":        res.UserCode,
		"verification_uri": res.VerificationURI,
		"expires_in":       res.ExpiresIn,
		"interval":         res.Interval,
	})
}

type deviceTokenRequest struct {
	DeviceCode string `json:"deviceCode"`
}

// Token POST /api/v1/oauth/device/token — CLI 轮询。
//
// 按 RFC 8628 §3.5：pending 返回 400 + authorization_pending；
// 过频返回 400 + slow_down；拒绝返回 400 + access_denied；
// 过期返回 400 + expired_token；成功返回 200 + 明文。
func (h *DeviceFlowHandler) Token(c *gin.Context) {
	var req deviceTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.DeviceCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "error_description": "缺少 deviceCode"})
		return
	}
	plaintext, err := h.svc.Poll(req.DeviceCode)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, service.ErrDeviceCodeNotFound):
			c.JSON(status, gin.H{"error": "invalid_grant", "error_description": "设备码不存在"})
		case errors.Is(err, service.ErrDeviceAuthorizationPending):
			c.JSON(status, gin.H{"error": "authorization_pending", "error_description": "等待用户在浏览器确认"})
		case errors.Is(err, service.ErrDeviceSlowDown):
			c.JSON(status, gin.H{"error": "slow_down", "error_description": "轮询过快，请按 interval 间隔重试"})
		case errors.Is(err, service.ErrDeviceAccessDenied):
			c.JSON(status, gin.H{"error": "access_denied", "error_description": "用户拒绝了授权"})
		case errors.Is(err, service.ErrDeviceCodeExpired):
			c.JSON(status, gin.H{"error": "expired_token", "error_description": "设备码已过期，请重新登录"})
		case errors.Is(err, service.ErrDeviceCodeConsumed):
			c.JSON(status, gin.H{"error": "invalid_grant", "error_description": "设备码已被使用"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error", "error_description": "处理失败"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token": plaintext,
		"token_type":   "Bearer",
		"prefix":       service.UserTokenPrefix,
	})
}

type deviceAuthorizeRequest struct {
	UserCode string `json:"userCode"`
	// Approve false 表示拒绝授权。
	Approve bool `json:"approve"`
}

// Authorize POST /api/v1/oauth/device/authorize — 浏览器确认。
func (h *DeviceFlowHandler) Authorize(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "请先登录"})
		return
	}
	var req deviceAuthorizeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.UserCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "缺少 userCode"})
		return
	}

	if !req.Approve {
		if err := h.svc.Deny(req.UserCode); err != nil {
			if errors.Is(err, service.ErrDeviceCodeNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "设备码不存在"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "处理失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "approved": false})
		return
	}

	if err := h.svc.Approve(req.UserCode, userID); err != nil {
		switch {
		case errors.Is(err, service.ErrDeviceCodeNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "设备码不存在或已失效"})
		case errors.Is(err, service.ErrDeviceCodeExpired):
			c.JSON(http.StatusGone, gin.H{"error": "EXPIRED", "message": "设备码已过期，请重新发起登录"})
		case errors.Is(err, service.ErrDeviceCodeConsumed):
			c.JSON(http.StatusConflict, gin.H{"error": "CONFLICT", "message": "该设备码已被使用"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "授权失败"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "approved": true})
}

// buildBaseURL 从请求推导 CP 对外基址（适配反代：优先 X-Forwarded-*）。
func buildBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if v := c.GetHeader("X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	host := c.Request.Host
	if v := c.GetHeader("X-Forwarded-Host"); v != "" {
		host = v
	}
	return scheme + "://" + host
}
