package logcoord

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

var (
	errNoResolver = errors.New("logcoord: TargetResolver is nil")
	errNoDialer   = errors.New("logcoord: WorkerDialer is nil")
	errNoView     = errors.New("logcoord: view not found")
	// ErrViewAuthz 复用 view 时授权主体不一致或当前授权集合不再覆盖 view 目标。
	ErrViewAuthz = errors.New("logcoord: view authorization mismatch")
	// ErrViewReuseMismatch 复用 view 时固定查询条件不一致（时间/过滤/onlineOnly/scope）。
	ErrViewReuseMismatch = errors.New("logcoord: view reuse query mismatch")
)

// Coordinator CP 跨 Worker 日志查询协调器（纯 service）。
type Coordinator struct {
	resolver TargetResolver
	dialer   WorkerDialer

	mu    sync.Mutex
	views map[string]View

	// 同参秒级 TTL 缓存（第二笔：首屏/分页重复查询提速，扇出成本 20s+ 时回收最大）。
	// 键＝授权集合摘要（**不含主体/scope**）⇒ 同一授权集合的多个调用方共享条目 ✓、
	// 跨集合绝不命中 ✗（论证见 searchCacheKey）；Export 路径不经过此处 ⇒ 天然旁路 ✗。
	searchCacheMu sync.Mutex
	searchCache   map[string]searchCacheEntry

	// exportBudget 导出物化的服务端上限（可配置）。
	// 零值 = 默认 10000 行 / 64MiB，与修复前的硬编常量一致；
	// 构造后不再改动 ⇒ 无锁读取安全（SetExportBudget 只在装配期调用）。
	exportBudget ExportBudget
}

// SetExportBudget 注入导出物化预算上限（装配期调用一次）。
//
// 为什么用 setter 而不是改 New 签名：New 被大量测试与装配点直接调用，
// 加参数会强制全部调用点改动且无收益——导出预算是可选覆盖项，
// 零值即默认值，保持 New(resolver, dialer) 的调用形态不变。
//
// 非法值（<=0）由 ExportBudget.normalize 回退默认，不在此处报错：
// 配置层已做 fail-fast 校验（见 CP config.ValidateLogExportConfig），
// 此处只需保证「任何输入都不会产出不可用预算」。
func (c *Coordinator) SetExportBudget(b ExportBudget) {
	c.exportBudget = b
}

// ExportBudgetLimits 返回当前生效的导出预算上限（供装配期日志与测试断言）。
func (c *Coordinator) ExportBudgetLimits() (maxRows int, maxBytes uint64) {
	b := c.exportBudget.normalize()
	return b.MaxRows, b.MaxBytes
}

// searchCacheTTL 双档 TTL：pending（覆盖校验后置 ✗）结果短档，避免新日志被掩盖过久；
// 已完成完整校验的结果可用常规档。
// 与"整分桶键"配合：同一分钟内的滚动窗口共用同一键 ⇒ TTL 稍长也不会放大陈旧度 ✗，
// 却能让"连打/翻页/多组件重复取数"稳定命中（配合预热器可做到用户侧恒热 ✓）。
//
// 2026-10-08 分档加固（面板切换恒热）：短窗（<30min）保持短 TTL 保新鲜；长窗
// （1h/24h/7d/无界 all）历史数据占绝对多数、变化缓慢，用长档 TTL 覆盖预热周期
// （40s ≪ TTL），把"用户来回切换窗口"从每次冷查（1-3s）压到缓存命中（~0.03s）。
// tail/跟随路径不经本缓存 ⇒ 实时性不受影响 ✓。
//
// 长档 pending 取 50s：预热 every=30s、调度 tick 20s ⇒ 实际轮间隔 = 40s，TTL 必须
// 严格大于相邻两轮间隔才能让每条自然分钟无缝隙命中（命中率不降）。同时把
// 「worker 侧投影重建/重放期的可见集波动」经缓存暴露给用户的滞后压到 ≤90s——
// 实测同一窗口两轮预热快照曾从 58 条变 6 条（可见集翻转属 worker 恢复链路的既有
// 问题），缓存不应把这个波动放大成分钟级。
const (
	searchCacheTTLPending       = 25 * time.Second
	searchCacheTTLCompleted     = 60 * time.Second
	searchCacheTTLPendingLong   = 50 * time.Second
	searchCacheTTLCompletedLong = 180 * time.Second
	searchCacheLongWindowCutoff = 30 * time.Minute
)

// searchCacheTTLFor 按查询时间跨度与 pending 状态选择 TTL（见上）。
func searchCacheTTLFor(q Query, pending bool) time.Duration {
	from, okFrom := parseCacheTime(q.FromUTC)
	to, okTo := parseCacheTime(q.ToUTC)
	if okFrom && okTo {
		span := to.Sub(from)
		if span > 0 && span < searchCacheLongWindowCutoff {
			if pending {
				return searchCacheTTLPending
			}
			return searchCacheTTLCompleted
		}
	}
	// 长窗或无法解析（无界 all）：长档。
	if pending {
		return searchCacheTTLPendingLong
	}
	return searchCacheTTLCompletedLong
}

// parseCacheTime 宽松解析 RFC3339（含纳秒）；失败返回 false。
func parseCacheTime(t string) (time.Time, bool) {
	if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
		return ts.UTC(), true
	}
	if ts, err := time.Parse(time.RFC3339, t); err == nil {
		return ts.UTC(), true
	}
	return time.Time{}, false
}

// shiftMinutes 把 RFC3339（含纳秒）时间串平移 delta 分钟；空串或解析失败原样返回。
func shiftMinutes(t string, delta int) string {
	ts, ok := parseCacheTime(t)
	if !ok {
		return t
	}
	return ts.Add(time.Duration(delta) * time.Minute).Format(time.RFC3339Nano)
}

type searchCacheEntry struct {
	resp     *SearchResponse
	expireAt time.Time
}

// searchCacheKeyBucket 把时间片对齐到整分桶：滚动窗口（如"最近 15 分钟"）每秒都会换
// from/to ⇒ 原样入键会让缓存永远未命中 ✗✓。取整后同一分钟共享键；查询本身仍用真实窗 ✓
// （键只影响命中，不影响结果语义 ✗）。
func searchCacheKeyBucket(t string) string {
	if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
		return ts.UTC().Truncate(time.Minute).Format(time.RFC3339Nano)
	}
	if ts, err := time.Parse(time.RFC3339, t); err == nil {
		return ts.UTC().Truncate(time.Minute).Format(time.RFC3339Nano)
	}
	return t
}

// searchCacheKey 同参键＝**授权集合摘要（targets digest ✓）** ‖ 时间片（整分桶 ✓） ‖ 过滤 ‖
// onlineOnly ‖ order ‖ limit/maxBytes ‖ Fast ‖ 复用视图 ID ‖ 游标。
//
// 为什么键内**不含主体/身份**（2026-10-07 裁定 ✓，2026-10-12 重申并补回归 ✓）：日志内容只
// 取决于**授权集合**（同一集合 ⇒ 同一结果 ✓），越权由上游 authorizedTargets 判定（403 用例保留 ✓）。
// 身份入键属过度保守 ✗，且会让"平台级/单实例预热"永远无法被真实用户命中——预热器没有真实用户
// 身份，只能写死占位值，与真实键**永不相同** ⇒ 预热零收益，却仍按窗口周期对全部 Worker 全量扇出 ✗
// （与"限制宽窗查询抢占"的初衷相反）。改为 targets digest 后个人级与平台级同规则、预热条目天然
// 共享 ✓，而**跨集合绝不命中** ✓（不同 targets ⇒ 不同 digest）。
//
// 去身份段后的三条安全前提（缺一不可，改动本键前必须逐条复核 ✓）：
//  1. View 身份不随条目外泄：命中路径一律经 rebindCachedSearchResponse 把 resp.View 换成**当前
//     调用方自己的 View**（PrincipalKey/PermissionScope/Budget 均取自本次请求 ✓），
//     绝不把创建者的 View 或游标交给调用方 ✗；
//  2. 复用校验先于查缓存：带 ViewID 的请求在 Search 内先走 resolveForQuery / resolveForQueryFast，
//     其中 authorizeViewReuse 强制主体相等与 scope 相等（不匹配即 ErrViewAuthz/ErrViewReuseMismatch，
//     快路径也只容忍 ErrViewReuseMismatch、ErrViewAuthz 原样上抛 ✓），因此跨主体复用 viewId 在
//     触达缓存之前就被拒 ✗；
//  3. 键内其余段仍是全部结果相关入参（授权集合/时间窗/过滤/onlineOnly/order/limit/maxBytes/
//     Fast/豁免标记），且 PermissionScope 不参与扇出（worker_adapter 下发前已清空）⇒ 同集合不同
//     scope 的两个调用方能各自查询到的行本就相同，共享条目不改变任何人可见的数据 ✗。
//
// 授权集合入键用 64 位截断摘要：targets 由 CP 侧构造（inst:<id>/node:<id>/*），不接受用户自由
// 文本 ⇒ 不存在"构造第二集合撞同一摘要"的注入面 ✓。
//
// 为什么 ViewID 必须入键（2026-10-08 修复）：带 ViewID 的复用调用必须重新扇出以执行
// ensureWorkerViewBinding 的 worker 绑定与 catalog 代次漂移校验（ErrViewReuseMismatch）——
// 若与"新建视图"（ViewID 为空）的条目同键，复用调用会被缓存直接短路，漂移被静默掩盖
// （TestCoordinatorRejectsWorkerGenerationDriftWithinView 回归）。同一 ViewID 的重复查询
// （翻页/重查）仍可命中，语义不变。
//
// 为什么 Cursor 必须入键（2026-10-08 修复）：不同页的 q 只差 Cursor，若不入键则第二页起的
// 查询全部命中第一页条目、返回同一批行与同一 NextCursor——Export 循环游标原地打转直到字节
// 超限，把"预算充足"误判为 incomplete（TestExportAccumulatesBytesAcrossPages 回归）。
//
// 为什么 AllowProjectionDrift 必须入键（2026-10-10）：它改变了**响应可由哪些读取产生**
// （置位时 Worker 跳过投影清单版本校验）。Export 走 c.Search ⇒ 会经本缓存读写；若不入键，
// 一份带豁免算出的响应会以同一键被「不带豁免」的调用命中，等于把豁免静默扩散到导出之外的
// 路径——正是本次要避免的放宽。置位后导出条目只在导出之间共享，语义边界与请求边界一致。
// 注：当前列表路径均置 Fast=true 而导出不置（键已天然分离），但不能依赖这个巧合。
func searchCacheKey(q Query) string {
	return strings.Join([]string{
		targetsDigest(q.AuthorizedTargetIDs),
		fmt.Sprint(q.Budget.MaxFanout),
		fmt.Sprint(q.Budget.TimeoutMS),
		searchCacheKeyBucket(q.FromUTC),
		searchCacheKeyBucket(q.ToUTC),
		normalizeFilter(q.Filter),
		fmt.Sprint(q.OnlineOnly),
		q.OrderVersion,
		fmt.Sprint(q.Budget.Limit),
		fmt.Sprint(q.Budget.MaxBytes),
		fmt.Sprint(q.Fast),
		fmt.Sprint(q.AllowProjectionDrift),
		q.ViewID,
		q.Cursor,
	}, "\x1f")
}

