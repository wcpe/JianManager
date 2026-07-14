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

// userListMaxLimit 分页信封形态单次返回上限（FR-336）。
const userListMaxLimit = 500

// List 用户列表（FR-336 增强 FR-002）。可选 q（用户名模糊）+ limit/offset 分页；
// 以「请求是否携带 limit」分流响应形态：带则返回 {items,total,limit,offset} 信封，
// 缺则保持旧裸数组兼容（q 两形态均生效，旧调用方零改动）。
func (h *UserHandler) List(c *gin.Context) {
	limitStr, hasLimit := c.GetQuery("limit")
	limit := 0
	if hasLimit {
		n, err := strconv.Atoi(limitStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": "limit 必须是整数",
			})
			return
		}
		// 钳制 [1,500]：形态开关已由「是否携带」表达，越界值就近归位。
		if n < 1 {
			n = 1
		}
		if n > userListMaxLimit {
			n = userListMaxLimit
		}
		limit = n
	}

	offset := 0
	if offsetStr, hasOffset := c.GetQuery("offset"); hasOffset {
		n, err := strconv.Atoi(offsetStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "INVALID_REQUEST",
				"message": "offset 必须是整数",
			})
			return
		}
		if n > 0 {
			offset = n
		}
	}

	users, total, err := h.userSvc.List(service.UserListFilter{
		Q:      c.Query("q"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "INTERNAL_ERROR",
			"message": "查询用户列表失败",
		})
		return
	}

	items := make([]gin.H, len(users))
	for i, u := range users {
		items[i] = gin.H{
			"id":        u.ID,
			"uuid":      u.UUID,
			"username":  u.Username,
			"role":      u.Role,
			"status":    u.Status,
			"createdAt": u.CreatedAt,
		}
	}

	if hasLimit {
		c.JSON(http.StatusOK, gin.H{
			"items":  items,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
		return
	}
	c.JSON(http.StatusOK, items)
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
	// Password 非空时重置该用户登录密码；长度下限 8 与初始化/创建一致（FR-156）。
	Password *string `json:"password" binding:"omitempty,min=8"`
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

	user, err := h.userSvc.Update(uint(id), req.Role, req.Status, req.Password)
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
