package vlrange

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 留出引擎组合额外过滤器的余量，不调整批准 VL 默认的 16KiB 上限。
const physicalMaxQueryBytes = 12 << 10

func physicalQueryFits(text, filter string) bool {
	return len(text) <= physicalMaxQueryBytes && len(filter) <= physicalMaxQueryBytes-len(text)
}

// Stats 在冻结的物理谓词上聚合；仅超长范围拆分，常规请求保持一次服务端聚合。
func (c *Client) Stats(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StatsResult, error) {
	if c == nil || c.vl == nil {
		return query.StatsResult{}, query.ErrRangeClientUnimplemented
	}
	ctx, cancel := query.WithQueryDeadline(ctx, q.Budget)
	defer cancel()
	ctx = query.WithPhysicalReadBudget(ctx, q.Budget)
	pipe, err := statsPipe(q)
	if err != nil {
		return query.StatsResult{}, err
	}
	selector := selectorQuery(rng, q)
	if physicalQueryFits(selector+pipe, q.Filter) {
		return c.statsSelector(ctx, rng, q, selector)
	}
	selectors, err := splitPhysicalTerms(disjointScopeTerms(rng, q), func(terms []string) string {
		return "(" + strings.Join(terms, " OR ") + ")" + pipe
	}, q.Filter)
	if err != nil {
		return query.StatsResult{}, err
	}
	out := query.StatsResult{DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}
	for _, terms := range selectors {
		if out.Bytes >= scanLimit(q.Budget) {
			out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			out.StatsQuality = query.StatsPartial
			break
		}
		partQ := q
		partQ.Budget.MaxBytes = scanLimit(q.Budget) - out.Bytes
		part, partErr := c.statsSelector(ctx, rng, partQ, "("+strings.Join(terms, " OR ")+")")
		out.Bytes += part.Bytes
		out.Points = mergePhysicalStats(out.Points, part.Points)
		out.CoverageReasons = append(out.CoverageReasons, part.CoverageReasons...)
		if len(part.CoverageReasons) > 0 || part.StatsQuality == query.StatsPartial {
			out.StatsQuality = query.StatsPartial
		}
		if partErr != nil {
			out.StatsQuality = query.StatsPartial
			if errors.Is(partErr, query.ErrRangeQueryBudget) {
				out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			}
			return out, partErr
		}
	}
	return out, nil
}

// disjointScopeTerms 把范围拆成**互斥的物理谓词**，供超长查询二分分片。
//
// **契约变更（2026-11-02 简化投递路径）**：原子不再含 `projection_generation` 维度。
//
// 旧实现按 (来源, 来源代次, 投影代次) 三元组拆 —— 那是为了「同一事件多份副本时
// 按批次分别取，再合并去重」。简化后写入端不再重写、查询不再按代次过滤，
// 谓词形状是 `(log_source_id AND source_generation AND record_end<=水位)`，
// 分片必须与它同形，否则「一个 scope 一个原子」在 617 个来源时会变成
// 单个超大不可分的谓词，直接撞 16KiB 预算。
func disjointScopeTerms(rng query.AuthoritativeRange, q query.RangeQuery) []string {
	type atom struct {
		source, sourceGeneration string
	}
	atoms := make(map[atom]uint64)
	add := func(a atom, seq uint64) {
		if previous, ok := atoms[a]; !ok || seq > previous {
			atoms[a] = seq
		}
	}
	projection := rng.Projection
	scoped := projection != nil && len(projection.SourceProjections) > 0
	if scoped {
		for _, scope := range projection.SourceProjections {
			seq := scope.ClosedVisibleSeq
			if q.ClosedVisibleSeq > 0 && seq > q.ClosedVisibleSeq {
				seq = q.ClosedVisibleSeq
			}
			add(atom{scope.LogSourceID, scope.SourceGeneration}, seq)
		}
	} else {
		if projection != nil {
			for _, source := range projection.CoveredSourceGenerations {
				if source.LogSourceID == "" || source.SourceGeneration == "" {
					continue
				}
				add(atom{source.LogSourceID, source.SourceGeneration}, q.ClosedVisibleSeq)
			}
		}
		if len(atoms) == 0 {
			// 回退分区命名空间（简化投递路径补建的记录只有它）。
			// 原子里的 sourceGeneration 留空，渲染时不得带上 `source_generation:=`
			// ——历史行/未知代次的行没有可枚举的代次值。
			add(atom{source: rng.Key.StorageNamespace}, q.ClosedVisibleSeq)
		}
	}
	terms := make([]string, 0, len(atoms))
	for a, seq := range atoms {
		parts := make([]string, 0, 3)
		isNamespaceAtom := !scoped && a.sourceGeneration == "" && a.source != ""
		if isNamespaceAtom {
			// 与 selectorQuery 同口径：命名空间回退必须匹配 `<ns>/...` 的子源，
			// 否则超长查询分片时同样一条都查不到（同源缺陷，见 namespaceSelector）。
			parts = append(parts, "("+namespaceSelector(a.source)+")")
		} else {
			if scoped || a.source != "" {
				parts = append(parts, `log_source_id:="`+escape(a.source)+`"`)
			}
			if scoped || a.sourceGeneration != "" {
				parts = append(parts, `source_generation:="`+escape(a.sourceGeneration)+`"`)
			}
		}
		parts = append(parts, "record_end:<="+strconv.FormatUint(seq, 10))
		terms = append(terms, "("+strings.Join(parts, " AND ")+")")
	}
	sort.Strings(terms)
	return terms
}