// targetsDigest 授权集合同摘要：排序去重后 sha256（长集合也不膨胀键 ✓；空集合用固定标记 ✓）。
func targetsDigest(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, "\x1e")))
	return fmt.Sprintf("%x", h[:8])
}

// rebindCachedSearchResponse 只复用缓存的行与覆盖摘要，绝不把创建者的 View/游标交给当前调用方。
// 时间桶命中时按当前精确窗口过滤行，避免旧窗口越界；并把游标按当前 View 重编码。
//
// **窗口重绑分支已删除**（N-3，2026-10-12 终审清理）：这里原有一个
// `windowAdjusted := cached.View.FromUTC != view.FromUTC || ...ToUTC != view.ToUTC`
// 分支（命中时改判 `CACHE_WINDOW_REBASED` + `Complete=false` + `EnumerationState=EnumOpen`）。
// 它**不可达**：唯一调用方 `lookupSearchCache` 在返回命中前已强制
// `entry.resp.View.FromUTC == q.FromUTC && entry.resp.View.ToUTC == q.ToUTC`
// （见该函数内的精确窗口守卫），而本函数收到的 `view` 由当前请求的 `q` 派生 ⇒
// 两侧窗口按构造必然相等 ⇒ 判据恒假。留着会让读者以为"分钟桶跨窗命中"这条路径存在，
// 与 `lookupSearchCache` 的实际语义矛盾（该守卫的注释已说明：精确窗口不一致必须回源）。
//
// 上面那段**按当前精确窗口过滤行**的逻辑仍保留：它对 `cached.Items` 做的是**下界/上界裁剪**
// （防御 `q` 的窗口比缓存载荷窄的情形，例如调用方传入子窗口），与"整窗被重绑"是两件事。
func rebindCachedSearchResponse(q Query, view View, cached *SearchResponse) *SearchResponse {
	if cached == nil {
		return nil
	}
	out := *cached
	out.View = view
	out.IngestPositionGaps = append([]IngestPositionGap(nil), cached.IngestPositionGaps...)
	from, fromErr := time.Parse(time.RFC3339Nano, q.FromUTC)
	to, toErr := time.Parse(time.RFC3339Nano, q.ToUTC)
	if fromErr == nil && toErr == nil && (q.FromUTC != "" || q.ToUTC != "") {
		filtered := make([]Event, 0, len(cached.Items))
		for _, event := range cached.Items {
			ts, err := time.Parse(time.RFC3339Nano, event.EventTimeUTC)
			if err != nil {
				continue
			}
			if q.FromUTC != "" && ts.Before(from) {
				continue
			}
			if q.ToUTC != "" && !ts.Before(to) {
				continue
			}
			filtered = append(filtered, event)
		}
		out.Items = filtered
	}
	if cached.NextCursor != "" && len(out.Items) > 0 {
		out.NextCursor = EncodePageCursor(PageCursor{
			ViewID: view.ViewID, OrderVersion: view.OrderVersion,
			SortKey: out.Items[len(out.Items)-1].SortKey(), PageLimit: view.Budget.Limit,
		})
	} else if len(out.Items) == 0 {
		out.NextCursor = ""
	}
	return &out
}

// lookupSearchCache 命中即返回缓存载荷；调用方必须通过当前请求 View 重新绑定。
func (c *Coordinator) lookupSearchCache(q Query) (*SearchResponse, bool) {
	key := searchCacheKey(q)
	c.searchCacheMu.Lock()
	defer c.searchCacheMu.Unlock()
	entry, ok := c.searchCache[key]
	if !ok || time.Now().After(entry.expireAt) {
		if ok {
			delete(c.searchCache, key)
		}
		return nil, false
	}
	// 分钟桶仍保留预热候选，但旧窗口无法证明当前窗口的完整排序前缀。
	// 精确窗口不一致必须回源，不能把旧页边界重绑成新 View 游标而跳过新增事件。
	if entry.resp == nil || entry.resp.View.FromUTC != q.FromUTC || entry.resp.View.ToUTC != q.ToUTC {
		return nil, false
	}
	return entry.resp, true
}

// storeSearchCache 写入同参缓存，TTL 按查询跨度与 pending 双档（上限保护防无界增长 ✓）。
func (c *Coordinator) storeSearchCache(q Query, resp *SearchResponse) {
	if resp == nil {
		return
	}
	// 空覆盖结果不缓存（2026-10-08）：CP 启动早期 dial 未就绪、worker 整体不可用等
	// 情形下，Search 以「空 items + 不完整覆盖」正常返回；若被缓存，用户与预热器会
	// 持续命中这份空结果（配合预热预写下一桶形成跨分钟雪球，面板长期显示 0 条）。
	// 空窗口每次重查代价 1-2s，安全性优先。
	if len(resp.Items) == 0 && !resp.Coverage.Complete {
		return
	}
	ttl := searchCacheTTLFor(q, resp.Coverage.Pending)
	key := searchCacheKey(q)
	c.searchCacheMu.Lock()
	defer c.searchCacheMu.Unlock()
	if c.searchCache == nil {
		c.searchCache = make(map[string]searchCacheEntry)
	}
	if len(c.searchCache) > 2048 {
		for k := range c.searchCache {
			delete(c.searchCache, k)
		}
	}
	c.searchCache[key] = searchCacheEntry{resp: resp, expireAt: time.Now().Add(ttl)}
}

// WarmSearch 在 Search 之上把结果**同时**写入「时间窗 +1 分钟」桶的缓存条目。
//
// 为什么需要（2026-10-08，面板来回切换恒冷）：缓存键把时间片对齐到整分桶，
// 桶 M 的条目只能由落在 M 内的查询写入 —— 预热周期（40s）大于桶宽时会留出
// 「桶首缝隙」（用户恰在缝隙内切换即冷查 1-3s）。预写下一桶后，桶 M+1 从
// 第 0 秒起即可命中；长档 TTL（120s）保证条目活到该桶末。
// 两次写入共享同一响应：命中「未来桶」时数据窗口最多滞后约 1 分钟，属浏览
// 路径可接受范围（tail/跟随路径不经本缓存 ✓）。无界查询（all，from/to 为空）
// 键不含时间片、本身稳定命中，无需重复写入。
//
// 必须绕开缓存读取（BypassCache）：预热若命中上一轮自己的条目，会把旧结果
// 原样续写进新桶——启动早期 dial 失败的空结果就这样被逐分钟「雪球」复制，
// 用户落进这些桶即拿到 0 条（2026-10-08 复盘）。空覆盖结果不预写（同
// storeSearchCache 守卫）。
func (c *Coordinator) WarmSearch(ctx context.Context, q Query) (*SearchResponse, error) {
	wq := q
	wq.BypassCache = true
	resp, err := c.Search(ctx, wq)
	if err != nil || resp == nil {
		return resp, err
	}
	if len(resp.Items) == 0 && !resp.Coverage.Complete {
		return resp, nil
	}
	nq := wq
	nq.FromUTC, nq.ToUTC = shiftMinutes(q.FromUTC, 1), shiftMinutes(q.ToUTC, 1)
	if nq.FromUTC == q.FromUTC && nq.ToUTC == q.ToUTC {
		return resp, nil
	}
	c.storeSearchCache(nq, resp)
	return resp, nil
}

// maxRetainedViews CP 视图注册表上限。与 Worker 侧同理：每个「不带 view_id 的首次查询」都新建视图，
// 持续负载下无界增长会泄漏内存（真机 30 分钟压测暴露）。插入前按创建时间淘汰最旧视图，被淘汰视图的
// 后续 Cursor 请求显式返回 view-not-found/VIEW_STALE。
const maxRetainedViews = 4096

// pruneViewsLocked 在插入前按创建时间淘汰超量视图。调用方须持有 c.mu。
func (c *Coordinator) pruneViewsLocked() {
	for len(c.views) >= maxRetainedViews {
		oldestID := ""
		var oldest time.Time
		for id, v := range c.views {
			if oldestID == "" || v.CreatedAt.Before(oldest) {
				oldestID, oldest = id, v.CreatedAt
			}
		}
		if oldestID == "" {
			break
		}
		delete(c.views, oldestID)
	}
}

