package vlrange

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// facets 改用官方 `/select/logsql/field_values`。
//
// # 为什么不直接用 `/select/logsql/facets`
//
// 2026-11-05 实测（v1.52.0）：官方 facets 的语义是「**自动发现**低基数字段」——
// 它扫描所有字段，返回「值数量 ≤ max_values_per_field 的那些字段」，
// 而 `field=` / `fields=` 参数都会被**忽略**：
//
//	max_values_per_field=2  -> ['level']
//	不传该参数              -> 7 个维度（含 _time / event_id / canonical_content_hash）
//	field=level             -> 被忽略，仍返回 7 个
//
// 我们的契约是 `Facets(ctx, rng, q, dimensions []string, ...)`——**按调用方指定的
// 维度**聚合。两者语义不匹配：用它就得「先查全部再自行筛」，既多扫字段，
// 又会因为「值太多而没进 auto 集合」的维度直接拿不到（静默缺项）。
//
// `field_values` 才是「按指定字段精确聚合」，与服务契约同形。
// 实测两者对 level 的结果逐值一致（INFO 880 / WARN 10）。
//
// # ⚠️ limit 是这些聚合端点的**通病**
//
// `streams` 与 `field_values` 都有同一个陷阱：limit 小于真实值数量时，
// **所有 hits 归零**（不是截断少给，是全零）。所以本层一律不向 VL 传 limit，
// 拿全量后自己裁。

// facetsWindow 算出 field_values 该用的 [start, end)。
//
// 与 Search / Stats 共用 windowParams 的窗口语义（分区日 ∩ 请求窗）：
// 两个入口的 start/end 必须逐字一致，否则同一天的分面会看到不同的数据面。
func (c *Client) facetsWindow(rng query.AuthoritativeRange, q query.RangeQuery) (time.Time, time.Time, bool, error) {
	day := rng.Key.UTCDay
	if day == "" {
		return time.Time{}, time.Time{}, true, nil
	}
	start, err := time.Parse("2006-01-02", day)
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("vlrange: invalid Catalog UTC day: %w", err)
	}
	end := start.AddDate(0, 0, 1)
	if q.TimeRange.FromUTC != "" {
		from, parseErr := time.Parse(time.RFC3339Nano, q.TimeRange.FromUTC)
		if parseErr != nil {
			return time.Time{}, time.Time{}, false, fmt.Errorf("vlrange: invalid query start: %w", parseErr)
		}
		if from.After(start) {
			start = from
		}
	}
	if q.TimeRange.ToUTC != "" {
		to, parseErr := time.Parse(time.RFC3339Nano, q.TimeRange.ToUTC)
		if parseErr != nil {
			return time.Time{}, time.Time{}, false, fmt.Errorf("vlrange: invalid query end: %w", parseErr)
		}
		if to.Before(end) {
			end = to
		}
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, true, nil
	}
	return start, end, false, nil
}

// facetsViaFieldValues 对单个维度做一次官方 field_values 聚合。
//
// # 字节预算
//
// 与 Stats 同口径：`field_values` 的 query 参数同样受 VL 的 16KiB 硬界约束，
// 所以先用 physicalQueryFits 判（并额外用 URL 编码后的真实长度复核），
// 超长则走 disjointScopeTerms 分片后逐片聚合、在内存里按值相加。
// 分片与 Stats 共用同一份**互斥**实现，所以相加不会重复计数。
func (c *Client) facetsViaFieldValues(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, dimension string, limit uint32) (query.RangeFacetsResult, error) {
	start, end, empty, err := c.facetsWindow(rng, q)
	if err != nil {
		return query.RangeFacetsResult{}, err
	}
	if empty {
		// 空窗是确定的空：0 个值，不是「被截断」。
		return query.RangeFacetsResult{}, nil
	}

	budget := scanLimit(q.Budget)
	out := query.RangeFacetsResult{}
	counts := make(map[string]uint64)

	collect := func(text string, remaining uint64) (uint64, error) {
		partQ := q
		partQ.Budget.MaxBytes = remaining
		values, complete, readBytes, err := c.fieldValuesOnce(ctx, text, q.Filter, dimension, limit, partQ, start, end)
		for _, v := range values {
			counts[v.Value] += v.Count
		}
		if !complete {
			out.Truncated = true
		}
		return readBytes, err
	}

	selector := selectorQuery(rng, q)
	if physicalQueryFits(selector, q.Filter) {
		readBytes, collectErr := collect(selector, budget)
		out.Bytes = readBytes
		return finishFacets(out, counts, dimension, limit, collectErr)
	}

	// 超长：沿用 Stats 的互斥物理分片，每片单独聚合。
	selectors, err := splitPhysicalTerms(disjointScopeTerms(rng, q), func(terms []string) string {
		return "(" + strings.Join(terms, " OR ") + ")"
	}, q.Filter)
	if err != nil {
		return query.RangeFacetsResult{}, err
	}
	for _, terms := range selectors {
		if out.Bytes >= budget {
			out.Truncated = true
			out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			break
		}
		readBytes, partErr := collect("("+strings.Join(terms, " OR ")+")", budget-out.Bytes)
		out.Bytes += readBytes
		if partErr != nil {
			out.Truncated = true
			if errors.Is(partErr, query.ErrRangeQueryBudget) {
				out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			}
			return finishFacets(out, counts, dimension, limit, partErr)
		}
	}
	return finishFacets(out, counts, dimension, limit, nil)
}

