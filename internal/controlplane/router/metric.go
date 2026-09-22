package router

import (
	"errors"
	"math"
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
	// capacityTrend 容量趋势告警器（FR-464）；nil 时预测仍返回、仅不发告警。
	capacityTrend *service.CapacityTrendAlerter
}

// NewMetricHandler 创建时序指标路由处理器。
func NewMetricHandler(metricSvc *service.MetricService, authz *service.AuthzService, capacityTrend *service.CapacityTrendAlerter) *MetricHandler {
	return &MetricHandler{metricSvc: metricSvc, authz: authz, capacityTrend: capacityTrend}
}

// RegisterRoutes 注册 /metrics 路由。
func (h *MetricHandler) RegisterRoutes(rg *gin.RouterGroup) {
	m := rg.Group("/metrics")
	m.GET("/series", h.Series)
	m.POST("/series/batch", h.SeriesBatch)
	m.GET("/bot-runtime", h.BotRuntime)
	m.GET("/overview", h.Overview)
	m.GET("/resource-attribution", h.ResourceAttribution)
	m.GET("/processes/top", h.ProcessTop)
	m.GET("/performance/attribution", h.PerformanceAttribution)
	m.GET("/instances/ranking", h.InstanceRanking)
	m.GET("/players/trend", h.PlayerTrend)
	m.GET("/slo", h.SLO)
	m.GET("/capacity/forecast", h.CapacityForecast)
}

