package query

import (
	"sort"
	"strings"
)

// 合并检索的任务规划（2026-10-08）：把 plan.Ranges 按 Owner 聚为
// 执行任务——同组多 range 由 MergedRangeClient 一次粗筛 + 内存归因完成
// （跨 UTC 天由实现按存储层单查上限切滑动段，见 vlrange.SearchMerged）；
// 任何不满足条件的情形自动退回逐 range（语义不变，仅性能差异）。
//
// 背景实测：面板 24h 平台级 162 目标逐 range 扇出 7.4-20.9s；
// 合并（VL `in()` 一次查询 + 代次归因）0.3-0.4s。

// searchTask 是一次待执行的检索任务：单个 range，或一组同 Owner 的
// 可合并 range。
type searchTask struct {
	rngs   []AuthoritativeRange
	merged bool
}

// SourceScope 是 range 在物理存储层（VictoriaLogs）的一条来源约束。
//
// **两种形态互斥，且绝不可互换**——这正是 2026-11-05 真机「日志页整页为空」的根因。
//
//   - 精确形态：LogSourceID 是**完整源 id**（`inst:318/file`），来自
//     `PublishedProjection.SourceProjections` / `CoveredSourceGenerations`；
//   - 命名空间形态：只有 Namespace（`inst:318`），来自 `PartitionKey.StorageNamespace`。
//
// VL 行上的 `log_source_id` 恒为完整源 id（`<TargetID>/<stream>`），**从不等于命名空间**。
// 简化投递路径补建的 Catalog 记录（`ingest.ensureCatalogPartition`）只填命名空间、
// 不填逐源投影，于是该 range 只能靠命名空间匹配；若此时拿命名空间去**精确**匹配
// `log_source_id`，就会一条都查不到，而接口仍报 complete=true / EXHAUSTED / quality=exact
// ——空结果被当成「完整且无数据」，比报错更难排查。
//
// 真机证据（inst:318，2026-11-04 单日窗，VL v1.52.0 直查）：
//
//	log_source_id:="inst:318"           -> 0 条
//	log_source_id:="inst:318/file"      -> 615 条
//	(log_source_id:="inst:318" OR log_source_id:"inst:318/"*) -> 615 条
//
// 因此命名空间形态必须渲染成「等值 OR 前缀」两条，而不是等值一条。
type SourceScope struct {
	// LogSourceID 非空 ⇒ 精确形态（此时 Namespace 为空）。
	LogSourceID string
	// Namespace 非空 ⇒ 命名空间形态：匹配 `<Namespace>` 本身与 `<Namespace>/...` 的全部源。
	Namespace string
}

// MergeableScope 返回该 range 可用于合并粗筛的来源约束集合；
// ok=false 表示该 range 在 LogsQL 里没有显式来源约束（单 range 查询退化为 `*`），
// 不能进入合并组（粗筛会放大其可见范围）。
//
// 判定与 vlrange.selectorQuery 的来源段逐字对齐：
//   - scoped：SourceProjections 的 LogSourceID（**不按 ProjectionGenerations 过滤**）；
//   - 非 scoped 且 Projection 非空：CoveredSourceGenerations 的 LogSourceID（精确形态）；
//   - covered 为空时回退 Key.StorageNamespace（**命名空间形态**，见 SourceScope）。
//
// 曾经的 `len(scope.ProjectionGenerations) == 0 → 跳过` 已删（2026-11-05）：
// 简化投递路径后 selectorQuery 不再渲染任何 `projection_generation` 谓词，
// 于是「有无代次白名单」不再影响 range 的真实谓词。保留它会让合并粗筛回退到
// 命名空间，而 range 谓词是精确源 id —— 两边不相等就破坏了合并的前提
// （粗筛集合必须恰好覆盖该 range 的可读来源，不多不少）。
func MergeableScope(rng AuthoritativeRange) ([]SourceScope, bool) {
	var covered []SourceScope
	proj := rng.Projection
	scoped := proj != nil && len(proj.SourceProjections) > 0
	if scoped {
		for _, scope := range proj.SourceProjections {
			if scope.LogSourceID != "" {
				covered = append(covered, SourceScope{LogSourceID: scope.LogSourceID})
			}
		}
	} else if proj != nil {
		for _, src := range proj.CoveredSourceGenerations {
			if src.LogSourceID != "" && src.SourceGeneration != "" {
				covered = append(covered, SourceScope{LogSourceID: src.LogSourceID})
			}
		}
	}
	if len(covered) > 0 {
		return covered, true
	}
	if rng.Key.StorageNamespace != "" {
		return []SourceScope{{Namespace: rng.Key.StorageNamespace}}, true
	}
	return nil, false
}

// SortKey 是来源约束的确定性排序键（只用于查询文本稳定，不参与匹配）。
// 精确与命名空间两种形态按固定前缀区分，避免「等值 id」与「命名空间」排序交错
// 导致同集合渲染出不同查询文本（会让日志/快照对照失真）。
func (s SourceScope) SortKey() string {
	if s.LogSourceID != "" {
		return "0" + s.LogSourceID
	}
	return "1" + s.Namespace
}

