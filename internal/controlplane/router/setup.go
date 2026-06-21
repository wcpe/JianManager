package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// SetupHandler 首次启动引导处理器。
type SetupHandler struct {
	authSvc *service.AuthService
}

// NewSetupHandler 创建引导处理器。
func NewSetupHandler(authSvc *service.AuthService) *SetupHandler {
	return &SetupHandler{authSvc: authSvc}
}

// Status 查询是否需要初始化。
func (h *SetupHandler) Status(c *gin.Context) {
	required, err := h.authSvc.SetupRequired()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "服务器内部错误",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"setupRequired": required,
	})
}

type setupRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Password string `json:"password" binding:"required,min=8,max=128"`
}

// CreateAdmin 创建初始管理员并返回 Token。
func (h *SetupHandler) CreateAdmin(c *gin.Context) {
	var req setupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "请求参数错误",
		})
		return
	}

	tokens, err := h.authSvc.SetupAdmin(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrAdminAlreadyExists) {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "ADMIN_ALREADY_EXISTS",
				"message": "管理员已存在，初始化已完成",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "服务器内部错误",
		})
		return
	}

	c.JSON(http.StatusCreated, tokens)
}

// RegisterRoutes 注册引导路由。
func (h *SetupHandler) RegisterRoutes(rg *gin.RouterGroup) {
	setup := rg.Group("/setup")
	{
		setup.GET("/status", h.Status)
		setup.POST("", h.CreateAdmin)
	}
}
