package router

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientUpdaterConfigHandler 按频道生成 jm-updater.json（FR-253，见 ADR-053）。
//
// FR-256 起 jm-updater.json 不再含 signPublicKey/signKeyId（验签已去，信任靠 HTTPS + 拉取密钥
// 鉴权，见 docs/specs/updater-arch-simplification/spec.md §2 A，推翻 ADR-022/053）。
// 限平台管理员（与频道管理同组）。
type ClientUpdaterConfigHandler struct {
	channelSvc *service.ClientChannelService
}

// NewClientUpdaterConfigHandler 创建 jm-updater.json 生成端点处理器。
func NewClientUpdaterConfigHandler(channelSvc *service.ClientChannelService) *ClientUpdaterConfigHandler {
	return &ClientUpdaterConfigHandler{channelSvc: channelSvc}
}

// GetUpdaterConfig GET /client-channels/:id/updater-config — 按频道生成 jm-updater.json。
//
// 返回 jm-updater.json 字段：channel + endpoint（CP 公网基址，按请求推断）+ key（占位空串，
// 运营粘贴拉取密钥）。频道不存在 → 404。
func (h *ClientUpdaterConfigHandler) GetUpdaterConfig(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	// 校验频道存在（复用既有服务，不另查库）。
	if _, err := h.channelSvc.GetChannel(channelID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"channel":        channelID,
		"key":            "", // 占位：运营在「拉取密钥」Tab 创建后粘贴。
		"endpoint":       resolvePublicBaseURL(c),
		"coreJar":        "updater-core.jar",
		"timeoutSec":     120,
		"telemetry":      true,
		"bootConfirmSec": 30,
		"coreVersion":    0,
	})
}

// resolvePublicBaseURL 推断 CP 对外公网基址（客户端拉 manifest 的 endpoint）。
// scheme 按 TLS / X-Forwarded-Proto 判定，host 取请求 Host（适配同机/反代部署）。
func resolvePublicBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/v1", scheme, requestHostname(c))
}

// RegisterRoutes 注册 jm-updater.json 生成端点（挂在平台管理员组的频道路由下）。
func (h *ClientUpdaterConfigHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-channels/:id/updater-config", h.GetUpdaterConfig)
}
