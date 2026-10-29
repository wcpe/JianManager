package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// LogFederationHandlerCP 跨 Worker 日志联邦查询 HTTP 面。
//
// 权限复用日志中心：路由组 RequireAnyPerm("log.read")；
// Handler 内平台管理员可查全平台/指定目标，非管理员收敛到可访问实例。
type LogFederationHandler struct {
	coord *logcoord.Coordinator
	authz *service.AuthzService
	// 面板聚合类（stats/facets）的同参秒级缓存：这两条查询同样打满 VL 并发槽，
	// 页面加载时与主列表互挤（实测 5.7–8.7s ✗）⇒ 命中即免扇出。键含授权主体/scope ⇒
	// 跨主体绝不命中 ✗；导出/校验路径不经这两个 handler ⇒ 天然旁路 ✗。
	aggCache *fedTTLCache

	// 热集与预热失败频控状态（**每 handler 一份**，见下）。
	// 原为包级 var，而 noteHotInstance 在 Search 请求路径上写入 ⇒ 同进程多 handler
	// （测试并行、未来多租户）会互相串扰：A 的查询会改变 B 的预热窗口选择，
	// 一个 handler 的失败频控会吞掉另一个的日志。挂到实例上与 aggCache 的既有写法一致 ✓。
	warmHotMu sync.Mutex
	warmHot   map[uint]time.Time

	warmFailMu   sync.Mutex
	warmFailOnce map[string]time.Time
}

// fedTTLCache 极简同参 TTL 缓存（只读复用，返回副本字节）。
type fedTTLCache struct {
	mu sync.Mutex
	m  map[string]fedTTLEntry
}

type fedTTLEntry struct {
	body []byte
	exp  time.Time
}

// fedAggTTL 聚合类短档 TTL：与 Coordinator 侧 pending 档同量级（新日志最多晚 TTL 秒 ✗）。
const fedAggTTL = 4 * time.Second

func (c *fedTTLCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.exp) {
		if ok {
			delete(c.m, key)
		}
		return nil, false
	}
	return e.body, true
}

func (c *fedTTLCache) put(key string, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]fedTTLEntry)
	}
	if len(c.m) > 512 {
		for k := range c.m {
			delete(c.m, k)
		}
	}
	c.m[key] = fedTTLEntry{body: body, exp: time.Now().Add(fedAggTTL)}
}

// fedAggPagingParams 聚合类请求中**对聚合结果完全无影响**的参数（分页标识），不入缓存键。
//
//   - cursor：Stats/Facets 不做分页，q.Cursor 在两条路径上从未被读取（Stats/Facets 也不校验
//     它）⇒ 纯粹是分页标识，入键只会让"同参不同页"各自成键；
//   - page/pageSize：从未被 buildQuery 读取（前端分页参数，服务端只认 limit）⇒ 同理。
//
// 为什么**不**把 viewId 一并排除（与"分页标识"的直觉相反）：它虽不参与聚合语义，但
// resolveForQuery 会用它做视图查找与复用校验（GetView → authorizeViewReuse），并据此锁定
// 目标集合快照；而本 handler 的缓存读取发生在 coord.Stats/Facets **之前** ⇒ 若把它移出键，
// 一个已失效/伪造的 viewId 会被缓存命中直接放行（跳过 400/403 与目标集合锁定）✗。
// 其余参数一律入键（safe-by-default ✓）：将来新增的结果相关参数（groupBy/timeBucket/
// metricField/dimensions/dimensionLimit/keyword/level/source/instanceId/nodeId/limit…）
// 不会因"忘了进白名单"而与旧条目串味 ✗。
var fedAggPagingParams = map[string]bool{"cursor": true, "page": true, "pageSize": true}

// fedAggCacheKey 聚合类同参键＝授权主体/scope/时间片/过滤/onlineOnly + **语义查询串**。
// 语义查询串＝原始查询串去掉分页标识（fedAggPagingParams）后按参数名排序重编码 ⇒
// 「结果相关参数不同绝不命中 ✓」与「仅分页标识不同仍命中同一聚合条目 ✓」同时成立。
func fedAggCacheKey(q logcoord.Query, c *gin.Context) string {
	return strings.Join([]string{
		q.PrincipalKey,
		q.PermissionScope,
		q.FromUTC,
		q.ToUTC,
		fmt.Sprint(q.OnlineOnly),
		fedAggSemanticQuery(c),
	}, "\x1f")
}

