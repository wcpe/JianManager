package router

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ClientDistObservabilityHandler 客户端分发观测查询端点（FR-217，见 ADR-049）。限平台管理员 + 审计。
type ClientDistObservabilityHandler struct {
	svc   *service.ClientDistObservabilityService
	audit *service.AuditService
}

// NewClientDistObservabilityHandler 创建观测查询处理器。audit 可为 nil（审计随之关闭）。
func NewClientDistObservabilityHandler(svc *service.ClientDistObservabilityService, audit *service.AuditService) *ClientDistObservabilityHandler {
	return &ClientDistObservabilityHandler{svc: svc, audit: audit}
}

// obsRangeDurations 无 from/to 时按 range 枚举回退的区间。
var obsRangeDurations = map[string]time.Duration{
	"24h":  24 * time.Hour,
	"7d":   7 * 24 * time.Hour,
	"30d":  30 * 24 * time.Hour,
	"90d":  90 * 24 * time.Hour,
	"180d": 180 * 24 * time.Hour,
}

// RegisterRoutes 注册观测端点（须挂 JWT 平台管理员组）。
func (h *ClientDistObservabilityHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/client-dist/observability", h.Query)
}

// Query GET /client-dist/observability?channelId=&from=&to=&range= —
// 客户端分发观测时序 + 区间分布聚合 + 汇总标量（总 / 单频道 / 时间范围）。
func (h *ClientDistObservabilityHandler) Query(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	from, to, ok := parseObsRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	channelID := c.Query("channelId")
	res, err := h.svc.Query(service.ObservabilityQuery{ChannelID: channelID, From: from, To: to})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "观测聚合失败"})
		return
	}
	h.recordAudit(c, channelID)
	c.JSON(http.StatusOK, res)
}

// recordAudit 记录观测查询审计（观测数据含 IP/机器码维度，访问留痕；detail 仅元数据）。
func (h *ClientDistObservabilityHandler) recordAudit(c *gin.Context, channelID string) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(map[string]any{"channelId": channelID})
	uid, _ := c.Get(middleware.CtxUserID)
	id, _ := uid.(uint)
	_ = h.audit.Record(id, "client_dist_observability.query", "client_channel", channelID, string(raw), c.ClientIP())
}

// parseObsRange 解析查询区间：优先 from/to（RFC3339），否则按 range 枚举回退、默认 7d。
func parseObsRange(c *gin.Context) (time.Time, time.Time, bool) {
	now := time.Now().UTC()
	if fromStr, toStr := c.Query("from"), c.Query("to"); fromStr != "" && toStr != "" {
		f, e1 := time.Parse(time.RFC3339, fromStr)
		t, e2 := time.Parse(time.RFC3339, toStr)
		if e1 != nil || e2 != nil || !t.After(f) {
			return time.Time{}, time.Time{}, false
		}
		return f.UTC(), t.UTC(), true
	}
	rng := c.Query("range")
	if rng == "" {
		rng = "7d"
	}
	dur, ok := obsRangeDurations[rng]
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return now.Add(-dur), now, true
}
