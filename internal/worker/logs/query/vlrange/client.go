// Package vlrange adapts the localhost VictoriaLogs LogsQL HTTP API to the
// worker query.RangeClient contract.
package vlrange

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

const (
	queryPath = "/select/logsql/query"
	// A request without an explicit byte budget still needs a bounded client-side
	// guard. The query service reports the guard as partial rather than claiming
	// a complete result after truncation.
	defaultMaxScanBytes uint64 = 32 << 20
)

// Client executes bounded LogsQL queries against one localhost VL instance.
type Client struct {
	vl *vlsup.Client
}

// New creates a RangeClient adapter. vlsup.Client already enforces loopback
// binding and local Basic auth.
func New(vl *vlsup.Client) (*Client, error) {
	if vl == nil {
		return nil, fmt.Errorf("vlrange: nil VictoriaLogs client")
	}
	return &Client{vl: vl}, nil
}

// Search reads the canonical event fields emitted by the managed log pipeline.
func (c *Client) Search(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	if c == nil || c.vl == nil {
		return query.RangeResult{}, query.ErrRangeClientUnimplemented
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()
	ctx = query.WithPhysicalReadBudget(ctx, q.Budget)
	params, empty, err := c.params(rng, q)
	if err != nil {
		return query.RangeResult{}, err
	}
	if empty {
		return query.RangeResult{Exhausted: true, DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}, nil
	}
	text := searchQuery(rng, q)
	if !physicalQueryFits(text, q.Filter) {
		// 长代次列表只粗筛有界来源，完整冻结谓词仍由逐行归因执行。
		boundedRange := rng
		boundedRange.ClosedVisibleSeq = q.ClosedVisibleSeq
		return c.SearchMerged(ctx, []query.AuthoritativeRange{boundedRange}, q)
	}
	params.Set("query", text)
	params.Set("limit", strconv.FormatUint(lookaheadRows(q), 10))
	return c.searchPage(ctx, params, q, lookaheadRows(q), func(ev logtypes.Event) bool {
		// 现代 scope 在 LogsQL 中已逐源封闭；旧格式的零水位绝不解释成无限。
		//
		// 2026-11-05：与 selectorQuery 同步修正。原条件把「零水位放行」绑定在 `scoped` 上：
		//     scoped && cvs == 0  ||  ev.Record.End <= cvs
		// 而 `ingest.ensureCatalogPartition` 补建的记录 `scoped=false` 且 `cvs=0`
		// ⇒ 第二个子项要求 `Record.End <= 0` ⇒ **行级也把每一条丢掉**。
		// 只改 selector 不改这里仍然查不到——两处必须同口径。
		//
		// 改后：`cvs == 0` 一律表示「没有封窗信息」⇒ 不加上界；
		// `cvs > 0` 时仍要求 `Record.End <= cvs`（封窗语义一字未变）。
		return q.ClosedVisibleSeq == 0 || ev.Record.End <= q.ClosedVisibleSeq
	})
}

// Stats uses the LogsQL stats pipe so aggregation happens in VL over the
// authoritative range, then maps each NDJSON row to the worker aggregate type.
func (c *Client) statsSelector(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, selector string) (query.StatsResult, error) {
	if c == nil || c.vl == nil {
		return query.StatsResult{}, query.ErrRangeClientUnimplemented
	}
	pipe, err := statsPipe(q)
	if err != nil {
		return query.StatsResult{}, err
	}
	params, empty, err := c.params(rng, q)
	if err != nil {
		return query.StatsResult{}, err
	}
	if empty {
		return query.StatsResult{DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}, nil
	}
	params.Set("query", selector+pipe)
	var out query.StatsResult
	bytesRead, truncated, err := c.stream(ctx, params, scanLimit(q.Budget), 0, func(line []byte) error {
		point, err := statsFromJSON(line, q)
		if err != nil {
			return err
		}
		out.Points = append(out.Points, point)
		return nil
	})
	out.Bytes = bytesRead
	if err != nil {
		return out, err
	}
	out.DuplicateQuality = query.DupExact
	out.StatsQuality = query.StatsExact
	if truncated {
		out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
		out.StatsQuality = query.StatsPartial
	}
	return out, nil
}

// Fields 返回该时间窗内**出现过的字段名全集**。
//
// # 2026-11-05 修正（架构审计 D3）
//
// 旧实现调 `Search` 拿一页日志、遍历返回行的 `Fields` map 去重——
// 那只能看到**本页那几十条**日志恰好带的字段，不是该时间窗的字段全集。
//
// 实测同一窗口（inst:318 / 2026-11-04）：
//
//	旧实现      → [level, stream]（因为那页日志只带这两个额外字段）
//	field_names → 15+ 个（_msg / event_id / canonical_content_hash / record_start / ...）
//
// 后者才是「这个窗口里有哪些字段」的正确答案。官方端点专为此设计，一次聚合即得。
//
// 仍然用 range 的权威谓词做作用域约束（`selectorQuery`）：字段名不能跨源泄漏，
// 否则用户会看到别的实例才有、本实例根本没有的字段，点了必然查不到。
func (c *Client) Fields(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.FieldsResult, error) {
	if c == nil || c.vl == nil {
		return query.FieldsResult{}, query.ErrRangeClientUnimplemented
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()

	start, end, empty, err := c.fieldNamesWindow(rng, q)
	if err != nil {
		return query.FieldsResult{Truncated: true}, err
	}
	if empty {
		return query.FieldsResult{Fields: nil, Truncated: false}, nil
	}
	names, err := c.vl.FieldNames(ctx, selectorQuery(rng, q), q.Filter, start, end)
	if err != nil {
		// 聚合查询失败不得伪装成「没有字段」：那会让前端把筛选面板整个清空。
		return query.FieldsResult{Truncated: true, CoverageReasons: []string{query.ReasonRangeUnavailable}}, err
	}
	sort.Strings(names)
	return query.FieldsResult{Fields: names, Truncated: false}, nil
}

// fieldNamesWindow 算出 field_names 查询该用的时间窗（与 range 的分区日 + 请求窗取交集）。
func (c *Client) fieldNamesWindow(rng query.AuthoritativeRange, q query.RangeQuery) (time.Time, time.Time, bool, error) {
	day := rng.Key.UTCDay
	if day == "" {
		return time.Time{}, time.Time{}, true, nil
	}
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("vlrange: invalid Catalog UTC day: %w", err)
	}
	start, end := parsed, parsed.AddDate(0, 0, 1)
	if q.TimeRange.FromUTC != "" {
		if from, parseErr := time.Parse(time.RFC3339Nano, q.TimeRange.FromUTC); parseErr == nil && from.After(start) {
			start = from
		}
	}
	if q.TimeRange.ToUTC != "" {
		if to, parseErr := time.Parse(time.RFC3339Nano, q.TimeRange.ToUTC); parseErr == nil && to.Before(end) {
			end = to
		}
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, true, nil
	}
	return start, end, false, nil
}

// Facets 按调用方指定的维度聚合，走官方 `/select/logsql/field_values`。
//
// # 与旧实现的区别
//
// 旧实现借 Stats 的 LogsQL 管道（`| stats by (dim) count()`）：每个维度一次请求，
// 且要自己拼管道、自己解 NDJSON。新实现打官方聚合端点，语义同为「按指定字段
// 精确聚合」，但由服务端直接给结构化结果、少一层管道解析。
//
// 请求数**不变**（仍然每个维度一次）——这是 VL 的接口形状决定的：
// `field_values` 只接受单个 `field`。所以本次改造的收益是「用对端点」，
// 不是「减少往返」。
//
// # 为什么不直接打官方 `/select/logsql/facets`
//
// 见 facets_field_values.go 文件头：它的语义是「自动发现低基数字段」，
// `field=` 被忽略，与「按指定维度查」的契约不匹配。
//
// # 预算与截断
//
// 单维度超长查询走互斥物理分片（与 Stats 同一份实现），逐片聚合后按值相加；
// 总字节额度递减与「见底即 partial」的语义与旧实现一致。
func (c *Client) Facets(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, dimensions []string, limit uint32) (query.RangeFacetsResult, error) {
	if c == nil || c.vl == nil {
		return query.RangeFacetsResult{}, query.ErrRangeClientUnimplemented
	}
	if limit == 0 {
		limit = 100
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()
	ctx = query.WithPhysicalReadBudget(ctx, q.Budget)
	return c.facetsAllDimensions(ctx, rng, q, dimensions, limit)
}

// facetsAllDimensions 逐维度聚合，共用同一份字节额度。
//
// 抽出来是因为 Facets 需要先建 ctx（deadline + 物理读预算）再进扇出；
// 维度循环本身不关心 ctx 怎么来的。
func (c *Client) facetsAllDimensions(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, dimensions []string, limit uint32) (query.RangeFacetsResult, error) {
	var out query.RangeFacetsResult
	for _, dimension := range dimensions {
		if !validField(dimension) || dimension == "event_id" || dimension == "message" || dimension == "_time" {
			continue
		}
		if out.Bytes >= scanLimit(q.Budget) {
			out.Truncated = true
			out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			break
		}
		copyQ := q
		copyQ.Budget.MaxBytes = scanLimit(q.Budget) - out.Bytes
		part, err := c.facetsViaFieldValues(ctx, rng, copyQ, dimension, limit)
		out.Bytes += part.Bytes
		out.CoverageReasons = append(out.CoverageReasons, part.CoverageReasons...)
		if part.Truncated {
			out.Truncated = true
		}
		out.Values = append(out.Values, part.Values...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// Tail implements the fixed VIEW_BOUNDED semantics. FOLLOW_LIVE needs the
// separate streaming subscription contract and remains explicitly unsupported.
func (c *Client) Tail(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, mode query.TailMode) (query.RangeResult, error) {
	if mode == query.TailFollowLive {
		return query.RangeResult{}, query.ErrRangeClientUnimplemented
	}
	return c.Search(ctx, rng, q)
}

// FollowLive reads one published range for a live cycle. The query Service
// owns Catalog refresh, cancellation and the observation window.
func (c *Client) FollowLive(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	res, err := c.Search(ctx, rng, q)
	res.Exhausted = false
	return res, err
}

func (c *Client) params(rng query.AuthoritativeRange, q query.RangeQuery) (url.Values, bool, error) {
	return c.dayParams(rng.Key.UTCDay, q)
}

// dayParams 构造某 UTC 天分区的 HTTP 查询参数（start/end/游标收窄/extra_filters）。
// 单 range 与合并粗筛（merged.go）共用 windowParams 的窗口语义——两个入口的
// start/end 计算必须逐字一致，否则合并路径会看到与逐 range 路径不同的数据面。
func (c *Client) dayParams(utcDay string, q query.RangeQuery) (url.Values, bool, error) {
	start, err := time.Parse("2006-01-02", utcDay)
	if err != nil {
		return nil, false, fmt.Errorf("vlrange: invalid Catalog UTC day: %w", err)
	}
	return c.windowParams(start, start.AddDate(0, 0, 1), q)
}

// windowParams 构造 [start,end) 窗口的 HTTP 查询参数。
//
// 2026-10-08：由 dayParams 抽出以支持**跨天窗口**（merged.go 的合并粗筛把同 Owner
// 的多天 rng 合并为一次查询——按天拆分会让 VL 对每个窗口独立全扫，实测 10× 差距）。
// 收窄规则（TimeRange 夹取 / 游标上界 / extra_filters）与单天路径逐字相同。
func (c *Client) windowParams(start, end time.Time, q query.RangeQuery) (url.Values, bool, error) {
	if q.TimeRange.FromUTC != "" {
		from, err := time.Parse(time.RFC3339Nano, q.TimeRange.FromUTC)
		if err != nil {
			return nil, false, fmt.Errorf("vlrange: invalid query start: %w", err)
		}
		if from.After(start) {
			start = from
		}
	}
	if q.TimeRange.ToUTC != "" {
		to, err := time.Parse(time.RFC3339Nano, q.TimeRange.ToUTC)
		if err != nil {
			return nil, false, fmt.Errorf("vlrange: invalid query end: %w", err)
		}
		if to.Before(end) {
			end = to
		}
	}
	// 游标页窗口收窄（P0-1，2026-10-02 独立评审）：
	// searchQuery 恒带 `| sort by (_time desc) | limit (N+1)`，若游标页与第一页共用同一窗口，
	// VL 会返回**同一批 top-(N+1) 行**；客户端 filterAfterCursor 过滤掉已返回过的行后只剩
	// ≤1 行 → 服务端判 exhausted 且 next_cursor 为空 → 第一页之后的日志全部不可达
	// （用户表现为「日志查不到」）。
	//
	// 收窄到「不晚于游标时刻」并**保留**服务端 top-K 截断（游标页预算 = N+2，见 lookaheadRows）：
	// 这样窗口内前 N+2 行必然覆盖「本页应返回的 N 行 + 1 条前瞻」，其中多出的 1 行正是
	// 窗口首行——游标行本身，它会被客户端全键过滤丢弃。
	// HTTP end 为开区间 [start,end)，故取 游标时刻+1ns 等价于 `_time <= 游标时刻`；
	// **不能**直接取游标时刻（开区间会整组排除同一时间戳的并列行 → 漏行）。
	// 并列行之间的先后由客户端全键比较 + filterAfterCursor 承接（残余风险：同一时间戳并列行
	// 若被 top-K 截断在组内仍可能漏，见 searchQuery 注释，毫秒级时间戳下极罕见）。
	if q.CursorSortKey != nil {
		cursorTime, err := time.Parse(time.RFC3339Nano, q.CursorSortKey.EventTimeUTC)
		if err != nil {
			return nil, false, fmt.Errorf("vlrange: invalid cursor sort key time %q: %w", q.CursorSortKey.EventTimeUTC, err)
		}
		if limit := cursorTime.Add(time.Nanosecond); limit.Before(end) {
			end = limit
		}
	}
	if !start.Before(end) {
		// 游标时刻早于窗口下界：窗口内不可能存在「排在游标之后」的行（更旧的行在窗内、
		// 更新的行已在前页返回），返回空页等价于该页确实为空。
		return nil, true, nil
	}
	// VL v1.52.0 treats the HTTP end parameter as exclusive, matching [start,end).
	params := url.Values{"start": {start.UTC().Format(time.RFC3339Nano)}, "end": {end.UTC().Format(time.RFC3339Nano)}}
	// 用户过滤走 extra_filters，由 VL 独立解析后与 query 内的作用域约束 AND。
	// 不得把用户过滤拼进 query：否则 (scope) AND (filter) 会被 OR/管道逃逸（实测
	// `(ZZZ) OR (*) | stream_context after 5` 可越界读到其它实例的日志）。
	if f := strings.TrimSpace(q.Filter); f != "" {
		params.Set("extra_filters", f)
	}
	return params, false, nil
}

// lookaheadRows 返回服务端单页需要返回的行数：N 条页内 + 1 条前瞻；游标页再 +1，
// 用于抵消「收窄后的窗口必含游标行本身、而它会被客户端全键过滤丢弃」这一固定损耗。
func lookaheadRows(q query.RangeQuery) uint64 {
	rows := uint64(q.Budget.EffectiveLimit()) + 1
	if q.CursorSortKey != nil {
		rows++
	}
	return rows
}

func searchQuery(rng query.AuthoritativeRange, q query.RangeQuery) string {
	// 管道顺序是性能关键（2026-09-30 生产事故，VL 直接 400）：
	//   ① `| fields` 必须先于 `| sort`：否则 VL 对**全宽行**（含 _msg 等）做全量排序物化；
	//   ② `| sort` 后必须**紧跟** `| limit`：相邻时 VL 才走 top-K（内存只保留 limit 行）。
	// 排序键只用 `_time desc`（2026-09-30 实测修正）：多键排序（log_source_id/record_* 等）
	// 会让 VL 无法沿**时间有序**的存储做块合并，必须整块物化再排序——实测单次联邦调用
	// 扫描 77M 行 / 169MB（~2.9s），而仅时间排序的同一批数据只要 ~40ms（差 ~70×）。
	// 全局次序由服务端已有的客户端全键排序（SortEvents）与游标过滤承接；游标按全键
	// 严格「大于」过滤，跨页重复被吸收。
	// 游标页的窗口由 params() 收窄到 `_time <= 游标时刻`（P0-1）：不放宽则此处 top-K 会
	// 返回与第一页相同的一批行，游标过滤后只剩 ≤1 行，第二页起全部不可达。
	// 时间 top-K 不能证明页边界并列组完整；searchPage 只补取该时间组，再按冻结复合键分页。
	// 未完整组不会输出为游标锚点，不把低概率并列当成漏行可以接受的理由。
	return selectorQuery(rng, q) +
		" | fields _time, _msg, event_id, log_source_id, source_generation, parser_version, record_start, record_end, ingest_time_utc, level, stream, instance_id, canonical_content_hash" +
		" | sort by (_time desc)" +
		" | limit " + strconv.FormatUint(lookaheadRows(q), 10)
}

func selectorQuery(rng query.AuthoritativeRange, q query.RangeQuery) string {
	parts := make([]string, 0, 3)
	scoped := rng.Projection != nil && len(rng.Projection.SourceProjections) > 0
	var covered []string
	if scoped {
		// **不再按 `projection_generation` 过滤**（2026-11-02 简化投递路径）。
		//
		// 旧谓词是 `source_generation=... AND (projection_generation IN 白名单) AND record_end<=水位`，
		// 且白名单为空时该 scope 被整个跳过、全部跳过则生成 `record_end:<0`（什么都查不到）。
		// 实测后果：一个源 22334 条数据全在 VL 里，但白名单只认 1 个代次
		// ⇒ 查询只看得见 1160 条 ⇒ 其余被误判成「没落库」⇒ 缺口永不消解 ⇒ 积压丢弃。
		//
		// 简化后写入端不再产生重写副本（一批只写一次，event_id 幂等），
		// 「同一事件多份副本需要按批次筛掉」这个前提消失了，因此直接去掉该维度。
		// 历史数据虽仍带该标签，但不再被查询，老数据照常可查（这正是要的效果）。
		//
		// 保留的约束（一条都没放宽）：
		//   · `log_source_id` 精确匹配 —— 跨源隔离不变
		//   · `source_generation` 精确匹配 —— 跨代隔离不变
		//   · `record_end <= ClosedVisibleSeq` —— 水位语义不变
		type scopeGroup struct {
			predicate string
			sources   []string
		}
		groups := make([]scopeGroup, 0, len(rng.Projection.SourceProjections))
		indices := make(map[string]int, len(rng.Projection.SourceProjections))
		for _, scope := range rng.Projection.SourceProjections {
			predicate := `source_generation:="` + escape(scope.SourceGeneration) + `" AND record_end:<=` + strconv.FormatUint(scope.ClosedVisibleSeq, 10)
			index, exists := indices[predicate]
			if !exists {
				index = len(groups)
				indices[predicate] = index
				groups = append(groups, scopeGroup{predicate: predicate})
			}
			groups[index].sources = append(groups[index].sources, `log_source_id:="`+escape(scope.LogSourceID)+`"`)
		}
		// 分组与成员均按首次出现顺序输出；单成员保持原谓词形状。
		for _, group := range groups {
			sources := group.sources[0]
			if len(group.sources) > 1 {
				sources = "(" + strings.Join(group.sources, " OR ") + ")"
			}
			covered = append(covered, "("+sources+" AND "+group.predicate+")")
		}
	} else if rng.Projection != nil {
		for _, src := range rng.Projection.CoveredSourceGenerations {
			if src.LogSourceID != "" && src.SourceGeneration != "" {
				covered = append(covered, `(log_source_id:="`+escape(src.LogSourceID)+`" AND source_generation:="`+escape(src.SourceGeneration)+`")`)
			}
		}
	}
	switch {
	case len(covered) > 0:
		parts = append(parts, "("+strings.Join(covered, " OR ")+")")
	default:
		// 回退到分区命名空间（`PartitionKey.StorageNamespace`）。
		//
		// 2026-11-05 修正（生产日志查询整页为空的**第三个**真 bug）：
		// 原写法把命名空间当**完整源 id** 做等值匹配 `log_source_id:="inst:318"`。
		// 而 VL 行上的 `log_source_id` 恒为 `<TargetID>/<stream>`（`inst:318/file`），
		// 两者从不相等 ⇒ 该 range 恒查到 0 行，且接口仍报
		// `complete=true` / `EXHAUSTED` / `quality=exact`（空结果被粉饰成「完整无数据」）。
		//
		// 何时会走到这里：简化投递路径后，Catalog 记录由 `ingest.ensureCatalogPartition`
		// 补建，它只填命名空间、**刻意不填** `SourceProjections`/`CoveredSourceGenerations`
		// ⇒ `scoped=false` 且 `covered` 为空 ⇒ 必然走本分支。也就是说这不是边角情况，
		// 而是新架构下投递路径产生的**唯一**形态。
		//
		// 真机证据（inst:318，2026-11-04 单日窗，VL v1.52.0）：
		//   - log_source_id:="inst:318"                       -> 0 条
		//   - log_source_id:="inst:318/file"                  -> 615 条
		//   - (log_source_id:="inst:318" OR log_source_id:"inst:318/"*) -> 615 条
		//
		// 为何保留「等值」那一支：历史上存在 `LogSourceID == StorageNamespace` 的源
		// （如 `node:1` 这种没有 `/stream` 后缀的），丢了它会把它们变不可见。
		// 前缀以 `/` 收口：不加则 `inst:3` 会顺带匹配到 `inst:31`（跨源泄漏）。
		//
		// 与内存归因侧同源：`query.SourceScope.MatchesLogSourceID` 是唯一判据，
		// 两处不得各写一套（那正是本次故障的同源形态）。
		if scope := (query.SourceScope{Namespace: rng.Key.StorageNamespace}); scope.Namespace != "" {
			parts = append(parts, "("+namespaceSelector(scope.Namespace)+")")
		}
	}
	// 非 scoped 的 `projection_generation` 过滤同样去掉（理由同上）。
	//
	// 2026-11-05 修正（生产日志查询整页为空的真 bug）：条件从 `!scoped || cvs > 0`
	// 收紧为 `cvs > 0`。
	//
	// # 为什么 `!scoped` 不能无条件加上界
	//
	// 简化投递路径后，Catalog 分区记录由 `ingest.ensureCatalogPartition` 补建，
	// 它**刻意不填** `SourceProjections` 与 `ClosedVisibleSeq`（见该函数注释：
	// 一批数据只写一次、行上没有代次标签）。于是这条记录来到此处时 `scoped=false`
	// 且 `cvs=0` ⇒ 旧条件命中 `!scoped` 分支 ⇒ 内建 `record_end:<=0`
	// ⇒ **VL 里每条都被过滤掉**。
	//
	// 真机证据（inst:320/file，同一时间窗）：
	//   加   record_end:<=0     -> 0 条
	//   去掉 record_end 上界   -> 194 条
	// 而接口报的是 `complete=true` / `EXHAUSTED` / `quality=exact`——
	// 空结果被当成“完整且无数据”，比报错更难排查。
	//
	// # 为什么 `cvs > 0` 就够
	//
	// 水位 > 0 是“该分区已封窗”的真实语义，上界必须保留（反面用例：
	// TestSelectorStillBoundsWhenClosedVisibleSeqIsSet）。而 cvs=0 只有一个含义：
	// “没有封窗信息” ⇒ 不得解释成零上界，否则就是把「不知道」当成「什么都不包含」。
	// 这与 Search 的行级归因谓词逐字同口径（client.go 的
	// `Projection != nil && len(SourceProjections) > 0 && ClosedVisibleSeq == 0 ||
	// ev.Record.End <= q.ClosedVisibleSeq`）——两处必须一致，否则 selector 会
	// 先把行筛掉，行级判断再好也无力回天。
	if q.ClosedVisibleSeq > 0 {
		parts = append(parts, "record_end:<="+strconv.FormatUint(q.ClosedVisibleSeq, 10))
	}
	base := "*"
	if len(parts) > 0 {
		base = strings.Join(parts, " AND ")
	}
	// 注意：用户过滤（q.Filter）不在此拼接。作用域约束必须独占 query 参数，
	// 否则用户语法可与作用域条件发生运算符优先级交互（`scope AND (f) OR (*)`
	// 会被解析为 `(scope AND f) OR (*)`），或借管道重扫越界。用户过滤统一由
	// params() 放进 extra_filters，由 VL 独立解析后与本 query AND 组合。
	return base
}

// namespaceSelector 把命名空间渲染成「等值 OR 前缀」的 LogsQL 来源谓词。
//
// 为何不是单条等值：VL 行的 `log_source_id` 是完整源 id（`<TargetID>/<stream>`），
// 与命名空间从不相等，只写等值就恒查不到（2026-11-05 真机故障）。
// 为何前缀以 `/` 收口：不收口会让 `inst:3` 匹配到 `inst:31` —— 那是跨源泄漏，
// 比查不到严重得多。VL 的 `"…"*` 是字面前缀匹配（已在本机 v1.52.0 实测：
// `log_source_id:"inst:31/"*` 得 0 条，`log_source_id:"inst:318/"*` 得 615 条）。
func namespaceSelector(namespace string) string {
	return `log_source_id:="` + escape(namespace) + `" OR log_source_id:"` + escape(namespace) + `/"*`
}

func projectionGenerations(projection *catalog.PublishedProjection) []string {
	if projection == nil {
		return nil
	}
	if len(projection.ProjectionGenerations) > 0 {
		return projection.ProjectionGenerations
	}
	if projection.ProjectionGeneration != "" {
		return []string{projection.ProjectionGeneration}
	}
	return nil
}

func statsPipe(q query.RangeQuery) (string, error) {
	groups := append([]string(nil), q.GroupBy...)
	if q.TimeBucket != "" {
		if !validField(q.TimeBucket) && !validTimeBucket(q.TimeBucket) {
			return "", fmt.Errorf("vlrange: invalid time bucket %q", q.TimeBucket)
		}
		groups = append([]string{"_time:" + q.TimeBucket}, groups...)
	}
	for _, field := range groups {
		if !validField(field) && !validTimeGroup(field) {
			return "", fmt.Errorf("vlrange: invalid group field %q", field)
		}
	}
	var b strings.Builder
	if q.MetricField != "" {
		if !validField(q.MetricField) {
			return "", fmt.Errorf("vlrange: invalid metric field %q", q.MetricField)
		}
		// sum/count is merged across Workers. Count the same numeric sample set
		// as sum, not every log including missing or non-numeric values.
		b.WriteString(" | filter ")
		b.WriteString(q.MetricField)
		b.WriteString(":range[-inf,inf]")
	}
	b.WriteString(" | stats")
	if len(groups) > 0 {
		b.WriteString(" by (")
		b.WriteString(strings.Join(groups, ","))
		b.WriteString(")")
	}
	b.WriteString(" count() as _jm_count")
	if q.MetricField != "" {
		if !validField(q.MetricField) {
			return "", fmt.Errorf("vlrange: invalid metric field %q", q.MetricField)
		}
		b.WriteString(", sum(")
		b.WriteString(q.MetricField)
		b.WriteString(") as _jm_sum, min(")
		b.WriteString(q.MetricField)
		b.WriteString(") as _jm_min, max(")
		b.WriteString(q.MetricField)
		b.WriteString(") as _jm_max, avg(")
		b.WriteString(q.MetricField)
		b.WriteString(") as _jm_avg")
	}
	return b.String(), nil
}

// searchPage 只在页边界组未被更旧时间证明完整时补取该时间组，不改变整窗时间 top-K。
// 补查与向更旧窗口续读共用同一字节额度；不完整组只公布严格更新的安全前缀。
func (c *Client) searchPage(ctx context.Context, params url.Values, q query.RangeQuery, rows uint64, matches func(logtypes.Event) bool, hydrationSelectors ...func(logtypes.Event) string) (query.RangeResult, error) {
	out := query.RangeResult{DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}
	maxBytes := scanLimit(q.Budget)
	// seen 按「id+哈希」去重：同一事件被重复写入时只保留一条。
	seen := make(map[string]bool)
	// hashByID 记录每个 event_id 首次出现的规范化哈希，用于识别「同 id 异哈希」。
	// 为什么需要它：同 id 异哈希是**无法判定真伪**的歧义副本（内容已分歧，却共用同一事件身份），
	// 两条都必须保留给上层归因，因此去重键不能只取 event_id；但质量维度必须据此降级，
	// 否则同一份数据经单 range 路径报 exact、经合并路径（merged.go 对同 id 多哈希置
	// DupUnresolved）报 unresolved，且导出门闸会放行这份歧义数据。
	hashByID := make(map[string]string)
	appendEvents := func(events []logtypes.Event) {
		for _, ev := range events {
			if !matches(ev) || (q.CursorSortKey != nil && query.CompareSortKey(query.SortKeyOf(ev), *q.CursorSortKey) <= 0) {
				continue
			}
			key := ev.EventID + "\x00" + ev.CanonicalHash
			if seen[key] {
				// 同 id 同哈希的纯重复：已在上方按 id+哈希丢弃，不构成歧义，
				// 因此这里**不能**降级质量（否则普通重复会把导出误封）。
				continue
			}
			seen[key] = true
			if prev, ok := hashByID[ev.EventID]; ok {
				if prev != ev.CanonicalHash {
					// 同 id 已出现不同哈希 ⇒ 歧义副本；标记一旦置上不回头（与合并路径一致）。
					out.DuplicateQuality = query.DupUnresolved
				}
			} else {
				hashByID[ev.EventID] = ev.CanonicalHash
			}
			out.Items = append(out.Items, ev)
		}
	}
	partial := func(stamp string) {
		out.SafeBeforeUTC = stamp
		out.Exhausted = false
		out.StatsQuality = query.StatsPartial
		out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
	}
	readEvents := func(p url.Values) ([]logtypes.Event, bool, error) {
		if out.Bytes >= maxBytes {
			return nil, true, nil
		}
		var events []logtypes.Event
		n, cut, err := c.stream(ctx, p, maxBytes-out.Bytes, 0, func(line []byte) error {
			ev, err := eventFromJSON(line)
			if err == nil {
				events = append(events, ev)
			}
			return err
		})
		out.Bytes += n
		return events, cut, err
	}
	p := cloneParams(params)
	baseQuery := p.Get("query")
	if i := strings.LastIndex(baseQuery, " | limit "); i >= 0 {
		baseQuery = baseQuery[:i]
	}
	for {
		raw, byteCut, err := readEvents(p)
		if err != nil {
			return out, err
		}
		if len(raw) == 0 {
			if byteCut {
				partial(p.Get("end"))
			} else {
				out.Exhausted = true
			}
			break
		}
		query.SortEvents(raw)
		boundary := raw[len(raw)-1].EventTimeUTC
		stamp, err := time.Parse(time.RFC3339Nano, boundary)
		if err != nil {
			return out, fmt.Errorf("vlrange: invalid boundary time: %w", err)
		}
		prefix := make([]logtypes.Event, 0, len(raw))
		for _, ev := range raw {
			t, err := time.Parse(time.RFC3339Nano, ev.EventTimeUTC)
			if err == nil && t.After(stamp) {
				prefix = append(prefix, ev)
			}
		}
		if byteCut {
			appendEvents(prefix)
			partial(boundary)
			break
		}
		if uint64(len(raw)) < rows {
			appendEvents(raw)
			out.Exhausted = true
			break
		}
		appendEvents(prefix)
		// 前缀已足一页，边界位于本页之后；保留正常一次 union，不扩大整窗排序成本。
		if len(out.Items) >= int(q.Budget.EffectiveLimit()) {
			break
		}
		if out.Bytes >= maxBytes {
			partial(boundary)
			break
		}
		groupParams := cloneParams(p)
		groupParams.Set("start", stamp.UTC().Format(time.RFC3339Nano))
		groupParams.Set("end", stamp.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano))
		// 字节数同时约束行数：每个合法 NDJSON 行至少占一字节，无隐含服务端默认上限。
		groupRows := maxBytes - out.Bytes
		groupParams.Set("limit", strconv.FormatUint(groupRows, 10))
		groupParams.Set("query", baseQuery+" | limit "+strconv.FormatUint(groupRows, 10))
		group, cut, err := readEvents(groupParams)
		if cut || err != nil {
			partial(boundary)
			break
		}
		appendEvents(group)
		if len(out.Items) > int(q.Budget.EffectiveLimit()) {
			break
		}
		// 游标组的已返回前缀可能占满 top-K；完整组过滤后不足一页时只续读更旧窗口。
		p.Set("end", stamp.UTC().Format(time.RFC3339Nano))
		start, err := time.Parse(time.RFC3339Nano, p.Get("start"))
		if err != nil {
			return out, err
		}
		if !start.Before(stamp) {
			out.Exhausted = true
			break
		}
	}
	query.SortEvents(out.Items)
	// Boundary groups can contain many projected candidates. Only the bounded
	// return prefix (including lookahead) needs full structured fields.
	if limit := int(lookaheadRows(q)); len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Exhausted = false
	}
	if err := c.hydrateSearchItems(ctx, params, q, &out, matches, hydrationSelectors...); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) stream(ctx context.Context, params url.Values, maxBytes, maxRows uint64, consume func([]byte) error) (uint64, bool, error) {
	var read, rows uint64
	truncated := false
	if !physicalQueryFits(params.Get("query"), params.Get("extra_filters")) {
		return 0, false, query.ErrRangeQueryBudget
	}
	if maxBytes == 0 {
		return 0, true, nil
	}
	admittedBytes, refund, err := query.ReservePhysicalRead(ctx, maxBytes)
	if err != nil {
		return 0, false, err
	}
	maxBytes = admittedBytes
	defer func() { refund(read) }()
	actual, err := c.vl.StreamBudget(ctx, queryPath, params, maxBytes, func(body io.Reader) error {
		// 在读入前限制真实字节数，Scanner 预读和超大单行也不能突破预留额度。
		n := maxBytes
		if n > uint64(1<<63-1) {
			n = uint64(1<<63 - 1)
		}
		limited := &io.LimitedReader{R: body, N: int64(n)}
		sc := bufio.NewScanner(limited)
		bufSize := uint64(64 * 1024)
		if n < bufSize {
			bufSize = n
		}
		sc.Buffer(make([]byte, int(bufSize)), 4<<20)
		for sc.Scan() {
			line := sc.Bytes()
			// 恰好触顶但未见完整换行的末行可能只是被硬边界截断的半条 JSON。
			if limited.N == 0 && uint64(len(line)) == n && !bytes.HasSuffix(line, []byte{'\n'}) {
				truncated = true
				break
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if err := consume(line); err != nil {
				if limited.N == 0 {
					truncated = true
					break
				}
				read = n - uint64(limited.N)
				return err
			}
			rows++
			if maxRows > 0 && rows >= maxRows {
				truncated = true
				break
			}
		}
		read = n - uint64(limited.N)
		if limited.N == 0 || sc.Err() == bufio.ErrTooLong {
			truncated = true
			return nil
		}
		return sc.Err()
	})
	// HTTP 非 2xx 正文由传输层读取，也必须归入本次预留，不能借错误反复退款。
	if actual > read {
		read = actual
	}
	return read, truncated, err
}

func cloneParams(p url.Values) url.Values {
	out := make(url.Values, len(p))
	for key, values := range p {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func scanLimit(b query.Budget) uint64 {
	if b.MaxBytes > 0 {
		return b.MaxBytes
	}
	return defaultMaxScanBytes
}

func eventFromJSON(line []byte) (logtypes.Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return logtypes.Event{}, fmt.Errorf("vlrange: decode event: %w", err)
	}
	var ev logtypes.Event
	var err error
	ev.EventID, err = rawString(fields, "event_id")
	if err != nil {
		return ev, err
	}
	if ev.EventID == "" {
		return ev, fmt.Errorf("vlrange: event_id missing")
	}
	ev.Source.LogSourceID, _ = rawString(fields, "log_source_id")
	ev.Source.SourceGeneration, _ = rawString(fields, "source_generation")
	ev.Source.ParserVersion, _ = rawString(fields, "parser_version")
	ev.EventTimeUTC, _ = rawString(fields, "_time")
	ev.IngestTimeUTC, _ = rawString(fields, "ingest_time_utc")
	ev.Level, _ = rawString(fields, "level")
	ev.Stream, _ = rawString(fields, "stream")
	ev.Message, _ = rawString(fields, "_msg")
	ev.CanonicalHash, _ = rawString(fields, "canonical_content_hash")
	ev.Record.Start = rawUint(fields, "record_start")
	ev.Record.End = rawUint(fields, "record_end")
	for k, raw := range fields {
		switch k {
		case "_time", "_msg", "event_id", "log_source_id", "source_generation", "parser_version", "record_start", "record_end", "ingest_time_utc", "level", "stream", "canonical_content_hash":
			continue
		}
		if value, ok := rawStringValue(raw); ok {
			if ev.Fields == nil {
				ev.Fields = make(map[string]string)
			}
			ev.Fields[k] = value
		}
	}
	return ev, nil
}

func statsFromJSON(line []byte, q query.RangeQuery) (query.StatsPoint, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return query.StatsPoint{}, fmt.Errorf("vlrange: decode stats: %w", err)
	}
	p := query.StatsPoint{Dimensions: make(map[string]string)}
	for _, group := range q.GroupBy {
		if value, ok := rawStringValue(fields[group]); ok {
			p.Dimensions[group] = value
		}
	}
	if q.TimeBucket != "" {
		if value, ok := rawStringValue(fields["_time"]); ok {
			p.Dimensions["_time"] = value
		}
	}
	p.Count = rawUint(fields, "_jm_count")
	p.Sum = rawFloat(fields, "_jm_sum")
	p.Min = rawFloat(fields, "_jm_min")
	p.Max = rawFloat(fields, "_jm_max")
	return p, nil
}

func rawString(fields map[string]json.RawMessage, key string) (string, error) {
	value, ok := rawStringValue(fields[key])
	if !ok && fields[key] != nil {
		return "", fmt.Errorf("vlrange: field %s is not a scalar string", key)
	}
	return value, nil
}

func rawStringValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return strings.TrimSpace(string(raw)), true
}

func rawUint(fields map[string]json.RawMessage, key string) uint64 {
	s, ok := rawStringValue(fields[key])
	if !ok {
		return 0
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func rawFloat(fields map[string]json.RawMessage, key string) float64 {
	s, ok := rawStringValue(fields[key])
	if !ok {
		return 0
	}
	n, _ := strconv.ParseFloat(s, 64)
	return n
}

func escape(s string) string {
	quoted := strconv.Quote(s)
	return quoted[1 : len(quoted)-1]
}

func validField(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validTimeBucket(s string) bool {
	return len(s) > 1 && (strings.HasSuffix(s, "s") || strings.HasSuffix(s, "m") || strings.HasSuffix(s, "h") || strings.HasSuffix(s, "d"))
}

func validTimeGroup(s string) bool {
	return strings.HasPrefix(s, "_time:") && validTimeBucket(strings.TrimPrefix(s, "_time:"))
}
