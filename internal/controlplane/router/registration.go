package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// RegistrationHandler proxy↔backend 注册关系路由（FR-032 / ADR-007）。注册在平台管理员组下。
type RegistrationHandler struct {
	svc *service.RegistrationService
}

// NewRegistrationHandler 创建注册路由处理器。
func NewRegistrationHandler(svc *service.RegistrationService) *RegistrationHandler {
	return &RegistrationHandler{svc: svc}
}

// List GET /proxies/:id/registrations
func (h *RegistrationHandler) List(c *gin.Context) {
	proxyID, err := parseID(c)
	if err != nil {
		return
	}
	regs, err := h.svc.List(proxyID)
	if err != nil {
		writeRegError(c, err)
		return
	}
	c.JSON(http.StatusOK, regs)
}

// Create POST /proxies/:id/registrations
func (h *RegistrationHandler) Create(c *gin.Context) {
	proxyID, err := parseID(c)
	if err != nil {
		return
	}
	var req service.CreateRegistrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	view, err := h.svc.Create(proxyID, req)
	if err != nil {
		// view 非空表示关系已落库但同步代理配置失败：201 + 警告（关系是事实来源，可后续重同步）。
		if view != nil {
			c.JSON(http.StatusCreated, gin.H{"registration": view, "warning": err.Error()})
			return
		}
		writeRegError(c, err)
		return
	}
	c.JSON(http.StatusCreated, view)
}

// Update PATCH /proxies/:id/registrations/:rid
func (h *RegistrationHandler) Update(c *gin.Context) {
	proxyID, err := parseID(c)
	if err != nil {
		return
	}
	rid, err := parseRID(c)
	if err != nil {
		return
	}
	var req service.UpdateRegistrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}
	view, err := h.svc.Update(proxyID, rid, req)
	if err != nil {
		if view != nil {
			c.JSON(http.StatusOK, gin.H{"registration": view, "warning": err.Error()})
			return
		}
		writeRegError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Delete DELETE /proxies/:id/registrations/:rid
func (h *RegistrationHandler) Delete(c *gin.Context) {
	proxyID, err := parseID(c)
	if err != nil {
		return
	}
	rid, err := parseRID(c)
	if err != nil {
		return
	}
	if err := h.svc.Delete(proxyID, rid); err != nil {
		// 删除成功但同步失败也返回 204（关系已移除）；仅真正的查找/删除错误映射为错误码。
		if errors.Is(err, service.ErrRegistrationNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "REGISTRATION_NOT_FOUND", "message": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	c.Status(http.StatusNoContent)
}

// RegisterRoutes 注册注册关系路由。
func (h *RegistrationHandler) RegisterRoutes(rg *gin.RouterGroup) {
	g := rg.Group("/proxies/:id/registrations")
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.PATCH("/:rid", h.Update)
		g.DELETE("/:rid", h.Delete)
	}
}

// parseRID 解析注册关系 ID（路径参数 :rid）。
func parseRID(c *gin.Context) (uint, error) {
	v, err := strconv.ParseUint(c.Param("rid"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "无效的注册 ID"})
		return 0, err
	}
	return uint(v), nil
}

// writeRegError 将注册服务错误映射为 HTTP 响应。
func writeRegError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrProxyNotFound), errors.Is(err, service.ErrBackendNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "INSTANCE_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrRegistrationNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "REGISTRATION_NOT_FOUND", "message": err.Error()})
	case errors.Is(err, service.ErrNotAProxy):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "NOT_A_PROXY", "message": err.Error()})
	case errors.Is(err, service.ErrNotABackend):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "NOT_A_BACKEND", "message": err.Error()})
	case errors.Is(err, service.ErrInvalidAlias):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "INVALID_ALIAS", "message": err.Error()})
	case errors.Is(err, service.ErrAliasConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "ALIAS_CONFLICT", "message": err.Error()})
	case errors.Is(err, service.ErrAlreadyRegistered):
		c.JSON(http.StatusConflict, gin.H{"error": "ALREADY_REGISTERED", "message": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": err.Error()})
	}
}
