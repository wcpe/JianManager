package router

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientDistRuntimeHandler 客户端运行态与分发请求近实时观测端点。
type ClientDistRuntimeHandler struct {
	runtime  *service.ClientRuntimeStateService
	tracking *service.ClientDistTrackingService
	channel  *service.ClientChannelService
	audit    *service.AuditService
	security *service.ClientDistSecurityService
}

// NewClientDistRuntimeHandler 创建观测处理器。
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
	// 机器级更新清单与钻取：前端 `clientDistMachines.ts` 调的就是这两个路径。
	//
	// 之前它们只存在于 devmock，真栈一直是 404——而 spec 却标着「已交付@v0.22.0」。
	// 现在由 CP 侧聚合明细表兑现，未新增任何表结构（spec §3「不改后端 schema」）。
	//
	// 挂在 admin 组（与 events/search 同权限口径：观测数据含 IP/机器码维度，访问留痕）。
	rg.GET("/client-dist/machines", h.Machines)
	rg.GET("/client-dist/machines/:machineId/events", h.MachineEvents)
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

// Heartbeat 接收 updater-core 启动心跳。
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
	if err := c.ShouldBindJSON(&body); err != nil {
		slog.Debug("客户端运行态心跳请求体按零值处理", "error", err)
	}
	machineID := c.GetHeader(machineIDHeader)
	h.runtime.RecordHeartbeatSafe(service.ClientRuntimeHeartbeatInput{
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

// Clients 查询客户端运行态聚合。
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

// Realtime 查询分发请求近实时聚合。
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

// ErrorSummary 查询错误码 TopN 与失败样例。
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

// SearchEvents 分页检索分发事件。
func (h *ClientDistRuntimeHandler) SearchEvents(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	// 时间窗（R31）：明细检索此前不吃时间参数，页头选 7 天也只给全量历史，
	// 与同一屏的导出（按 range/from-to 过滤）不是同一集合。口径与 observability 一致。
	//
	// 但这里**不强制**传窗：调用方（MCP/排障脚本/旧客户端）不带时间参数时保持原有
	// 「全量最近 N 条」语义。只有在真的给了 from/to/range 时才下推，避免把可选筛选变成必填。
	eventFilter := eventFilterFromQuery(c)
	if hasObsRangeParam(c) {
		from, to, ok := parseObsRange(c)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
			return
		}
		eventFilter.Since = &from
		eventFilter.Until = &to
	}
	f := service.ClientDistEventSearchFilter{ClientDistEventFilter: eventFilter, ArtifactSHA: c.Query("artifactSha"), CoreVersion: c.Query("coreVersion"), Platform: c.Query("platform")}
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

// Machines 机器级更新清单：服务端聚合 + 排序 + 分页。
//
// 时间窗必填（from/to 或 range，口径与 observability 一致）：不提供「无窗即全量」——
// 一次全量聚合会把短保留（14 天）的明细表扫穿，也会让「最近 7 天」的页在缺参时
// 静默变成「有史以来」，而运营看到的是同一个标题。
func (h *ClientDistRuntimeHandler) Machines(c *gin.Context) {
	if h.tracking == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "分发追踪服务未初始化"})
		return
	}
	from, to, ok := parseObsRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	res, err := h.tracking.QueryMachines(service.ClientMachineListQuery{
		ChannelID: c.Query("channelId"),
		From:      from,
		To:        to,
		Sort:      c.Query("sort"),
		Order:     c.Query("order"),
		Page:      parseIntDefault(c.Query("page"), 1),
		PageSize:  parseIntDefault(c.Query("pageSize"), 20),
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	h.recordAudit(c, "client_dist_machines.list", map[string]any{"channelId": c.Query("channelId")})
	c.JSON(http.StatusOK, res)
}

// MachineEvents 单台机器的更新时间线（钻取）。
func (h *ClientDistRuntimeHandler) MachineEvents(c *gin.Context) {
	if h.tracking == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "分发追踪服务未初始化"})
		return
	}
	from, to, ok := parseObsRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	machineID := c.Param("machineId")
	res, err := h.tracking.QueryMachineEvents(machineID, c.Query("channelId"), from, to)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": err.Error()})
		return
	}
	h.recordAudit(c, "client_dist_machines.events", map[string]any{"machineId": machineID})
	c.JSON(http.StatusOK, res)
}

// EventDetail 查询单条分发事件脱敏详情。
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

// hasObsRangeParam 报告请求是否显式带了时间窗参数（from/to 任意一个，或 range）。
//
// 用于区分「调用方要按窗口筛」与「调用方根本没传、只想要最近若干条」：
// 后者不该被 400 拒掉，也不该被悄悄默认成 7d（那会改变既有全量语义）。
func hasObsRangeParam(c *gin.Context) bool {
	return c.Query("from") != "" || c.Query("to") != "" || c.Query("range") != ""
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
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	h.audit.RecordSafe(id, action, "client_dist", "", marshalAuditDetail(detail), c.ClientIP())
}