// New 创建协调器。
func New(resolver TargetResolver, dialer WorkerDialer) *Coordinator {
	return &Coordinator{
		resolver: resolver,
		dialer:   dialer,
		views:    make(map[string]View),
	}
}

// CreateView 解析授权目标并创建跨 Worker Query View。
//
// online_only 仅在显式 true 时收缩扇出集合；被排除目标仍写入 view 元数据与后续 coverage。
func (c *Coordinator) CreateView(ctx context.Context, q Query) (*View, error) {
	view, _, err := c.createView(ctx, q)
	return view, err
}

// createView 是 CreateView 的内部形态：额外返回本次解析结果，供调用方免去二次解析。
//
// 为什么要带出 resolved（2026-10-09）：缓慢路径 resolveForQuery 原先「先 CreateView
// （内部已解析一次）再 ResolveTargets 解析第二次」，同一份入参在同一请求内解析两遍
// ⇒ 目标解析 DB 开销翻倍（实测 Stats/Facets 各 6 条 SQL 而非 3 条），而这两条正是
// 生产最慢的联邦端点。把解析结果直接带出后降为一次。
//
// 顺带修掉一处既有的不一致：view.TargetIDs 原取自解析 #1、扇出集合取自解析 #2，
// 两次快照可能不同（例如 bindWorkerViews 期间隧道变化），会造成「视图声明的目标」
// 与「实际扇出目标」不匹配。改用同一份解析结果后二者天然一致。
func (c *Coordinator) createView(ctx context.Context, q Query) (*View, *ResolvedTargets, error) {
	if err := ValidateQuery(q); err != nil {
		return nil, nil, err
	}
	resolved, err := ResolveTargets(ctx, c.resolver, q.AuthorizedTargetIDs, q.OnlineOnly)
	if err != nil {
		return nil, nil, err
	}
	view := View{
		ViewID:                   NewViewID(),
		OrderVersion:             OrderVersion,
		FromUTC:                  q.FromUTC,
		ToUTC:                    q.ToUTC,
		TargetIDs:                resolved.TargetIDs(),
		AuthorizedIDs:            append([]string(nil), q.AuthorizedTargetIDs...),
		PrincipalKey:             q.PrincipalKey,
		OnlineOnly:               q.OnlineOnly,
		Budget:                   q.Budget,
		Filter:                   q.Filter,
		PermissionScope:          q.PermissionScope,
		WorkerViewIDs:            make(map[string]string),
		WorkerViewErrors:         make(map[string]string),
		WorkerCatalogGenerations: make(map[string]string),
		TargetAuthorizationIDs:   make(map[string]string),
		CreatedAt:                time.Now().UTC(),
	}
	for _, target := range resolved.All {
		if target.AuthorizationID != "" {
			view.TargetAuthorizationIDs[target.ID] = target.AuthorizationID
		}
	}
	c.bindWorkerViews(ctx, &view, resolved)
	c.mu.Lock()
	c.pruneViewsLocked()
	c.views[view.ViewID] = view
	c.mu.Unlock()
	return &view, resolved, nil
}

// bindWorkerViews creates one immutable local view per Worker. The CP view ID
// is never sent to Worker Planner as if it were a Worker-local view ID.
// 逐 worker 并发建视图：各 worker 之间相互独立，故并发取数；
// 结果按 order 顺序合并，落盘顺序与串行版本逐一对应（保持语义不变）。
func (c *Coordinator) bindWorkerViews(ctx context.Context, view *View, resolved *ResolvedTargets) {
	if c == nil || c.dialer == nil || view == nil || resolved == nil {
		return
	}
	order, byWorker := resolved.ByWorker()

	type bindResult struct {
		workerID string
		viewID   string
		errMsg   string
	}
	results := make([]bindResult, len(order))
	reserved := reserveWorkerBudgets(order, view.Budget)

	// 并发上限：worker 较多时避免一次性压满所有节点。
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, workerID := range order {
		local, ok := reserved[workerID]
		if !ok {
			results[i] = bindResult{workerID: workerID, errMsg: "BUDGET_EXCEEDED"}
			continue
		}
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = bindResult{workerID: id, errMsg: ctx.Err().Error()}
				return
			}

			targets := byWorker[id]
			targetIDs := make([]string, 0, len(targets))
			for _, t := range targets {
				targetIDs = append(targetIDs, workerTargetID(t))
			}
			client, err := c.dialer.Dial(ctx, id)
			if err != nil {
				results[idx] = bindResult{workerID: id, errMsg: err.Error()}
				return
			}
			resp, err := client.OpenView(ctx, WorkerSearchRequest{
				TargetIDs:    targetIDs,
				FromUTC:      view.FromUTC,
				ToUTC:        view.ToUTC,
				Filter:       view.Filter,
				Budget:       local,
				OrderVersion: view.OrderVersion,
			})
			if err != nil {
				results[idx] = bindResult{workerID: id, errMsg: err.Error()}
				return
			}
			if resp == nil || resp.Unsupported || resp.Error != "" || resp.ViewID == "" {
				msg := "worker view creation failed"
				if resp != nil && resp.Error != "" {
					msg = resp.Error
				}
				results[idx] = bindResult{workerID: id, errMsg: msg}
				return
			}
			results[idx] = bindResult{workerID: id, viewID: resp.ViewID}
		}(i, workerID)
	}
	wg.Wait()

	// 顺序合并：与串行版本一致的写入次序，避免落盘次序不确定。
	for _, r := range results {
		if r.errMsg != "" {
			view.WorkerViewErrors[r.workerID] = r.errMsg
			continue
		}
		view.WorkerViewIDs[r.workerID] = r.viewID
	}
}

func (c *Coordinator) bindExistingView(ctx context.Context, view View, resolved *ResolvedTargets) View {
	if c == nil || resolved == nil || (len(view.WorkerViewIDs) > 0 || len(view.WorkerViewErrors) > 0) {
		return view
	}
	if view.WorkerViewIDs == nil {
		view.WorkerViewIDs = make(map[string]string)
	}
	if view.WorkerViewErrors == nil {
		view.WorkerViewErrors = make(map[string]string)
	}
	c.bindWorkerViews(ctx, &view, resolved)
	c.mu.Lock()
	c.views[view.ViewID] = view
	c.mu.Unlock()
	return view
}

func workerViewID(view View, workerID string) (string, string, bool) {
	if id := view.WorkerViewIDs[workerID]; id != "" {
		return id, "", true
	}
	if reason := view.WorkerViewErrors[workerID]; reason != "" {
		return "", reason, false
	}
	return "", "worker view is not bound", false
}

func workerErrorCoverage(target TargetInfo, workerID string, results []WorkerTargetResult, message string) TargetCoverage {
	for _, result := range results {
		if result.TargetID != workerTargetID(target) {
			continue
		}
		state := result.State
		if state == "" || state == CoverageSuccess {
			state = CoveragePartial
		}
		return TargetCoverage{
			TargetID: target.ID, WorkerID: workerID, State: state,
			Reasons:          append(append([]string(nil), result.Reasons...), "worker_error", message),
			ClosedVisibleSeq: result.ClosedVisibleSeq, CatalogGeneration: result.CatalogGeneration,
			HistoricalHolder: target.HistoricalHolder,
		}
	}
	coverage := CoverageFromReadiness(target, []string{"worker_error", message})
	if coverage.State == CoverageSuccess {
		coverage.State = CoveragePartial
	}
	return coverage
}

func workerTargetID(target TargetInfo) string {
	if target.WorkerTargetID != "" {
		return target.WorkerTargetID
	}
	return target.ID
}

func ensureWorkerViewBinding(view View, workerID string, targets []TargetInfo, respViewID string, results []WorkerTargetResult) error {
	wantView, _, ok := workerViewID(view, workerID)
	if !ok {
		// 快路径（q.Fast）的视图不绑 Worker（见 resolve_fast.go 与 L503-506 的既定语义：
		// "视图未绑定属预期" ✗），此时无期望 viewId ⇒ 不做绑定比对，避免误判 ErrViewReuseMismatch
		// 而被映射成 409 VIEW_STALE ✗。慢路径视图始终绑定，故本支对慢路径不可达 ✓。
		return nil
	}
	if respViewID != wantView {
		return fmt.Errorf("%w: worker %s returned view %q want %q", ErrViewReuseMismatch, workerID, respViewID, wantView)
	}
	got := make(map[string]WorkerTargetResult, len(results))
	for _, result := range results {
		got[result.TargetID] = result
	}
	for _, target := range targets {
		wantGeneration := view.WorkerCatalogGenerations[target.ID]
		if wantGeneration == "" {
			continue
		}
		result, ok := got[workerTargetID(target)]
		if !ok || result.CatalogGeneration != wantGeneration {
			return fmt.Errorf("%w: worker %s target %s generation changed", ErrViewReuseMismatch, workerID, target.ID)
		}
	}
	return nil
}

// GetView 读取已创建视图。
func (c *Coordinator) GetView(viewID string) (View, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.views[viewID]
	return v, ok
}

