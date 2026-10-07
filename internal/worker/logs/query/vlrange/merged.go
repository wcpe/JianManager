package vlrange

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

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

// searchMaxWindow 是合并粗筛单次 VL 查询的时间窗口上限，必须不大于 VL 启动参数
// `-search.maxQueryTimeRange`（当前 31d，见 vlsup.BuildArgs）：超过即被 VL 以 400
// `too big time range selected` 拒绝。31d 覆盖 retention 30d 场景下的一切并集窗口
// （"all"=30 天窗最多跨 31 个 UTC 天 ⇒ 31×24h，实测恰在 VL 边界内），故常态下
// **单次查询**即完成——VL 的 sort+limit top-K 从最新分区早停，实测 30 天窗 1.2-1.3s。
// 仅当窗口超过 31d（保留期外的防御路径）才切分为滑动段。
const searchMaxWindow = 31 * 24 * time.Hour

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
		// 2026-10-08 修正：合并组**允许跨 UTC 天**（窗口取各天并集，行归因按各 rng
		// 权威谓词）——此前的"同天"防御与跨天合并改造冲突，会让整组报错并使
		// 全部 target 标 RANGE_UNAVAILABLE（24h 跨天查询返回 0 条的生产缺陷）。
		// 仅保留同 Owner 约束（不同分布层级的数据时间可重叠，不得合并）。
		if rng.Owner != first.Owner {
			return query.RangeResult{}, fmt.Errorf(
				"vlrange: merged ranges must share owner (got %s and %s)",
				first.Owner, rng.Owner)
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

	// 窗口 = 组内各天分区的**并集**（跨天一次查）。按天拆分会让 VL 对每个窗口
	// 独立全扫（"窗口内匹配行数 < limit"时无法 top-K 早停）——实测 154 源 24h
	// 跨天一次查 0.44-0.83s，而分天后旧天单查即 4.8-6.2s（10× 差距）。
	// 归因时按各 rng 的权威谓词（含其天分区水位）过滤，行不会跨天误配。
	//
	// 2026-10-08 二次修正（历史）：VL 曾有 `-search.maxQueryTimeRange=24h` 硬限制，
	// 超过 24h 的单次查询被 VL 以 400 拒绝（2 天及以上窗口整组 RANGE_UNAVAILABLE）。
	// 三次修正：VL 上限已放宽到 31d（见 vlsup.BuildArgs，覆盖 retention 全期与
	// "all" 30 天窗的 31 天跨度）——**恢复单次并集查询**（v6 语义：VL 的 sort+limit
	// top-K 从最新分区早停，实测 30 天窗 1.2-1.3s），不再按 24h 切段。
	//
	// 段切分仅保留为 >31d 窗口的防御路径：从新到老、首尾相接不重叠，每段独立应用
	// 收窄规则（游标/TimeRange 夹取），恒 ≤ 31d。段查询失败且已有结果时不得整组
	// 丢弃（曾致 30 天实例视图 19s 后返回 0 条）：标记原因、保留已收集行、停在
	// 该段，更老数据由游标翻页继续触达。
	minDay, maxDay := first.Key.UTCDay, first.Key.UTCDay
	for _, rng := range rngs {
		if rng.Key.UTCDay < minDay {
			minDay = rng.Key.UTCDay
		}
		if rng.Key.UTCDay > maxDay {
			maxDay = rng.Key.UTCDay
		}
	}
	winStart, err := time.Parse("2006-01-02", minDay)
	if err != nil {
		return query.RangeResult{}, fmt.Errorf("vlrange: invalid merged window start day %q: %w", minDay, err)
	}
	winEnd, err := time.Parse("2006-01-02", maxDay)
	if err != nil {
		return query.RangeResult{}, fmt.Errorf("vlrange: invalid merged window end day %q: %w", maxDay, err)
	}
	winEnd = winEnd.AddDate(0, 0, 1)

	var out query.RangeResult
	seenHash := make(map[string]string, mergedScanMinRows)
	duplicated := false
	exhausted := true
	needRows := int(lookaheadRows(q))
	// 临时诊断（2026-10-08）：合并查询的 VL 侧耗时（区分"等 VL"与"本地处理"）。
	vlStart := time.Now()
	segments := 0
	for segEnd := winEnd; ; {
		segStart := segEnd.Add(-searchMaxWindow)
		if segStart.Before(winStart) {
			segStart = winStart
		}
		segments++
		params, empty, err := c.windowParams(segStart, segEnd, q)
		if err != nil {
			return query.RangeResult{}, err
		}
		if !empty {
			params.Set("query", mergedSearchQuery(sources, q))
			params.Set("limit", strconv.FormatUint(mergedScanRows(q), 10))
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
				if len(out.Items) == 0 {
					return query.RangeResult{}, err
				}
				// 已有结果：更老段失败不得整组丢弃。标记原因、停在该段，
				// 更老数据由游标翻页继续触达。
				slog.Warn("合并粗筛段查询失败，保留已收集结果",
					"targets", len(rngs), "segment_start", segStart.Format(time.RFC3339), "err", err)
				exhausted = false
				out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
				break
			}
			out.Bytes += bytesRead
			if truncated {
				// 任一段被预算截断：不得宣称读完。
				exhausted = false
				out.CoverageReasons = append(out.CoverageReasons, query.ReasonBudgetExceeded)
			}
		}
		// 已到最老段：本段即最后一段，exhausted 由各段 truncated 决定。
		if !segStart.After(winStart) {
			break
		}
		// 够页早停：组内候选行已达本页预算 ⇒ 更老段不可能贡献本页 top-N；
		// 但更老段未查，不得宣称读完（翻页时由游标收窄窗口继续）。
		if len(out.Items) >= needRows {
			exhausted = false
			break
		}
		segEnd = segStart
	}

	// 临时诊断：VL 请求耗时（含网络往返 + 流式读取 + 归因过滤）。
	slog.Debug("query-timing: vl-merged",
		"targets", len(rngs), "segments", segments, "vl_ms", time.Since(vlStart).Milliseconds(),
		"rows", len(out.Items), "bytes", out.Bytes, "truncated", !exhausted)
	out.Exhausted = exhausted
	out.DuplicateQuality = query.DupExact
	if duplicated {
		// 跨 range 并集出现同 id 多哈希：不得对外宣称 exact（与逐 range 路径
		// 「任一 partial → unresolved」的保守取向一致）。
		out.DuplicateQuality = query.DupUnresolved
	}
	out.StatsQuality = query.StatsExact
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
