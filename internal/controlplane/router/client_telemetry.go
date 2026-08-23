package router

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientTelemetryHandler 客户端遥测上报端点（FR-094 / FR-360，见 ADR-023、contract §4.3）。
// 面向玩家公网：拉取密钥（X-Client-Key）鉴权 + X-Machine-Id；202 Accepted、best-effort 落库不阻塞。
type ClientTelemetryHandler struct {
	svc      *service.ClientTelemetryService
	channel  *service.ClientChannelService
	security *service.ClientDistSecurityService
}

// NewClientTelemetryHandler 创建遥测处理器。
func NewClientTelemetryHandler(svc *service.ClientTelemetryService, channel *service.ClientChannelService, security ...*service.ClientDistSecurityService) *ClientTelemetryHandler {
	var sec *service.ClientDistSecurityService
	if len(security) > 0 {
		sec = security[0]
	}
	return &ClientTelemetryHandler{svc: svc, channel: channel, security: sec}
}

// telemetryBody 遥测上报体（contract §4.3 + FR-360 字段；未知字段忽略；缺字段不 4xx）。
type telemetryBody struct {
	Channel     string `json:"channel"`
	Result      string `json:"result"`
	FromVersion int    `json:"fromVersion"`
	ToVersion   int    `json:"toVersion"`
	CoreVersion string `json:"coreVersion"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	JavaVersion string `json:"javaVersion"`
	JavaVendor  string `json:"javaVendor"`
	Launcher    string `json:"launcher"`
	Locale      string `json:"locale"`
	Timezone    string `json:"timezone"`
	MemoryTier  string `json:"memoryTier"`
	DurationMs  int64  `json:"durationMs"`
	BootSuccess bool   `json:"bootSuccess"`
	Error       string `json:"error"`
}

// Post POST /client-telemetry — 接收客户端遥测（玩家，拉取密钥鉴权）。
func (h *ClientTelemetryHandler) Post(c *gin.Context) {
	if h.security != nil {
		if _, err := h.security.VerifyAnyKey(c.GetHeader(clientKeyHeader)); err != nil {
			respondSecurityAuthErr(c, h.security, err)
			return
		}
	} else if _, err := h.channel.VerifyAnyKey(c.GetHeader(clientKeyHeader)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "INVALID_CLIENT_KEY", "message": "拉取密钥无效"})
		return
	}
	var body telemetryBody
	if err := c.ShouldBindJSON(&body); err != nil {
		slog.Debug("客户端遥测请求体按零值处理", "error", err)
	}
	if h.svc != nil {
		machineID := c.GetHeader(machineIDHeader)
		playerName := h.playerNameFromRequest(c, body.Channel, machineID)
		h.svc.RecordSafe(service.ClientTelemetryInput{
			ChannelID:   body.Channel,
			MachineID:   machineID,
			PlayerName:  playerName,
			IP:          c.ClientIP(),
			Result:      body.Result,
			FromVersion: body.FromVersion,
			ToVersion:   body.ToVersion,
			CoreVersion: body.CoreVersion,
			OS:          body.OS,
			Arch:        body.Arch,
			JavaVersion: body.JavaVersion,
			JavaVendor:  body.JavaVendor,
			Launcher:    body.Launcher,
			Locale:      body.Locale,
			Timezone:    body.Timezone,
			MemoryTier:  body.MemoryTier,
			DurationMs:  body.DurationMs,
			BootSuccess: body.BootSuccess,
			Error:       body.Error,
		})
	}
	c.Status(http.StatusAccepted) // 202：不阻塞客户端（隐私可关在客户端，contract §4.3）。
}

// RegisterRoutes 注册遥测端点（须挂面向玩家公网组：拉取密钥鉴权 + L7 守卫）。
func (h *ClientTelemetryHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.POST("/client-telemetry", h.Post)
}

func (h *ClientTelemetryHandler) playerNameFromRequest(c *gin.Context, channelID, machineID string) string {
	if v := c.GetHeader(playerNameHeader); v != "" {
		return v
	}
	if h.security == nil {
		return ""
	}
	return h.security.ResolveProfilePlayerName(channelID, machineID, c.GetHeader(installIDHeader))
}
