package router

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientSignKeyHandler 暴露客户端 OTA manifest 的签名**公钥**供运营者配到客户端 updater-core（FR-248，见 ADR-052）。
//
// 只读、只暴露公钥（信任根私钥绝不出服务端）。签名器由 main.go 经 ResolveManifestSignerWithAutogen
// 裁决（env 注入 / 生产自动生成 / dev 回退三轨），来源随 source 透出供面板展示徽章。
type ClientSignKeyHandler struct {
	signer *service.ManifestSigner
	source string
}

// NewClientSignKeyHandler 创建签名公钥端点处理器。signer 可为 nil（未配置→查看返 503）。
func NewClientSignKeyHandler(signer *service.ManifestSigner, source string) *ClientSignKeyHandler {
	return &ClientSignKeyHandler{signer: signer, source: source}
}

// GetSignKey GET /client-dist/sign-key — 返回签名公钥 + keyId + 来源（运营，平台管理员）。
//
// signer 为 nil（理论上仅自动生成失败已 fatal，正常不达）→ 503 SIGN_KEY_NOT_CONFIGURED。
func (h *ClientSignKeyHandler) GetSignKey(c *gin.Context) {
	if !requirePlatformAdmin(c) {
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
		"publicKey": pub,
		"keyId":     h.signer.KeyID(),
		"source":    h.source,
	})
}

// RegisterRoutes 注册签名公钥端点（运营操作，须挂 JWT 平台管理员组）。
func (h *ClientSignKeyHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/sign-key", h.GetSignKey)
}