// fieldValuesOnce 发一次 field_values 请求。
//
// 返回 (值列表, 条数是否可信, 本次读到的字节数, 错误)。
//
// # ⚠️ 绝不向 VL 传 `limit`
//
// 2026-11-05 实测（v1.52.0）：`field_values` 的 `limit` 与 `streams` 有**同一个陷阱**——
// 一旦 limit 小于真实值数量，会把**所有** hits 静默归零：
//
//	field=log_source_id&limit=2  -> 2 项, sum_hits=0      ← 真实 29 个值
//	field=log_source_id（无limit） -> 29 项, sum_hits=11837
//
// 所以 `vlsup.FieldValues` 压根不接 limit 参数，裁剪放在本层做：
// 拿全量后按 `limit` 截断，并置 complete=false（= Truncated）。
// 这样做的好处是「截断时仍保有正确条数」，而不是像 VL 那样把整张表变噪声。
func (c *Client) fieldValuesOnce(ctx context.Context, selector, filter, dimension string, limit uint32, q query.RangeQuery, start, end time.Time) ([]query.FacetValue, bool, uint64, error) {
	// 中文 / 引号 / 空格编码后会膨胀，所以这里用真实编码长度复核。
	//
	// 两道门的分工：
	//   - physicalMaxQueryBytes（12KiB）是**字面量**分片目标，留给引擎组合额外过滤器余量；
	//   - 16KiB 是 VL 实际卡的上限，对齐才有意义。
	// 把两者都写成 12KiB 会把「字面量合规、编码后 14KB」的请求误拦下（实测 617 源就撞这个）。
	encoded := url.Values{"query": {selector}, "field": {dimension}}
	if filter != "" {
		encoded.Set("extra_filters", filter)
	}
	if len(encoded.Encode()) > physicalRequestLineMaxBytes {
		return nil, false, 0, query.ErrRangeQueryBudget
	}
	// 不传 limit：见函数头。
	//
	// # 必须先向请求级物理预算登记
	//
	// 与 Search/Stats 走同一条 ReservePhysicalRead 路径：它同时管**字节**与**扇出次数**。
	// 绕开它直接发请求会出两个真问题：
	//   - MaxFanout 失效：多个 facet 维度各自发请求，但都不记账 ⇒ 维度数不受限；
	//   - MaxBytes 失效：本层只能事后统计字节数，拦不住「已经发出去了」。
	// 回归见 TestQueryTextBudgetFanoutSharedAcrossShardsAndFacets。
	//
	// 额度取 q.Budget.MaxBytes（调用方已把「剩余额度」写进这个字段）：0 表示不限，
	// 交给 ReservePhysicalRead 自己判断，不在本层重算。
	admittedBytes, refund, reserveErr := query.ReservePhysicalRead(ctx, q.Budget.MaxBytes)
	if reserveErr != nil {
		return nil, false, 0, reserveErr
	}
	var readBytes uint64
	defer func() { refund(readBytes) }()
	// field_values 不支持服务端字节限流（没有等价于 limit 的字节参数），
	// 所以只能「登记后按实读计数退差价」。登记本身已经验证了 fanout 与额度门。
	_ = admittedBytes
	values, err := c.vl.FieldValues(ctx, selector, filter, dimension, start, end)
	if err != nil {
		return nil, false, 0, err
	}
	complete := true
	out := make([]query.FacetValue, 0, len(values))
	// 字节数按返回值近似统计（值长度 + 一个计数的量级），
	// 与 Stats 的「读了多少字节」同一语义：用于总预算递减，不用于展示。
	for _, v := range values {
		readBytes += uint64(len(v.Value)) + 8
		out = append(out, query.FacetValue{Dimension: dimension, Value: v.Value, Count: v.Hits})
	}
	if limit > 0 && uint32(len(out)) > limit {
		out = out[:limit]
		complete = false
	}
	return out, complete, readBytes, nil
}

// finishFacets 排序、回填维度名并按 limit 裁剪。
//
// 排序与既有实现同口径（Count 降序、同数 Value 升序）：
// 不能信任 VL 的返回顺序——那是内部顺序，前端会原样渲染，
// 「最需要看的那个值」可能排在最后。
func finishFacets(out query.RangeFacetsResult, counts map[string]uint64, dimension string, limit uint32, err error) (query.RangeFacetsResult, error) {
	values := make([]query.FacetValue, 0, len(counts))
	for value, count := range counts {
		values = append(values, query.FacetValue{Dimension: dimension, Value: value, Count: count})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Count != values[j].Count {
			return values[i].Count > values[j].Count
		}
		return values[i].Value < values[j].Value
	})
	if limit > 0 && uint32(len(values)) > limit {
		values = values[:limit]
		out.Truncated = true
	}
	out.Values = values
	return out, err
}
