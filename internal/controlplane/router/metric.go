package router

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/controlplane/service"
)

// MetricHandler 时序指标查询路由（FR-060）。
type MetricHandler struct {
	metricSvc *service.MetricService
	authz     *service.AuthzService
}

// NewMetricHandler 创建时序指标路由处理器。
func NewMetricHandler(metricSvc *service.MetricService, authz *service.AuthzService) *MetricHandler {
	return &MetricHandler{metricSvc: metricSvc, authz: authz}
}

// RegisterRoutes 注册 /metrics 路由。
func (h *MetricHandler) RegisterRoutes(rg *gin.RouterGroup) {
	m := rg.Group("/metrics")
	m.GET("/series", h.Series)
	m.GET("/overview", h.Overview)
}

var metricRangeDurations = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

// Series 返回某节点/实例的历史曲线，按区间自动选档。
// 权限：node 维度对认证用户开放（与既有节点指标暴露一致）；instance 维度按 CanAccessInstance 收敛。
func (h *MetricHandler) Series(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	scope := model.MetricScope(c.Query("scope"))
	targetID := c.Query("targetId")
	if targetID == "" || (scope != model.MetricScopeNode && scope != model.MetricScopeInstance) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SCOPE", "message": "scope 必须为 node 或 instance，且 targetId 非空"})
		return
	}

	resolution := c.Query("resolution")
	switch resolution {
	case "", "auto", "raw", "5m", "1h":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RESOLUTION", "message": "resolution 非法"})
		return
	}

	from, to, ok := parseMetricRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}

	q := service.SeriesQuery{
		Scope:      scope,
		MetricKeys: splitMetricKeys(c.Query("metrics")),
		From:       from,
		To:         to,
		Resolution: resolution,
	}

	switch scope {
	case model.MetricScopeNode:
		exists, err := h.metricSvc.NodeExists(targetID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "节点不存在"})
			return
		}
		q.NodeUUID = targetID
	default: // instance
		id, found, err := h.metricSvc.ResolveInstanceID(targetID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "实例不存在"})
			return
		}
		allowed, err := h.authz.CanAccessInstance(access, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问该实例指标"})
			return
		}
		q.InstanceID = targetID
	}

	res, series, err := h.metricSvc.QuerySeries(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"resolution": res, "from": from, "to": to, "series": series})
}

// Overview 返回总览页跨节点聚合：当前总量 + 聚合曲线（总 CPU 均值 / 总内存 / 总在线玩家）。
// 权限：对认证用户开放（与 node 维度指标一致；仅聚合总量与曲线，不暴露单实例明细）。
func (h *MetricHandler) Overview(c *gin.Context) {
	if access := getAccess(c); access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	resolution := c.Query("resolution")
	switch resolution {
	case "", "auto", "raw", "5m", "1h":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RESOLUTION", "message": "resolution 非法"})
		return
	}

	from, to, ok := parseMetricRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}

	ov, err := h.metricSvc.Overview(from, to, resolution)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, ov)
}

// parseMetricRange 解析查询区间：优先 from/to（RFC3339），否则按 range 枚举回退、默认 24h。
func parseMetricRange(c *gin.Context) (time.Time, time.Time, bool) {
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
		rng = "24h"
	}
	dur, ok := metricRangeDurations[rng]
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return now.Add(-dur), now, true
}

func splitMetricKeys(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
