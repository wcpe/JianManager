package vlrange

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 合并粗筛的行预算（2026-10-08 PoC 实测依据）：
//   - 面板分页 100 条、89 目标、24h 窗口：limit=300 时经「代次/水位归因过滤 +
//     同 id 去重」后仍余 101 条有效行（原始 101 行仅 56 唯一 event_id）；
//   - limit 从 101 提到 300 的 VL 成本可忽略（top-K 早停：0.29s → 0.35s）。
//
// 放大系数按「页预算 × 3」起步：投影重建期间同 id 双份（旧代次被过滤）是主要
// 损耗来源；若未来观测到有效行不足页大小，再评估迭代补查（当前不做：
// 如实少行 + 枚举未结束语义已由 CoverageReasons/Exhausted 承接）。
const (
	mergedScanMinRows = 300
	mergedScanMaxRows = 2000
	mergedScanFactor  = 3
)

// projectionGenerationField 是投影代次字段名（eventFromJSON 会把它放进 Event.Fields）。
const projectionGenerationField = "projection_generation"

// mergedScanRows 返回合并粗筛的 VL 行预算（页预算 × 系数，钳制在上下限内）。
func mergedScanRows(q query.RangeQuery) uint64 {
	rows := lookaheadRows(q) * mergedScanFactor
	if rows < mergedScanMinRows {
		rows = mergedScanMinRows
	}
	if rows > mergedScanMaxRows {
		rows = mergedScanMaxRows
	}
	return rows
}

// rangeMatchesEvent 在内存中复刻 selectorQuery（client.go）对单 range 的 LogsQL
// 谓词语义，用于合并粗筛后的行级归因。两处（LogsQL 与内存）必须逐字等价——
// merged_test.go 有对照测试守护。
//
// 语义分四段（全部为非空段的合取）：
//  1. scoped（SourceProjections 非空）：命中任一 scope 的
//     (log_source_id, source_generation, projection_generation ∈ set,
//     且 record_end <= scope.ClosedVisibleSeq)；
//  2. 非 scoped 且 Projection 非空：命中任一 CoveredSourceGenerations 的
//     (log_source_id, source_generation)；
//  3. covered 为空 → 回退 Key.StorageNamespace 相等；
//  4. 非 scoped 且有 generation 集 → 行的 projection_generation 必须 ∈ 集合。
//
// range 级前缀水位（selectorQuery 第三段 + Search 回调双重检查的对象）在此一并
// 用 rng.ClosedVisibleSeq 表达（合并路径不得使用全局 q.ClosedVisibleSeq——
// 组内各 range 水位不同）。
func rangeMatchesEvent(rng query.AuthoritativeRange, ev logtypes.Event) bool {
	proj := rng.Projection
	scoped := proj != nil && len(proj.SourceProjections) > 0
	projGen := ev.Fields[projectionGenerationField]

	covered := false
	if scoped {
		for _, scope := range proj.SourceProjections {
			if len(scope.ProjectionGenerations) == 0 {
				continue
			}
			if ev.Source.LogSourceID != scope.LogSourceID {
				continue
			}
			if ev.Source.SourceGeneration != scope.SourceGeneration {
				continue
			}
			if !containsString(scope.ProjectionGenerations, projGen) {
				continue
			}
			if ev.Record.End > scope.ClosedVisibleSeq {
				continue
			}
			covered = true
			break
		}
	} else if proj != nil {
		for _, src := range proj.CoveredSourceGenerations {
			if src.LogSourceID == "" || src.SourceGeneration == "" {
				continue
			}
			if ev.Source.LogSourceID == src.LogSourceID && ev.Source.SourceGeneration == src.SourceGeneration {
				covered = true
				break
			}
		}
	}
	if !covered {
		if rng.Key.StorageNamespace == "" {
			// 无来源约束：单 range 查询等价于 `*`，合并路径不会收纳这类 range；
			// 若仍被调用则按不匹配处理（宁可少收，不可放大可见范围）。
			return false
		}
		if ev.Source.LogSourceID != rng.Key.StorageNamespace {
			return false
		}
	}

	// 段 4：非 scoped 的投影代次全局约束（selectorQuery parts 第二段）。
	if proj != nil && !scoped {
		gens := projectionGenerations(proj)
		switch {
		case len(gens) == 1:
			if projGen != gens[0] {
				return false
			}
		case len(gens) > 1:
			if !containsString(gens, projGen) {
				return false
			}
		}
	}

	// range 级前缀水位。
	if rng.ClosedVisibleSeq > 0 && ev.Record.End > rng.ClosedVisibleSeq {
		return false
	}
	return true
}

