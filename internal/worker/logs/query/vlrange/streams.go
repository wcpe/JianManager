package vlrange

import (
	"context"
	"fmt"
	"sort"

	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// Streams 返回该 range 内的 stream 集合与条数。
//
// # 作用域约束
//
// 用 `selectorQuery(rng, q)` 限制在 range 的权威作用域内——与 Search / Fields 同一口径。
// 不约束的后果是「查 inst:318 却看到 inst:320 的流」，比查不到更难排查。
//
// # 为何不传 limit
//
// 见 vlsup.Client.Streams：`streams` 端点的 limit 一旦小于实际流数，
// 会把**所有** hits 静默归零（不是截断返回，是全零）。
// 这是服务端参数，不是我们能在客户端绕过的，所以干脆不传。
//
// # 为什么没有 RangeClient 的预算短路
//
// `streams` 是聚合端点，返回体积只与**流数量**有关（生产 21 个，~2KB），
// 与事件条数无关——不会因为时间窗大而变大。而窗口本身已被
// fieldNamesWindow 夹到「range 分区日 ∩ 请求窗」，两边都有限。
// 真正的失控风险在上游（VL 的 maxQueryDuration 10s 会兜住），
// 所以不为它伪造一个字节预算判据——那只会把一个不会发生的事写进分支。
func (c *Client) Streams(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeStreamsResult, error) {
	if c == nil || c.vl == nil {
		return query.RangeStreamsResult{}, query.ErrRangeClientUnimplemented
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()

	start, end, empty, err := c.fieldNamesWindow(rng, q)
	if err != nil {
		return query.RangeStreamsResult{Complete: false}, err
	}
	if empty {
		// 空窗是**确定的**空：Complete=true 但零条。
		// 不能报 Complete=false —— 那会让前端把「这天没数据」显示成「数据不完整」。
		return query.RangeStreamsResult{Complete: true}, nil
	}
	streams, complete, err := c.vl.Streams(ctx, selectorQuery(rng, q), start, end)
	if err != nil {
		// 聚合失败不得伪装成「没有流」：那会让前端把筛选面板整个清空。
		return query.RangeStreamsResult{Complete: false, CoverageReasons: []string{query.ReasonRangeUnavailable}}, err
	}
	out := query.RangeStreamsResult{Complete: complete}
	out.Streams = make([]query.StreamValue, 0, len(streams))
	for _, s := range streams {
		out.Streams = append(out.Streams, query.StreamValue{
			LogSourceID: s.LogSourceID,
			Stream:      s.StreamName,
			Raw:         s.Raw,
			Hits:        s.Hits,
		})
	}
	return out, nil
}

// StreamFieldNames 返回 stream 的标签维度名。
//
// 实测恒为 [`log_source_id`, `stream`]，但**不在本层写死**：
// 将来 VL 给流加新标签，前端应无需改码即可看到。
func (c *Client) StreamFieldNames(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StreamFieldNamesResult, error) {
	if c == nil || c.vl == nil {
		return query.StreamFieldNamesResult{}, query.ErrRangeClientUnimplemented
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()

	start, end, empty, err := c.fieldNamesWindow(rng, q)
	if err != nil {
		return query.StreamFieldNamesResult{}, err
	}
	if empty {
		return query.StreamFieldNamesResult{}, nil
	}
	names, err := c.vl.StreamFieldNames(ctx, selectorQuery(rng, q), start, end)
	if err != nil {
		return query.StreamFieldNamesResult{CoverageReasons: []string{query.ReasonRangeUnavailable}}, err
	}
	sort.Strings(names)
	return query.StreamFieldNamesResult{Fields: names}, nil
}

// StreamFieldValues 返回某 stream 标签维度的取值分布。
//
// 非 stream 标签字段（如 `level`）由 VL 返回空 values 而非报错——
// 本层不把「合法但空」翻译成错误，也不把空结果翻译成 Complete=false。
func (c *Client) StreamFieldValues(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, field string) (query.RangeStreamFieldValuesResult, error) {
	if c == nil || c.vl == nil {
		return query.RangeStreamFieldValuesResult{}, query.ErrRangeClientUnimplemented
	}
	if !validField(field) {
		return query.RangeStreamFieldValuesResult{Complete: false}, fmt.Errorf("vlrange: invalid stream field %q", field)
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()

	start, end, empty, err := c.fieldNamesWindow(rng, q)
	if err != nil {
		return query.RangeStreamFieldValuesResult{Complete: false}, err
	}
	if empty {
		return query.RangeStreamFieldValuesResult{Complete: true}, nil
	}
	values, complete, err := c.vl.StreamFieldValues(ctx, selectorQuery(rng, q), field, start, end)
	if err != nil {
		return query.RangeStreamFieldValuesResult{Complete: false, CoverageReasons: []string{query.ReasonRangeUnavailable}}, err
	}
	out := query.RangeStreamFieldValuesResult{Complete: complete}
	out.Values = make([]query.FacetValue, 0, len(values))
	for _, v := range values {
		out.Values = append(out.Values, query.FacetValue{Dimension: field, Value: v.Value, Count: v.Hits})
	}
	return out, nil
}
