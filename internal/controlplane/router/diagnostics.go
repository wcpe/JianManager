package router

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// DiagnosticsHandler 连通性测试路由（FR-229）：出站 HTTP 可达性 + 节点存活探测，仅平台管理员。
// 注册在 admin 组——出站 HTTP 测试可让 CP 请求任意 URL（SSRF 面），限平台管理员降低滥用。
type DiagnosticsHandler struct {
	svc *service.DiagnosticsService
}

// NewDiagnosticsHandler 创建连通性测试处理器。
func NewDiagnosticsHandler(svc *service.DiagnosticsService) *DiagnosticsHandler {
	return &DiagnosticsHandler{svc: svc}
}

type httpTestRequest struct {
	URL string `json:"url" binding:"required"`
}

// TestHTTP POST /diagnostics/http-test — 经出站代理测试目标 URL 可达性（代理 / 下载源连通性）。
func (h *DiagnosticsHandler) TestHTTP(c *gin.Context) {
	var req httpTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "缺少 url"})
		return
	}
	res, err := h.svc.TestHTTPReachability(c.Request.Context(), req.URL)
	if err != nil {
		if errors.Is(err, service.ErrInvalidTestURL) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_URL", "message": "仅支持 http/https URL"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "TEST_FAILED", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// PingNode POST /nodes/:id/ping — 主动探测节点 Worker 是否存活（下载前先测，避免对离线/卡顿节点发起会卡死的下载）。
func (h *DiagnosticsHandler) PingNode(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "BAD_REQUEST", "message": "节点 id 非法"})
		return
	}
	res, perr := h.svc.PingNode(c.Request.Context(), uint(id))
	if perr != nil {
		if errors.Is(perr, service.ErrNodeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "NOT_FOUND", "message": "节点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "PING_FAILED", "message": perr.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// RegisterRoutes 注册连通性测试路由（挂在 admin 组上）。
func (h *DiagnosticsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/diagnostics/http-test", h.TestHTTP)
	rg.POST("/nodes/:id/ping", h.PingNode)
}
