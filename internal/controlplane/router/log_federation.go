package router

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// federationAllTargets 平台管理员未指定实例/节点时的通配授权标记。
// TargetResolver 可将其展开为全部已注册日志目标。
const federationAllTargets = "*"

// testLogCoord 测试注入的协调器；生产经 Services.LogCoord 装配。
// 置 nil 后 federation 端点不注册（与其它可选服务一致）。
var testLogCoord *logcoord.Coordinator

// LogFederationHandler FR-480 CP 跨 Worker 日志联邦查询 HTTP 面。
//
// 权限复用日志中心：路由组 RequireAnyPerm("log.read")；
// Handler 内平台管理员可查全平台/指定目标，非管理员收敛到可访问实例。
type LogFederationHandler struct {
	coord *logcoord.Coordinator
	authz *service.AuthzService
}

// NewLogFederationHandler 创建联邦查询处理器。
func NewLogFederationHandler(coord *logcoord.Coordinator, authz *service.AuthzService) *LogFederationHandler {
	return &LogFederationHandler{coord: coord, authz: authz}
}

// RegisterRoutes 注册联邦查询路由（挂在 log.read 权限组下）。
func (h *LogFederationHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.coord == nil {
		return
	}
	// 门面：LogsPage/双路径客户端探测 GET /logs/federation（FR-482）。
	// 语义 = Search + envelope（ok/sourceTag/coverage/quality/items/notes）。
	rg.GET("/logs/federation", h.FacadeSearch)
	rg.GET("/logs/federation/search", h.Search)
	rg.GET("/logs/federation/tail", h.Tail)
	rg.GET("/logs/federation/stats", h.Stats)
	rg.GET("/logs/federation/fields", h.Fields)
	rg.GET("/logs/federation/facets", h.Facets)
	rg.POST("/logs/federation/export", h.Export)
}

// FacadeSearch 前端门面：Search 结果包装为双路径 envelope。
// coverage/quality 保持 FR-473 snake_case wire 形态，由前端 adapter 归一。
func (h *LogFederationHandler) FacadeSearch(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	resp, err := h.coord.Search(c.Request.Context(), q)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	notes := make([]string, 0, 4)
	if !resp.Coverage.Complete {
		notes = append(notes, resp.Coverage.PartialReasons...)
	}
	if resp.BudgetCut {
		notes = append(notes, "budget_exceeded")
	}
	if !resp.Coverage.Complete || strings.EqualFold(string(resp.Quality.DuplicateQuality), "unresolved") ||
		strings.EqualFold(string(resp.Quality.DuplicateQuality), "conflict") {
		notes = append(notes, "export_incomplete")
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"sourceTag":   "federated",
		"items":       resp.Items,
		"coverage":    resp.Coverage,
		"quality":     resp.Quality,
		"view":        resp.View,
		"exhausted":   resp.Exhausted,
		"budget_cut":  resp.BudgetCut,
		"next_cursor": resp.NextCursor,
		"notes":       notes,
	})
}

func instanceTargetID(id uint) string { return fmt.Sprintf("inst:%d", id) }
func nodeTargetID(id uint) string     { return fmt.Sprintf("node:%d", id) }

// federationPrincipalKey 构造 Query View 的授权主体键（用户 + 角色 + 目标集摘要）。
// 同一用户授权集合变化时 view 不可复用（目标集进入摘要）。
func federationPrincipalKey(access *service.UserAccess, targets []string) string {
	if access == nil {
		return ""
	}
	sorted := append([]string(nil), targets...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "|")))
	return fmt.Sprintf("user:%d|role:%d|targets:%x", access.UserID, access.Role, sum[:8])
}