// BotRuntime 返回节点关联的共享 Bot Worker 历史观测；不将进程资源归属到单个 Bot 或会话。
func (h *MetricHandler) BotRuntime(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	query, ok := parseBotRuntimeQuery(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_ARGUMENT", "message": "关联目标、时间范围或分辨率非法"})
		return
	}
	if err := h.authorizeBotRuntime(access, query); err != nil {
		h.writeBotRuntimeAuthorizationError(c, err)
		return
	}
	result, err := h.metricSvc.QueryBotRuntime(query)
	if errors.Is(err, service.ErrBotRuntimeTargetNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "关联目标不存在"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询共享 Bot Worker 指标失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func parseBotRuntimeQuery(c *gin.Context) (service.BotRuntimeQuery, bool) {
	_, hasFrom := c.GetQuery("from")
	_, hasTo := c.GetQuery("to")
	if hasFrom != hasTo {
		return service.BotRuntimeQuery{}, false
	}
	nodeID, nodePresent, ok := parseOptionalPositiveMetricID(c, "nodeId")
	if !ok {
		return service.BotRuntimeQuery{}, false
	}
	instanceID, instancePresent, ok := parseOptionalPositiveMetricID(c, "instanceId")
	if !ok {
		return service.BotRuntimeQuery{}, false
	}
	sessionID, sessionPresent, ok := parseOptionalPositiveMetricID(c, "sessionId")
	if !ok || boolCount(nodePresent, instancePresent, sessionPresent) > 1 {
		return service.BotRuntimeQuery{}, false
	}
	resolution := c.DefaultQuery("resolution", "auto")
	switch resolution {
	case "auto", "raw", "5m", "1h":
	default:
		return service.BotRuntimeQuery{}, false
	}
	from, to, ok := parseMetricRange(c)
	if !ok {
		return service.BotRuntimeQuery{}, false
	}
	return service.BotRuntimeQuery{NodeID: nodeID, InstanceID: instanceID, SessionID: sessionID, From: from, To: to, Resolution: resolution}, true
}

func parseOptionalPositiveMetricID(c *gin.Context, key string) (uint, bool, bool) {
	raw, present := c.GetQuery(key)
	if !present {
		return 0, false, true
	}
	id, err := strconv.ParseUint(raw, 10, 0)
	if err != nil || id == 0 {
		return 0, true, false
	}
	return uint(id), true, true
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func (h *MetricHandler) authorizeBotRuntime(access *service.UserAccess, query service.BotRuntimeQuery) error {
	if query.NodeID != 0 {
		return nil
	}
	if query.InstanceID != 0 {
		return h.authorizeBotRuntimeInstance(access, query.InstanceID)
	}
	if query.SessionID != 0 {
		instanceID, found, err := h.metricSvc.ResolveBotRuntimeSessionInstanceID(query.SessionID)
		if err != nil {
			return err
		}
		if !found {
			return service.ErrBotRuntimeTargetNotFound
		}
		return h.authorizeBotRuntimeInstance(access, instanceID)
	}
	if !access.IsPlatformAdmin {
		return errBotRuntimeForbidden
	}
	return nil
}

var errBotRuntimeForbidden = errors.New("无权访问共享 Bot Worker 指标")

func (h *MetricHandler) authorizeBotRuntimeInstance(access *service.UserAccess, instanceID uint) error {
	_, found, err := h.metricSvc.ResolveInstanceUUID(instanceID)
	if err != nil {
		return err
	}
	if !found {
		return service.ErrBotRuntimeTargetNotFound
	}
	allowed, err := h.authz.CanAccessInstance(access, instanceID)
	if err != nil {
		return err
	}
	if !allowed {
		return errBotRuntimeForbidden
	}
	return nil
}

func (h *MetricHandler) writeBotRuntimeAuthorizationError(c *gin.Context, err error) {
	if errors.Is(err, errBotRuntimeForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问关联实例的共享 Bot Worker 指标"})
		return
	}
	if errors.Is(err, service.ErrBotRuntimeTargetNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "关联目标不存在"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询关联权限失败"})
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
	"1y":  365 * 24 * time.Hour,
}

// metricRankingWindows 排行 window 参数白名单（自审 S4）。
//
// 原先用 `time.ParseDuration` 直接接受任意时长（如 `1h30m`、`90s`），
// 这会让「窗口跨度 → 样本档」的选择出现非预期组合，也让前端选项与后端可取值脱钩。
// 白名单与 metricRangeDurations 的键一致，便于前后端共用同一套语义。
var metricRankingWindows = map[string]time.Duration{
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
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

// ResourceAttribution 返回首页 Tooltip 使用的有界受管资源归因（FR-400）。
// 节点和跨实例进程明细只对平台管理员开放，避免扩展原 overview 的聚合权限边界。
func (h *MetricHandler) ResourceAttribution(c *gin.Context) {
	if !requirePlatformAdmin(c) {
		return
	}
	sortBy := c.DefaultQuery("sort", "memory")
	if sortBy != "cpu" && sortBy != "memory" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_ARGUMENT", "message": "sort 必须为 cpu 或 memory"})
		return
	}
	limit := 5
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 10 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_ARGUMENT", "message": "limit 必须为 1 到 10"})
			return
		}
		limit = parsed
	}
	result, err := h.metricSvc.ResourceAttribution(service.ResourceAttributionQuery{Sort: sortBy, Limit: limit})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询受管资源归因失败"})
		return
	}
	c.JSON(http.StatusOK, result)
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
	// m4：nodeId 是节点 UUID，须与 Series/SLO/CapacityForecast 同口径先验存在性。
	// 行为上不越权（不存在的 UUID 只让 SQL 过滤条件恒假 → 空结果），但「静默返回空」
	// 与「目标不存在」在前端不可区分：运维会以为「该节点当前没有受管进程」，
	// 而真相可能是选错了节点或节点已被删除。故补 404，与 authorizeInstanceTarget 的
	// TARGET_NOT_FOUND 语义对齐（此处不涉权限，仅存在性）。
	if q.NodeUUID != "" {
		exists, err := h.metricSvc.NodeExists(q.NodeUUID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "节点不存在"})
			return
		}
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

// authorizeInstanceTarget 校验实例维度查询目标：解析 UUID → 数值 ID，再按 CanAccessInstance 收敛。
// 返回 (实例 UUID, 是否通过)；未通过时已写好 404/403 响应，调用方直接 return。
func (h *MetricHandler) authorizeInstanceTarget(c *gin.Context, access *service.UserAccess, targetID string) (string, bool) {
	if targetID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TARGET", "message": "targetId 非空"})
		return "", false
	}
	id, found, err := h.metricSvc.ResolveInstanceID(targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return "", false
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "实例不存在"})
		return "", false
	}
	allowed, err := h.authz.CanAccessInstance(access, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return "", false
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问该实例指标"})
		return "", false
	}
	return targetID, true
}

