package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientStatsHandler 分发统计后台只读端点（FR-095，见 ADR-023）。限平台管理员。
type ClientStatsHandler struct {
	svc *service.ClientDistStatsService
}

// NewClientStatsHandler 创建统计处理器。
func NewClientStatsHandler(svc *service.ClientDistStatsService) *ClientStatsHandler {
	return &ClientStatsHandler{svc: svc}
}

// Overview GET /client-dist/stats?channelId=&days= — 频道分发统计复合视图（下载趋势/版本分布/成功率/活跃机器码/TopIP）。
func (h *ClientStatsHandler) Overview(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	days, _ := strconv.Atoi(c.Query("days"))
	stats, err := h.svc.Overview(c.Query("channelId"), days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "统计聚合失败"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// RegisterRoutes 注册统计端点（须挂 JWT 平台管理员组）。
func (h *ClientStatsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/stats", h.Overview)
}