// resolveForQuery 复用 view 或新建；返回 view、本次解析结果。
//
// 安全（F-001）：复用 viewId 时必须——
//  1. PrincipalKey 与创建主体一致（调用方由 CP 鉴权层注入）；
//  2. 当前 AuthorizedTargetIDs 仍覆盖 view.TargetIDs 与 AuthorizedIDs 的交集目标。
//
// 任一不满足返回 ErrViewAuthz / ErrViewReuseMismatch，禁止用创建者集合越权查询。
func (c *Coordinator) resolveForQuery(ctx context.Context, q Query) (View, *ResolvedTargets, error) {
	if q.ViewID != "" {
		v, ok := c.GetView(q.ViewID)
		if !ok {
			return View{}, nil, fmt.Errorf("%w: %s", errNoView, q.ViewID)
		}
		if err := authorizeViewReuse(v, q); err != nil {
			return View{}, nil, err
		}
		// 复用同一逻辑目标集合；不因当前 readiness 变化而重新选择集合。
		// 仍需 TargetInfo 以便 fanout/coverage：按 view.TargetIDs 再解析身份映射，
		// 但查询目标集合锁定为 view.TargetIDs。
		infos, err := c.resolver.Resolve(ctx, v.AuthorizedIDs)
		if err != nil {
			return View{}, nil, err
		}
		byID := make(map[string]TargetInfo, len(infos))
		for _, t := range infos {
			byID[t.ID] = t
		}
		rt := &ResolvedTargets{All: infos, OnlineOnly: v.OnlineOnly}
		for _, id := range v.TargetIDs {
			if t, ok := byID[id]; ok {
				rt.Queried = append(rt.Queried, t)
			} else {
				// view 中的目标已消失：保留占位，coverage 记 not_ready。
				rt.Queried = append(rt.Queried, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		}
		// 排除项（online_only）从 All-Queried 推导。
		inView := make(map[string]bool, len(rt.Queried))
		for _, t := range rt.Queried {
			inView[t.ID] = true
		}
		for _, t := range infos {
			if !inView[t.ID] {
				rt.Excluded = append(rt.Excluded, t)
			}
		}
		v.Budget = readBudgetForQuery(v, q)
		v = c.bindExistingView(ctx, v, rt)
		return v, rt, nil
	}
	// 慢路径新建视图：直接复用 createView 内部那次解析的结果，不再重复解析
	// （见 createView 注释：目标解析 DB 开销由 2 次降为 1 次，且 view.TargetIDs
	// 与实际扇出集合保证同源）。
	view, resolved, err := c.createView(ctx, q)
	if err != nil {
		return View{}, nil, err
	}
	return *view, resolved, nil
}

// authorizeViewReuse 校验当前调用方可复用既有 Query View（F-001/F-006）。
//
// 复用必须同时满足：
//  1. 同一授权主体（PrincipalKey）；
//  2. 固定查询维度与创建时一致（时间范围、filter、onlineOnly、permission scope）；
//  3. 当前授权集合仍覆盖 view.TargetIDs。
//
// 不一致返回 ErrViewReuseMismatch / ErrViewAuthz，禁止静默忽略新条件或越权复用。
func authorizeViewReuse(v View, q Query) error {
	if v.PrincipalKey == "" || q.PrincipalKey == "" || v.PrincipalKey != q.PrincipalKey {
		return fmt.Errorf("%w: principal mismatch", ErrViewAuthz)
	}
	// 固定查询条件指纹（F-006）。
	if v.FromUTC != q.FromUTC || v.ToUTC != q.ToUTC {
		return fmt.Errorf("%w: time range %s..%s != view %s..%s",
			ErrViewReuseMismatch, q.FromUTC, q.ToUTC, v.FromUTC, v.ToUTC)
	}
	if normalizeFilter(v.Filter) != normalizeFilter(q.Filter) {
		return fmt.Errorf("%w: filter mismatch", ErrViewReuseMismatch)
	}
	if v.OnlineOnly != q.OnlineOnly {
		return fmt.Errorf("%w: onlineOnly mismatch", ErrViewReuseMismatch)
	}
	if v.PermissionScope != q.PermissionScope {
		return fmt.Errorf("%w: permission scope mismatch", ErrViewReuseMismatch)
	}
	if v.OrderVersion != "" && q.OrderVersion != "" && v.OrderVersion != q.OrderVersion {
		return fmt.Errorf("%w: order version mismatch", ErrViewReuseMismatch)
	}

	allowed := make(map[string]bool, len(q.AuthorizedTargetIDs))
	for _, id := range q.AuthorizedTargetIDs {
		allowed[id] = true
	}
	if allowed["*"] {
		return nil
	}
	for _, id := range v.TargetIDs {
		authID := id
		if mapped := v.TargetAuthorizationIDs[id]; mapped != "" {
			authID = mapped
		}
		if !allowed[authID] {
			return fmt.Errorf("%w: target %s not in current authorization", ErrViewAuthz, id)
		}
	}
	return nil
}

func normalizeFilter(s string) string {
	return strings.TrimSpace(s)
}

// fanoutSearch 扇出各 Worker Search，返回每路事件、覆盖汇总输入、全局 cut 标记与 Worker 顶层覆盖。
func (c *Coordinator) fanoutSearch(ctx context.Context, view View, resolved *ResolvedTargets, q Query, tailMode string) (
	streams []EventStream,
	covByTarget map[string]TargetCoverage,
	quality Quality,
	workerCut bool,
	workerOpen bool,
	workerCoverage Coverage,
	gaps []IngestPositionGap,
	err error,
) {
	if c.dialer == nil {
		return nil, nil, quality, false, false, Coverage{}, nil, errNoDialer
	}
	covByTarget = make(map[string]TargetCoverage)
	quality.DuplicateQuality = DupExact
	workerCoverage.Complete = true
	workerCoverage.EnumerationState = EnumExhausted
	order, byWorker := resolved.ByWorker()

	// max_fanout 预算：超出的 worker 不扇出，coverage 记 partial。
	fanoutBudget := 0
	if q.Budget.MaxFanout > 0 {
		fanoutBudget = int(q.Budget.MaxFanout)
	}

	budget := readBudgetForQuery(view, q)

	type fanoutResult struct {
		streams         []EventStream
		coverage        map[string]TargetCoverage
		coverageSummary *Coverage
		gaps            []IngestPositionGap
		cut             bool
		open            bool
		quality         Quality
		err             error
	}
	reserved := reserveWorkerBudgets(order, budget)
	if budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	ctx, cancelFanout := context.WithCancel(ctx)
	defer cancelFanout()
	sem := make(chan struct{}, 8)
	results := make(chan fanoutResult, len(order))
	launched := 0
	for i, workerID := range order {
		targets := byWorker[workerID]
		local, allowed := reserved[workerID]
		if !allowed || (fanoutBudget > 0 && i >= fanoutBudget) {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"BUDGET_EXCEEDED"})
				tc.State = CoveragePartial
				covByTarget[t.ID] = tc
			}
			workerCut = true
			workerOpen = true
			workerCoverage.Complete = false
			workerCoverage.PartialReasons = appendUniqueReason(workerCoverage.PartialReasons, "BUDGET_EXCEEDED")
			continue
		}
		launched++
		go func(workerID string, targets []TargetInfo, budget QueryBudget) {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- fanoutResult{err: ctx.Err()}
				return
			}
			if ctx.Err() != nil {
				results <- fanoutResult{err: ctx.Err()}
				return
			}
			workerStreams, workerCoverage, coverageSummary, workerGaps, cut, open, workerQuality, callErr := c.fanoutOne(ctx, view, q, budget, workerID, targets, tailMode)
			results <- fanoutResult{streams: workerStreams, coverage: workerCoverage, coverageSummary: coverageSummary, gaps: workerGaps, cut: cut, open: open, quality: workerQuality, err: callErr}
		}(workerID, append([]TargetInfo(nil), targets...), local)
	}
	for i := 0; i < launched; i++ {
		result := <-results
		if result.err != nil {
			return nil, nil, quality, false, false, workerCoverage, nil, result.err
		}
		streams = append(streams, result.streams...)
		gaps = append(gaps, result.gaps...)
		mergeWorkerCoverage(&workerCoverage, result.coverageSummary)
		for targetID, coverage := range result.coverage {
			covByTarget[targetID] = coverage
		}
		// 按最差值聚合：任一 Worker 报冲突/未决即整体降级，不能被其它 Worker 的 exact 掩盖。
		quality.DuplicateQuality = worstDuplicateQuality(quality.DuplicateQuality, result.quality.DuplicateQuality)
		quality.StatsQuality = worstStatsQuality(quality.StatsQuality, result.quality.StatsQuality)
		workerCut = workerCut || result.cut
		workerOpen = workerOpen || result.open
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].WorkerID < streams[j].WorkerID })

	// duplicate quality：任一 partial → unresolved
	for _, tc := range covByTarget {
		if tc.State == CoveragePartial || tc.State == CoverageStale {
			quality.DuplicateQuality = worstDuplicateQuality(quality.DuplicateQuality, DupUnresolved)
			break
		}
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		a, b := gaps[i], gaps[j]
		if a.WorkerID != b.WorkerID {
			return a.WorkerID < b.WorkerID
		}
		if a.LogSourceID != b.LogSourceID {
			return a.LogSourceID < b.LogSourceID
		}
		if a.SourceGeneration != b.SourceGeneration {
			return a.SourceGeneration < b.SourceGeneration
		}
		return a.StorageNamespace < b.StorageNamespace
	})
	return streams, covByTarget, quality, workerCut, workerOpen, workerCoverage, gaps, nil
}

// readBudgetForQuery 只保留分页 Limit；读取成本属于本次请求，不能由旧 View 覆盖。
func readBudgetForQuery(view View, q Query) QueryBudget {
	budget := q.Budget
	if view.Budget.Limit > 0 {
		budget.Limit = view.Budget.Limit
	}
	if budget.Limit <= 0 {
		budget.Limit = DefaultPageLimit
	}
	if budget.MaxBytes == 0 {
		budget.MaxBytes = 32 << 20
	}
	return budget
}