// PerformanceAttribution 返回指定实例窗口内目标指标（默认 TPS）劣化的主要贡献因子与权重（FR-465）。
// 权限：instance 维度按 CanAccessInstance 收敛（与 Series 同口径）。
func (h *MetricHandler) PerformanceAttribution(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	if scope := c.DefaultQuery("scope", string(model.MetricScopeInstance)); scope != string(model.MetricScopeInstance) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SCOPE", "message": "scope 必须为 instance"})
		return
	}
	from, to, ok := parseMetricRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	targetID, ok := h.authorizeInstanceTarget(c, access, c.Query("targetId"))
	if !ok {
		return
	}
	res, err := h.metricSvc.AnalyzeAttribution(service.AttributionQuery{
		InstanceID: targetID,
		Target:     c.Query("metric"),
		From:       from,
		To:         to,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// metricRankingWindowMax 排行代表值窗口上限（30d：超出则样本档与窗口语义失真）。
const metricRankingWindowMax = 30 * 24 * time.Hour

// InstanceRanking 返回跨节点全量实例某指标代表值的排行（FR-469）。
// 权限：非管理员按 AccessibleInstanceIDs 在集合内排行（scoped=true），不整拒。
func (h *MetricHandler) InstanceRanking(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	metricKey := c.DefaultQuery("metric", model.MetricInstTPS)
	if !service.RankingSupportedMetric(metricKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_METRIC", "message": "metric 不在支持列表内"})
		return
	}
	order := c.Query("order")
	if order != "" && order != "asc" && order != "desc" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_ORDER", "message": "order 必须为 asc 或 desc"})
		return
	}
	window := 5 * time.Minute
	if raw := c.Query("window"); raw != "" {
		// S4 修复：白名单化，不再接受任意 time.ParseDuration（如 1h30m/90s）。
		d, ok := metricRankingWindows[raw]
		if !ok || d <= 0 || d > metricRankingWindowMax {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_WINDOW", "message": "window 必须为 5m|15m|1h|6h|24h|7d|30d"})
			return
		}
		window = d
	}
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_LIMIT", "message": "limit 必须为 1 到 100"})
			return
		}
		limit = n
	}
	nodeUUID := c.Query("nodeId")
	if nodeUUID != "" {
		exists, err := h.metricSvc.NodeExists(nodeUUID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "节点不存在"})
			return
		}
	}
	allowedIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	res, err := h.metricSvc.InstanceRanking(service.RankingQuery{
		MetricKey: metricKey, Order: order, Window: window, Limit: limit,
		NodeUUID: nodeUUID, AllowedIDs: allowedIDs, Scoped: scoped,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// PlayerTrend 返回全网玩家在线趋势 + 24 时段分布 + 峰值/日均（FR-469）。
// 权限：聚合总量对认证用户开放（与 Overview 一致，不暴露单实例明细）；时段按时区口径分桶。
func (h *MetricHandler) PlayerTrend(c *gin.Context) {
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
	// M4 修复：tz 解析失败回退 UTC，不再 400。
	//
	// 原先直接 400 有两个问题：① 官方容器镜像（alpine，无 tzdata）与部分裸机/容器
	// 缺 /usr/share/zoneinfo，`time.LoadLocation` 对**任何合法时区名**都会失败——
	// 前端每次都下发浏览器时区，于是官方部署下该卡片必然 400，属「部署形态决定可用性」的缺陷；
	// ② 即便时区库齐全，前端损坏的时区串也不该让整张卡片不可用。
	// 现在：入口内嵌 time/tzdata 保证合法名必可解析；仍解析失败（真正非法名）时回退 UTC，
	// 由响应 `timezone` 字段如实回显实际生效时区，前端据此展示，用户能看出发生了回退。
	var loc *time.Location
	tzFallback := ""
	if tz := c.Query("tz"); tz != "" {
		parsed, err := time.LoadLocation(tz)
		if err != nil {
			loc = time.UTC
			tzFallback = tz
		} else {
			loc = parsed
		}
	}
	from, to, ok := parseMetricRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	res, err := h.metricSvc.PlayerTrend(service.PlayerTrendQuery{
		From: from, To: to, Resolution: resolution, Location: loc,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	if tzFallback != "" {
		// m6：回显前截断。tzFallback 是**未净化的用户输入**，Gin 的 JSON 编码会正确转义
		// （无注入风险），但原实现无长度上限——`?tz=<1MB 串>` 会原样进入响应体，
		// 白耗带宽/内存且让响应体尺寸由调用方决定。
		// 截断只影响诊断提示的可读长度，不影响实际生效时区（已固定为 UTC）。
		// 用 len 而非 rune 计数：这里只需一个有界字节数，按字节截断更简单，
		// 且截断点落在多字节字符中间时 encoding/json 会用 U+FFFD 替换，不会产生非法 JSON。
		res.Timezone = time.UTC.String() + " (fallback from " + truncateForEcho(tzFallback, tzFallbackMaxLen) + ")"
	}
	c.JSON(http.StatusOK, res)
}

// tzFallbackMaxLen tz 回退提示里回显原值时的最大字节数。
// 合法 IANA 时区名最长约 32 字节（如 "America/Argentina/ComodRivadavia"），
// 64 留足余量，又能把响应体尺寸钉在常数级。
const tzFallbackMaxLen = 64

// truncateForEcho 把用户输入截断到至多 max 字节，并在截断时追加省略号标记，
// 让调用方一眼看出「这里显示的不是完整原值」——静默截断会被误读为「用户下发就是这个串」。
func truncateForEcho(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// SLO 返回窗口内可用率、故障次数、MTTR/MTBF 与误差预算（FR-463）。
// 权限：platform/node 维度对认证用户开放（platform 汇总对非管理员收敛到可访问实例）；
// instance 维度按 CanAccessInstance 收敛（无权 403）。
func (h *MetricHandler) SLO(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	scope := model.MetricScope(c.DefaultQuery("scope", string(model.MetricScopePlatform)))
	switch scope {
	case model.MetricScopePlatform, model.MetricScopeNode, model.MetricScopeInstance:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SCOPE", "message": "scope 必须为 platform、node 或 instance"})
		return
	}
	targetID := c.Query("targetId")
	if scope != model.MetricScopePlatform && targetID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SCOPE", "message": "node/instance 维度要求 targetId 非空"})
		return
	}
	from, to, ok := parseMetricRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	target := 0.0
	if raw := c.Query("target"); raw != "" {
		v, ok := parseFiniteFloat(raw, func(v float64) bool { return v > 0 && v <= 1 })
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_TARGET", "message": "target 必须为 (0,1] 内的有限小数"})
			return
		}
		target = v
	}

	q := service.SLOQuery{Scope: scope, From: from, To: to, Target: target}
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
	case model.MetricScopeInstance:
		uuid, ok := h.authorizeInstanceTarget(c, access, targetID)
		if !ok {
			return
		}
		q.InstanceID = uuid
	default: // platform：非管理员收敛到可访问实例集合
		allowedIDs, scoped, err := h.authz.AccessibleInstanceIDs(access)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return
		}
		if scoped {
			uuids, err := h.metricSvc.ResolveInstanceUUIDs(allowedIDs)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
				return
			}
			q.InstanceUUIDs = uuids
		}
	}

	res, err := h.metricSvc.ComputeSLO(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, res)
}