// MatchesLogSourceID 报告某个完整的 `log_source_id` 是否落在该来源约束内。
//
// 这是 selectorQuery（LogsQL 侧）与 rangeMatchesEvent（内存归因侧）**共用的唯一判据**：
// 两处各写一套正是这次故障的同源形态（一处改了另一处没改，就表现为「粗筛有行、归因全丢」
// 或反过来的静默漏行）。
//
// 命名空间形态接受两种行：`<ns>` 本身（历史上 StorageNamespace 与 LogSourceID 相同的源）
// 与 `<ns>/...`（实例/节点的每个流）。前缀必须以 `/` 收口，否则 `inst:3` 会匹配到 `inst:31`。
func (s SourceScope) MatchesLogSourceID(logSourceID string) bool {
	if s.LogSourceID != "" {
		return logSourceID == s.LogSourceID
	}
	if s.Namespace == "" {
		return false
	}
	return logSourceID == s.Namespace || strings.HasPrefix(logSourceID, s.Namespace+"/")
}

// groupTasksByOwner 把任务按 Owner 聚为批次（批内保持原顺序）。
//
// 2026-10-08 修正（直测 10× 差距，推翻按天拆分）：
// **不得按 UTC 天拆查询**——VL 在「窗口内匹配行数 < limit」时必须扫完整个窗口
// （无法 top-K 早停），按天拆分把一次查询放大成「每天一次全扫」：实测 154 源
// 24h 跨天一次查 0.44-0.83s（top-303 早停，根本不触旧天），而分天后旧天单查
// 即 4.8-6.2s（含空源的全窗扫描）。同 Owner 的全部 rng 合并为**一次跨天查询**，
// 行归因按各 rng 谓词（含各自天分区的水位）在内存完成。
//
// 批次边界只保留 Owner（不同分布层级的数据时间可重叠，不得跨 Owner 早停）。
func groupTasksByOwner(tasks []searchTask) [][]searchTask {
	byOwner := make(map[string][]searchTask, 4)
	owners := make([]string, 0, 4)
	for _, t := range tasks {
		if len(t.rngs) == 0 {
			continue
		}
		owner := string(t.rngs[0].Owner)
		if _, ok := byOwner[owner]; !ok {
			owners = append(owners, owner)
		}
		byOwner[owner] = append(byOwner[owner], t)
	}
	sort.Strings(owners)
	out := make([][]searchTask, 0, len(owners))
	for _, o := range owners {
		out = append(out, byOwner[o])
	}
	return out
}

// groupSearchTasks 把 ranges 聚为任务列表：
//   - canMerge=false（客户端未实现 MergedRangeClient）或 ranges<=1 → 全部逐 range 任务；
//   - 同 Owner 组大小 >1 且每个 range 都有可枚举来源 → **一次跨天合并任务**；
//   - 组内出现不可合并 range → 该组整体拆回逐 range（保守，绝不放大可见范围）。
//
// 2026-10-08 修正：任务键从 (Owner, UTC 天) 改为 **Owner**——按天拆任务会让 VL
// 对每个天窗口独立全扫（"窗口内匹配行数 < limit"时无法 top-K 早停），实测
// 154 源 24h 跨天一次查 0.44-0.83s、分天后旧天单查即 4.8-6.2s（10× 差距）。
// 行归因由各 rng 的权威谓词（含其天分区水位）在内存完成，跨天合并语义安全。
//
// 任务顺序按 ranges 的首现顺序（副作用施加顺序与串行执行保持一致）。
func groupSearchTasks(ranges []AuthoritativeRange, canMerge bool) []searchTask {
	if !canMerge || len(ranges) <= 1 {
		tasks := make([]searchTask, 0, len(ranges))
		for _, rng := range ranges {
			tasks = append(tasks, searchTask{rngs: []AuthoritativeRange{rng}})
		}
		return tasks
	}
	type bucket struct {
		rngs []AuthoritativeRange
	}
	var buckets []*bucket
	index := make(map[string]*bucket, len(ranges))
	for _, rng := range ranges {
		key := string(rng.Owner)
		b, ok := index[key]
		if !ok {
			b = &bucket{}
			index[key] = b
			buckets = append(buckets, b)
		}
		b.rngs = append(b.rngs, rng)
	}
	tasks := make([]searchTask, 0, len(buckets))
	for _, b := range buckets {
		if len(b.rngs) > 1 {
			allMergeable := true
			for _, rng := range b.rngs {
				if _, ok := MergeableScope(rng); !ok {
					allMergeable = false
					break
				}
			}
			if allMergeable {
				tasks = append(tasks, searchTask{rngs: b.rngs, merged: true})
				continue
			}
		}
		for _, rng := range b.rngs {
			tasks = append(tasks, searchTask{rngs: []AuthoritativeRange{rng}})
		}
	}
	return tasks
}
