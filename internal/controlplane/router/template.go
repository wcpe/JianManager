package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// TemplateHandler 模板路由处理器。
type TemplateHandler struct {
	templateSvc *service.TemplateService
}

func NewTemplateHandler(templateSvc *service.TemplateService) *TemplateHandler {
	return &TemplateHandler{templateSvc: templateSvc}
}

func (h *TemplateHandler) List(c *gin.Context) {
	templates, err := h.templateSvc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	c.JSON(http.StatusOK, templates)
}

func (h *TemplateHandler) Create(c *gin.Context) {
	var req service.CreateTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求参数错误"})
		return
	}

	t, err := h.templateSvc.Create(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}

	c.JSON(http.StatusCreated, t)
}

func (h *TemplateHandler) RegisterRoutes(rg *gin.RouterGroup) {
	templates := rg.Group("/templates")
	{
		templates.GET("", h.List)
		templates.POST("", h.Create)
	}
}
