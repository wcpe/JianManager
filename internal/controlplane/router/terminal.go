package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// TerminalHandler 终端路由处理器。
type TerminalHandler struct {
	terminalSvc *service.TerminalService
}

// NewTerminalHandler 创建终端路由处理器。
func NewTerminalHandler(terminalSvc *service.TerminalService) *TerminalHandler {
	return &TerminalHandler{terminalSvc: terminalSvc}
}

// IssueToken 签发终端连接 token。
func (h *TerminalHandler) IssueToken(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}

	permission := c.DefaultQuery("permission", "write")
	if permission != "read" && permission != "write" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "permission 必须为 read 或 write"})
		return
	}

	token, err := h.terminalSvc.IssueToken(id, permission)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, token)
}

// RegisterRoutes 注册终端路由。
func (h *TerminalHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/instances/:id/terminal-token", h.IssueToken)
}