// reserveWorkerBudgets 在发起读取前预留总额度，禁止把零剩余额度传成 Worker 默认额度。
// 未声明额度沿用既有 VL 单次默认值作为整个联邦请求的上限；每路额度之和不超过总量。
func reserveWorkerBudgets(order []string, budget QueryBudget) map[string]QueryBudget {
	out := make(map[string]QueryBudget, len(order))
	count := len(order)
	if budget.MaxFanout > 0 && uint64(count) > uint64(budget.MaxFanout) {
		count = int(budget.MaxFanout)
	}
	if count == 0 {
		return out
	}
	total := budget.MaxBytes
	if total == 0 {
		total = 32 << 20
	}
	remaining := total
	for _, id := range order[:count] {
		share := remaining / uint64(count)
		if remaining%uint64(count) > 0 {
			share++
		}
		remaining -= share
		count--
		if share == 0 {
			continue
		}
		local := budget
		local.MaxBytes = share
		out[id] = local
	}
	return out
}

// mergeWorkerCoverage 保留顶层部分覆盖与原因；开放/失效枚举不能被另一完整响应覆盖。
func mergeWorkerCoverage(dst *Coverage, src *Coverage) {
	if src == nil {
		return
	}
	dst.Complete = dst.Complete && src.Complete
	for _, r := range src.PartialReasons {
		dst.PartialReasons = appendUniqueReason(dst.PartialReasons, r)
	}
	rank := func(s EnumerationState) int {
		switch s {
		case EnumCancelled:
			return 4
		case EnumStale:
			return 3
		case EnumOpen:
			return 2
		case EnumExhausted:
			return 1
		}
		return 0
	}
	if rank(src.EnumerationState) > rank(dst.EnumerationState) {
		dst.EnumerationState = src.EnumerationState
	}
}