// fedAggSemanticQuery 把原始查询串归一为「语义参数」的稳定表示（去掉分页标识 + 参数名/取值排序）。
// 解析失败时退回原始串：宁可少命中，也不能把"解析不了"当成"无参数"而让不同请求串味 ✗。
func fedAggSemanticQuery(c *gin.Context) string {
	raw := c.Request.URL.RawQuery
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	names := make([]string, 0, len(values))
	for name := range values {
		if fedAggPagingParams[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		vals := append([]string(nil), values[name]...)
		sort.Strings(vals)
		for _, v := range vals {
			b.WriteString(name)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte('\x1e')
		}
	}
	return b.String()
}

// 预热热门窗（fedWarmWindows 全覆盖 × 平台级 ✓）：与用户走同一条 Search 代码路径、结果写进同一
// TTL 缓存，唯一差别是"发起者身份"——缓存键不含身份段（键＝授权集合摘要 ✓，论证见
// logcoord.searchCacheKey），故**同一授权集合**的真实用户查询能命中预热条目 ✓。
//
// 命中的附加前提（勿读成"任何同名窗口都命中"）：Coordinator 侧 lookupSearchCache 还要求**精确
// from/to 相等**（防旧窗口冒充新窗口的排序前缀 ✗，见 TestWarmSearchExactWindowHitsButShiftedBucketsRefetch）。
// 因此当前**只有 all 预设**（两侧都不带 from/to ⇒ 均为空串）能稳定命中；15m/1h/24h/7d 的 from/to
// 由浏览器按毫秒生成、预热按秒生成（RFC3339 无小数秒），字符串几乎不可能相等 ⇒ 这些滚动窗的预热
// 命中收益≈0（仍会在 Worker 侧起到投影/索引预热作用）。这是既有守卫的取舍，若要恢复滚动窗命中，
// 须同时改该守卫与窗口重绑用例，属独立议题 ✗。
//
// 失败只做频控日志，绝不影响请求路径 ✗。
//
// 节奏（按窗口定间隔 ✗✓）：24h 冷扫本身要 20–40s ⇒ 若与 15m 同用短间隔，等于**持续占满 VL 的
// 并发槽**（实测第 1 次 24h 被掐在 39.99s ✗）。故：15m⇒25s、1h⇒150s、24h⇒12min；
// 全局 20s tick 串行调度（同一时刻只有一个预热在飞 ✓），单次 25s 超时到点即放弃（不堆叠 ✗，
// ctx 取消会让 VL 立刻放槽 ✓）。
//
// 单实例维度（零配置 ✓）：记录最近被真实查询过的实例（热集），对其 15m/1h 窗预热 ⇒
// 用户切到"刚看过的实例"时直接命中，无需任何配置项 ✗；热集只保留最近 K 个、随时间淘汰 ✗。
const (
	fedWarmTick    = 20 * time.Second
	fedWarmTimeout = 25 * time.Second
	fedWarmHotKeep = 3
	fedWarmHotTTL  = 30 * time.Minute
	// fedWarmPageLimit 必须与前端 LogsPage 的 PAGE_SIZE（limit 参数）及 logcoord
	// 缓存的 Budget.Limit 键段一致：此前预热用 50 而用户发 100，键不同 ⇒ 预热条目
	// 永远不命中（2026-10-08 面板来回切换恒冷复盘）。
	fedWarmPageLimit = 100
)

type fedWarmWindow struct {
	win   time.Duration
	every time.Duration
}

// fedWarmWindows 覆盖前端全部时间预设（logs-filters.ts: all/15m/1h/24h/7d）。
// every 与缓存 TTL 必须满足「实际轮间隔 < TTL」才不至于留桶首缝隙：调度 tick 为
// 20s，every=30s 的实际间隔恒为 40s（< 长档 TTL 50s），every=20s 即每 tick 一轮
// （15m 短档 TTL 25s）。配合 WarmSearch 预写下一桶，每条自然分钟从第 0 秒起即可
// 命中。win=0 表示无界（all，不传 from/to）。
var fedWarmWindows = []fedWarmWindow{
	{win: 15 * time.Minute, every: 20 * time.Second},
	{win: time.Hour, every: 30 * time.Second},
	{win: 24 * time.Hour, every: 30 * time.Second},
	{win: 7 * 24 * time.Hour, every: 30 * time.Second},
	{win: 0, every: 30 * time.Second},
}

// 热集：最近查询过的单实例（按最近使用排序，最多 fedWarmHotKeep 个），状态在 handler 字段上。

// noteHotInstance 请求路径调用（常量开销 ✓）：记录单实例查询，供预热器选择窗口。
func (h *LogFederationHandler) noteHotInstance(id uint) {
	if h == nil || id == 0 {
		return
	}
	h.warmHotMu.Lock()
	if h.warmHot == nil {
		h.warmHot = make(map[uint]time.Time)
	}
	h.warmHot[id] = time.Now()
	h.warmHotMu.Unlock()
}

// hotInstances 返回最近使用、未过期的实例 id（最多 K 个 ✓）。
func (h *LogFederationHandler) hotInstances() []uint {
	if h == nil {
		return nil
	}
	h.warmHotMu.Lock()
	defer h.warmHotMu.Unlock()
	type pair struct {
		id uint
		at time.Time
	}
	all := make([]pair, 0, len(h.warmHot))
	for id, at := range h.warmHot {
		if time.Since(at) <= fedWarmHotTTL {
			all = append(all, pair{id, at})
		} else {
			delete(h.warmHot, id)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	if len(all) > fedWarmHotKeep {
		all = all[:fedWarmHotKeep]
	}
	ids := make([]uint, 0, len(all))
	for _, p := range all {
		ids = append(ids, p.id)
	}
	return ids
}

func (h *LogFederationHandler) startWarmLoop() {
	if h == nil || h.coord == nil {
		return
	}
	// 测试路径（经 testLogCoord 注入的协调器/假 dialer）不启预热：避免后台并发打假客户端造成
	// 数据竞争与用例间干扰 ✗（生产装配不设 testLogCoord ⇒ 正常预热 ✓）。
	if testLogCoord != nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ticker := time.NewTicker(fedWarmTick)
		defer ticker.Stop()
		last := map[time.Duration]time.Time{}
		lastHot := map[time.Duration]time.Time{}
		for {
			now := time.Now()
			for _, w := range fedWarmWindows {
				// 串行＋按窗口间隔：到点的窗口才跑，且同一时刻至多一个在飞 ✓
				if t, ok := last[w.win]; !ok || now.Sub(t) >= w.every {
					last[w.win] = now
					h.warmWindow(now.UTC(), w.win, nil)
				}
			}
			// 热集（最近查询过的单实例 ⇒ 键为 targets digest，预热可被真实用户命中 ✓）：
			// 只预热 15m/1h 两窗（24h 单实例成本高、不常驻 ✗），间隔复用同表 ✓。
			for _, id := range h.hotInstances() {
				for _, w := range fedWarmWindows[:2] {
					key := w.win
					if t, ok := lastHot[key]; !ok || time.Since(t) >= w.every {
						lastHot[key] = time.Now()
						h.warmWindow(now.UTC(), w.win, []string{fmt.Sprintf("inst:%d", id)})
					}
				}
			}
			<-ticker.C
		}
	}()
}

// warmWindow 单窗口预热（targets 为空 ⇒ 平台级 "*" ✓；否则为单实例集 ✓；
// win=0 ⇒ 无界 all 预设，不传 from/to，与前端 timeRangeToParams 逐字一致）；
// 使用 WarmSearch（同时预写下一分钟桶，消除整分桶键的桶首缝隙）；
// 任何 panic/错误都被吞掉并频控记录（每窗口每分钟最多一条 ✗）。
func (h *LogFederationHandler) warmWindow(now time.Time, win time.Duration, targets []string) {
	defer func() { _ = recover() }()
	ctx, cancel := context.WithTimeout(context.Background(), fedWarmTimeout)
	defer cancel()
	if len(targets) == 0 {
		targets = []string{"*"}
	}
	q := logcoord.Query{
		AuthorizedTargetIDs: targets,
		// 身份段是**显式占位**：预热器没有真实用户身份，而缓存键不含身份段
		// （键＝授权集合摘要 ⇒ 预热条目可被同授权集合的真实用户命中 ✓，见
		// logcoord.searchCacheKey 的论证）。占位值另有两个作用：让预热创建的 View
		// 永不满足 authorizeViewReuse 的主体校验（不可被复用 ✗），并在排查时
		// 一眼看出该 View 来自预热而非真实请求 ✓。
		PrincipalKey:    "warm",
		PermissionScope: "warm",
		Budget:          logcoord.QueryBudget{Limit: fedWarmPageLimit},
		Fast:            true,
	}
	if win > 0 {
		q.FromUTC = now.Add(-win).Format(time.RFC3339)
		q.ToUTC = now.Format(time.RFC3339)
	}
	if _, err := h.coord.WarmSearch(ctx, q); err != nil {
		key := win.String()
		h.warmFailMu.Lock()
		if h.warmFailOnce == nil {
			h.warmFailOnce = make(map[string]time.Time)
		}
		last, ok := h.warmFailOnce[key]
		if !ok || time.Since(last) > time.Minute {
			h.warmFailOnce[key] = time.Now()
		}
		h.warmFailMu.Unlock()
		if !ok || time.Since(last) > time.Minute {
			slog.Debug("日志联邦预热失败（频控）", "window", key, "error", err)
		}
	}
}

// NewLogFederationHandler 创建联邦查询处理器（并启动热门窗预热器 ✓）。
func NewLogFederationHandler(coord *logcoord.Coordinator, authz *service.AuthzService) *LogFederationHandler {
	h := &LogFederationHandler{coord: coord, authz: authz, aggCache: &fedTTLCache{},
		warmHot: make(map[uint]time.Time), warmFailOnce: make(map[string]time.Time)}
	h.startWarmLoop()
	return h
}

// RegisterRoutes 注册联邦查询路由（挂在 log.read 权限组下）。
func (h *LogFederationHandler) RegisterRoutes(rg *gin.RouterGroup) {
	if h == nil || h.coord == nil {
		return
	}
	// 门面：LogsPage/双路径客户端探测 GET /logs/federation。
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
// coverage/quality 保持snake_case wire 形态，由前端 adapter 归一。
func (h *LogFederationHandler) FacadeSearch(c *gin.Context) {
	q, ok := h.buildQuery(c)
	if !ok {
		return
	}
	// 首屏快路径（Query.Fast）默认开启：分页/首屏只取当页所需（有界时间片＋limit），
	// 逐目标完整覆盖校验后置为 Coverage.Pending（该字段独立呈现、**绝不入 notes** ✗），
	// 完整校验留给导出/显式全量（其 eager 语义与下方导出闸判定一字不动 ✗）。
	// 2026-10-07 复盘：上次默认开启引发裸查询 409 VIEW_STALE，根因是「前端旧 viewId 复用」
	// ——已由 d5363a60（viewId 生命周期）修复，且本带 planner 空视图守卫；另有安全带回滚兜底。
	q.Fast = true
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
	body := gin.H{
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
	}
	if len(resp.IngestPositionGaps) > 0 {
		body["ingest_position_gaps"] = resp.IngestPositionGaps
	}
	c.JSON(http.StatusOK, body)
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
	// 首屏/翻页默认走快路径（分页优先）：只取当页所需，完整覆盖校验后置为 Coverage.Pending
	// （独立呈现、绝不入 notes ✗）。前提已具备：快路径现容忍"固定条件变化"的 view 复用
	// （改为按当前条件重建视图 ✓，授权主体/覆盖校验仍严守 ✗ —— 见 resolve_fast.go）。
	// 导出（Export，下方 L432）不置位 ⇒ 导出闸与 eager 校验语义一字不动 ✗。
	q.Fast = true
	// 热集登记（零配置 ✓）：FacadeSearch 是面板主路径 ⇒ 在此记录单实例查询
	//（键已改为 targets digest ⇒ 热集预热条目可被真实用户命中 ✓）。
	for _, tid := range q.AuthorizedTargetIDs {
		if raw := strings.TrimPrefix(tid, "inst:"); raw != tid {
			if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
				h.noteHotInstance(uint(id))
			}
		}
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
	// 同参 TTL：命中即直接回放（面板"stats 延后加载"时仍可能重复取数 ✓）；
	// 冷未命中的真实耗时如实保留（VL 扫描成本 ✗），不做任何伪造 ✓。
	key := "s\x1f" + fedAggCacheKey(q, c)
	if body, ok := h.aggCache.get(key); ok {
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		return
	}
	resp, err := h.coord.Stats(c.Request.Context(), sq)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	if body, mErr := json.Marshal(resp); mErr == nil {
		h.aggCache.put(key, body)
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
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
	// 同参 TTL：与 Stats 同口径（键含主体/scope/RawQuery ✓），命中回放、冷查如实计耗时 ✓。
	key := "f\x1f" + fedAggCacheKey(q, c)
	if body, ok := h.aggCache.get(key); ok {
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		return
	}
	resp, err := h.coord.Facets(c.Request.Context(), fq)
	if err != nil {
		writeFederationError(c, err)
		return
	}
	if body, mErr := json.Marshal(resp); mErr == nil {
		h.aggCache.put(key, body)
		c.Data(http.StatusOK, "application/json; charset=utf-8", body)
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
		// limits 是「本次实际采用的上限」——前端据此把笼统的「预算超了」说成
		// 「超过 N 行上限，请缩小时间范围或调大 log_export.max_rows」。
		c.JSON(http.StatusOK, gin.H{
			"view":               res.View,
			"coverage":           res.Coverage,
			"quality":            res.Quality,
			"export_incomplete":  true,
			"incomplete_reasons": dedupeFederationReasons(reasons),
			"budget_exceeded":    res.BudgetExceeded,
			"cut":                res.Cut,
			"limits":             res.Limits,
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
