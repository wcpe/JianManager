package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientTelemetryHandler 客户端遥测上报端点（FR-094，见 ADR-023、contract §4.3）。
// 面向玩家公网：拉取密钥（X-Client-Key）鉴权 + X-Machine-Id；202 Accepted、best-effort 落库不阻塞。
type ClientTelemetryHandler struct {
	svc     *service.ClientTelemetryService
	channel *service.ClientChannelService
}

// NewClientTelemetryHandler 创建遥测处理器。
func NewClientTelemetryHandler(svc *service.ClientTelemetryService, channel *service.ClientChannelService) *ClientTelemetryHandler {
	return &ClientTelemetryHandler{svc: svc, channel: channel}
}

// telemetryBody 遥测上报体（contract §4.3 + channel 由客户端携带便于按频道聚合）。
type telemetryBody struct {
	Channel     string `json:"channel"`
	Result      string `json:"result"`
	FromVersion int    `json:"fromVersion"`
	ToVersion   int    `json:"toVersion"`
	OS          string `json:"os"`
	JavaVersion string `json:"javaVersion"`
	Launcher    string `json:"launcher"`
	DurationMs  int64  `json:"durationMs"`
	BootSuccess bool   `json:"bootSuccess"`
	Error       string `json:"error"`
}

// Post POST /client-telemetry — 接收客户端遥测（玩家，拉取密钥鉴权）。
func (h *ClientTelemetryHandler) Post(c *gin.Context) {
	if _, err := h.channel.VerifyAnyKey(c.GetHeader(clientKeyHeader)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "INVALID_CLIENT_KEY", "message": "拉取密钥无效"})
		return
	}
	var body telemetryBody
	_ = c.ShouldBindJSON(&body) // 容忍部分字段缺失：遥测尽力收集，不因 body 不全拒绝。
	if h.svc != nil {
		_ = h.svc.Record(service.ClientTelemetryInput{
			ChannelID:   body.Channel,
			MachineID:   c.GetHeader(machineIDHeader),
			IP:          c.ClientIP(),
			Result:      body.Result,
			FromVersion: body.FromVersion,
			ToVersion:   body.ToVersion,
			OS:          body.OS,
			JavaVersion: body.JavaVersion,
			Launcher:    body.Launcher,
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
