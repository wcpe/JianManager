package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// UserTokenHandler 用户自助令牌管理（FR-561 / ADR-112）。
//
// 与 AgentTokenHandler 的差异：挂在 JWT 保护下的「本人」路由组，
// 普通用户即可操作，不需要平台管理员；管理员可额外吊销他人令牌。
type UserTokenHandler struct {
	svc   *service.UserTokenService
	audit *service.AuditService
}

// NewUserTokenHandler 创建处理器。
func NewUserTokenHandler(svc *service.UserTokenService, audit *service.AuditService) *UserTokenHandler {
	return &UserTokenHandler{svc: svc, audit: audit}
}

// RegisterRoutes 挂到 JWT protected 组（登录用户即可访问）。
func (h *UserTokenHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/user/tokens")
	g.GET("", h.List)
	g.POST("", h.Create)
	g.DELETE("/:id", h.Revoke)
}

// userTokenView 令牌投影：不含明文与 hash。
type userTokenView struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	DeviceLabel       string     `json:"deviceLabel"`
	TokenPrefix       string     `json:"tokenPrefix"`
	ScopedNodeIDs     []uint     `json:"scopedNodeIds"`
	ScopedInstanceIDs []uint     `json:"scopedInstanceIds"`
	Capabilities      []string   `json:"capabilities"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	Revoked           bool       `json:"revoked"`
	LastUsedAt        *time.Time `json:"lastUsedAt"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// toUserTokenView 把模型投影为对外视图。
func toUserTokenView(t model.UserToken) userTokenView {
	return userTokenView{
		ID:                t.ID,
		Name:              t.Name,
		DeviceLabel:       t.DeviceLabel,
		TokenPrefix:       t.TokenPrefix,
		ScopedNodeIDs:     decodeUints(t.ScopedNodeIDs),
		ScopedInstanceIDs: decodeUints(t.ScopedInstanceIDs),
		Capabilities:      decodeStrings(t.Capabilities),
		ExpiresAt:         t.ExpiresAt,
		Revoked:           t.Revoked,
		LastUsedAt:        t.LastUsedAt,
		CreatedAt:         t.CreatedAt,
	}
}

// List GET /api/v1/user/tokens — 列出本人令牌。
func (h *UserTokenHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "缺少认证信息"})
		return
	}
	tokens, err := h.svc.List(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "列出口令失败"})
		return
	}
	views := make([]userTokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, toUserTokenView(t))
	}
	c.JSON(http.StatusOK, gin.H{"tokens": views, "count": len(views)})
}

// Create POST /api/v1/user/tokens — 手工创建令牌，明文只返回一次。
func (h *UserTokenHandler) Create(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "缺少认证信息"})
		return
	}
	var req service.CreateUserTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	res, err := h.svc.Create(userID, req)
	if err != nil {
		if errors.Is(err, service.ErrAgentForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "scope 超出你的权限范围"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "创建令牌失败"})
		return
	}
	if h.audit != nil {
		h.audit.RecordSafe(userID, "user_token.create", "user", "", res.Token.Name, c.ClientIP())
	}
	c.JSON(http.StatusCreated, gin.H{
		"token": toUserTokenView(*res.Token),
		// 明文仅此一次；关闭后不可再查看，需重新创建。
		"plaintext": res.Plaintext,
		"notice":    "请立即保存，此明文只显示一次",
	})
}

// Revoke DELETE /api/v1/user/tokens/:id — 吊销令牌。
//
// 本人可吊销自己的；平台管理员可吊销任意。越权与不存在统一返回 403，避免成为存在性探针。
func (h *UserTokenHandler) Revoke(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if userID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHORIZED", "message": "缺少认证信息"})
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "令牌 ID 无效"})
		return
	}
	allowAny := middleware.IsPlatformAdmin(c)
	if err := h.svc.Revoke(uint(id), userID, allowAny); err != nil {
		if errors.Is(err, service.ErrAgentForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权吊销该令牌"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "吊销令牌失败"})
		return
	}
	if h.audit != nil {
		h.audit.RecordSafe(userID, "user_token.revoke", "user", "", "token_id="+c.Param("id"), c.ClientIP())
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// decodeStrings 宽松解析 JSON 字符串数组，nil 时返回空切片（保证 JSON 输出为 [] 而非 null）。
func decodeStrings(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// decodeUints 宽松解析 JSON 数字数组，nil 时返回空切片。
func decodeUints(raw string) []uint {
	out := []uint{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []uint{}
	}
	if out == nil {
		return []uint{}
	}
	return out
}
