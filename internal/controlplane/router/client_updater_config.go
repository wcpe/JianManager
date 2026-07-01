package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientUpdaterConfigHandler 按频道生成带本机签名公钥的 jm-updater.json（FR-253，见 ADR-053）。
//
// 复用 FR-248 签名器公钥，返回完整 jm-updater.json 字段（含 signPublicKey/signKeyId），
// 运营者直接下载放入整合包即建立客户端信任根——无需改源码重编 updater-core。
// 限平台管理员（与频道管理同组）；只暴露公钥（私钥绝不出服务端）。
type ClientUpdaterConfigHandler struct {
	channelSvc *service.ClientChannelService
	signer     *service.ManifestSigner
}

// NewClientUpdaterConfigHandler 创建 jm-updater.json 生成端点处理器。
// signer 为 nil（未配置）→ 503 SIGN_KEY_NOT_CONFIGURED。
func NewClientUpdaterConfigHandler(channelSvc *service.ClientChannelService, signer *service.ManifestSigner) *ClientUpdaterConfigHandler {
	return &ClientUpdaterConfigHandler{channelSvc: channelSvc, signer: signer}
}

// GetUpdaterConfig GET /client-channels/:id/updater-config — 按频道生成 jm-updater.json（含本机签名公钥）。
//
// 返回完整 jm-updater.json 字段：signPublicKey（本机签名器公钥）+ signKeyId + channel + endpoint
// （CP 公网基址，按请求推断）+ key（占位空串，运营粘贴拉取密钥）。频道不存在 → 404。
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
	if h.signer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "SIGN_KEY_NOT_CONFIGURED", "message": "签名密钥未配置，OTA 分发不可用",
		})
		return
	}
	pub, err := h.signer.PublicKeySPKIBase64()
	if err != nil {
		slog.Error("导出客户端签名公钥失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "导出公钥失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"channel":         channelID,
		"key":             "", // 占位：运营在「拉取密钥」Tab 创建后粘贴。
		"endpoint":        resolvePublicBaseURL(c),
		"coreJar":         "updater-core.jar",
		"timeoutSec":      120,
		"telemetry":       true,
		"bootConfirmSec":  30,
		"coreVersion":     0,
		"signPublicKey":   pub,
		"signKeyId":       h.signer.KeyID(),
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
