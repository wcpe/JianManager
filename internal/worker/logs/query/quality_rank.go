package query

import "strings"

// 本文件提供 Worker 侧质量等级的「越差越胜」合并。
//
// 为什么需要一份本地实现：Control Plane 侧 internal/controlplane/logcoord 已有
// worstDuplicateQuality / worstStatsQuality，但进程边界不可逾越（三进程模型），
// query 包绝不能反向依赖 controlplane/logcoord。故此处逐值复刻同一口径；
// 两处 rank 必须保持一致，否则同一份结果在 Worker 内聚合与 CP 聚合会给出不同等级。
//
// 排序（越差越前，与 logcoord.worstDuplicateQuality 逐值一致）：
// conflict(4) > unresolved(3) > unspecified(2) > exact(1)。
// 关键点：unspecified 必须高于 exact——它代表「没有任何来源断言过这份结果的质量」，
// 若记作最低，worst(exact, unspecified) 会得到 exact，等于让未上报来源冒充精确来源。
// unspecified 仍低于 unresolved：后者是可归因的具体发现，合并时保留信息量更大的值。
//
// 归一化统一走 normalize*：空串（零值 Quality）与一切本版本不认识的未来枚举
// 都收敛成 unspecified，避免「未上报」与「上报了不认识的取值」落在不同 rank 上。

// normalizeDuplicateQuality 把任意来源的重复质量收敛到本包四个枚举之一。
// 同时接受 proto 全名（LOG_QUALITY_DUPLICATE_EXACT）与 logcoord 的短名（exact），
// 因为 Worker 内部既用全名常量，也可能收到跨层透传的短名。
func normalizeDuplicateQuality(q DuplicateQuality) DuplicateQuality {
	s := strings.ToLower(strings.TrimSpace(string(q)))
	s = strings.TrimPrefix(s, "log_quality_duplicate_")
	switch s {
	case "exact":
		return DupExact
	case "unresolved":
		return DupUnresolved
	case "conflict":
		return DupConflict
	default: // unspecified、空串与未知枚举
		return DupUnspecified
	}
}

// duplicateQualityRank 返回「越差越大」的排序值。
func duplicateQualityRank(q DuplicateQuality) int {
	switch normalizeDuplicateQuality(q) {
	case DupConflict:
		return 4
	case DupUnresolved:
		return 3
	case DupUnspecified:
		return 2
	default: // 只剩 exact
		return 1
	}
}

// worstDuplicateQuality 取两者中更差的重复质量（rank 大者胜）。
//
// 调用点不得用无条件赋值：多 target 聚合时结果是按 key 排序顺序累积的，
// 若直接覆盖，更差的等级会被后续更好的等级冲掉，最终结论取决于分区命名顺序。
func worstDuplicateQuality(a, b DuplicateQuality) DuplicateQuality {
	pa, pb := normalizeDuplicateQuality(a), normalizeDuplicateQuality(b)
	if duplicateQualityRank(pb) > duplicateQualityRank(pa) {
		return pb
	}
	return pa
}

// normalizeStatsQuality 同上，用于统计质量（口径与 logcoord.ParseStatsQuality 一致）。
func normalizeStatsQuality(q StatsQuality) StatsQuality {
	s := strings.ToLower(strings.TrimSpace(string(q)))
	s = strings.TrimPrefix(s, "log_quality_stats_")
	switch s {
	case "exact":
		return StatsExact
	case "partial":
		return StatsPartial
	case "unavailable":
		return StatsUnavailable
	default: // unspecified、空串与未知枚举
		return StatsUnspecified
	}
}

// statsQualityRank 排序（越差越大，与 logcoord.worstStatsQuality 逐值一致）：
// unavailable(4) > partial(3) > unspecified(2) > exact(1)。
func statsQualityRank(q StatsQuality) int {
	switch normalizeStatsQuality(q) {
	case StatsUnavailable:
		return 4
	case StatsPartial:
		return 3
	case StatsUnspecified:
		return 2
	default: // 只剩 exact
		return 1
	}
}

// worstStatsQuality 取两者中更差的统计质量（rank 大者胜）。
func worstStatsQuality(a, b StatsQuality) StatsQuality {
	pa, pb := normalizeStatsQuality(a), normalizeStatsQuality(b)
	if statsQualityRank(pb) > statsQualityRank(pa) {
		return pb
	}
	return pa
}