// parseFiniteFloat 解析一个必须为**有限**实数的查询参数，并按 given 谓词校验取值范围。
//
// M-3 修复（为什么不能写成 `err != nil || v <= 0`）：`strconv.ParseFloat("NaN", 64)`
// 会**成功**返回 (NaN, nil)，而 NaN 与任何数的比较恒为 false——
//   - `thresholdDays=NaN`：`v <= 0` 为 false → 被接受 → `Notify` 的判据
//     `*fr.ExhaustLowDays >= thresholdDays` 也恒为 false → **所有**带 ExhaustLowDays 的预测
//     都被判为「命中阈值」，一个普通用户即可把趋势告警变成每 6h × 每指标 × 每目标的告警洪泛；
//   - `target=NaN`：`v <= 0 || v > 1` 两个条件都不成立 → 被接受 → `SLOQuery.Target = NaN`
//     → `budgetAllowed = span*(1-NaN) = NaN` → `c.JSON` 编不出 NaN，写出 **200 + 空 body**
//     （调用方既拿不到数据也拿不到错误，比 500 更难排查）。
//
// `±Inf` 同族：`ParseFloat("Inf")` 也返回 nil err，且 `+Inf <= 0` 为 false。
//
// 注意：服务层已有出口侧兜底（`sloNormalizeTarget`），但入口层不该是唯一防线、
// 也不该是唯一「被绕过也无所谓」的一层——NaN 让**语义**（而非数值）失效，
// 必须在最靠近用户输入处拒绝。
func parseFiniteFloat(raw string, ok func(float64) bool) (float64, bool) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if !ok(v) {
		return 0, false
	}
	return v, true
}