// mergedSearchQuery 构造合并粗筛的 LogsQL：`log_source_id:in(...)` 覆盖组内全部
// 来源，管道与 searchQuery 逐字一致；fields 额外带 projection_generation（供内存
// 归因过滤）。管道顺序（fields → sort → limit 相邻）的原因见 searchQuery 注释。
func mergedSearchQuery(sources []string, q query.RangeQuery) string {
	terms := make([]string, 0, len(sources))
	for _, s := range sources {
		terms = append(terms, `"`+escape(s)+`"`)
	}
	return "log_source_id:in(" + strings.Join(terms, ",") + ")" +
		" | fields _time, _msg, event_id, log_source_id, source_generation, " +
		projectionGenerationField +
		", parser_version, record_start, record_end, ingest_time_utc, level, stream, instance_id, canonical_content_hash" +
		" | sort by (_time desc)" +
		" | limit " + strconv.FormatUint(mergedScanRows(q), 10)
}

// SearchMerged 合并检索：一次 VL 粗筛 + 内存归因过滤（见 query.MergedRangeClient 约定）。
//
// 前置：rngs 非空、同 Owner 同 UTC 天、每个 range 有可枚举来源
// （query.MergeableScope ok）；任一不满足即返回错误——由调用方降级回逐 range
// 执行，绝不静默放大可见范围。
func (c *Client) SearchMerged(ctx context.Context, rngs []query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	if c == nil || c.vl == nil {
		return query.RangeResult{}, query.ErrRangeClientUnimplemented
	}
	if len(rngs) == 0 {
		return query.RangeResult{}, fmt.Errorf("vlrange: empty merged range set")
	}
	first := rngs[0]
	sources := make([]string, 0, len(rngs))
	seen := make(map[string]bool, len(rngs))
	for _, rng := range rngs {
		if rng.Owner != first.Owner || rng.Key.UTCDay != first.Key.UTCDay {
			return query.RangeResult{}, fmt.Errorf(
				"vlrange: merged ranges must share owner and UTC day (got %s/%s and %s/%s)",
				first.Owner, first.Key.UTCDay, rng.Owner, rng.Key.UTCDay)
		}
		scopes, ok := query.MergeableScope(rng)
		if !ok {
			return query.RangeResult{}, fmt.Errorf(
				"vlrange: range %q has no expressible source scope; not mergeable", rng.TargetID)
		}
		for _, s := range scopes {
			if !seen[s] {
				seen[s] = true
				sources = append(sources, s)
			}
		}
	}
	// 查询文本确定性：来源排序后拼接（便于日志/调试对照，不影响命中集合）。
	sort.Strings(sources)

	params, empty, err := c.dayParams(first.Key.UTCDay, q)
	if err != nil {
		return query.RangeResult{}, err
	}
	if empty {
		return query.RangeResult{Exhausted: true, DuplicateQuality: query.DupExact, StatsQuality: query.StatsExact}, nil
	}
	params.Set("query", mergedSearchQuery(sources, q))
	params.Set("limit", strconv.FormatUint(mergedScanRows(q), 10))

	var out query.RangeResult
	seenHash := make(map[string]string, mergedScanMinRows)
	duplicated := false
	bytesRead, truncated, err := c.stream(ctx, params, scanLimit(q.Budget), func(line []byte) error {
		ev, err := eventFromJSON(line)
		if err != nil {
			return err
		}
		// 归因：命中任一 range 的权威谓词才保留（旧代次/超水位行在此被过滤）。
		matched := false
		for _, rng := range rngs {
			if rangeMatchesEvent(rng, ev) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		// 去重语义与 CP 侧 dedupeMergedRows 一致：同 id 同 hash 去重；
		// 同 id 异 hash 保留（内容不同的两份都要可见），并触发质量降级。
		if prev, ok := seenHash[ev.EventID]; ok {
			duplicated = true
			if prev == ev.CanonicalHash {
				return nil
			}
		}
		seenHash[ev.EventID] = ev.CanonicalHash
		out.Items = append(out.Items, ev)
		return nil
	})
	if err != nil {
		return query.RangeResult{}, err
	}
	out.Bytes = bytesRead
	out.Exhausted = !truncated
	out.DuplicateQuality = query.DupExact
	if duplicated {
		// 跨 range 并集出现同 id 多哈希：不得对外宣称 exact（与逐 range 路径
		// 「任一 partial → unresolved」的保守取向一致）。
		out.DuplicateQuality = query.DupUnresolved
	}
	out.StatsQuality = query.StatsExact
	if truncated {
		out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
	}
	return out, nil
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
