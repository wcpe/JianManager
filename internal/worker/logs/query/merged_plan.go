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

// groupTasksByUTCDay 把任务按 UTC 天分组为批次（天降序，批内保持原顺序）。
//
// 用途是 tail-first 早停：分区的数据时间边界严格（更旧天的所有行 _time 都早于
// 更新天的所有行——由 catalog 的天分区归属语义保证），因此**最新天的候选行数
// 达到「页大小+1」时，更旧天不可能贡献本页 top-N**，其查询可直接跳过。
//
// 正确性边界（2026-10-08 实测 + 论证）：
//   - 早停以「每个请求的窗口」为界独立判断——分页/导出的下一页请求窗口已收窄到
//     游标之前，会从更早的批次继续取，**枚举不丢行**；
//   - 跳过批次不需要标记 coverage（数据够页时跳过是语义正确，非缺失）；
//   - 批内含同日多 Owner 的任务（同天的 hot/cold 行时间可重叠，不得跨 Owner 早停）。
func groupTasksByUTCDay(tasks []searchTask) [][]searchTask {
	byDay := make(map[string][]searchTask, 8)
	days := make([]string, 0, 8)
	for _, t := range tasks {
		if len(t.rngs) == 0 {
			continue
		}
		day := t.rngs[0].Key.UTCDay
		if _, ok := byDay[day]; !ok {
			days = append(days, day)
		}
		byDay[day] = append(byDay[day], t)
	}
	// YYYY-MM-DD 字典序即时间序；降序 = 最新天在前。
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	out := make([][]searchTask, 0, len(days))
	for _, d := range days {
		out = append(out, byDay[d])
	}
	return out
}

// groupSearchTasks 把 ranges 聚为任务列表：
//   - canMerge=false（客户端未实现 MergedRangeClient）或 ranges<=1 → 全部逐 range 任务；
//   - 同 (Owner, UTCDay) 组大小 >1 且每个 range 都有可枚举来源 → 合并任务；
//   - 组内出现不可合并 range → 该组整体拆回逐 range（保守，绝不放大可见范围）。
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
		key := string(rng.Owner) + "\x1f" + rng.Key.UTCDay
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
