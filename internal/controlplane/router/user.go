package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// UserHandler 用户路由处理器。
type UserHandler struct {
	userSvc       *service.UserService
	invitationSvc *service.UserInvitationService
}

// NewUserHandler 创建用户路由处理器。
func NewUserHandler(userSvc *service.UserService) *UserHandler {
	return &UserHandler{userSvc: userSvc, invitationSvc: userSvc.InvitationService()}
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

type createUserRequest struct {
	Username string            `json:"username" binding:"required,min=3,max=64"`
	Password string            `json:"password" binding:"required,min=8,max=128"`
	Role     *model.UserRole   `json:"role" binding:"required"`
	Status   *model.UserStatus `json:"status" binding:"required"`
}

// Create 由平台管理员在单一请求中创建用户与权限状态。
func (h *UserHandler) Create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	user, err := h.userSvc.Create(req.Username, req.Password, *req.Role, *req.Status)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUserExists):
			c.JSON(http.StatusConflict, gin.H{"error": "USER_EXISTS", "message": "用户名已存在"})
		case errors.Is(err, service.ErrUserInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "服务器内部错误"})
		}
		return
	}
	c.JSON(http.StatusCreated, userResponse(user))
}

type createInvitationRequest struct {
	Email     string `json:"email" binding:"required"`
	SendEmail bool   `json:"sendEmail"`
}

// CreateInvitation 签发一次性成员邀请；邮件失败时仍返回手动发送链接。
func (h *UserHandler) CreateInvitation(c *gin.Context) {
	var req createInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	userID, _ := c.Get("userId")
	result, err := h.invitationSvc.Create(userID.(uint), req.Email, req.SendEmail)
	if err != nil {
		if errors.Is(err, service.ErrInvitationPublicBaseURLNotConfigured) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "未配置邀请公共基址"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	invitation := result.Invitation
	c.JSON(http.StatusCreated, gin.H{
		"id":            invitation.ID,
		"email":         invitation.Email,
		"role":          invitation.Role,
		"expiresAt":     invitation.ExpiresAt,
		"invitationUrl": result.InvitationURL,
		"emailDelivery": invitation.EmailDelivery,
	})
}

// ListInvitations 返回不含令牌与 SMTP 凭据的邀请列表。
func (h *UserHandler) ListInvitations(c *gin.Context) {
	invitations, err := h.invitationSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	items := make([]gin.H, 0, len(invitations))
	for _, invitation := range invitations {
		items = append(items, invitationResponse(&invitation))
	}
	c.JSON(http.StatusOK, items)
}

// RevokeInvitation 撤销未使用邀请。
func (h *UserHandler) RevokeInvitation(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的邀请 ID"})
		return
	}
	if err := h.invitationSvc.Revoke(uint(id)); err != nil {
		switch {
		case errors.Is(err, service.ErrInvitationAlreadyUsed):
			c.JSON(http.StatusConflict, gin.H{"error": "INVITATION_ALREADY_USED", "message": "邀请已使用"})
		default:
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "邀请不存在"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已撤销"})
}

func userResponse(user *model.User) gin.H {
	return gin.H{"id": user.ID, "uuid": user.UUID, "username": user.Username, "role": user.Role, "status": user.Status, "createdAt": user.CreatedAt}
}

func invitationResponse(invitation *model.UserInvitation) gin.H {
	return gin.H{
		"id":            invitation.ID,
		"email":         invitation.Email,
		"role":          invitation.Role,
		"expiresAt":     invitation.ExpiresAt,
		"used":          invitation.UsedAt != nil,
		"usedAt":        invitation.UsedAt,
		"revoked":       invitation.RevokedAt != nil,
		"revokedAt":     invitation.RevokedAt,
		"createdBy":     invitation.CreatedByID,
		"emailSentAt":   invitation.EmailSentAt,
		"emailDelivery": invitation.EmailDelivery,
		"createdAt":     invitation.CreatedAt,
	}
}

// RegisterRoutes 注册用户路由。
func (h *UserHandler) RegisterRoutes(rg *gin.RouterGroup) {
	users := rg.Group("/users")
	{
		users.POST("", h.Create)
		users.GET("", h.List)
		users.POST("/invitations", h.CreateInvitation)
		users.GET("/invitations", h.ListInvitations)
		users.DELETE("/invitations/:id", h.RevokeInvitation)
		users.GET("/:id", h.Get)
		users.PUT("/:id", h.Update)
		users.DELETE("/:id", h.Delete)
	}
}
