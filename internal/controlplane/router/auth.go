package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// AuthHandler 认证路由处理器。
type AuthHandler struct {
	authSvc       *service.AuthService
	invitationSvc *service.UserInvitationService
}

// NewAuthHandler 创建认证路由处理器。
func NewAuthHandler(authSvc *service.AuthService, invitationSvc *service.UserInvitationService) *AuthHandler {
	return &AuthHandler{authSvc: authSvc, invitationSvc: invitationSvc}
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

// Login 用户登录。
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "请求参数错误",
		})
		return
	}

	tokens, err := h.authSvc.Login(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCreds) || errors.Is(err, service.ErrUserDisabled) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "用户名或密码错误",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "服务器内部错误",
		})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

// RefreshToken 刷新 access token。
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "请求参数错误",
		})
		return
	}

	tokens, err := h.authSvc.RefreshToken(req.RefreshToken)
	if err != nil {
		if errors.Is(err, service.ErrInvalidToken) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":   "UNAUTHORIZED",
				"message": "refreshToken 无效或已过期",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "服务器内部错误",
		})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

type acceptInvitationRequest struct {
	Token    string `json:"token" binding:"required"`
	Username string `json:"username" binding:"required,min=3,max=64"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// AcceptInvitation 使用一次性邀请创建 active member。
func (h *AuthHandler) AcceptInvitation(c *gin.Context) {
	var req acceptInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	user, err := h.invitationSvc.Accept(req.Token, req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvitationInvalid):
			c.JSON(http.StatusUnauthorized, gin.H{"error": "INVITATION_INVALID", "message": "邀请无效"})
		case errors.Is(err, service.ErrUserExists):
			c.JSON(http.StatusConflict, gin.H{"error": "USER_EXISTS", "message": "用户名已存在"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "服务器内部错误"})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": user.ID, "username": user.Username, "createdAt": user.CreatedAt})
}

// RegisterRoutes 注册认证路由。
func (h *AuthHandler) RegisterRoutes(rg *gin.RouterGroup) {
	auth := rg.Group("/auth")
	{
		auth.POST("/login", h.Login)
		auth.POST("/refresh", h.RefreshToken)
		if h.invitationSvc != nil {
			auth.POST("/invitations/accept", h.AcceptInvitation)
		}
	}
}
