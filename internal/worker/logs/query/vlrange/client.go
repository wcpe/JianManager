// Package vlrange adapts the localhost VictoriaLogs LogsQL HTTP API to the
// worker query.RangeClient contract.
package vlrange

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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
	params, empty, err := c.params(rng, q)
	if err != nil {
		return query.RangeResult{}, err
	}
	if empty {
		return query.RangeResult{Exhausted: true, DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}, nil
	}
	params.Set("query", searchQuery(rng, q))
	// 每页请求 N+1 条前瞻（多出的 1 条用于判定「是否还有下一页」）；游标页为 N+2：
	// 收窄后的窗口必然包含游标行本身（_time == 游标时刻），而它会被客户端全键过滤丢弃，
	// 只留 N+1 会让每页恰好只剩 N 条 → 服务端误判 exhausted、next 为空 → 第 3 页起仍不可达。
	params.Set("limit", strconv.FormatUint(lookaheadRows(q), 10))
	var out query.RangeResult
	bytesRead, truncated, err := c.stream(ctx, params, scanLimit(q.Budget), func(line []byte) error {
		ev, err := eventFromJSON(line)
		if err != nil {
			return err
		}
		if q.ClosedVisibleSeq > 0 && ev.Record.End > q.ClosedVisibleSeq {
			return nil
		}
		out.Items = append(out.Items, ev)
		return nil
	})
	if err != nil {
		return query.RangeResult{}, err
	}
	out.Bytes = bytesRead
	out.Exhausted = !truncated
	out.DuplicateQuality = query.DupExact
	out.StatsQuality = query.StatsExact
	if truncated {
		out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
	}
	return out, nil
}

// Stats uses the LogsQL stats pipe so aggregation happens in VL over the
// authoritative range, then maps each NDJSON row to the worker aggregate type.
func (c *Client) Stats(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StatsResult, error) {
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
	params.Set("query", selectorQuery(rng, q)+pipe)
	var out query.StatsResult
	bytesRead, truncated, err := c.stream(ctx, params, scanLimit(q.Budget), func(line []byte) error {
		point, err := statsFromJSON(line, q)
		if err != nil {
			return err
		}
		out.Points = append(out.Points, point)
		return nil
	})
	if err != nil {
		return query.StatsResult{}, err
	}
	out.Bytes = bytesRead
	out.DuplicateQuality = query.DupExact
	out.StatsQuality = query.StatsExact
	if truncated {
		out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
		out.StatsQuality = query.StatsPartial
	}
	return out, nil
}

// Fields enumerates fields from the same canonical range used by Search.
// The result is bounded by the request budget and therefore reports truncation.
func (c *Client) Fields(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.FieldsResult, error) {
	if c == nil || c.vl == nil {
		return query.FieldsResult{}, query.ErrRangeClientUnimplemented
	}
	if q.Budget.Limit == 0 || q.Budget.Limit > 1000 {
		q.Budget.Limit = 1000
	}
	res, err := c.Search(ctx, rng, q)
	if err != nil {
		return query.FieldsResult{}, err
	}
	set := make(map[string]struct{})
	for _, ev := range res.Items {
		for field := range ev.Fields {
			set[field] = struct{}{}
		}
	}
	fields := make([]string, 0, len(set))
	for field := range set {
		fields = append(fields, field)
	}
	return query.FieldsResult{Fields: fields, Bytes: res.Bytes, CoverageReasons: res.CoverageReasons, Truncated: !res.Exhausted}, nil
}

// Facets aggregates controlled dimensions through the same Stats path and
// preserves the range budget/truncation signal.
func (c *Client) Facets(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, dimensions []string, limit uint32) (query.RangeFacetsResult, error) {
	if c == nil || c.vl == nil {
		return query.RangeFacetsResult{}, query.ErrRangeClientUnimplemented
	}
	if limit == 0 {
		limit = 100
	}
	var out query.RangeFacetsResult
	for _, dimension := range dimensions {
		if !validField(dimension) || dimension == "event_id" || dimension == "message" || dimension == "_time" {
			continue
		}
		copyQ := q
		copyQ.GroupBy = []string{dimension}
		stats, err := c.Stats(ctx, rng, copyQ)
		if err != nil {
			return query.RangeFacetsResult{}, err
		}
		out.Bytes += stats.Bytes
		out.CoverageReasons = append(out.CoverageReasons, stats.CoverageReasons...)
		if len(stats.Points) > int(limit) {
			out.Truncated = true
		}
		for i, point := range stats.Points {
			if uint32(i) >= limit {
				break
			}
			out.Values = append(out.Values, query.FacetValue{Dimension: dimension, Value: point.Dimensions[dimension], Count: point.Count})
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
	start, err := time.Parse("2006-01-02", rng.Key.UTCDay)
	if err != nil {
		return nil, false, fmt.Errorf("vlrange: invalid Catalog UTC day: %w", err)
	}
	end := start.AddDate(0, 0, 1)
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
	// 残余风险：同一时间戳的并列行若被 top-K 截断在**组内**，客户端全键次序无法补回未取回
	// 的并列行（毫秒级时间戳下极罕见）；并列组完整取回时由全键过滤正确拆分。
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
		for _, scope := range rng.Projection.SourceProjections {
			generations := make([]string, 0, len(scope.ProjectionGenerations))
			for _, generation := range scope.ProjectionGenerations {
				generations = append(generations, `projection_generation:="`+escape(generation)+`"`)
			}
			if len(generations) == 0 {
				continue
			}
			covered = append(covered, `(log_source_id:="`+escape(scope.LogSourceID)+`" AND source_generation:="`+
				escape(scope.SourceGeneration)+`" AND (`+strings.Join(generations, " OR ")+`) AND record_end:<=`+
				strconv.FormatUint(scope.ClosedVisibleSeq, 10)+`)`)
		}
	} else if rng.Projection != nil {
		for _, src := range rng.Projection.CoveredSourceGenerations {
			if src.LogSourceID != "" && src.SourceGeneration != "" {
				covered = append(covered, `(log_source_id:="`+escape(src.LogSourceID)+`" AND source_generation:="`+escape(src.SourceGeneration)+`")`)
			}
		}
	}
	if len(covered) > 0 {
		parts = append(parts, "("+strings.Join(covered, " OR ")+")")
	} else if rng.Key.StorageNamespace != "" {
		parts = append(parts, `log_source_id:="`+escape(rng.Key.StorageNamespace)+`"`)
	}
	if rng.Projection != nil && !scoped {
		generations := projectionGenerations(rng.Projection)
		if len(generations) == 1 {
			parts = append(parts, `projection_generation:="`+escape(generations[0])+`"`)
		} else if len(generations) > 1 {
			terms := make([]string, 0, len(generations))
			for _, generation := range generations {
				terms = append(terms, `projection_generation:="`+escape(generation)+`"`)
			}
			parts = append(parts, "("+strings.Join(terms, " OR ")+")")
		}
	}
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
		b.WriteString(" sum(")
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

func (c *Client) stream(ctx context.Context, params url.Values, maxBytes uint64, consume func([]byte) error) (uint64, bool, error) {
	var read uint64
	truncated := false
	err := c.vl.Stream(ctx, queryPath, params, func(body io.Reader) error {
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 64*1024), 4<<20)
		for sc.Scan() {
			line := sc.Bytes()
			read += uint64(len(line) + 1)
			if read > maxBytes {
				truncated = true
				break
			}
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			if err := consume(append([]byte(nil), line...)); err != nil {
				return err
			}
		}
		return sc.Err()
	})
	return read, truncated, err
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
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
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
