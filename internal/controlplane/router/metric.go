package router

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
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
	m.POST("/series/batch", h.SeriesBatch)
	m.GET("/overview", h.Overview)
	m.GET("/processes/top", h.ProcessTop)
}

// metricBatchMaxTargets 批量序列端点的目标数硬上限（FR-340）。去重后超出即 422。
// 与 processes/top 的 limit 上限（parseProcessTopLimit）对齐取 50。
const metricBatchMaxTargets = 50

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

// seriesBatchRequest 批量序列请求体（FR-340）。POST 承载只读查询：50 个 UUID 入 query 过长，body 语义清晰。
type seriesBatchRequest struct {
	Scope      string   `json:"scope"`
	TargetIDs  []string `json:"targetIds"`
	Metrics    []string `json:"metrics"`
	Range      string   `json:"range"`
	Resolution string   `json:"resolution"`
}

// skippedTarget 被剔除的目标（无权/不存在），随批量响应返回，供前端区分「无数据」与「被剔除」（FR-340）。
type skippedTarget struct {
	TargetID string `json:"targetId"`
	Reason   string `json:"reason"` // forbidden | not_found
}

// SeriesBatch 批量返回多个实例目标的历史曲线，消 NodeInstanceCompare 的 N+1 请求（FR-340）。
// 逐目标复用实例访问收敛（等价 CanAccessInstance，走 AccessibleInstanceIDs 集合判定）：
// 无权/不存在的目标剔除并列入 skipped，不整拒——对比场景个别目标越权不应让整图空白。
// v1 仅支持 scope=instance（对比场景只有实例维度有 N+1，node 单查询无此问题）。
func (h *MetricHandler) SeriesBatch(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	var req seriesBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "请求体非法"})
		return
	}
	if req.Scope != string(model.MetricScopeInstance) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SCOPE", "message": "scope 必须为 instance"})
		return
	}

	targetIDs := dedupeStrings(req.TargetIDs)
	if len(targetIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "targetIds 缺失或为空"})
		return
	}
	if len(targetIDs) > metricBatchMaxTargets {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "TOO_MANY_TARGETS", "message": "对比目标过多，最多 50 个"})
		return
	}

	switch req.Resolution {
	case "", "auto", "raw", "5m", "1h":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RESOLUTION", "message": "resolution 非法"})
		return
	}

	rng := req.Range
	if rng == "" {
		rng = "24h"
	}
	dur, ok := metricRangeDurations[rng]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range 非法"})
		return
	}
	now := time.Now().UTC()
	from, to := now.Add(-dur), now

	// uuid→id 一次解析；解析不到的入 skipped(not_found)。
	idByUUID, err := h.metricSvc.ResolveInstanceIDs(targetIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}

	// 鉴权批量化：一次取可访问实例 id 集（platform admin 返回 scoped=false 全放行），
	// 集合外的目标入 skipped(forbidden)——与逐目标 CanAccessInstance 判定等价但免 N 次查询。
	allowedIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	allowedSet := make(map[uint]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowedSet[id] = struct{}{}
	}

	survivors := make([]string, 0, len(targetIDs))
	skipped := make([]skippedTarget, 0)
	for _, uuid := range targetIDs {
		id, found := idByUUID[uuid]
		if !found {
			skipped = append(skipped, skippedTarget{TargetID: uuid, Reason: "not_found"})
			continue
		}
		if scoped {
			if _, ok := allowedSet[id]; !ok {
				skipped = append(skipped, skippedTarget{TargetID: uuid, Reason: "forbidden"})
				continue
			}
		}
		survivors = append(survivors, uuid)
	}

	q := service.SeriesQuery{
		Scope:      model.MetricScopeInstance,
		MetricKeys: req.Metrics,
		From:       from,
		To:         to,
		Resolution: req.Resolution,
	}
	res, series, err := h.metricSvc.QuerySeriesBatch(q, survivors)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"resolution": res, "from": from, "to": to, "series": series, "skipped": skipped})
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

// ProcessTop 返回受管实例进程 TOPN 快照（FR-170）。
func (h *MetricHandler) ProcessTop(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}

	q := service.ProcessTopQuery{
		NodeUUID: c.Query("nodeId"),
		Sort:     c.DefaultQuery("sort", "cpu"),
		Limit:    parseProcessTopLimit(c.Query("limit")),
	}
	if q.Sort != "cpu" && q.Sort != "memory" && q.Sort != "io" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SORT", "message": "sort 必须为 cpu、memory 或 io"})
		return
	}

	if instanceID := c.Query("instanceId"); instanceID != "" {
		id64, err := strconv.ParseUint(instanceID, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_INSTANCE", "message": "instanceId 非法"})
			return
		}
		id := uint(id64)
		allowed, err := h.authz.CanAccessInstance(access, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !allowed {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问该实例进程指标"})
			return
		}
		uuid, found, err := h.metricSvc.ResolveInstanceUUID(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "实例不存在"})
			return
		}
		q.InstanceUUID = uuid
	} else if !access.IsPlatformAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "请指定可访问的 instanceId"})
		return
	}

	items, err := h.metricSvc.QueryProcessTop(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, items)
}

func parseProcessTopLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > 50 {
		return 10
	}
	return n
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

// dedupeStrings 去重并保序，剔除空白项（批量目标 UUID 归一，FR-340）。
func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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