// splitPhysicalTerms 按最终转义后的物理文本字节递归划分，不用 ID 个数猜测引擎边界。
func splitPhysicalTerms[T any](terms []T, build func([]T) string, filter string) ([][]T, error) {
	if len(terms) == 0 {
		return nil, query.ErrRangeQueryBudget
	}
	if physicalQueryFits(build(terms), filter) {
		return [][]T{terms}, nil
	}
	if len(terms) == 1 {
		return nil, fmt.Errorf("%w: one physical scope exceeds %d bytes", query.ErrRangeQueryBudget, physicalMaxQueryBytes)
	}
	middle := len(terms) / 2
	left, err := splitPhysicalTerms(terms[:middle], build, filter)
	if err != nil {
		return nil, err
	}
	right, err := splitPhysicalTerms(terms[middle:], build, filter)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func physicalDimensionKey(dimensions map[string]string) string {
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(strconv.Quote(key))
		out.WriteByte(':')
		out.WriteString(strconv.Quote(dimensions[key]))
		out.WriteByte(',')
	}
	return out.String()
}

func mergePhysicalStats(dst, src []query.StatsPoint) []query.StatsPoint {
	indices := make(map[string]int, len(dst)+len(src))
	for i, point := range dst {
		indices[physicalDimensionKey(point.Dimensions)] = i
	}
	for _, point := range src {
		key := physicalDimensionKey(point.Dimensions)
		if i, ok := indices[key]; ok {
			if dst[i].Count == 0 || point.Count > 0 && point.Min < dst[i].Min {
				dst[i].Min = point.Min
			}
			if dst[i].Count == 0 || point.Count > 0 && point.Max > dst[i].Max {
				dst[i].Max = point.Max
			}
			dst[i].Count += point.Count
			dst[i].Sum += point.Sum
		} else {
			indices[key] = len(dst)
			dst = append(dst, point)
		}
	}
	return dst
}

func (c *Client) splitMergedSources(ctx context.Context, rngs []query.AuthoritativeRange, sources []query.SourceScope, q query.RangeQuery) (query.RangeResult, error) {
	groups, err := splitPhysicalTerms(sources, func(group []query.SourceScope) string { return mergedSearchQuery(group, q) }, q.Filter)
	if err != nil {
		return query.RangeResult{}, err
	}
	out := query.RangeResult{Exhausted: true, DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}
	seen := make(map[string]string)
	for _, group := range groups {
		if out.Bytes >= scanLimit(q.Budget) {
			out.Exhausted = false
			out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			out.StatsQuality = query.StatsPartial
			break
		}
		partQ := q
		partQ.Budget.MaxBytes = scanLimit(q.Budget) - out.Bytes
		part, partErr := c.searchMerged(ctx, rngs, partQ, group)
		out.Bytes += part.Bytes
		out.Exhausted = out.Exhausted && part.Exhausted
		out.CoverageReasons = append(out.CoverageReasons, part.CoverageReasons...)
		if part.DuplicateQuality != query.DupExact {
			out.DuplicateQuality = part.DuplicateQuality
		}
		if part.SafeBeforeUTC != "" || !part.Exhausted && len(part.Items) < int(q.Budget.EffectiveLimit()) {
			// 任一分片缺少完整页前缀时，不以其他分片旧行推进全局游标。
			out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
		}
		for _, ev := range part.Items {
			if previous, ok := seen[ev.EventID]; ok {
				if previous == ev.CanonicalHash {
					continue
				}
				out.DuplicateQuality = query.DupUnresolved
			}
			seen[ev.EventID] = ev.CanonicalHash
			out.Items = append(out.Items, ev)
		}
		if partErr != nil {
			query.SortEvents(out.Items)
			return out, partErr
		}
	}
	if len(out.CoverageReasons) > 0 {
		out.StatsQuality = query.StatsPartial
	}
	query.SortEvents(out.Items)
	return out, nil
}
