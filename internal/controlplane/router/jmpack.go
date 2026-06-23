package router

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// JmPackHandler 服务端 .jmpack 打包端点（FR-097，见 ADR-021/022）。运营操作、限平台管理员。
type JmPackHandler struct {
	svc   *service.JmPackService
	audit *service.AuditService
}

// NewJmPackHandler 创建打包处理器。
func NewJmPackHandler(svc *service.JmPackService, audit *service.AuditService) *JmPackHandler {
	return &JmPackHandler{svc: svc, audit: audit}
}

// Pack POST /client-channels/:id/pack — 把频道 latest 版本打成 .jmpack 入库（type=client-pack）。
func (h *JmPackHandler) Pack(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	channelID := c.Param("id")
	res, err := h.svc.PackVersion(channelID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrChannelNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "CHANNEL_NOT_FOUND", "message": "频道不存在"})
		case errors.Is(err, service.ErrNoLatestVersion):
			c.JSON(http.StatusNotFound, gin.H{"error": "NO_LATEST_VERSION", "message": "频道尚未发布版本"})
		case errors.Is(err, service.ErrAssetNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "ARTIFACT_NOT_FOUND", "message": "制品缺失，无法打包"})
		case errors.Is(err, service.ErrInvalidVersionFiles):
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_VERSION_FILES", "message": err.Error()})
		case errors.Is(err, service.ErrSignKeyNotConfigured):
			c.JSON(http.StatusInternalServerError, gin.H{"error": "SIGN_KEY_NOT_CONFIGURED", "message": "签名私钥未配置"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "打包失败"})
		}
		return
	}
	h.recordAudit(c, "client_pack.create", map[string]any{"channelId": channelID, "sha256": res.SHA256, "size": res.Size})
	c.JSON(http.StatusCreated, res)
}

func (h *JmPackHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, action, "client_channel", "", string(raw), c.ClientIP())
}

// RegisterRoutes 注册打包端点（须挂 JWT 平台管理员组）。
func (h *JmPackHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/client-channels/:id/pack", h.Pack)
}