// federationPermissionScope 稳定权限范围摘要（F-008）。
//
// 由平台管理员标记、角色键、AuthVersion、权限树节点集合与授权目标共同构成；
// 权限范围变化而目标集合未变时，旧 View 复用必须失败。
func federationPermissionScope(access *service.UserAccess, targets []string) string {
	if access == nil {
		return ""
	}
	kind := "scoped"
	if access.IsPlatformAdmin {
		kind = "platform_admin"
	}
	nodes := make([]string, 0, len(access.Nodes))
	for n := range access.Nodes {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	nsum := sha256.Sum256([]byte(strings.Join(nodes, ",")))
	sorted := append([]string(nil), targets...)
	sort.Strings(sorted)
	tsum := sha256.Sum256([]byte(strings.Join(sorted, "|")))
	return fmt.Sprintf("%s|role:%s|auth:%d|nodes:%x|targets:%x",
		kind, access.RoleKey, access.AuthVersion, nsum[:6], tsum[:6])
}

// federationFilter 把结构化筛选参数组合为 LogsQL 过滤表达式。
//
// 背景：wire 契约（LogQueryRequestBase）只有单一 filter 表达式字段，没有结构化的
// level / source 等参数。经典 /logs 路径支持按级别与来源筛选（log.go 的 buildFilter），
// 联邦路径若不在此组合，用户在联邦视图点「错误」或选「来源」不会有任何反应——
// 同一界面两条路径行为不一致。
//
// 组合时必须给 keyword 加括号：LogsQL 中 AND 优先级高于 OR，`a OR b AND level:X`
// 会被解析为 `a OR (b AND level:X)`，使级别筛选对含 OR 的关键词失效。实测
// `LIVE-TEST OR Notch AND level:INFO` 命中 4 条，而 `(LIVE-TEST OR Notch) AND level:INFO`
// 命中 2 条——故存在结构化约束时把 keyword 包成 `(keyword) AND ...`。
//
// level 与 source 均走白名单：它们来自用户输入，直接拼进表达式会引入注入面。
func federationFilter(c *gin.Context) string {
	keyword := strings.TrimSpace(c.Query("keyword"))

	terms := make([]string, 0, 2)
	if level := strings.ToLower(strings.TrimSpace(c.Query("level"))); isFederationLevel(level) {
		// VL 中级别以大写存储（ERROR/INFO/WARN）；LogsQL 大小写敏感，故统一转大写。
		terms = append(terms, "level:"+strings.ToUpper(level))
	}
	if prefix, ok := federationSourcePrefix(strings.ToLower(strings.TrimSpace(c.Query("source")))); ok {
		terms = append(terms, `log_source_id:"`+prefix+`"`)
	}

	if len(terms) == 0 {
		// 无结构化约束：保持既有行为，keyword 原样下发（其内部布尔结构由用户自负）。
		return keyword
	}
	joined := strings.Join(terms, " AND ")
	if keyword == "" {
		return joined
	}
	return "(" + keyword + ") AND " + joined
}

// isFederationLevel 白名单校验：仅接受契约登记的四个级别。
func isFederationLevel(v string) bool {
	switch v {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// federationSourcePrefix 把前端来源类别映射为 log_source_id 前缀。
//
// 事件结构（SourceIdentity）只有 log_source_id、不含类别字段；前端下拉取值域为
// model.LogSource 的 instance/control_plane/worker，与事件实际取值不同构，故按前缀
// 推导对齐。生产实证取值形如 `inst:<实例ID>/<流>`（构造点 worker/logs/ingest/instances.go）。
// VL 的字段匹配即前缀语义：实测 `log_source_id:"inst:"` 与 `"node:"` 分别命中
// 354797 / 0 条（生产仅实例类），故不带通配符即可完成前缀过滤。
//
// 取值走白名单：来自用户输入，须防注入。
func federationSourcePrefix(v string) (string, bool) {
	switch v {
	case "instance":
		// 实例进程 stdout/stderr 日志，采集侧以 inst:<实例ID> 登记。
		return "inst:", true
	case "worker":
		// Worker/Node 自身日志，采集侧以 node:<节点ID> 登记（见 configs/worker.yml 示例）。
		return "node:", true
	case "control_plane":
		// CP 自身结构化日志只落 CP 库、不进联邦数据面，该类别在联邦视图无匹配源。
		// 显式给一个不可能存在的前缀，使联邦视图诚实地返回零结果，
		// 而非当作「无约束」放行全量。
		return "control_plane:", true
	default:
		return "", false
	}
}

// buildQuery 从查询参数构造 logcoord.Query，并完成授权目标收敛。
// 返回 (query, ok)；ok=false 表示已写出错误响应。
func (h *LogFederationHandler) buildQuery(c *gin.Context) (logcoord.Query, bool) {
	access := getAccess(c)
	if access == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "权限不足"})
		return logcoord.Query{}, false
	}

	targets, ok := h.authorizedTargets(c, access)
	if !ok {
		return logcoord.Query{}, false
	}

	q := logcoord.Query{
		RequestID:           c.GetHeader("X-Request-ID"),
		FromUTC:             c.Query("from"),
		ToUTC:               c.Query("to"),
		AuthorizedTargetIDs: targets,
		// F-001：Query View 绑定调用方主体，禁止跨用户复用 viewId。
		PrincipalKey: federationPrincipalKey(access, targets),
		// F-008：稳定 PermissionScope 摘要（权限节点/角色/授权版本/目标），供 View 复用校验。
		PermissionScope: federationPermissionScope(access, targets),
		OnlineOnly:      parseBoolParam(c.Query("onlineOnly")),
		Filter:          federationFilter(c),
		ViewID:          c.Query("viewId"),
		Cursor:          c.Query("cursor"),
	}
	q.Budget.Limit = parseIntDefault(c.Query("limit"), 0)
	if q.Budget.Limit < 0 {
		q.Budget.Limit = 0
	}
	return q, true
}

