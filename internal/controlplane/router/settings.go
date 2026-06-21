package router

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// SettingsHandler 平台配置路由处理器（FR-063 / ADR-015）。
// 挂在 admin 分组下，仅平台管理员可访问。
type SettingsHandler struct {
	svc *service.SettingsService
}

func NewSettingsHandler(svc *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{svc: svc}
}

// Get 返回当前有效配置视图（可编辑项 + 只读项，敏感项脱敏）。
func (h *SettingsHandler) Get(c *gin.Context) {
	view, err := h.svc.Get()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, view)
}

// updateSettingsRequest 一次提交一批「键 → 覆盖值」。
type updateSettingsRequest struct {
	Values map[string]string `json:"values" binding:"required"`
}

// Update 按白名单写入配置覆盖；非法键/值整体拒绝。
func (h *SettingsHandler) Update(c *gin.Context) {
	var req updateSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST"})
		return
	}
	if err := h.svc.Update(req.Values); err != nil {
		// 键不可写 / 值非法为可预期校验错误，回 422。
		if errors.Is(err, service.ErrSettingKeyNotWritable) || errors.Is(err, service.ErrSettingValueInvalid) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "BUSINESS_ERROR", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	// 返回更新后的最新视图，便于前端直接回填。
	view, err := h.svc.Get()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "已保存"})
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *SettingsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/settings")
	{
		g.GET("", h.Get)
		g.PUT("", h.Update)
	}
}
