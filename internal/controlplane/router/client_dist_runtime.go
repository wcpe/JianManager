package router

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientDistRuntimeHandler 客户端运行态与分发请求近实时观测端点（FR-265）。
type ClientDistRuntimeHandler struct {
	runtime  *service.ClientRuntimeStateService
	tracking *service.ClientDistTrackingService
	channel  *service.ClientChannelService
	audit    *service.AuditService
	security *service.ClientDistSecurityService
}

// NewClientDistRuntimeHandler 创建 FR-265 观测处理器。
func NewClientDistRuntimeHandler(runtime *service.ClientRuntimeStateService, tracking *service.ClientDistTrackingService, channel *service.ClientChannelService, audit *service.AuditService, security ...*service.ClientDistSecurityService) *ClientDistRuntimeHandler {
	var sec *service.ClientDistSecurityService
	if len(security) > 0 {
		sec = security[0]
	}
	return &ClientDistRuntimeHandler{runtime: runtime, tracking: tracking, channel: channel, audit: audit, security: sec}
}

// RegisterAdminRoutes 注册平台管理员观测端点。
func (h *ClientDistRuntimeHandler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/clients", h.Clients)
	rg.GET("/client-dist/realtime", h.Realtime)
	rg.GET("/client-dist/error-summary", h.ErrorSummary)
	rg.GET("/client-dist/events/search", h.SearchEvents)
	rg.GET("/client-dist/events/:id", h.EventDetail)
}

// RegisterConsumerRoutes 注册玩家启动心跳端点。
func (h *ClientDistRuntimeHandler) RegisterConsumerRoutes(rg *gin.RouterGroup) {
	rg.POST("/client-channels/:id/telemetry/heartbeat", h.Heartbeat)
}

type runtimeHeartbeatBody struct {
	Platform     string `json:"platform"`
	JavaVersion  string `json:"javaVersion"`
	Launcher     string `json:"launcher"`
	CoreVersion  string `json:"coreVersion"`
	LocalVersion int    `json:"localVersion"`
}

// Heartbeat 接收 updater-core 启动心跳（FR-265）。
func (h *ClientDistRuntimeHandler) Heartbeat(c *gin.Context) {
	channelID := c.Param("id")
	if h.channel == nil || h.runtime == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "运行态服务未初始化"})
		return
	}
	if _, err := h.channel.VerifyKey(channelID, c.GetHeader(clientKeyHeader)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "INVALID_CLIENT_KEY", "message": "拉取密钥无效"})
		return
	}
	var body runtimeHeartbeatBody
	_ = c.ShouldBindJSON(&body)
	machineID := c.GetHeader(machineIDHeader)
	_ = h.runtime.RecordHeartbeat(service.ClientRuntimeHeartbeatInput{
		ChannelID: channelID, MachineID: machineID, PlayerName: h.playerNameFromRequest(c, channelID, machineID), IP: c.ClientIP(),
		Platform: body.Platform, JavaVersion: body.JavaVersion, Launcher: body.Launcher,
		CoreVersion: body.CoreVersion, LocalVersion: body.LocalVersion,
	})
	c.Status(http.StatusAccepted)
}

func (h *ClientDistRuntimeHandler) playerNameFromRequest(c *gin.Context, channelID, machineID string) string {
	if v := c.GetHeader(playerNameHeader); v != "" {
		return v
	}
	if h.security == nil {
		return ""
	}
	return h.security.ResolveProfilePlayerName(channelID, machineID, c.GetHeader(installIDHeader))
}

// Clients 查询客户端运行态聚合（FR-265）。
func (h *ClientDistRuntimeHandler) Clients(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.runtime.Overview(service.ClientRuntimeQuery{ChannelID: c.Query("channelId"), Range: c.DefaultQuery("range", "7d")})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询客户端运行态失败"})
		return
	}
	h.recordAudit(c, "client_dist_clients.query", map[string]any{"channelId": c.Query("channelId"), "range": c.DefaultQuery("range", "7d")})
	c.JSON(http.StatusOK, out)
}

// Realtime 查询分发请求近实时聚合（FR-265）。
func (h *ClientDistRuntimeHandler) Realtime(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	out, err := h.tracking.Realtime(service.ClientDistRealtimeQuery{ChannelID: c.Query("channelId")})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询分发实时状态失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// ErrorSummary 查询错误码 TopN 与失败样例（FR-357）。
func (h *ClientDistRuntimeHandler) ErrorSummary(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	from, to, ok := parseObsRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "range 非法"})
		return
	}
	out, err := h.tracking.ErrorSummary(service.ClientDistErrorSummaryQuery{
		ChannelID: c.Query("channelId"), From: from, To: to,
		TopN: parseIntDefault(c.Query("topN"), 10), SampleLimit: parseIntDefault(c.Query("sampleLimit"), 20),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询错误摘要失败"})
		return
	}
	h.recordAudit(c, "client_dist_error_summary.query", map[string]any{"channelId": c.Query("channelId"), "range": c.DefaultQuery("range", "7d")})
	c.JSON(http.StatusOK, out)
}

// SearchEvents 分页检索分发事件（FR-265）。
func (h *ClientDistRuntimeHandler) SearchEvents(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	f := service.ClientDistEventSearchFilter{ClientDistEventFilter: eventFilterFromQuery(c), ArtifactSHA: c.Query("artifactSha"), CoreVersion: c.Query("coreVersion"), Platform: c.Query("platform")}
	f.RuntimeVersion = parseIntPtr(c.Query("runtimeVersion"))
	f.Lag = parseIntPtr(c.Query("lag"))
	f.Page = parseIntDefault(c.Query("page"), 1)
	f.PageSize = parseIntDefault(c.Query("pageSize"), 100)
	out, err := h.tracking.SearchEvents(f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "检索失败"})
		return
	}
	c.JSON(http.StatusOK, out)
}

// EventDetail 查询单条分发事件脱敏详情（FR-265）。
func (h *ClientDistRuntimeHandler) EventDetail(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "事件 ID 非法"})
		return
	}
	detail, err := h.tracking.GetEventDetail(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "EVENT_NOT_FOUND", "message": "事件不存在"})
		return
	}
	h.recordAudit(c, "client_dist_event.detail", map[string]any{"eventId": id})
	c.JSON(http.StatusOK, detail)
}

func eventFilterFromQuery(c *gin.Context) service.ClientDistEventFilter {
	f := service.ClientDistEventFilter{ChannelID: c.Query("channelId"), MachineID: c.Query("machineId"), IP: c.Query("ip"), Kind: c.Query("kind"), Outcome: c.Query("outcome"), ErrCode: c.Query("errCode")}
	f.Version = parseIntPtr(c.Query("version"))
	return f
}

func parseIntPtr(s string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

func parseIntDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func (h *ClientDistRuntimeHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, action, "client_dist", "", string(raw), c.ClientIP())
}