// authorizedTargets 计算调用方授权目标集合（平台管理员或可访问实例范围）。
func (h *LogFederationHandler) authorizedTargets(c *gin.Context, access *service.UserAccess) ([]string, bool) {
	instFilter := c.Query("instanceId")
	nodeFilter := c.Query("nodeId")

	// 非平台管理员：强制收敛到可访问实例；显式 instanceId 必须落在授权集合内。
	if !access.IsPlatformAdmin {
		ids, constrained, err := h.authz.AccessibleInstanceIDs(access)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询失败"})
			return nil, false
		}
		allowed := make(map[uint]bool)
		if constrained {
			for _, id := range ids {
				allowed[id] = true
			}
		}
		if instFilter != "" {
			id, err := strconv.ParseUint(instFilter, 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "instanceId 无效"})
				return nil, false
			}
			if !constrained || !allowed[uint(id)] {
				c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无权访问该实例日志"})
				return nil, false
			}
			return []string{instanceTargetID(uint(id))}, true
		}
		if nodeFilter != "" {
			// 非管理员不开放按节点全量联邦（越权侧信道）。
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "仅平台管理员可按节点联邦查询"})
			return nil, false
		}
		// 无显式过滤：收敛到可访问实例集合。
		if !constrained {
			// Authz 未声明收敛时禁止回落到全平台 "*"（契约：强制收敛）。
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无授权日志目标"})
			return nil, false
		}
		if len(ids) == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN", "message": "无授权日志目标"})
			return nil, false
		}
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, instanceTargetID(id))
		}
		return out, true
	}

	// 平台管理员
	if instFilter != "" {
		id, err := strconv.ParseUint(instFilter, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "instanceId 无效"})
			return nil, false
		}
		return []string{instanceTargetID(uint(id))}, true
	}
	if nodeFilter != "" {
		id, err := strconv.ParseUint(nodeFilter, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "nodeId 无效"})
			return nil, false
		}
		return []string{nodeTargetID(uint(id))}, true
	}
	return []string{federationAllTargets}, true
}

func parseBoolParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Search 跨 Worker 日志联邦检索。
func (h *LogFederationHandler) Search(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	resp, err := h.coord.Search(c.Request.Context(), q)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *LogFederationHandler) Tail(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	mode := strings.ToUpper(strings.TrimSpace(c.DefaultQuery("mode", "FOLLOW_LIVE")))
	resp, err := h.coord.Tail(c.Request.Context(), q, mode)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *LogFederationHandler) Fields(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	resp, err := h.coord.Fields(c.Request.Context(), q)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Stats 跨 Worker 可合并聚合。
func (h *LogFederationHandler) Stats(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	sq := logcoord.StatsQuery{
		Query:       q,
		GroupBy:     splitCSV(c.Query("groupBy")),
		TimeBucket:  c.Query("timeBucket"),
		MetricField: c.Query("metricField"),
	}
	resp, err := h.coord.Stats(c.Request.Context(), sq)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Facets 跨 Worker Facet 汇总。
func (h *LogFederationHandler) Facets(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	fq := logcoord.FacetsQuery{
		Query:          q,
		Dimensions:     splitCSV(c.Query("dimensions")),
		DimensionLimit: uint32(parseIntDefault(c.Query("dimensionLimit"), 0)),
	}
	if len(fq.Dimensions) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "dimensions 不能为空"})
		return
	}
	resp, err := h.coord.Facets(c.Request.Context(), fq)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// Export 有界物化导出。
//
// 覆盖不完整 / 截断 / 超预算 / quality=unresolved 时：
// 返回 200 JSON（export_incomplete=true），不交付成功附件。
func (h *LogFederationHandler) Export(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	res, err := h.coord.Export(c.Request.Context(), q)
	if err != nil {
		writeFederationError(c, err)
		return
	}

	// 质量门禁：exact/unresolved 大小写不敏感；unresolved/conflict/partial/unavailable 阻断导出。
	blocks := logcoord.QualityBlocksExport(res.Quality) || res.ExportIncomplete || res.Artifact == nil
	if blocks {
		reasons := append([]string(nil), res.IncompleteReasons...)
		if logcoord.QualityBlocksExport(res.Quality) {
			reasons = append(reasons,
				"quality_"+strings.ToLower(string(res.Quality.DuplicateQuality)),
				"stats_"+strings.ToLower(string(res.Quality.StatsQuality)),
			)
		}
		// 不返回 Artifact 字节：缺口/截断/超预算/质量未决不交付成功附件。
		c.JSON(http.StatusOK, gin.H{
			"view":               res.View,
			"coverage":           res.Coverage,
			"quality":            res.Quality,
			"export_incomplete":  true,
			"incomplete_reasons": dedupeFederationReasons(reasons),
			"budget_exceeded":    res.BudgetExceeded,
			"cut":                res.Cut,
			"artifact":           nil,
		})
		return
	}

	filename := fmt.Sprintf("logs-federation-%s.ndjson", time.Now().Format("20060102-150405"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/x-ndjson")
	c.Data(http.StatusOK, "application/x-ndjson", res.Artifact)
}

func dedupeFederationReasons(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func writeFederationError(c *gin.Context, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, logcoord.ErrViewAuthz):
		// F-007：授权拒绝/主体或目标不匹配 → 403，不得当作内部错误。
		c.JSON(http.StatusForbidden, gin.H{
			"error":     "FORBIDDEN",
			"message":   "Query View 授权不匹配，拒绝复用",
			"errorCode": "LOG_VIEW_AUTHZ_DENIED",
		})
	case errors.Is(err, logcoord.ErrViewReuseMismatch):
		// F-006：固定查询条件变化 → VIEW_STALE 语义，客户端应弃用该 viewId 重建。
		c.JSON(http.StatusConflict, gin.H{
			"error":     "VIEW_STALE",
			"message":   "Query View 固定查询条件不一致，请使用新的查询重建视图",
			"errorCode": "LOG_VIEW_STALE",
		})
	case strings.Contains(msg, "authorized target set is empty"):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "授权目标集合为空"})
	case strings.Contains(msg, "invalid from_utc"), strings.Contains(msg, "invalid to_utc"),
		strings.Contains(msg, "time range"):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "时间范围无效"})
	case strings.Contains(msg, "view not found"):
		c.JSON(http.StatusBadRequest, gin.H{"error": "INVALID_REQUEST", "message": "view 不存在"})
	case strings.Contains(msg, "TargetResolver is nil"), strings.Contains(msg, "WorkerDialer is nil"):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "UNAVAILABLE", "message": "日志联邦服务未就绪"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "日志联邦查询失败"})
	}
}
