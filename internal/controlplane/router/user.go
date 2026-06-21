package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// UserHandler 用户路由处理器。
type UserHandler struct {
	userSvc *service.UserService
}

// NewUserHandler 创建用户路由处理器。
func NewUserHandler(userSvc *service.UserService) *UserHandler {
	return &UserHandler{userSvc: userSvc}
}

// List 用户列表。
func (h *UserHandler) List(c *gin.Context) {
	users, err := h.userSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "查询用户列表失败",
		})
		return
	}

	result := make([]gin.H, len(users))
	for i, u := range users {
		result[i] = gin.H{
			"id":        u.ID,
			"uuid":      u.UUID,
			"username":  u.Username,
			"role":      u.Role,
			"status":    u.Status,
			"createdAt": u.CreatedAt,
		}
	}

	c.JSON(http.StatusOK, result)
}

// Get 用户详情。
func (h *UserHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "无效的用户 ID",
		})
		return
	}

	user, err := h.userSvc.GetByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "NOT_FOUND",
			"message": "用户不存在",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":        user.ID,
		"uuid":      user.UUID,
		"username":  user.Username,
		"role":      user.Role,
		"status":    user.Status,
		"createdAt": user.CreatedAt,
	})
}

type updateUserRequest struct {
	Role   *model.UserRole   `json:"role"`
	Status *model.UserStatus `json:"status"`
}

// Update 更新用户。
func (h *UserHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "无效的用户 ID",
		})
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "请求参数错误",
		})
		return
	}

	user, err := h.userSvc.Update(uint(id), req.Role, req.Status)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "NOT_FOUND",
			"message": "用户不存在",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":        user.ID,
		"uuid":      user.UUID,
		"username":  user.Username,
		"role":      user.Role,
		"status":    user.Status,
		"createdAt": user.CreatedAt,
	})
}

// Delete 删除用户。
func (h *UserHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": "无效的用户 ID",
		})
		return
	}

	if err := h.userSvc.Delete(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "删除用户失败",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RegisterRoutes 注册用户路由。
func (h *UserHandler) RegisterRoutes(rg *gin.RouterGroup) {
	users := rg.Group("/users")
	{
		users.GET("", h.List)
		users.GET("/:id", h.Get)
		users.PUT("/:id", h.Update)
		users.DELETE("/:id", h.Delete)
	}
}