// worstDuplicateQuality 按「越差越优先」合并两个 Worker 上报的重复质量。
//
// 为什么需要：Export 的质量闸门只拦截 unresolved/conflict，故聚合必须是保守的——
// 任一 Worker 报冲突或未决，整体就不能宣称 exact，否则不完整来源会被放行（M-3）。
// 未知取值按与 unspecified 同级处理，不参与升级判定。
func worstDuplicateQuality(a, b DuplicateQuality) DuplicateQuality {
	rank := func(q DuplicateQuality) int {
		switch strings.ToLower(string(q)) {
		case string(DupConflict):
			return 3
		case string(DupUnresolved):
			return 2
		case string(DupExact):
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// worstStatsQuality 同上，用于统计质量（unavailable > partial > exact）。
func worstStatsQuality(a, b StatsQuality) StatsQuality {
	rank := func(q StatsQuality) int {
		switch strings.ToLower(string(q)) {
		case string(StatsQUnavailable):
			return 3
		case string(StatsQPartial):
			return 2
		case string(StatsQExact):
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

func (c *Coordinator) fanoutOne(ctx context.Context, view View, q Query, budget QueryBudget, workerID string, targets []TargetInfo, tailMode string) (
	streams []EventStream, coverage map[string]TargetCoverage, coverageSummary *Coverage, gaps []IngestPositionGap, cut bool, open bool, workerQuality Quality, err error,
) {
	coverage = make(map[string]TargetCoverage, len(targets))
	targetIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		targetIDs = append(targetIDs, workerTargetID(target))
	}
	client, dialErr := c.dialer.Dial(ctx, workerID)
	if dialErr != nil {
		for _, target := range targets {
			tc := CoverageFromReadiness(target, []string{"dial_failed", dialErr.Error()})
			if target.Readiness == ReadyOnline {
				tc.State = CoverageOffline
			}
			coverage[target.ID] = tc
		}
		return
	}
	workerView, viewErr, viewOK := workerViewID(view, workerID)
	// 首屏快路径（q.Fast）不经 bindWorkerViews ⇒ 视图未绑定属**预期** ✓：不发 worker_view_failed，
	// 而是以 ViewID:"" 调 Worker——其 planFromRequest 直接从请求推导计划（已在 service.go:212 核实 ✓），
	// 慢路径（导出等）不置 Fast ⇒ 原有的失败早退一字不动 ✗。
	if !viewOK && !q.Fast {
		for _, target := range targets {
			// 显式补 NotReady：不依赖 CoverageFromReadiness 的原因白名单单点生效，
			// 与 dial_failed/search_failed 的既有写法一致（B-1 双保险）。
			tc := CoverageFromReadiness(target, []string{"worker_view_failed", viewErr})
			if tc.State == CoverageSuccess {
				tc.State = CoverageNotReady
			}
			coverage[target.ID] = tc
		}
		return
	}
	req := WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC, ToUTC: view.ToUTC,
		Filter: view.Filter, Budget: budget, ViewID: workerView,
		Cursor: workerCursor(q.Cursor, workerView, budget.Limit), OrderVersion: view.OrderVersion,
		// 请求级豁免标记随本次查询下发（仅导出路径在 Export 置位 q，其余路径恒为 false）。
		AllowProjectionDrift: q.AllowProjectionDrift}
	var resp *WorkerSearchResponse
	if tailMode == "" {
		resp, err = client.Search(ctx, req)
	} else {
		resp, err = client.Tail(ctx, req, tailMode)
	}
	if err != nil {
		callErr := err
		err = nil
		for _, target := range targets {
			tc := CoverageFromReadiness(target, []string{"search_failed", callErr.Error()})
			if target.Readiness == ReadyOnline {
				tc.State = CoveragePartial
			}
			coverage[target.ID] = tc
		}
		return
	}
	if resp == nil || resp.Unsupported || resp.Error != "" {
		for _, target := range targets {
			switch {
			case resp == nil:
				coverage[target.ID] = CoverageFromReadiness(target, []string{"empty_worker_response"})
			case resp.Unsupported:
				coverage[target.ID] = CoverageFromReadiness(target, []string{"log_rpc_unsupported"})
			default:
				coverage[target.ID] = workerErrorCoverage(target, workerID, resp.Targets, resp.Error)
			}
		}
		return
	}
	coverageSummary = resp.Coverage
	if coverageSummary != nil {
		// 临时诊断（2026-10-11）：追踪 cut 的来源
		if coverageSummary.EnumerationState == EnumOpen || coverageSummary.EnumerationState == EnumStale || coverageSummary.EnumerationState == EnumCancelled {
			open = true
		}
		for _, reason := range coverageSummary.PartialReasons {
			if reason == "BUDGET_EXCEEDED" || reason == "TRUNCATED" {
				cut = true
				break
			}
		}
	}
	if err = ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); err != nil {
		return
	}
	// Worker 上报的查询质量必须带回聚合：它可能与 coverage 不同步（例如 projection 冲突时
	// coverage 仍 complete），丢弃会让 CP 把该结果当成 exact 并放行导出（M-3）。
	workerQuality = resp.Quality
	cut, open = cut || resp.Truncated, open || !resp.Exhausted
	gaps = append([]IngestPositionGap(nil), resp.IngestPositionGaps...)
	for i := range gaps {
		gaps[i].WorkerID = workerID
	}
	if len(resp.Items) > 0 {
		items := append([]Event(nil), resp.Items...)
		SortEventsInPlace(items)
		primary := ""
		if len(targets) == 1 {
			primary = targets[0].ID
		}
		streams = append(streams, EventStream{WorkerID: workerID, TargetID: primary, Items: items})
	}
	got := make(map[string]WorkerTargetResult, len(resp.Targets))
	for _, target := range resp.Targets {
		got[target.TargetID] = target
	}
	for _, target := range targets {
		result, ok := got[workerTargetID(target)]
		if !ok {
			tc := CoverageFromReadiness(target, absentTargetReasons(resp.Targets))
			if len(absentTargetReasons(resp.Targets)) > 0 && tc.State == CoverageSuccess {
				tc.State = CoverageNotReady
			}
			coverage[target.ID] = tc
			continue
		}
		tc := TargetCoverage{TargetID: target.ID, State: result.State,
			Reasons:           append([]string(nil), result.Reasons...),
			ClosedVisibleSeq:  firstNonEmpty(result.ClosedVisibleSeq, target.ClosedVisibleSeq),
			CatalogGeneration: firstNonEmpty(result.CatalogGeneration, target.CatalogGeneration),
			WorkerID:          workerID, HistoricalHolder: target.HistoricalHolder}
		if tc.State == CoverageSuccess && resp.Truncated {
			tc.State = CoveragePartial
			tc.Reasons = append(tc.Reasons, "worker_truncated")
		}
		if tc.State == CoverageUnspecified {
			tc.State = CoverageNotReady
			tc.Reasons = append(tc.Reasons, "unspecified_worker_state")
		}
		coverage[target.ID] = tc
	}
	return
}

// dedupeMergedRows 对本页合并结果做 (EventID, CanonicalHash) **双键**去重，并回报"去重前是否存在同 EventID 多行"：
//   - 去重：同 id **同 hash** ⇒ 真重复（跨代次并集持有同一事件）⇒ 丢弃后出现者，保留排序键最靠前者 ✓（不破坏游标续页语义 ✗）；
//     同 id **不同 hash** ⇒ 内容不同 ⇒ **必须保留** ✓（不得误删）；
//   - 回报：任一同 EventID 出现 >1 ⇒ duplicated=true ⇒ 调用方把质量从 exact 降级为 unresolved ✓——
//     跨代次并集让同一事件重复出现时，不得对外宣称 exact，否则导出闸会放行含重复的结果 ✗。
func dedupeMergedRows(rows []Event) ([]Event, bool) {
	if len(rows) < 2 {
		return rows, false
	}
	seenHash := make(map[string]string, len(rows))
	duplicated := false
	out := make([]Event, 0, len(rows))
	for _, event := range rows {
		if hash, ok := seenHash[event.EventID]; ok {
			duplicated = true
			if hash == event.CanonicalHash {
				continue // 同 id 同 hash：真重复 ⇒ 去 ✓
			}
		}
		seenHash[event.EventID] = event.CanonicalHash
		out = append(out, event)
	}
	return out, duplicated
}

func workerCursor(raw, workerViewID string, pageLimit int) string {
	if raw == "" {
		return ""
	}
	cursor, err := DecodePageCursor(raw)
	if err != nil {
		return ""
	}
	if pageLimit <= 0 {
		pageLimit = cursor.PageLimit
	}
	return workerquery.EncodeCursor(workerquery.Cursor{
		ViewID: workerViewID, OrderVersion: workerquery.SortVersion, PageLimit: uint32(pageLimit),
		SortKey: workerquery.SortKey{
			EventTimeUTC: cursor.SortKey.EventTimeUTC.UTC().Format(time.RFC3339Nano),
			LogSourceID:  cursor.SortKey.LogSourceID, SourceGeneration: cursor.SortKey.SourceGeneration,
			RecordStart: cursor.SortKey.RecordStart, RecordEnd: cursor.SortKey.RecordEnd, EventID: cursor.SortKey.EventID,
		},
	})
}

// Search 跨 Worker K-way merge。
func (c *Coordinator) Search(ctx context.Context, q Query) (*SearchResponse, error) {
	if q.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(q.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	var pageCursor PageCursor
	if q.Cursor != "" {
		var cursorErr error
		pageCursor, cursorErr = DecodePageCursor(q.Cursor)
		if cursorErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrViewReuseMismatch, cursorErr)
		}
		if q.ViewID != "" && q.ViewID != pageCursor.ViewID {
			return nil, fmt.Errorf("%w: cursor view mismatch", ErrViewReuseMismatch)
		}
		q.ViewID = pageCursor.ViewID
		if q.OrderVersion != "" && q.OrderVersion != pageCursor.OrderVersion {
			return nil, fmt.Errorf("%w: cursor order mismatch", ErrViewReuseMismatch)
		}
		q.OrderVersion = pageCursor.OrderVersion
		if q.Budget.Limit != 0 && q.Budget.Limit != pageCursor.PageLimit {
			return nil, fmt.Errorf("%w: cursor page limit mismatch", ErrViewReuseMismatch)
		}
		q.Budget.Limit = pageCursor.PageLimit
	}
	// 首屏快路径（w21 快首屏）：Fast ⇒ resolveForQueryFast（省 bind 往返 ✗，workerView 为空 ⇒
	// fanoutOne 走显式 View=nil 第二路径 ✓）；导出路径不置 Fast ⇒ 慢路径一字不动 ✓。
	var (
		viewFast     View
		resolvedFast *ResolvedTargets
		err          error
	)
	if q.Fast {
		viewFast, resolvedFast, err = c.resolveForQueryFast(ctx, q)
	} else {
		viewFast, resolvedFast, err = c.resolveForQuery(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	view, resolved := viewFast, resolvedFast
	view.Budget = readBudgetForQuery(view, q)
	// 同参秒级 TTL 缓存：命中直接返回（重复/翻页查询免扇出 ✓）。键＝授权集合摘要，**不含主体/
	// scope** ⇒ 同一授权集合的多个调用方共享条目 ✓（跨集合绝不命中、View 身份经 rebind 换绑，
	// 论证见 searchCacheKey）。Export 逐页走本函数，靠 AllowProjectionDrift/Fast/Cursor 段与列表
	// 天然分键 ✗（不是"绕过缓存"——见 searchCacheKey 的豁免标记段注释）。
	// BypassCache（仅预热器）：跳过读取但照常写入，防止预热命中自身旧条目形成雪球。
	if !q.BypassCache {
		if cached, ok := c.lookupSearchCache(q); ok {
			return rebindCachedSearchResponse(q, view, cached), nil
		}
	}
	streams, covBy, quality, workerCut, workerOpen, workerCoverage, gaps, err := c.fanoutSearch(ctx, view, resolved, q, "")
	if err != nil {
		return nil, err
	}

	capRows := view.Budget.Limit
	if capRows == 0 {
		capRows = q.Budget.Limit
	}
	if capRows <= 0 {
		capRows = DefaultPageLimit
	}
	maxBytes := view.Budget.MaxBytes
	if maxBytes == 0 {
		maxBytes = q.Budget.MaxBytes
	}
	rows, pageMore, byteCut := MergeStreams(streams, capRows, maxBytes)
	if workerCut {
		// 现有 wire 未携带 Worker 的安全时间栅栏：其它路的旧行可能越过未完整并列组。
		// 多路扫描截断时不猜边界，返回明确不完整的空前缀；独立缺口凭据仍原样保留。
		workers, _ := resolved.ByWorker()
		if len(workers) > 1 {
			rows = nil
			pageMore = false
		}
	}
	// 唯一口径（见 dedupeMergedRows）：本页双键去重；任一同 id 多行 ⇒ 质量不得保持 exact ✗。
	if deduped, duplicated := dedupeMergedRows(rows); duplicated {
		rows = deduped
		quality.DuplicateQuality = worstDuplicateQuality(quality.DuplicateQuality, DupUnresolved)
	} else {
		rows = deduped
	}
	hasMore := pageMore || workerOpen

	enum := EnumExhausted
	if hasMore || byteCut || workerCut {
		enum = EnumOpen
	}
	for _, s := range streams {
		// 若某路仍有未消费事件，枚举未结束——MergeStreams 已在 budgetCut 反映。
		_ = s
	}

	// 首屏快路径只跳过 closed_visible_seq 固化，不得丢弃已经观察到的逐目标失败。
	// 先构造完整覆盖摘要，再用 Pending 表示尚未完成更严格的快照校验。
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, enum)
	if q.Fast {
		cov.Pending = true
	}
	if !workerCoverage.Complete {
		cov.Complete = false
	}
	for _, reason := range workerCoverage.PartialReasons {
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, reason)
	}
	if workerCoverage.EnumerationState != "" && workerCoverage.EnumerationState != EnumExhausted {
		cov.EnumerationState = workerCoverage.EnumerationState
	}
	if byteCut {
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "BUDGET_EXCEEDED")
		cov.Complete = false
	}
	// FR-480：Search 完成后把 coverage 上的 closed_visible_seq 向量固化到 View，
	// Export 与后续 view 复用必须看到同一向量。
	// 快路径（Fast）未做逐目标校验 ⇒ 该向量无意义，**不固化** ✗（避免下游把它当已校验证据 ✓）。
	if !q.Fast {
		c.persistClosedVisibleSeq(&view, cov)
	}

	resp := &SearchResponse{
		View:               view,
		Coverage:           cov,
		Quality:            quality,
		Items:              rows,
		Exhausted:          !hasMore && !byteCut && !workerCut && cov.Complete,
		BudgetCut:          byteCut || workerCut,
		IngestPositionGaps: gaps,
	}
	if !workerCut && (hasMore || byteCut) && len(rows) > 0 {
		resp.NextCursor = EncodePageCursor(PageCursor{
			ViewID: view.ViewID, OrderVersion: view.OrderVersion,
			SortKey: rows[len(rows)-1].SortKey(), PageLimit: capRows,
		})
	}
	// 同参缓存写入（pending 短档／完整校验常规档 ✓）；返回的指针仅供只读使用 ✓。
	c.storeSearchCache(q, resp)
	return resp, nil
}

// Tail fans out the explicit Tail mode. FOLLOW_LIVE is a bounded dynamic
// window and always keeps enumeration OPEN; it never inherits fixed-history
// completeness from Search pagination.
func (c *Coordinator) Tail(ctx context.Context, q Query, mode string) (*SearchResponse, error) {
	if !strings.EqualFold(mode, "FOLLOW_LIVE") {
		return c.Search(ctx, q)
	}
	view, resolved, err := c.resolveForQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	view.Budget = readBudgetForQuery(view, q)
	streams, covBy, quality, workerCut, _, workerCoverage, gaps, err := c.fanoutSearch(ctx, view, resolved, q, "FOLLOW_LIVE")
	if err != nil {
		return nil, err
	}
	limit := view.Budget.Limit
	if limit <= 0 {
		limit = q.Budget.Limit
	}
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	rows, _, byteCut := MergeStreams(streams, limit, view.Budget.MaxBytes)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumOpen)
	if !workerCoverage.Complete {
		cov.Complete = false
	}
	for _, reason := range workerCoverage.PartialReasons {
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, reason)
	}
	if byteCut || workerCut {
		cov.Complete = false
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "TRUNCATED")
	}
	c.persistClosedVisibleSeq(&view, cov)
	return &SearchResponse{View: view, Coverage: cov, Quality: quality, Items: rows,
		Exhausted: false, BudgetCut: byteCut || workerCut, IngestPositionGaps: gaps}, nil
}

