package router

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

const clientDistExportCooldown = time.Minute

var clientDistExportRanges = map[string]time.Duration{
	"24h":  24 * time.Hour,
	"7d":   7 * 24 * time.Hour,
	"30d":  30 * 24 * time.Hour,
	"90d":  90 * 24 * time.Hour,
	"180d": 180 * 24 * time.Hour,
}

var clientDistExportLogTypes = map[string]bool{
	"": true, "all": true, "hello": true, "risk": true, "action": true,
	"request": true, "runtime": true, "telemetry": true,
}

// ClientDistExportHandler 提供平台管理员 CSV 导出、独立冷却限流和审计。
type ClientDistExportHandler struct {
	svc     *service.ClientDistExportService
	audit   *service.AuditService
	mu      sync.Mutex
	lastUse map[string]time.Time
	now     func() time.Time
}

// NewClientDistExportHandler 创建 CSV 导出处理器。
func NewClientDistExportHandler(svc *service.ClientDistExportService, audit *service.AuditService) *ClientDistExportHandler {
	return &ClientDistExportHandler{svc: svc, audit: audit, lastUse: map[string]time.Time{}, now: time.Now}
}

// RegisterRoutes 注册冻结端点 GET /client-dist/export。
func (h *ClientDistExportHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/export", h.Export)
}

// Export 校验筛选、执行每用户一分钟冷却，并流式写出 CSV。
func (h *ClientDistExportHandler) Export(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	filter, ok := parseClientDistExportFilter(c, h.now().UTC())
	if !ok {
		return
	}
	if !h.allow(c, h.now()) {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "RATE_LIMITED", "message": "CSV 导出每分钟最多一次"})
		return
	}
	truncated, err := h.svc.Truncated(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "统计导出行数失败"})
		return
	}
	h.writeHeaders(c, filter.Kind, truncated)
	if err := h.svc.WriteCSV(c.Writer, filter, truncated); err != nil {
		return
	}
	h.recordAudit(c, filter, truncated)
}

func (h *ClientDistExportHandler) allow(c *gin.Context, now time.Time) bool {
	uid, _ := c.Get(middleware.CtxUserID)
	key := "ip:" + c.ClientIP()
	if id, ok := uid.(uint); ok && id > 0 {
		key = "user:" + strconv.FormatUint(uint64(id), 10)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	last := h.lastUse[key]
	if !last.IsZero() && now.Sub(last) < clientDistExportCooldown {
		return false
	}
	h.lastUse[key] = now
	return true
}

func (h *ClientDistExportHandler) writeHeaders(c *gin.Context, kind string, truncated bool) {
	filename := "client-dist-" + kind + "-" + h.now().UTC().Format("20060102150405") + ".csv"
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	if truncated {
		c.Header("X-Export-Truncated", "true")
	}
	c.Status(http.StatusOK)
}

func (h *ClientDistExportHandler) recordAudit(c *gin.Context, filter service.ClientDistExportFilter, truncated bool) {
	if h.audit == nil {
		return
	}
	detail := marshalAuditDetail(map[string]any{
		"kind": filter.Kind, "channelId": filter.ChannelID, "range": filter.Range,
		"type": filter.LogType, "truncated": truncated,
	})
	h.audit.RecordSafe(getUserID(c), "client_dist.export.csv", "client_dist", filter.Kind, detail, c.ClientIP())
}

func parseClientDistExportFilter(c *gin.Context, now time.Time) (service.ClientDistExportFilter, bool) {
	kind := c.Query("kind")
	if !service.ValidClientDistExportKind(kind) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "kind 非法"})
		return service.ClientDistExportFilter{}, false
	}
	from, to, rng, ok := parseClientDistExportRange(c, now, kind)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "时间窗非法或明细超过 30 天"})
		return service.ClientDistExportFilter{}, false
	}
	eventKind, outcome, logType, version, ok := parseClientDistExportSelectors(c)
	if !ok {
		return service.ClientDistExportFilter{}, false
	}
	return service.ClientDistExportFilter{
		Kind: kind, EventKind: eventKind, ChannelID: c.Query("channelId"), Range: rng, ErrCode: c.Query("errCode"),
		Outcome: outcome, IP: c.Query("ip"), MachineID: c.Query("machineId"), PlayerName: c.Query("playerName"),
		LogType: logType, Version: version, From: from, To: to,
	}, true
}

func parseClientDistExportSelectors(c *gin.Context) (string, string, string, *int, bool) {
	outcome := c.Query("outcome")
	if outcome != "" && outcome != "success" && outcome != "failure" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "outcome 非法"})
		return "", "", "", nil, false
	}
	eventKind := c.Query("eventKind")
	if eventKind != "" && eventKind != "manifest" && eventKind != "artifact" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "eventKind 非法"})
		return "", "", "", nil, false
	}
	logType := c.Query("type")
	if !clientDistExportLogTypes[logType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "type 非法"})
		return "", "", "", nil, false
	}
	version, valid := parseExportVersion(c.Query("version"))
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "version 非法"})
		return "", "", "", nil, false
	}
	return eventKind, outcome, logType, version, true
}

func parseClientDistExportRange(c *gin.Context, now time.Time, kind string) (time.Time, time.Time, string, bool) {
	fromRaw, toRaw := c.Query("from"), c.Query("to")
	if fromRaw != "" || toRaw != "" {
		from, errFrom := time.Parse(time.RFC3339, fromRaw)
		to, errTo := time.Parse(time.RFC3339, toRaw)
		if errFrom != nil || errTo != nil || !to.After(from) || detailRangeTooLong(kind, to.Sub(from)) {
			return time.Time{}, time.Time{}, "", false
		}
		return from.UTC(), to.UTC(), "custom", true
	}
	rng := c.DefaultQuery("range", "7d")
	if daysRaw := c.Query("days"); daysRaw != "" {
		days, err := strconv.Atoi(daysRaw)
		if err != nil || days <= 0 || detailRangeTooLong(kind, time.Duration(days)*24*time.Hour) {
			return time.Time{}, time.Time{}, "", false
		}
		rng = strconv.Itoa(days) + "d"
		return now.Add(-time.Duration(days) * 24 * time.Hour), now, rng, true
	}
	duration, ok := clientDistExportRanges[rng]
	if !ok || detailRangeTooLong(kind, duration) {
		return time.Time{}, time.Time{}, "", false
	}
	return now.Add(-duration), now, rng, true
}

func detailRangeTooLong(kind string, duration time.Duration) bool {
	if duration > 180*24*time.Hour {
		return true
	}
	return kind != service.ClientDistExportStatsSummary && duration > 30*24*time.Hour
}

func parseExportVersion(raw string) (*int, bool) {
	if raw == "" {
		return nil, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return nil, false
	}
	return &value, true
}