// metricForecastMaxKeys capacity/forecast 的 `metrics` 参数条数上限（FR-464）。
//
// M-4 修复：`splitMetricKeys` 原先不做条数限制，而 `ForecastCapacity` 对**每个** key
// 各调一次 `forecastPoints`（1 次 QuerySeries）+ `forecastLimit`（1 次 QuerySeries，
// 可能再回退到节点快照查询）——即每个 key ≈ 2~3 条 SELECT。
// 审查员实测：`metrics` 传 200 个键 → 返回 200 条、发出 200 条 SELECT。
// query string 长度受服务器 header 上限约束（实测可达数百个 key），
// 配合前端 60s 轮询即形成持续的 DB 压力放大。
//
// 取值 8（与「node 维默认 2 个键、instance 维默认 1 个键」拉开余量，
// 同时把上界压到 8×3=24 条 SELECT/请求的常数级）：比 metricBatchMaxTargets=50 更严，
// 因为那两个端点的单个目标成本是 1 次查询、这里单个 key 是 2~3 次。
const metricForecastMaxKeys = 8

// CapacityForecast 返回关键资源的耗尽预测与 80% 置信区间（FR-464）。
// 命中「预计 N 天内耗尽」时经告警体系发趋势告警（去抖）；thresholdDays 可调（默认 7）。
func (h *MetricHandler) CapacityForecast(c *gin.Context) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return
	}
	scope := model.MetricScope(c.DefaultQuery("scope", string(model.MetricScopeNode)))
	targetID := c.Query("targetId")
	if targetID == "" || (scope != model.MetricScopeNode && scope != model.MetricScopeInstance) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_SCOPE", "message": "scope 必须为 node 或 instance，且 targetId 非空"})
		return
	}
	// M-4：条数上限在校验 range/thresholdDays **之前**判——它是纯入参形状校验，
	// 先拒掉能避免为一个注定 422 的请求白跑后续解析。
	metricKeys := dedupeStrings(splitMetricKeys(c.Query("metrics")))
	if len(metricKeys) > metricForecastMaxKeys {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "TOO_MANY_METRICS",
			"message": "预测指标过多，最多 8 个"})
		return
	}
	from, to, ok := parseMetricRange(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_RANGE", "message": "range/from/to 非法"})
		return
	}
	thresholdDays := service.CapacityTrendThresholdDays
	if raw := c.Query("thresholdDays"); raw != "" {
		v, ok := parseFiniteFloat(raw, func(v float64) bool { return v > 0 })
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_THRESHOLD", "message": "thresholdDays 必须为正的有限数"})
			return
		}
		thresholdDays = v
	}

	q := service.ForecastQuery{Scope: scope, Metrics: metricKeys, From: from, To: to}
	var targetName string
	if scope == model.MetricScopeNode {
		node, ok := h.metricSvc.NodeByUUID(targetID)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "TARGET_NOT_FOUND", "message": "节点不存在"})
			return
		}
		q.NodeUUID, targetName = targetID, node.Name
	} else {
		uuid, ok := h.authorizeInstanceTarget(c, access, targetID)
		if !ok {
			return
		}
		q.InstanceID = uuid
		id, _, _ := h.metricSvc.ResolveInstanceID(uuid)
		if inst, ok := h.metricSvc.InstanceByID(id); ok {
			targetName = inst.Name
		}
	}

	results, err := h.metricSvc.ForecastCapacity(q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
		return
	}
	if h.capacityTrend != nil {
		id := uint(0)
		if scope == model.MetricScopeNode {
			id, _ = h.metricSvc.NodeIDByUUID(targetID)
		} else {
			id, _, _ = h.metricSvc.ResolveInstanceID(q.InstanceID)
		}
		h.capacityTrend.Notify(scope, id, targetName, results, thresholdDays)
	}
	c.JSON(http.StatusOK, gin.H{"forecasts": results})
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