func (c *Coordinator) Fields(ctx context.Context, q Query) (*FieldsResponse, error) {
	if c.dialer == nil {
		return nil, errNoDialer
	}
	view, resolved, err := c.resolveForQuery(ctx, q)
	if err != nil {
		return nil, err
	}
	fieldSet := map[string]bool{}
	covBy := make(map[string]TargetCoverage)
	order, byWorker := resolved.ByWorker()
	for _, workerID := range order {
		targets := byWorker[workerID]
		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			targetIDs = append(targetIDs, workerTargetID(target))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		workerView, _, viewOK := workerViewID(view, workerID)
		if dialErr != nil || !viewOK {
			for _, target := range targets {
				covBy[target.ID] = CoverageFromReadiness(target, []string{"fields_failed"})
			}
			continue
		}
		resp, callErr := client.Fields(ctx, WorkerSearchRequest{TargetIDs: targetIDs, FromUTC: view.FromUTC,
			ToUTC: view.ToUTC, Filter: view.Filter, Budget: view.Budget, ViewID: workerView, OrderVersion: view.OrderVersion})
		if callErr != nil || resp == nil || resp.Unsupported || resp.Error != "" {
			for _, target := range targets {
				covBy[target.ID] = CoverageFromReadiness(target, []string{"fields_failed"})
			}
			continue
		}
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		for _, field := range resp.Fields {
			fieldSet[field] = true
		}
		got := make(map[string]WorkerTargetResult)
		for _, target := range resp.Targets {
			got[target.TargetID] = target
		}
		for _, target := range targets {
			result, ok := got[workerTargetID(target)]
			if !ok {
				covBy[target.ID] = CoverageFromReadiness(target, absentTargetReasons(resp.Targets))
				continue
			}
			covBy[target.ID] = TargetCoverage{TargetID: target.ID, State: result.State,
				Reasons: result.Reasons, ClosedVisibleSeq: result.ClosedVisibleSeq,
				CatalogGeneration: result.CatalogGeneration, WorkerID: workerID,
				HistoricalHolder: target.HistoricalHolder}
		}
	}
	fields := make([]string, 0, len(fieldSet))
	for field := range fieldSet {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	c.persistClosedVisibleSeq(&view, cov)
	return &FieldsResponse{View: view, Coverage: cov, Fields: fields}, nil
}

// persistClosedVisibleSeq 从 coverage targets 提取 closed_visible_seq 向量写回 view（含存储视图）。
func (c *Coordinator) persistClosedVisibleSeq(view *View, cov Coverage) {
	if view == nil {
		return
	}
	cvs := make(map[string]string)
	generations := make(map[string]string)
	for _, tc := range cov.Targets {
		if tc.ClosedVisibleSeq != "" {
			cvs[tc.TargetID] = tc.ClosedVisibleSeq
		}
		if tc.CatalogGeneration != "" {
			generations[tc.TargetID] = tc.CatalogGeneration
		}
	}
	if len(cvs) == 0 && len(generations) == 0 {
		return
	}
	view.ClosedVisibleSeq = cvs
	view.WorkerCatalogGenerations = generations
	viewID := view.ViewID
	if viewID == "" {
		return
	}
	c.mu.Lock()
	if stored, ok := c.views[viewID]; ok {
		stored.ClosedVisibleSeq = cvs
		stored.WorkerCatalogGenerations = generations
		c.views[viewID] = stored
	}
	c.mu.Unlock()
}

// Stats 二次聚合：可合并 count/sum/min/max；avg = sum/count（加权）。
func (c *Coordinator) Stats(ctx context.Context, q StatsQuery) (*StatsResponse, error) {
	if q.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(q.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}
	view.Budget = readBudgetForQuery(view, q.Query)
	covBy := make(map[string]TargetCoverage)
	quality := Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}
	order, byWorker := resolved.ByWorker()

	var merged []StatsPoint
	anyTrunc := false
	workerCoverage := Coverage{Complete: true, EnumerationState: EnumExhausted}
	reserved := reserveWorkerBudgets(order, view.Budget)
	if view.Budget.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(view.Budget.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	for _, workerID := range order {
		targets := byWorker[workerID]
		local, allowed := reserved[workerID]
		if !allowed || ctx.Err() != nil {
			reason := "BUDGET_EXCEEDED"
			if ctx.Err() != nil {
				reason = "CANCELLED"
			}
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{reason})
				tc.State = CoveragePartial
				covBy[t.ID] = tc
			}
			anyTrunc = true
			workerCoverage.Complete = false
			workerCoverage.PartialReasons = appendUniqueReason(workerCoverage.PartialReasons, reason)
			workerCoverage.EnumerationState = EnumOpen
			quality.StatsQuality = StatsQPartial
			continue
		}
		targetIDs := make([]string, 0, len(targets))
		for _, t := range targets {
			targetIDs = append(targetIDs, workerTargetID(t))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"dial_failed", dialErr.Error()})
				if t.Readiness == ReadyOnline {
					tc.State = CoverageOffline
				}
				covBy[t.ID] = tc
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"worker_view_failed", viewErr})
				if tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		resp, callErr := client.Stats(ctx, WorkerSearchRequest{
			TargetIDs:    targetIDs,
			FromUTC:      view.FromUTC,
			ToUTC:        view.ToUTC,
			Filter:       view.Filter,
			Budget:       local,
			ViewID:       workerView,
			OrderVersion: view.OrderVersion,
			MetricField:  q.MetricField,
		}, q.GroupBy, q.TimeBucket)
		if callErr != nil || resp == nil {
			// 稳定字面量必须保留：它是 CoverageFromReadiness 白名单与 normalize 的识别锚点。
			// 若用 callErr.Error() 覆盖它，原因既不入白名单、也无法归一，目标会停留 success
			// 并使 complete 误判为 true（与 B-1 同源的假 complete）。错误文本改为附加原因。
			reasons := []string{"stats_failed"}
			if callErr != nil {
				reasons = append(reasons, callErr.Error())
			}
			for _, t := range targets {
				tc := CoverageFromReadiness(t, reasons)
				if tc.State == CoverageSuccess {
					tc.State = CoveragePartial
				}
				covBy[t.ID] = tc
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		if resp.Unsupported {
			for _, t := range targets {
				covBy[t.ID] = CoverageFromReadiness(t, []string{"log_rpc_unsupported"})
			}
			quality.StatsQuality = StatsQUnavailable
			continue
		}
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		if resp.Error != "" {
			for _, t := range targets {
				covBy[t.ID] = workerErrorCoverage(t, workerID, resp.Targets, resp.Error)
			}
			quality.StatsQuality = StatsQPartial
			continue
		}
		// Worker 上报的统计质量按最差值并入（M-3）：它在 coverage 仍 complete 时也可能
		// 为 partial/unavailable，丢弃会让 CP 把不完整统计当成 exact 并放行导出。
		mergeWorkerCoverage(&workerCoverage, resp.Coverage)
		quality.StatsQuality = worstStatsQuality(quality.StatsQuality, resp.Quality.StatsQuality)
		quality.DuplicateQuality = worstDuplicateQuality(quality.DuplicateQuality, resp.Quality.DuplicateQuality)
		if resp.Truncated {
			anyTrunc = true
		}
		got := make(map[string]WorkerTargetResult)
		for _, tr := range resp.Targets {
			got[tr.TargetID] = tr
		}
		for _, t := range targets {
			tr, ok := got[workerTargetID(t)]
			if !ok {
				tc := CoverageFromReadiness(t, absentTargetReasons(resp.Targets))
				if len(absentTargetReasons(resp.Targets)) > 0 && tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
				continue
			}
			tc := TargetCoverage{
				TargetID:          t.ID,
				State:             tr.State,
				Reasons:           append([]string(nil), tr.Reasons...),
				ClosedVisibleSeq:  firstNonEmpty(tr.ClosedVisibleSeq, t.ClosedVisibleSeq),
				CatalogGeneration: firstNonEmpty(tr.CatalogGeneration, t.CatalogGeneration),
				WorkerID:          workerID,
				HistoricalHolder:  t.HistoricalHolder,
			}
			if tr.State == CoverageSuccess && resp.Truncated {
				tc.State = CoveragePartial
				tc.Reasons = append(tc.Reasons, "stats_truncated")
			}
			covBy[t.ID] = tc
		}
		merged = append(merged, WorkerPointsToStats(resp.Points)...)
	}

	points := MergeStatsPoints(merged)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	mergeWorkerCoverage(&cov, &workerCoverage)
	if anyTrunc {
		cov.Complete = false
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "BUDGET_EXCEEDED")
		cov.EnumerationState = EnumOpen
	}
	if !cov.Complete && quality.StatsQuality == StatsQExact {
		quality.StatsQuality = StatsQPartial
	}
	if anyTrunc && quality.StatsQuality == StatsQExact {
		quality.StatsQuality = StatsQPartial
	}
	c.persistClosedVisibleSeq(&view, cov)

	return &StatsResponse{
		View:       view,
		Coverage:   cov,
		Quality:    quality,
		Points:     points,
		Metric:     q.MetricField,
		TimeBucket: q.TimeBucket,
		BudgetCut:  anyTrunc,
	}, nil
}

