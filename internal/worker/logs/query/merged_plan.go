package query

import "sort"

// 合并检索的任务规划（2026-10-08）：把 plan.Ranges 按 (Owner, UTC 天) 聚为
// 执行任务——同组多 range 由 MergedRangeClient 一次粗筛 + 内存归因完成；
// 任何不满足条件的情形自动退回逐 range（语义不变，仅性能差异）。
//
// 背景实测：面板 24h 平台级 162 目标逐 range 扇出 7.4-20.9s；
// 合并（VL `in()` 一次查询 + 代次归因）0.3-0.4s。

// searchTask 是一次待执行的检索任务：单个 range，或一组同 (Owner, UTC 天) 的
// 可合并 range。
type searchTask struct {
	rngs   []AuthoritativeRange
	merged bool
}

// MergeableScope 返回该 range 可用于 `log_source_id:in(...)` 粗筛的来源集合；
// ok=false 表示该 range 在 LogsQL 里没有显式来源约束（单 range 查询退化为 `*`），
// 不能进入合并组（粗筛会放大其可见范围）。
//
// 判定与 vlrange.selectorQuery 的来源段逐字对齐：
//   - scoped：SourceProjections 里 generation 非空的 scope 的 LogSourceID；
//   - 非 scoped 且 Projection 非空：CoveredSourceGenerations 的 LogSourceID；
//   - covered 为空时回退 Key.StorageNamespace。
func MergeableScope(rng AuthoritativeRange) ([]string, bool) {
	var covered []string
	proj := rng.Projection
	scoped := proj != nil && len(proj.SourceProjections) > 0
	if scoped {
		for _, scope := range proj.SourceProjections {
			if len(scope.ProjectionGenerations) == 0 {
				continue
			}
			if scope.LogSourceID != "" {
				covered = append(covered, scope.LogSourceID)
			}
		}
	} else if proj != nil {
		for _, src := range proj.CoveredSourceGenerations {
			if src.LogSourceID != "" && src.SourceGeneration != "" {
				covered = append(covered, src.LogSourceID)
			}
		}
	}
	if len(covered) > 0 {
		return covered, true
	}
	if rng.Key.StorageNamespace != "" {
		return []string{rng.Key.StorageNamespace}, true
	}
	return nil, false
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