// Facets 受控维度精确合并；高基数截断语义传染。
func (c *Coordinator) Facets(ctx context.Context, q FacetsQuery) (*FacetsResponse, error) {
	view, resolved, err := c.resolveForQuery(ctx, q.Query)
	if err != nil {
		return nil, err
	}
	if c.dialer == nil {
		return nil, errNoDialer
	}

	covBy := make(map[string]TargetCoverage)
	quality := Quality{DuplicateQuality: DupExact, StatsQuality: StatsQExact}
	order, byWorker := resolved.ByWorker()

	var perWorker []WorkerFacetsResponse

	for _, workerID := range order {
		targets := byWorker[workerID]
		targetIDs := make([]string, 0, len(targets))
		for _, t := range targets {
			targetIDs = append(targetIDs, workerTargetID(t))
		}
		client, dialErr := c.dialer.Dial(ctx, workerID)
		if dialErr != nil {
			for _, t := range targets {
				covBy[t.ID] = CoverageFromReadiness(t, []string{"dial_failed", dialErr.Error()})
			}
			continue
		}
		workerView, viewErr, viewOK := workerViewID(view, workerID)
		if !viewOK {
			for _, t := range targets {
				tc := CoverageFromReadiness(t, []string{"worker_view_failed", viewErr})
				if tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
			}
			continue
		}
		resp, callErr := client.Facets(ctx, WorkerSearchRequest{
			TargetIDs:    targetIDs,
			FromUTC:      view.FromUTC,
			ToUTC:        view.ToUTC,
			Filter:       view.Filter,
			Budget:       view.Budget,
			ViewID:       workerView,
			OrderVersion: view.OrderVersion,
		}, q.Dimensions, q.DimensionLimit)
		if callErr != nil || resp == nil {
			// 同 Stats：稳定字面量是白名单识别锚点，错误文本只作附加原因，不得覆盖它。
			// Facets 无独立质量标记，若此处失败被误判为 success，用户会静默失去整个
			// Worker 的 facet 面板且无从察觉，故必须显式降为 partial。
			reasons := []string{"facets_failed"}
			if callErr != nil {
				reasons = append(reasons, callErr.Error())
			}
			for _, t := range targets {
				tc := CoverageFromReadiness(t, reasons)
				if tc.State == CoverageSuccess {
					tc.State = CoveragePartial
				}
				covBy[t.ID] = tc
			}
			continue
		}
		if resp.Unsupported {
			for _, t := range targets {
				covBy[t.ID] = CoverageFromReadiness(t, []string{"log_rpc_unsupported"})
			}
			continue
		}
		if bindErr := ensureWorkerViewBinding(view, workerID, targets, resp.ViewID, resp.Targets); bindErr != nil {
			return nil, bindErr
		}
		if resp.Error != "" {
			for _, t := range targets {
				covBy[t.ID] = workerErrorCoverage(t, workerID, resp.Targets, resp.Error)
			}
			continue
		}
		got := make(map[string]WorkerTargetResult)
		for _, tr := range resp.Targets {
			got[tr.TargetID] = tr
		}
		for _, t := range targets {
			tr, ok := got[workerTargetID(t)]
			if !ok {
				tc := CoverageFromReadiness(t, absentTargetReasons(resp.Targets))
				if len(absentTargetReasons(resp.Targets)) > 0 && tc.State == CoverageSuccess {
					tc.State = CoverageNotReady
				}
				covBy[t.ID] = tc
				continue
			}
			covBy[t.ID] = TargetCoverage{
				TargetID:          t.ID,
				State:             tr.State,
				Reasons:           append([]string(nil), tr.Reasons...),
				ClosedVisibleSeq:  firstNonEmpty(tr.ClosedVisibleSeq, t.ClosedVisibleSeq),
				CatalogGeneration: firstNonEmpty(tr.CatalogGeneration, t.CatalogGeneration),
				WorkerID:          workerID,
				HistoricalHolder:  t.HistoricalHolder,
			}
		}
		perWorker = append(perWorker, *resp)
	}

	dims := MergeFacets(perWorker, q.Dimensions, q.DimensionLimit)
	truncated := FacetsTruncated(dims)
	cov := BuildCoverage(resolved.All, covBy, resolved.OnlineOnly, resolved.Excluded, EnumExhausted)
	if truncated {
		cov.PartialReasons = append(cov.PartialReasons, "facet_truncated")
		// Facet 截断不等于目标覆盖失败，但禁止把截断结果当成完整列表交付成功导出。
	}
	c.persistClosedVisibleSeq(&view, cov)

	return &FacetsResponse{
		View:       view,
		Coverage:   cov,
		Quality:    quality,
		Dimensions: dims,
		Truncated:  truncated,
	}, nil
}

// Export 复用同一 view/budget/coverage 的有界物化。
//
// 任一 partial / cut / 超预算 → ExportIncomplete=true 且 Artifact=nil。
// 源 Worker 后续离线不改变已完成成功产物的语义（完整通过后 Artifact 不再依赖 Worker）。
//
// 上限来源与方向（2026-10 修复：导出恒失败）：
//   - 服务端配置（SetExportBudget，默认 10000 行 / 64MiB）是**放宽**的唯一途径；
//   - 请求参数（q.Budget）只能**收紧**，取两者较小值——否则任意调用方传 limit=1e9
//     即可让 CP 逐页读全量并在内存物化，把有界导出变成无界资源入口。
//
// 生产实证：前端导出会主动删掉 limit（apps/control-plane-web/src/api/logFederation.ts），
// 而请求侧又被当作唯一来源，导致 165 目标 × 数十页、每页仅数十行的查询在累计到
// 10000 行时被判 budget_exceeded，用户看到「导出未通过完整性校验」且无任何调大途径。
func (c *Coordinator) Export(ctx context.Context, q Query) (*ExportResult, error) {
	// 服务端配置预算是基准；请求参数只可收紧。
	budget := c.exportBudget.normalize()
	totalRows := budget.MaxRows
	if q.Budget.Limit > 0 && q.Budget.Limit < totalRows {
		totalRows = q.Budget.Limit
	}
	totalBytes := budget.MaxBytes
	if q.Budget.MaxBytes > 0 && q.Budget.MaxBytes < totalBytes {
		totalBytes = q.Budget.MaxBytes
	}
	pageLimit := DefaultPageLimit
	if totalRows < pageLimit {
		pageLimit = totalRows
	}

	// 强制同 view：若调用方未带 ViewID，先建 view 再 Search，保证导出与列表同快照语义。
	searchQ := q
	// 导出豁免「投影清单版本变化」判 VIEW_STALE：本视图由本次导出自己创建、自己读完，
	// 耗时可达数十秒，期间任一源写入新投影（业务服务器仍在产出日志）都会让全部目标降级
	// partial 从而永远拿不到附件（生产实证）。豁免只覆盖该一条分支，closed_visible_seq
	// 缩水（数据变少）仍在 Worker 侧拦截 ⇒ 不会放过数据丢失场景。
	searchQ.AllowProjectionDrift = true
	if searchQ.ViewID == "" {
		viewQuery := q
		viewQuery.Budget.Limit = pageLimit
		viewQuery.Budget.MaxBytes = 0
		view, err := c.CreateView(ctx, viewQuery)
		if err != nil {
			return nil, err
		}
		searchQ.ViewID = view.ViewID
	}
	searchQ.Budget.Limit = pageLimit
	searchQ.Budget.MaxBytes = 0
	var items []Event
	var last *SearchResponse
	var exportQuality Quality
	// 累计字节必须跨页累加：原先每页都对 items 从头重算，页数为 p、总行数为 n 时
	// 总量约 O(n²)（M-2）。此处只对当前页增量累加，判定语义不变。
	var used uint64
	for {
		resp, err := c.Search(ctx, searchQ)
		if err != nil {
			return nil, err
		}
		last = resp
		items = append(items, resp.Items...)
		// 导出共用唯一口径（dedupeMergedRows ✓）：跨页/跨轮同 id 同 hash ⇒ 去重；
		// 任一同 id 多行 ⇒ 质量降级 unresolved。质量只累积到本地变量，禁止改写缓存响应。
		if dedupedItems, duplicatedItems := dedupeMergedRows(items); duplicatedItems {
			items = dedupedItems
			exportQuality.DuplicateQuality = worstDuplicateQuality(exportQuality.DuplicateQuality, DupUnresolved)
		} else {
			items = dedupedItems
		}
		exportQuality.DuplicateQuality = worstDuplicateQuality(exportQuality.DuplicateQuality, resp.Quality.DuplicateQuality)
		exportQuality.StatsQuality = worstStatsQuality(exportQuality.StatsQuality, resp.Quality.StatsQuality)
		for _, event := range resp.Items {
			used += event.ApproxBytes()
		}
		if len(items) > totalRows || used > totalBytes {
			exportView := resp.View
			exportView.Budget.Limit = totalRows
			exportView.Budget.MaxBytes = totalBytes
			// 归因取「哪一条先越界」：两者同时越界时都标，UI 两条提示都可给出。
			return BuildExportWithCut(exportView, resp.Coverage, exportQuality, items, ExportCut{
				Budget: true,
				Rows:   len(items) > totalRows,
				Bytes:  used > totalBytes,
				Worker: true,
			}), nil
		}
		if resp.BudgetCut || !resp.Coverage.Complete {
			exportView := resp.View
			exportView.Budget.Limit = totalRows
			exportView.Budget.MaxBytes = totalBytes
			// resp.BudgetCut 由 Search 侧给出，无法细分行/字节 ⇒ 只标笼统预算码。
			return BuildExportWithCut(exportView, resp.Coverage, resp.Quality, items, ExportCut{
				Budget: resp.BudgetCut,
				Worker: true,
			}), nil
		}
		if resp.Exhausted {
			break
		}
		if resp.NextCursor == "" || len(items) >= totalRows {
			exportView := resp.View
			exportView.Budget.Limit = totalRows
			exportView.Budget.MaxBytes = totalBytes
			// 达到行数上限而停止取页（实取行数恰好等于上限，故必须显式标 Rows，
			// 否则「len(items) > Limit」永不成立，用户只能看到笼统的 budget_exceeded）。
			return BuildExportWithCut(exportView, resp.Coverage, exportQuality, items, ExportCut{
				Budget: true,
				Rows:   len(items) >= totalRows,
				Worker: true,
			}), nil
		}
		searchQ.Cursor = resp.NextCursor
		searchQ.ViewID = resp.View.ViewID
		if cursor, decodeErr := DecodePageCursor(resp.NextCursor); decodeErr == nil {
			searchQ.Budget.Limit = cursor.PageLimit
		}
	}
	if last == nil {
		return nil, fmt.Errorf("logcoord: export produced no search response")
	}
	exportView := last.View
	exportView.Budget.Limit = totalRows
	exportView.Budget.MaxBytes = totalBytes
	return BuildExport(exportView, last.Coverage, exportQuality, items, false, coverageHasCut(last.Coverage)), nil
}

func coverageHasCut(cov Coverage) bool {
	for _, tc := range cov.Targets {
		for _, r := range tc.Reasons {
			if r == "worker_truncated" || r == "stats_truncated" || r == "fanout_budget_exceeded" {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
